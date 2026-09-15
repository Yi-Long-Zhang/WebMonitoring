param(
    [Parameter(Mandatory = $true)]
    [string]$BackupRoot,
    [switch]$Execute,
    [string]$ConfirmText,
    # Restore a stack started under a different Compose project (for example the
    # one left running by compose-smoke.ps1 -KeepRunning).
    [string]$ProjectName = ""
)

$ErrorActionPreference = "Stop"
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

# The MinIO client runs as an ephemeral Compose service on the Compose network, so
# the rehearsal needs no host `mc` installation and no published MinIO port. The
# credentials are handed over through MC_HOST_backup rather than argv so they do
# not appear in the process list.
function Invoke-Mc([string]$Command, [string]$Mount) {
    $env:MC_HOST_backup = "http://$($env:S3_ACCESS_KEY):$($env:S3_SECRET_KEY)@$minioEndpoint"
    $arguments = @("compose") + $composeArgs + @(
        "run", "--rm", "-T", "--no-deps", "--entrypoint", "sh", "-e", "MC_HOST_backup"
    )
    if ($Mount) {
        $arguments += @("-v", $Mount)
    }
    $arguments += @("mc", "-c", $Command)
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
        Invoke-Mc "mc mirror --overwrite backup/$Bucket /verify" "$((Resolve-Path -LiteralPath $verifyRoot).Path):/verify"
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
    $password = if ($env:ADMIN_PASSWORD) { $env:ADMIN_PASSWORD } else { "change-me-before-production" }
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
# The mc container talks to MinIO over the Compose network, not through the host
# port mapping. Override for a deployment whose object store lives elsewhere.
$minioEndpoint = if ($env:S3_INTERNAL_ENDPOINT) { $env:S3_INTERNAL_ENDPOINT } else { "minio:9000" }

Invoke-Compose @("stop", "api", "http-worker")
# The dump is copied into the container instead of being piped, so no binary
# stream crosses the PowerShell pipeline (which needs PowerShell 7 for that).
$copyArguments = @("compose") + $composeArgs + @("cp", $databaseDump, "postgres:/tmp/webmonitoring.dump")
Invoke-Required "docker" $copyArguments
Invoke-Compose @("exec", "-T", "postgres", "pg_restore", "-U", "webmonitoring", "-d", "webmonitoring", "--clean", "--if-exists", "/tmp/webmonitoring.dump")
# A restore target is usually a fresh object store where the API has not run yet,
# so the bucket does not exist until it is created here; `mc mirror` refuses to
# pick the destination otherwise.
Invoke-Mc "mc mb --ignore-existing backup/$s3Bucket"
Invoke-Mc "mc mirror --overwrite --remove /backup backup/$s3Bucket" "$((Resolve-Path -LiteralPath $evidenceRoot).Path):/backup"
Invoke-Compose @("run", "--rm", "migrate", "up")
Invoke-Compose @("up", "-d", "api", "http-worker")
$apiBase = if ($env:API_BASE_URL) { $env:API_BASE_URL } else { "http://localhost:8080" }
Wait-Ready "$apiBase/readyz"
Test-RestoredEvidence $manifest $s3Bucket $resolvedBackup
Test-RestoredTaskState $apiBase
$detectionPending = Get-PendingCount "webmonitoring:detection-jobs" "detection-workers"
$alertPending = Get-PendingCount "webmonitoring:alert-jobs" "alert-workers"
Write-Host "Restore verification passed: ready, evidence SHA-256, job states, detection pending=$detectionPending, alert pending=$alertPending"
