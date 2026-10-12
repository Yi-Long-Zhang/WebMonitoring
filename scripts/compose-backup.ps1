param(
    [string]$OutputRoot = ".data-backups",
    # Backup a stack started under a different Compose project (for example the
    # one left running by compose-smoke.ps1 -KeepRunning).
    [string]$ProjectName = "",
    # How many backups to keep under $OutputRoot. Oldest timestamped backup
    # directories are pruned after each successful backup; directories that do
    # not match the backup timestamp naming are never touched. Overridable via
    # BACKUP_KEEP_COUNT for scheduled runs that pass no parameters.
    [int]$KeepBackups = 14,
    # The deployment env file consulted for S3 credentials that are not already
    # set in the process environment. Explicit environment variables always win,
    # so a scheduled task needs no preloaded shell. Set to an empty string to
    # disable the lookup.
    [string]$EnvFile = "deployments/compose/.env"
)

$ErrorActionPreference = "Stop"

# Scheduled runs (schtasks, systemd timers) start from a bare process with no
# exported variables. Load S3 credentials from the deployment env file when the
# operator has not provided them explicitly; values are never echoed.
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
if ($env:BACKUP_KEEP_COUNT) {
    $KeepBackups = [int]$env:BACKUP_KEEP_COUNT
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
# otherwise address. NO_CHECK_BUCKET is deliberately NOT set -- with the check
# left on, a backup aimed at a bucket that does not exist fails loudly instead of
# quietly producing an empty backup, and `rclone mkdir` performs a real
# MakeBucket (with the check off it is a silent no-op, which would leave a
# restore into an empty store failing later with NoSuchBucket and no explanation).
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

# Path.GetRelativePath and -AsByteStream are PowerShell 7 only, while this script
# suite targets Windows PowerShell 5.1 as well; both are replaced below.
function Get-RelativePath([string]$Root, [string]$Path) {
    $rootFull = [IO.Path]::GetFullPath($Root).TrimEnd("\", "/") + [IO.Path]::DirectorySeparatorChar
    $pathFull = [IO.Path]::GetFullPath($Path)
    if (-not $pathFull.StartsWith($rootFull, [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Path is outside $Root"
    }
    return $pathFull.Substring($rootFull.Length)
}

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$backupRoot = Join-Path $OutputRoot $timestamp
$evidenceRoot = Join-Path $backupRoot "evidence"
New-Item -ItemType Directory -Force -Path $evidenceRoot | Out-Null

if (-not $env:S3_ACCESS_KEY -or -not $env:S3_SECRET_KEY) {
    throw "S3_ACCESS_KEY and S3_SECRET_KEY are required for evidence backup"
}
$s3Bucket = if ($env:S3_BUCKET) { $env:S3_BUCKET } else { "evidence" }
# The rclone container talks to the object store over the Compose network, not
# through the host port mapping. Override for a deployment whose object store
# lives elsewhere.
$objectStoreEndpoint = if ($env:S3_INTERNAL_ENDPOINT) { $env:S3_INTERNAL_ENDPOINT } else { "objectstore:9000" }

# pg_dump writes the dump inside the container and docker copies it out, so no
# binary stream crosses the PowerShell pipeline.
$databaseDump = Join-Path $backupRoot "webmonitoring.dump"
Write-Host "Creating PostgreSQL backup: $databaseDump"
Invoke-Compose @(
    "exec", "-T", "postgres",
    "pg_dump", "-U", "webmonitoring", "-d", "webmonitoring", "--format=custom",
    "-f", "/tmp/webmonitoring.dump"
)
$copyArguments = @("compose") + $composeArgs + @("cp", "postgres:/tmp/webmonitoring.dump", $databaseDump)
Invoke-Required "docker" $copyArguments

$mount = "$((Resolve-Path -LiteralPath $evidenceRoot).Path):/backup"
# --ignore-times reproduces `mc mirror --overwrite`: without it rclone skips any
# object whose size and mtime already match, which would leave a re-run against a
# non-empty destination silently holding the previous run's bytes.
Invoke-Rclone @("copy", "--ignore-times", "backup:$s3Bucket", "/backup") $mount

# Best-effort Redis snapshot. The streams are transient work queues whose source
# of truth is the jobs table, so the restore path never replays this file; it is
# kept as a forensic reference for what the queues held at backup time.
# --rdb streams an RDB snapshot over the replication protocol and works while
# AOF is enabled. REDISCLI_AUTH is passed as an environment variable (not -a)
# so the password never appears in the process list; an empty value is fine for
# the password-less development stack.
$redisRoot = Join-Path $backupRoot "redis"
New-Item -ItemType Directory -Force -Path $redisRoot | Out-Null
$redisDump = Join-Path $redisRoot "dump.rdb"
try {
    Invoke-Compose @("exec", "-T", "-e", "REDISCLI_AUTH", "redis", "sh", "-c", "redis-cli --rdb /tmp/dump.rdb")
    $copyArguments = @("compose") + $composeArgs + @("cp", "redis:/tmp/dump.rdb", $redisDump)
    Invoke-Required "docker" $copyArguments
}
catch {
    # A missing snapshot must not void an otherwise complete backup; the dump
    # and evidence are the restore-relevant artifacts.
    Write-Host "WARNING: Redis snapshot skipped: $($_.Exception.Message)"
    if (Test-Path -LiteralPath $redisDump) {
        Remove-Item -LiteralPath $redisDump -Force
    }
    Remove-Item -LiteralPath $redisRoot -Force
}

$files = Get-ChildItem -File -Recurse $backupRoot | ForEach-Object {
    $hash = Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName
    [ordered]@{
        path   = (Get-RelativePath $backupRoot $_.FullName).Replace("\", "/")
        bytes  = $_.Length
        sha256 = $hash.Hash.ToLowerInvariant()
    }
}
$manifest = [ordered]@{
    createdAtUtc     = (Get-Date).ToUniversalTime().ToString("o")
    databaseDump     = "webmonitoring.dump"
    evidencePath     = "evidence"
    redisSnapshot    = if (Test-Path -LiteralPath $redisDump) { "redis/dump.rdb" } else { $null }
    redisPersistence = (Invoke-Compose @("exec", "-T", "redis", "redis-cli", "INFO", "persistence") 2>&1 | Out-String).Trim()
    files            = @($files)
}
# Written without a BOM: the manifest is JSON that non-PowerShell tools may read,
# and Windows PowerShell 5.1's -Encoding utf8 would prepend one.
$manifestJson = $manifest | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText((Join-Path $backupRoot "manifest.json"), $manifestJson, [System.Text.UTF8Encoding]::new($false))
Write-Host "Backup created at $backupRoot"

# Rotate: keep the newest $KeepBackups timestamped backup directories. Only
# directories named exactly like this script names them are eligible, so an
# operator's notes or a copied backup with another name survive the sweep.
$timestampPattern = '^\d{8}-\d{6}$'
$backups = Get-ChildItem -Directory -LiteralPath $OutputRoot |
    Where-Object { $_.Name -match $timestampPattern } |
    Sort-Object -Property Name -Descending
$prunable = @($backups | Select-Object -Skip $KeepBackups)
foreach ($item in $prunable) {
    if ($item.FullName -eq $backupRoot) {
        continue
    }
    Remove-Item -LiteralPath $item.FullName -Recurse -Force
    Write-Host "Pruned old backup: $($item.FullName)"
}
if ($prunable.Count -gt 0) {
    Write-Host "Retention: kept $($backups.Count - $prunable.Count) of $($backups.Count) backups (KeepBackups=$KeepBackups)"
}
