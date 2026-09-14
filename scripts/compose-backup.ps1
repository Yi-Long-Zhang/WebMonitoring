param(
    [string]$OutputRoot = ".data-backups"
)

$ErrorActionPreference = "Stop"

function Invoke-Compose([string[]]$Arguments) {
    & docker compose -f deployments/compose/docker-compose.yml @Arguments
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

$timestamp = Get-Date -AsUTC -Format "yyyyMMdd-HHmmss"
$backupRoot = Join-Path $OutputRoot $timestamp
$evidenceRoot = Join-Path $backupRoot "evidence"
New-Item -ItemType Directory -Force -Path $evidenceRoot | Out-Null

$databaseDump = Join-Path $backupRoot "webmonitoring.dump"
Write-Host "Creating PostgreSQL backup: $databaseDump"
& docker compose -f deployments/compose/docker-compose.yml exec -T postgres `
    pg_dump -U webmonitoring -d webmonitoring --format=custom |
    Set-Content -Path $databaseDump -AsByteStream
if ($LASTEXITCODE -ne 0) {
    throw "pg_dump failed"
}

if (-not $env:S3_ACCESS_KEY -or -not $env:S3_SECRET_KEY) {
    throw "S3_ACCESS_KEY and S3_SECRET_KEY are required for evidence backup"
}
$s3Endpoint = if ($env:S3_ENDPOINT) { $env:S3_ENDPOINT } else { "http://localhost:9000" }
$s3Bucket = if ($env:S3_BUCKET) { $env:S3_BUCKET } else { "evidence" }
Invoke-Required "mc" @("alias", "set", "webmonitoring-backup", $s3Endpoint, $env:S3_ACCESS_KEY, $env:S3_SECRET_KEY)
Invoke-Required "mc" @("mirror", "--overwrite", "webmonitoring-backup/$s3Bucket", $evidenceRoot)

$files = Get-ChildItem -File -Recurse $backupRoot | ForEach-Object {
    $hash = Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName
    [ordered]@{
        path   = ([IO.Path]::GetRelativePath($backupRoot, $_.FullName)).Replace('\', '/')
        bytes  = $_.Length
        sha256 = $hash.Hash.ToLowerInvariant()
    }
}
$manifest = [ordered]@{
    createdAtUtc = (Get-Date).ToUniversalTime().ToString("o")
    databaseDump = "webmonitoring.dump"
    evidencePath = "evidence"
    redisPersistence = (Invoke-Compose @("exec", "-T", "redis", "redis-cli", "INFO", "persistence") 2>&1 | Out-String).Trim()
    files = @($files)
}
$manifest | ConvertTo-Json -Depth 6 | Set-Content -Path (Join-Path $backupRoot "manifest.json") -Encoding utf8
Write-Host "Backup created at $backupRoot"
