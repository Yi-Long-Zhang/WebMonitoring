param(
    [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.IO.Compression.FileSystem
if (-not $env:ALERT_SECRET_KEY) {
    $env:ALERT_SECRET_KEY = [Convert]::ToBase64String([byte[]](1..32))
}
$projectName = if ($env:SMOKE_PROJECT_NAME) { $env:SMOKE_PROJECT_NAME } else { "webmonitoring-smoke" }
$compose = @(
    "--project-name", $projectName,
    "-f", "deployments/compose/docker-compose.yml",
    "-f", "deployments/compose/docker-compose.smoke.yml"
)
$smokeRoot = Join-Path (Join-Path (Join-Path (Get-Location) ".data-smoke") "runs") "$PID"
$env:SMOKE_DATA_ROOT = $smokeRoot
$apiBase = "http://localhost:18080"
$targetRoot = Join-Path $smokeRoot "target"
$wellKnown = Join-Path $targetRoot ".well-known"
New-Item -ItemType Directory -Force -Path $wellKnown | Out-Null
@"
<!doctype html>
<html><head><title>Smoke target</title></head>
<body><main id="content">initial smoke content</main></body></html>
"@ | Set-Content -Path (Join-Path $targetRoot "index.html") -Encoding utf8

function Invoke-Compose([string[]]$Arguments) {
    & docker compose @compose @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed: $($Arguments -join ' ')"
    }
}

function Wait-Healthy([string]$Service) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $container = (& docker compose @compose ps -aq $Service).Trim()
        if ($container) {
            $state = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $container).Trim()
            if ($Service -eq "migrate" -and $state -eq "exited") {
                $exitCode = (& docker inspect --format '{{.State.ExitCode}}' $container).Trim()
                if ($exitCode -ne "0") {
                    throw "migrate exited with code $exitCode"
                }
                return
            }
            if ($state -eq "healthy") {
                return
            }
        }
        Start-Sleep -Seconds 2
    }
    throw "$Service did not become healthy"
}

function Wait-JobStatus($WebSession, [string]$JobID, [string]$Expected) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $job = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/detection-jobs" -WebSession $WebSession |
            Select-Object -ExpandProperty items | Where-Object id -eq $JobID
        if ($job.status -eq $Expected) {
            return
        }
        if ($job.status -in @("FAILED", "TIMED_OUT", "CANCELLED")) {
            throw "job $JobID ended in $($job.status): $($job.errorMessage)"
        }
        Start-Sleep -Seconds 2
    }
    throw "job $JobID did not reach $Expected"
}

function Test-EvidenceArchive([string]$Path, $Incident, $EvidenceItems) {
    $archive = [System.IO.Compression.ZipFile]::OpenRead($Path)
    try {
        foreach ($evidenceID in $Incident.evidenceIds) {
            $metadata = $EvidenceItems | Where-Object id -eq $evidenceID | Select-Object -First 1
            if (-not $metadata) {
                throw "evidence metadata $evidenceID is missing"
            }
            $entry = $archive.Entries | Where-Object FullName -like "evidence/$evidenceID.*" | Select-Object -First 1
            if (-not $entry) {
                throw "evidence archive entry $evidenceID is missing"
            }
            $stream = $entry.Open()
            try {
                $sha256 = [System.Security.Cryptography.SHA256]::Create()
                try {
                    $actual = ([BitConverter]::ToString($sha256.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
                }
                finally {
                    $sha256.Dispose()
                }
            }
            finally {
                $stream.Dispose()
            }
            if ($actual -ne $metadata.sha256) {
                throw "evidence checksum mismatch for $evidenceID"
            }
        }
    }
    finally {
        $archive.Dispose()
    }
}

try {
    Invoke-Compose @("config")
    Invoke-Compose @("up", "-d", "--build")
    foreach ($service in @("postgres", "redis", "minio", "browser-worker", "migrate", "api", "http-worker", "console", "smoke-target")) {
        Wait-Healthy $service
    }
    Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null

    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $login = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $session -ContentType "application/json" `
        -Body '{"username":"admin","password":"change-me-before-production"}'
    $csrf = @{ "X-CSRF-Token" = $login.csrfToken }
    $asset = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body '{"name":"Compose smoke target","url":"http://smoke-target","type":"OFFICIAL"}'
    $issued = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.id)/verification" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"action":"ISSUE"}'
    $tokenPath = Join-Path $wellKnown "webmonitoring-verification.txt"
    [System.IO.File]::WriteAllText($tokenPath, $issued.content, [System.Text.UTF8Encoding]::new($false))
    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.id)/verification" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"action":"VERIFY"}' | Out-Null

    $job = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"assetId`":`"$($asset.id)`",`"mode`":`"HTTP`"}"
    Wait-JobStatus $session $job.id "SUCCEEDED"
    $baselines = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/baselines?assetId=$($asset.id)" -WebSession $session
    $baseline = $baselines.items | Select-Object -First 1
    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/baselines/$($baseline.id)/approve" `
        -WebSession $session -Headers $csrf | Out-Null

    (Get-Content (Join-Path $targetRoot "index.html") -Raw).Replace("initial smoke content", "changed smoke content") |
        Set-Content -Path (Join-Path $targetRoot "index.html") -Encoding utf8
    foreach ($mode in @("HTTP", "BROWSER")) {
        $job = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
            -WebSession $session -Headers $csrf -ContentType "application/json" `
            -Body "{`"assetId`":`"$($asset.id)`",`"mode`":`"$mode`"}"
        Wait-JobStatus $session $job.id "SUCCEEDED"
    }
    $incidents = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents" -WebSession $session
    $evidence = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/evidence?assetId=$($asset.id)" -WebSession $session
    if ($incidents.items.Count -lt 1 -or $evidence.items.Count -lt 2) {
        throw "detection did not create incident and evidence"
    }
    $reportPath = Join-Path $smokeRoot "incident.zip"
    Invoke-WebRequest -Uri "$apiBase/api/v1/reports/$($incidents.items[0].id)" `
        -WebSession $session -OutFile $reportPath | Out-Null
    Test-EvidenceArchive $reportPath $incidents.items[0] $evidence.items

    Invoke-Compose @("restart", "api", "http-worker")
    Wait-Healthy "api"
    Wait-Healthy "http-worker"
    Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $session -ContentType "application/json" `
        -Body '{"username":"admin","password":"change-me-before-production"}' | Out-Null
    $persisted = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/assets" -WebSession $session
    if (@($persisted.items | Where-Object id -eq $asset.id).Count -ne 1) {
        throw "asset did not persist after restart"
    }
    $env:CONSOLE_BASE_URL = "http://localhost:18088"
    $env:ADMIN_PASSWORD = "change-me-before-production"
    & pnpm --filter @webmonitoring/console e2e
    if ($LASTEXITCODE -ne 0) {
        throw "console E2E failed"
    }
    Write-Host "Compose smoke test passed"
}
finally {
    if (-not $KeepRunning) {
        Invoke-Compose @("down", "--remove-orphans")
    }
}
