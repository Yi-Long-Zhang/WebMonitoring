# Rehearsal for scheduled backup operation: run two backup cycles against the
# running stack with retention set to one, prove the older cycle is pruned, and
# prove the surviving backup passes the restore script's checksum verification.
# Nothing destructive happens here: the restore half is a dry run.
#
# Requires the main stack to be up (deployments/compose). S3 credentials are
# loaded from the deployment env file by the backup/restore scripts themselves.
param(
    [string]$RehearsalRoot = ".data-backup-rehearsal"
)

$ErrorActionPreference = "Stop"

function Invoke-Step([string]$Caption, [scriptblock]$Step) {
    Write-Host "== $Caption"
    & $Step
    if ($LASTEXITCODE -ne 0) {
        throw "$Caption failed (exit $LASTEXITCODE)"
    }
}

if (Test-Path -LiteralPath $RehearsalRoot) {
    Remove-Item -LiteralPath $RehearsalRoot -Recurse -Force
}

try {
    Invoke-Step "backup cycle 1" {
        powershell -ExecutionPolicy Bypass -File scripts/compose-backup.ps1 `
            -OutputRoot $RehearsalRoot -KeepBackups 1
    }
    $firstBackup = Get-ChildItem -Directory -LiteralPath $RehearsalRoot |
        Where-Object { $_.Name -match '^\d{8}-\d{6}$' }
    if (@($firstBackup).Count -ne 1) {
        throw "expected exactly one backup after cycle 1, found $(@($firstBackup).Count)"
    }

    # A second cycle must rotate the first one away: KeepBackups=1 leaves only
    # the newest timestamped directory.
    Invoke-Step "backup cycle 2" {
        powershell -ExecutionPolicy Bypass -File scripts/compose-backup.ps1 `
            -OutputRoot $RehearsalRoot -KeepBackups 1
    }
    $surviving = Get-ChildItem -Directory -LiteralPath $RehearsalRoot |
        Where-Object { $_.Name -match '^\d{8}-\d{6}$' }
    if (@($surviving).Count -ne 1) {
        throw "retention broken: expected 1 surviving backup, found $(@($surviving).Count)"
    }
    if ($surviving[0].FullName -eq $firstBackup[0].FullName) {
        throw "retention broken: the older backup survived rotation"
    }
    Write-Host "Retention rotation verified: $($firstBackup[0].Name) pruned, $($surviving[0].Name) kept"

    $manifest = Get-Content -Raw -LiteralPath (Join-Path $surviving[0].FullName "manifest.json") | ConvertFrom-Json
    if (-not $manifest.redisSnapshot) {
        throw "manifest has no redisSnapshot field; the Redis RDB step did not run"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $surviving[0].FullName $manifest.redisSnapshot))) {
        throw "redis snapshot missing from backup: $($manifest.redisSnapshot)"
    }
    Write-Host "Redis snapshot present: $($manifest.redisSnapshot)"

    Invoke-Step "restore dry-run verification" {
        powershell -ExecutionPolicy Bypass -File scripts/compose-restore.ps1 `
            -BackupRoot $surviving[0].FullName
    }

    Write-Host "Backup rehearsal passed"
}
finally {
    if (Test-Path -LiteralPath $RehearsalRoot) {
        Remove-Item -LiteralPath $RehearsalRoot -Recurse -Force
    }
}
