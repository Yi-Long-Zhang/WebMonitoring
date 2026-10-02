param(
    [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.IO.Compression.FileSystem
if (-not $env:ALERT_SECRET_KEY) {
    $env:ALERT_SECRET_KEY = [Convert]::ToBase64String([byte[]](1..32))
}
# Pin the lockout threshold so the P0-1 rehearsal asserts an exact attempt count
# instead of whatever the host environment happens to set.
$env:LOGIN_MAX_FAILURES = "5"
$lockoutMaxFailures = 5
# The stack's admin password, exported before `up` so the base compose file's
# ":?" requirement is satisfied. It is the same value the smoke overlay sets for
# the API, and both rest on one fact: this stack is torn down at the end of the
# run, so the password needs no secrecy. The script asserts behaviour, not
# credentials, so the value only has to be consistent with the overlay.
$env:ADMIN_PASSWORD = "smoke-test-admin-password"
# The remaining secrets the base Compose file now requires outright (":?"). Same
# reasoning as ADMIN_PASSWORD above: a throwaway stack needs values, not secrecy,
# and this is the single place each is set. They must not reuse the development
# defaults from .env.example, because the production gate refuses those.
$env:POSTGRES_PASSWORD = "smoke-test-db-password"
$env:S3_ACCESS_KEY = "smoke-test-access-key"
$env:S3_SECRET_KEY = "smoke-test-object-secret"
$env:BROWSER_WORKER_TOKEN = "smoke-test-worker-token"
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

# --- P4-06/P4-07 browser login-state fixture --------------------------------
# A second nginx host on the same container serves a cookie-gated page. It has
# its own server_name and document root so the tamper assertions above keep
# seeing the ungated index at http://smoke-target/ -- gating "/" would make
# every unauthenticated check see a login page and the content-change
# assertions pass for the wrong reason.
$utf8NoBom = [System.Text.UTF8Encoding]::new($false)
$loginRoot = Join-Path $targetRoot "login"
$loginWellKnown = Join-Path $loginRoot ".well-known"
New-Item -ItemType Directory -Force -Path $loginWellKnown | Out-Null
[System.IO.File]::WriteAllText((Join-Path $loginRoot "login.html"), @"
<!doctype html>
<html><head><title>Smoke login</title></head>
<body><form method="post" action="/login">
<input name="username" autocomplete="username">
<input name="password" type="password" autocomplete="current-password">
<button id="submit-login" type="submit">submit</button>
</form></body></html>
"@, $utf8NoBom)
[System.IO.File]::WriteAllText((Join-Path $loginRoot "protected.html"), @"
<!doctype html>
<html><head><title>Smoke protected console</title></head>
<body><main id="content">protected smoke content</main></body></html>
"@, $utf8NoBom)
# No Secure attribute: this stack is plain http and Chromium drops a Secure
# cookie, which would make the test fail for the wrong reason. The cookie name
# must be exactly "session" to match nginx's $cookie_session.
# A single-quoted here-string: the nginx variables must reach the file
# verbatim, and a double-quoted one would expand $uri and friends.
$loginConf = @'
server {
    listen 80 default_server;
    server_name _;
    root /usr/share/nginx/html;
    location / { try_files $uri $uri/ =404; }
}

server {
    listen 80;
    server_name smoke-login;
    root /usr/share/nginx/html/login;

    location = /login {
        if ($request_method = POST) {
            add_header Set-Cookie "session=authenticated; Path=/; HttpOnly";
            return 302 /protected.html;
        }
        try_files /login.html =404;
    }

    location = /protected.html {
        if ($cookie_session != "authenticated") {
            return 302 /login;
        }
        try_files /protected.html =404;
    }

    location / { try_files $uri $uri/ =404; }
}
'@
[System.IO.File]::WriteAllText((Join-Path $smokeRoot "smoke-target.conf"), $loginConf, $utf8NoBom)

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

# Get-SnapshotTitle runs one BROWSER check for an asset and returns the title the
# worker captured. Reading the title rather than the HTML is deliberate: it is
# enough to tell the login page from the protected page, and AGENTS.md forbids
# rendering collected page content outside the sandbox that produced it.
function Get-SnapshotTitle {
    param($WebSession, $Headers, [string]$AssetID)
    $job = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
        -WebSession $WebSession -Headers $Headers -ContentType "application/json" `
        -Body "{`"assetId`":`"$AssetID`",`"mode`":`"BROWSER`"}"
    Wait-JobStatus $WebSession $job.id "SUCCEEDED"
    $snapshot = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/evidence?assetId=$AssetID" -WebSession $WebSession |
        Select-Object -ExpandProperty items |
        Where-Object { $_.jobId -eq $job.id -and $_.kind -eq "snapshot.json" } |
        Select-Object -First 1
    if (-not $snapshot) {
        throw "browser job $($job.id) produced no snapshot.json evidence"
    }
    # -UseBasicParsing: Windows PowerShell 5.1 otherwise hands the response to
    # the IE engine, which throws a NullReferenceException when IE has never
    # been configured. This host is exactly that case.
    $raw = (Invoke-WebRequest -Method Get -UseBasicParsing -Uri "$apiBase/api/v1/evidence/$($snapshot.id)/download" -WebSession $WebSession).Content
    return ($raw | ConvertFrom-Json).title
}

# Invoke-ApiExpectError performs a request that must fail and returns the status
# code plus the decoded error envelope. Windows PowerShell 5.1 throws on any
# non-2xx response, so the body has to be recovered from the exception; both the
# response stream and ErrorDetails are tried because 5.1 may have consumed one.
function Invoke-ApiExpectError {
    param(
        [string]$Method,
        [string]$Uri,
        $WebSession,
        $Headers,
        [string]$Body
    )
    $arguments = @{ Method = $Method; Uri = $Uri; ErrorAction = "Stop" }
    if ($WebSession) { $arguments.WebSession = $WebSession }
    if ($Headers) { $arguments.Headers = $Headers }
    if ($Body) {
        $arguments.Body = $Body
        $arguments.ContentType = "application/json"
    }
    try {
        Invoke-RestMethod @arguments | Out-Null
    }
    catch {
        $response = $_.Exception.Response
        if (-not $response) {
            throw
        }
        $raw = ""
        try {
            $stream = $response.GetResponseStream()
            $reader = New-Object System.IO.StreamReader($stream)
            $raw = $reader.ReadToEnd()
            $reader.Dispose()
        }
        catch { }
        if (-not $raw -and $_.ErrorDetails) {
            $raw = $_.ErrorDetails.Message
        }
        $payload = $null
        if ($raw) {
            try { $payload = $raw | ConvertFrom-Json } catch { }
        }
        return [pscustomobject]@{
            Status = [int]$response.StatusCode
            Code   = if ($payload) { $payload.code } else { "" }
            Raw    = $raw
        }
    }
    throw "expected $Method $Uri to fail, but it succeeded"
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
    Invoke-Compose @("config") | Out-Null
    Invoke-Compose @("up", "-d", "--build")
    foreach ($service in @("postgres", "redis", "minio", "browser-worker", "migrate", "api", "http-worker", "console", "smoke-target")) {
        Wait-Healthy $service
    }
    Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null

    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $login = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $session -ContentType "application/json" `
        -Body "{`"username`":`"admin`",`"password`":`"$env:ADMIN_PASSWORD`"}"
    $csrf = @{ "X-CSRF-Token" = $login.csrfToken }
    $site = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/sites" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body '{"name":"Compose smoke site","domain":"smoke-target"}'
    $sites = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/sites" -WebSession $session
    if (@($sites.items | Where-Object id -eq $site.id).Count -ne 1) {
        throw "created site did not appear in site listing"
    }
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
    # --- P4-08 incident timeline --------------------------------------------
    # The incident row's signals are overwritten on every recurrence, so the
    # per-occurrence detail lives only on the timeline. Both write points are
    # covered here: the detection that created the incident, and a transition.
    $incidentId = $incidents.items[0].id
    $timeline = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents/$incidentId/timeline" -WebSession $session
    if ($timeline.items.Count -lt 1) {
        throw "detection did not record the incident on its timeline"
    }
    if ($timeline.items[0].type -ne "CREATED") {
        throw "timeline entry 0 is $($timeline.items[0].type), expected CREATED"
    }
    if (-not $timeline.items[0].signals) {
        throw "the CREATED entry carries no signals"
    }

    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/incidents/$incidentId/transition" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"status":"ACKNOWLEDGED"}' | Out-Null
    $timeline = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents/$incidentId/timeline" -WebSession $session
    if ($timeline.items.Count -lt 2 -or $timeline.items[0].type -ne "STATUS_CHANGED") {
        throw "the transition was not recorded on the timeline"
    }
    if ($timeline.items[0].fromStatus -ne "OPEN" -or $timeline.items[0].toStatus -ne "ACKNOWLEDGED") {
        throw "the transition entry records $($timeline.items[0].fromStatus) -> $($timeline.items[0].toStatus)"
    }

    # --- incident filtering and batch transition ----------------------------
    # The smoke run holds exactly one incident so far, which is enough to prove
    # both halves that a filter cannot fake: a matching filter still returns it,
    # and a non-matching one returns nothing. A filter that was silently dropped
    # would pass the first check and fail the second.
    $openOnly = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents?status=OPEN" -WebSession $session
    if (@($openOnly.items | Where-Object id -eq $incidentId).Count -ne 1) {
        throw "status=OPEN did not return the open incident"
    }
    $dismissedOnly = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents?status=FALSE_POSITIVE" -WebSession $session
    if (@($dismissedOnly.items).Count -ne 0) {
        throw "status=FALSE_POSITIVE returned $(@($dismissedOnly.items).Count) incidents, expected none"
    }
    $keywordMiss = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents?keyword=no-such-title-fragment" -WebSession $session
    if (@($keywordMiss.items).Count -ne 0) {
        throw "a keyword matching nothing returned $(@($keywordMiss.items).Count) incidents"
    }
    # A malformed filter is refused rather than answered: letting it through
    # would reach Postgres as a bad uuid cast (a 500) or silently return a
    # subset the caller did not ask for.
    foreach ($malformed in @("?severity=URGENT", "?assetId=not-a-uuid", "?status=ARCHIVED")) {
        $rejected = Invoke-ApiExpectError -Method Get -Uri "$apiBase/api/v1/incidents$malformed" -WebSession $session
        if ($rejected.Status -ne 400 -or $rejected.Code -ne "INVALID_FILTER") {
            throw "malformed filter $malformed returned $($rejected.Status)/$($rejected.Code), expected 400/INVALID_FILTER"
        }
    }

    # A batch over one incident that is already ACKNOWLEDGED: the row must be
    # rejected per row rather than failing the call, because "some rows cannot
    # move" is the normal case for a state-dependent transition.
    $batch = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/incidents/batch-transition" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"incidentIds`":[`"$incidentId`"],`"status`":`"ACKNOWLEDGED`"}"
    if ($batch.requested -ne 1 -or $batch.updated -ne 0) {
        throw "batch re-acknowledging = requested $($batch.requested) updated $($batch.updated), expected 1/0"
    }
    if ($batch.results[0].status -ne "REJECTED" -or $batch.results[0].code -ne "INVALID_INCIDENT_TRANSITION") {
        throw "batch row = $($batch.results[0].status)/$($batch.results[0].code), expected a rejected transition"
    }
    # And the move that is legal still lands, with the count to prove it.
    $batchInvestigating = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/incidents/batch-transition" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"incidentIds`":[`"$incidentId`"],`"status`":`"INVESTIGATING`"}"
    if ($batchInvestigating.updated -ne 1) {
        throw "batch acknowledge -> investigating updated $($batchInvestigating.updated), expected 1"
    }
    $afterBatch = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/incidents/$incidentId" -WebSession $session
    if ($afterBatch.status -ne "INVESTIGATING") {
        throw "batch transition left the incident at $($afterBatch.status)"
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
    $relogin = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $session -ContentType "application/json" `
        -Body "{`"username`":`"admin`",`"password`":`"$env:ADMIN_PASSWORD`"}"
    # The CSRF token belongs to the session, so the post-restart login needs its
    # own token before any further write request.
    $csrf = @{ "X-CSRF-Token" = $relogin.csrfToken }
    $persisted = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/assets" -WebSession $session
    if (@($persisted.items | Where-Object id -eq $asset.id).Count -ne 1) {
        throw "asset did not persist after restart"
    }
    $persistedSites = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/sites" -WebSession $session
    if (@($persistedSites.items | Where-Object id -eq $site.id).Count -ne 1) {
        throw "site did not persist after restart"
    }

    # --- P1-5 keyset pagination ---------------------------------------------
    # Two filler assets bring the tenant to three, so limit=2 must return a full
    # first page plus a cursor and a single-item, cursor-free second page.
    $import = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/import" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body '{"assets":[{"name":"smoke filler 1","url":"http://smoke-target/filler-1.html","type":"OFFICIAL"},{"name":"smoke filler 2","url":"http://smoke-target/filler-2.html","type":"OFFICIAL"}]}'
    if ($import.created -ne 2) {
        throw "asset import created $($import.created) rows, want 2"
    }
    $page1 = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/assets?limit=2" -WebSession $session
    if (@($page1.items).Count -ne 2) {
        throw "limit=2 returned $(@($page1.items).Count) assets"
    }
    if (-not $page1.nextCursor) {
        throw "limit=2 over three assets returned no nextCursor"
    }
    $page2 = Invoke-RestMethod -Method Get `
        -Uri "$apiBase/api/v1/assets?limit=2&cursor=$([uri]::EscapeDataString($page1.nextCursor))" -WebSession $session
    if (@($page2.items).Count -ne 1) {
        throw "second page returned $(@($page2.items).Count) assets, want 1"
    }
    if ($page2.nextCursor) {
        throw "last page returned a nextCursor"
    }
    $pageIds = @($page1.items.id) + @($page2.items.id)
    if (@($pageIds | Sort-Object -Unique).Count -ne $pageIds.Count) {
        throw "keyset pagination returned duplicate assets"
    }

    # --- P0-1 login lockout -------------------------------------------------
    # A random suffix keeps a re-run over a reused data root from colliding with
    # the account a previous run locked.
    $lockoutUser = "smoke-lockout-" + [guid]::NewGuid().ToString("N").Substring(0, 8)
    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/users" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"username`":`"$lockoutUser`",`"password`":`"smoke-lockout-password`",`"role`":`"OPERATOR`"}" | Out-Null
    for ($attempt = 1; $attempt -le $lockoutMaxFailures; $attempt++) {
        $failed = Invoke-ApiExpectError -Method Post -Uri "$apiBase/api/v1/auth/session" `
            -Body "{`"username`":`"$lockoutUser`",`"password`":`"wrong-password`"}"
        if ($failed.Status -ne 401 -or $failed.Code -ne "INVALID_CREDENTIALS") {
            throw "failed login $attempt returned $($failed.Status)/$($failed.Code)"
        }
    }
    # The lockout is checked before the password is verified, so even the correct
    # password must be refused while the window is armed.
    $locked = Invoke-ApiExpectError -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -Body "{`"username`":`"$lockoutUser`",`"password`":`"smoke-lockout-password`"}"
    if ($locked.Status -ne 401 -or $locked.Code -ne "ACCOUNT_LOCKED") {
        throw "expected ACCOUNT_LOCKED after $lockoutMaxFailures failures, got $($locked.Status)/$($locked.Code)"
    }
    # Lockout is per account: an unrelated session must survive it.
    $stillIn = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/auth/session" -WebSession $session
    if ($stillIn.user.username -ne "admin") {
        throw "unrelated account lost its session during another account's lockout"
    }

    # --- P1-6 asset suspend / resume ---------------------------------------
    $suspended = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.id)/suspend" `
        -WebSession $session -Headers $csrf
    if ($suspended.verificationStatus -ne "SUSPENDED" -or $suspended.enabled) {
        throw "suspend left asset $($suspended.verificationStatus)/enabled=$($suspended.enabled)"
    }
    $invalidSuspend = Invoke-ApiExpectError -Method Post -Uri "$apiBase/api/v1/assets/$($asset.id)/suspend" `
        -WebSession $session -Headers $csrf
    if ($invalidSuspend.Status -ne 409 -or $invalidSuspend.Code -ne "INVALID_ASSET_STATE") {
        throw "re-suspend returned $($invalidSuspend.Status)/$($invalidSuspend.Code)"
    }
    $rejectedJob = Invoke-ApiExpectError -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
        -WebSession $session -Headers $csrf `
        -Body "{`"assetId`":`"$($asset.id)`",`"mode`":`"HTTP`"}"
    if ($rejectedJob.Status -ne 409 -or $rejectedJob.Code -ne "JOB_REJECTED") {
        throw "queueing a suspended asset returned $($rejectedJob.Status)/$($rejectedJob.Code)"
    }
    $resumedAsset = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.id)/resume" `
        -WebSession $session -Headers $csrf
    if ($resumedAsset.verificationStatus -ne "VERIFIED" -or $resumedAsset.enabled) {
        throw "resume left asset $($resumedAsset.verificationStatus)/enabled=$($resumedAsset.enabled)"
    }
    # Resuming deliberately does not re-enable monitoring; that stays an explicit
    # operator action through PATCH.
    $reenabled = Invoke-RestMethod -Method Patch -Uri "$apiBase/api/v1/assets/$($asset.id)" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"enabled":true}'
    if (-not $reenabled.enabled) {
        throw "re-enabling a resumed asset failed"
    }

    # --- P1-7 job cancellation ---------------------------------------------
    # Stopping the worker makes cancellation deterministic: the job cannot be
    # picked up between queueing and the cancel call.
    Invoke-Compose @("stop", "http-worker")
    $queued = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"assetId`":`"$($asset.id)`",`"mode`":`"HTTP`"}"
    if ($queued.status -ne "QUEUED") {
        throw "job started as $($queued.status) with the worker stopped, want QUEUED"
    }
    $cancelled = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs/$($queued.id)/cancel" `
        -WebSession $session -Headers $csrf
    if ($cancelled.status -ne "CANCELLED") {
        throw "cancel left job in $($cancelled.status)"
    }
    $reCancel = Invoke-ApiExpectError -Method Post -Uri "$apiBase/api/v1/detection-jobs/$($queued.id)/cancel" `
        -WebSession $session -Headers $csrf
    if ($reCancel.Status -ne 409 -or $reCancel.Code -ne "INVALID_JOB_STATE") {
        throw "re-cancelling a cancelled job returned $($reCancel.Status)/$($reCancel.Code)"
    }
    Invoke-Compose @("start", "http-worker")
    Wait-Healthy "http-worker"
    $afterRestart = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body "{`"assetId`":`"$($asset.id)`",`"mode`":`"HTTP`"}"
    Wait-JobStatus $session $afterRestart.id "SUCCEEDED"
    # The restarted worker drained the cancelled job from the queue; a late
    # SUCCEEDED must not overwrite the terminal CANCELLED state.
    $terminal = Invoke-RestMethod -Method Get -Uri "$apiBase/api/v1/detection-jobs" -WebSession $session |
        Select-Object -ExpandProperty items | Where-Object id -eq $queued.id
    if (-not $terminal -or $terminal.status -ne "CANCELLED") {
        throw "cancelled job was overwritten by a late result: $($terminal.status)"
    }

    # --- P4-06/P4-07 browser login-state detection --------------------------
    # Step 1 is the negative control: with no AuthConfig the worker cannot get
    # past the cookie gate, so the snapshot must be the LOGIN page. That is what
    # makes step 2 evidence about the AuthConfig rather than about the fixture.
    $loginAsset = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body '{"name":"Smoke login asset","url":"http://smoke-login/protected.html","type":"LOGIN"}'
    $loginIssued = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($loginAsset.id)/verification" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"action":"ISSUE"}'
    [System.IO.File]::WriteAllText((Join-Path $loginWellKnown "webmonitoring-verification.txt"), $loginIssued.content, $utf8NoBom)
    Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($loginAsset.id)/verification" `
        -WebSession $session -Headers $csrf -ContentType "application/json" -Body '{"action":"VERIFY"}' | Out-Null

    $anonymousTitle = Get-SnapshotTitle $session $csrf $loginAsset.id
    if ($anonymousTitle -ne "Smoke login") {
        throw "unauthenticated browser check saw '$anonymousTitle', expected the login page"
    }

    # submitSelector is set because the worker otherwise presses Enter in the
    # password field and then waits for networkidle; a real submit button makes
    # that deterministic.
    Invoke-RestMethod -Method Put -Uri "$apiBase/api/v1/policies/$($loginAsset.id)" `
        -WebSession $session -Headers $csrf -ContentType "application/json" `
        -Body '{"httpIntervalSeconds":300,"browserIntervalSeconds":1800,"timeoutSeconds":30,"maxResponseBytes":1048576,"ignoreSelectors":[],"trustedDomains":[],"auth":{"loginUrl":"http://smoke-login/login","usernameField":"username","passwordField":"password","submitSelector":"#submit-login","username":"smoke","password":"smoke-password"}}' | Out-Null

    $authenticatedTitle = Get-SnapshotTitle $session $csrf $loginAsset.id
    if ($authenticatedTitle -ne "Smoke protected console") {
        throw "authenticated browser check saw '$authenticatedTitle', expected the protected page"
    }

    # No ADMIN_PASSWORD export here: it is set before `up` above, because the base
    # compose file now requires it to build the api service's environment.
    $env:CONSOLE_BASE_URL = "http://localhost:18088"
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
