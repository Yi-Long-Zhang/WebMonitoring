param(
    [Parameter(Mandatory = $true)]
    [string]$BackupRoot,
    [switch]$Execute,
    [string]$ConfirmText,
    # Restore a stack started under a different Compose project (for example the
    # one left running by compose-smoke.ps1 -KeepRunning).
    [string]$ProjectName = "",
    # The deployment env file consulted for S3 credentials that are not already
    # set in the process environment. Explicit environment variables always win.
    # Set to an empty string to disable the lookup.
    [string]$EnvFile = "deployments/compose/.env"
)

$ErrorActionPreference = "Stop"

# See compose-backup.ps1 for the rationale: scheduled or bare-shell runs load
# S3 credentials from the deployment env file, never echoed.
if ($EnvFile -and (Test-Path -LiteralPath $EnvFile)) {
    foreach ($line in [System.IO.File]::ReadAllLines((Resolve-Path -LiteralPath $EnvFile).Path)) {
        if ($line -match '^\s*#' -or $line -notmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
            continue
        }
        $name = $Matches[1]
        if (-not [string]::IsNullOrEmpty([Environment]::GetEnvironmentVariable($name))) {
            continue
        }
        [Environment]::SetEnvironmentVariable($name, $Matches[2].Trim().Trim('"').Trim("'"))
    }
}
$resolvedBackup = (Resolve-Path -LiteralPath $BackupRoot).Path
$manifestPath = Join-Path $resolvedBackup "manifest.json"
$databaseDump = Join-Path $resolvedBackup "webmonitoring.dump"
$evidenceRoot = Join-Path $resolvedBackup "evidence"
if (-not (Test-Path -LiteralPath $manifestPath) -or
    -not (Test-Path -LiteralPath $databaseDump) -or
    -not (Test-Path -LiteralPath $evidenceRoot)) {
    throw "backup is incomplete: expected manifest.json, webmonitoring.dump and evidence/"
}

$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
foreach ($file in @($manifest.files)) {
    $path = Join-Path $resolvedBackup $file.path
    if (-not (Test-Path -LiteralPath $path)) {
        throw "backup file is missing: $($file.path)"
    }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
    if ($actual -ne $file.sha256) {
        throw "backup checksum mismatch: $($file.path)"
    }
}

Write-Host "Backup verification passed: $resolvedBackup"
if (-not $Execute) {
    Write-Host "Dry run only. Use -Execute -ConfirmText RESTORE to apply this backup."
    exit 0
}
if ($ConfirmText -cne "RESTORE") {
    throw "destructive restore requires -ConfirmText RESTORE"
}

$composeArgs = @()
if ($ProjectName) {
    $composeArgs += @("--project-name", $ProjectName)
}
$composeArgs += @("-f", "deployments/compose/docker-compose.yml")

function Invoke-Compose([string[]]$Arguments) {
    & docker compose @composeArgs @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed: $($Arguments -join ' ')"
    }
}

function Invoke-Required([string]$Command, [string[]]$Arguments) {
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed"
    }
}

# rclone runs as an ephemeral Compose service on the Compose network, so the
# rehearsal needs no host rclone installation and no published object-store port.
# The credentials are exported into this process and handed to the container by
# name (`-e NAME`) rather than by value, so they never appear in the process list.
#
# The remote is described entirely by RCLONE_CONFIG_<REMOTE>_* environment
# variables. FORCE_PATH_STYLE is required: the Compose network has no DNS entry
# for the virtual-host form `bucket.objectstore`, which is what rclone would
# otherwise address. NO_CHECK_BUCKET is deliberately NOT set -- the `mkdir` below
# has to perform a real MakeBucket, and with the check off rclone turns it into a
# silent no-op, which is how a restore into an empty store would end up failing
# later with NoSuchBucket instead of creating the bucket here.
$objectStoreRemoteVariables = @(
    "RCLONE_CONFIG_BACKUP_TYPE",
    "RCLONE_CONFIG_BACKUP_PROVIDER",
    "RCLONE_CONFIG_BACKUP_ENDPOINT",
    "RCLONE_CONFIG_BACKUP_ACCESS_KEY_ID",
    "RCLONE_CONFIG_BACKUP_SECRET_ACCESS_KEY",
    "RCLONE_CONFIG_BACKUP_FORCE_PATH_STYLE"
)

