param(
    [string]$OutputRoot = ".data-backups",
    # Backup a stack started under a different Compose project (for example the
    # one left running by compose-smoke.ps1 -KeepRunning).
    [string]$ProjectName = ""
)

$ErrorActionPreference = "Stop"
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
# The mc container talks to MinIO over the Compose network, not through the host
# port mapping. Override for a deployment whose object store lives elsewhere.
$minioEndpoint = if ($env:S3_INTERNAL_ENDPOINT) { $env:S3_INTERNAL_ENDPOINT } else { "minio:9000" }

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
Invoke-Mc "mc mirror --overwrite backup/$s3Bucket /backup" $mount

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
    redisPersistence = (Invoke-Compose @("exec", "-T", "redis", "redis-cli", "INFO", "persistence") 2>&1 | Out-String).Trim()
    files            = @($files)
}
# Written without a BOM: the manifest is JSON that non-PowerShell tools may read,
# and Windows PowerShell 5.1's -Encoding utf8 would prepend one.
$manifestJson = $manifest | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText((Join-Path $backupRoot "manifest.json"), $manifestJson, [System.Text.UTF8Encoding]::new($false))
Write-Host "Backup created at $backupRoot"
