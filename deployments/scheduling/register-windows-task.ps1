# Registers (or updates) a daily scheduled task that runs the Compose backup
# against the deployment in this repository. Run from the repository root:
#
#   powershell -ExecutionPolicy Bypass -File deployments/scheduling/register-windows-task.ps1
#
# The task invokes compose-backup.ps1 with -ExecutionPolicy Bypass from the
# repository root, so relative paths inside the scripts resolve. S3 credentials
# are loaded from deployments/compose/.env by the backup script itself and are
# never part of the task definition.
#
# Re-run with the same -TaskName to update an existing registration; use
#   schtasks /Delete /TN <name> /F
# to remove it.
param(
    [string]$TaskName = "WebMonitoring-Backup",
    [string]$RepoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..\..")).Path,
    [string]$OutputRoot = ".data-backups",
    [int]$KeepBackups = 14,
    # Local time of day, HH:mm. Backups are I/O heavy; pick a quiet window.
    [string]$StartTime = "02:30"
)

$ErrorActionPreference = "Stop"

$action = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$RepoRoot\scripts\compose-backup.ps1`" -OutputRoot `"$OutputRoot`" -KeepBackups $KeepBackups" `
    -WorkingDirectory $RepoRoot
$trigger = New-ScheduledTaskTrigger -Daily -At $StartTime
$principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Hours 2)

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings -Force | Out-Null

Write-Host "Registered scheduled task '$TaskName' (daily at $StartTime, repo: $RepoRoot)"
Write-Host "Manual run:  schtasks /Run /TN $TaskName"
Write-Host "Remove:      schtasks /Delete /TN $TaskName /F"
Write-Host "Retention keeps the newest $KeepBackups backups under $OutputRoot"