function Invoke-Rclone([string[]]$Command, [string]$Mount) {
    $env:RCLONE_CONFIG_BACKUP_TYPE = "s3"
    # "Other" is rclone's label for any S3-compatible server that is not one of
    # the named clouds; the Ceph profile it would otherwise pick among is for a
    # different quirk set.
    $env:RCLONE_CONFIG_BACKUP_PROVIDER = "Other"
    $env:RCLONE_CONFIG_BACKUP_ENDPOINT = "http://$objectStoreEndpoint"
    $env:RCLONE_CONFIG_BACKUP_ACCESS_KEY_ID = $env:S3_ACCESS_KEY
    $env:RCLONE_CONFIG_BACKUP_SECRET_ACCESS_KEY = $env:S3_SECRET_KEY
    $env:RCLONE_CONFIG_BACKUP_FORCE_PATH_STYLE = "true"

    $arguments = @("compose") + $composeArgs + @("run", "--rm", "-T", "--no-deps")
    foreach ($variable in $objectStoreRemoteVariables) {
        $arguments += @("-e", $variable)
    }
    if ($Mount) {
        $arguments += @("-v", $Mount)
    }
    # The image's entrypoint is already `rclone`, so the command follows directly.
    $arguments += @("rclone") + $Command
    Invoke-Required "docker" $arguments
}

function Wait-Ready([string]$Url) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing
            if ($response.StatusCode -eq 200) {
                return
            }
        }
        catch {
            # The service may still be starting after the restore.
        }
        Start-Sleep -Seconds 2
    }
    throw "$Url did not become ready"
}

function Get-PendingCount([string]$Stream, [string]$Group) {
    $output = @(Invoke-Compose @("exec", "-T", "redis", "redis-cli", "XPENDING", $Stream, $Group))
    if ($output.Count -eq 0) {
        throw "redis pending query returned no output for $Stream/$Group"
    }
    $firstLine = [string]$output[0]
    $parts = $firstLine.Trim() -split "\s+"
    $count = 0L
    if (-not [long]::TryParse($parts[0], [System.Globalization.NumberStyles]::Integer, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$count)) {
        throw "invalid Redis pending count for $Stream/$Group`: $firstLine"
    }
    return $count
}

function Test-RestoredEvidence($Manifest, [string]$Bucket, [string]$Root) {
    $verifyRoot = Join-Path ([IO.Path]::GetTempPath()) "webmonitoring-restore-$PID"
    try {
        New-Item -ItemType Directory -Force -Path $verifyRoot | Out-Null
        Invoke-Rclone @("copy", "--ignore-times", "backup:$Bucket", "/verify") "$((Resolve-Path -LiteralPath $verifyRoot).Path):/verify"
        foreach ($file in @($Manifest.files | Where-Object path -like "evidence/*")) {
            $path = Join-Path $Root $file.path
            if (-not (Test-Path -LiteralPath $path)) {
                throw "evidence file is missing from backup: $($file.path)"
            }
            $restoredPath = Join-Path $verifyRoot ($file.path.Substring("evidence/".Length))
            if (-not (Test-Path -LiteralPath $restoredPath)) {
                throw "evidence file is missing after restore: $($file.path)"
            }
            $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $restoredPath).Hash.ToLowerInvariant()
            if ($actual -ne $file.sha256) {
                throw "restored evidence checksum mismatch: $($file.path)"
            }
        }
    }
    finally {
        if (Test-Path -LiteralPath $verifyRoot) {
            Remove-Item -LiteralPath $verifyRoot -Recurse -Force
        }
    }
}

function Test-RestoredTaskState([string]$ApiBase) {
    $username = if ($env:ADMIN_USERNAME) { $env:ADMIN_USERNAME } else { "admin" }
    # No fallback password: the repository's development default is no longer
    # compiled in anywhere, and a restore is frequently run against a stack whose
    # admin password was rotated. Guessing would turn a missing variable into a
    # confusing 401 "invalid credentials" instead of naming what is unset.
    if (-not $env:ADMIN_PASSWORD) {
        throw "ADMIN_PASSWORD is not set; export the restored stack's administrator password before verifying its task state"
    }
    $password = $env:ADMIN_PASSWORD
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $loginBody = @{ username = $username; password = $password } | ConvertTo-Json -Compress
    Invoke-RestMethod -Method Post -Uri "$ApiBase/api/v1/auth/session" `
        -WebSession $session -ContentType "application/json" -Body $loginBody | Out-Null
    $jobs = Invoke-RestMethod -Method Get -Uri "$ApiBase/api/v1/detection-jobs" -WebSession $session
    $validStatuses = @("QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "TIMED_OUT", "CANCELLED")
    foreach ($job in @($jobs.items)) {
        if ($job.status -notin $validStatuses) {
            throw "restored job $($job.id) has invalid status $($job.status)"
        }
    }
}

if (-not $env:S3_ACCESS_KEY -or -not $env:S3_SECRET_KEY) {
    throw "S3_ACCESS_KEY and S3_SECRET_KEY are required for restore"
}
$s3Bucket = if ($env:S3_BUCKET) { $env:S3_BUCKET } else { "evidence" }
# The rclone container talks to the object store over the Compose network, not
# through the host port mapping. Override for a deployment whose object store
# lives elsewhere.
$objectStoreEndpoint = if ($env:S3_INTERNAL_ENDPOINT) { $env:S3_INTERNAL_ENDPOINT } else { "objectstore:9000" }

Invoke-Compose @("stop", "api", "http-worker")
# The dump is copied into the container instead of being piped, so no binary
# stream crosses the PowerShell pipeline (which needs PowerShell 7 for that).
$copyArguments = @("compose") + $composeArgs + @("cp", $databaseDump, "postgres:/tmp/webmonitoring.dump")
Invoke-Required "docker" $copyArguments
Invoke-Compose @("exec", "-T", "postgres", "pg_restore", "-U", "webmonitoring", "-d", "webmonitoring", "--clean", "--if-exists", "/tmp/webmonitoring.dump")
# A restore target is usually a fresh object store where the API has not run yet,
# so the bucket does not exist until it is created here; rclone refuses to pick
# the destination otherwise. This is the one call that must reach a real
# MakeBucket -- see the note on NO_CHECK_BUCKET in Invoke-Rclone.
Invoke-Rclone @("mkdir", "backup:$s3Bucket")
# `sync` rather than `copy` for the same reason the mc form used --remove: a
# restore must leave the bucket holding exactly the backup's contents, not the
# backup's contents merged into whatever the target already had. --ignore-times
# again stands in for mc's unconditional overwrite.
Invoke-Rclone @("sync", "--ignore-times", "/backup", "backup:$s3Bucket") "$((Resolve-Path -LiteralPath $evidenceRoot).Path):/backup"
Invoke-Compose @("run", "--rm", "migrate", "up")
Invoke-Compose @("up", "-d", "api", "http-worker")
$apiBase = if ($env:API_BASE_URL) { $env:API_BASE_URL } else { "http://localhost:8080" }
Wait-Ready "$apiBase/readyz"
Test-RestoredEvidence $manifest $s3Bucket $resolvedBackup
Test-RestoredTaskState $apiBase
$detectionPending = Get-PendingCount "webmonitoring:detection-jobs" "detection-workers"
$alertPending = Get-PendingCount "webmonitoring:alert-jobs" "alert-workers"
Write-Host "Restore verification passed: ready, evidence SHA-256, job states, detection pending=$detectionPending, alert pending=$alertPending"
