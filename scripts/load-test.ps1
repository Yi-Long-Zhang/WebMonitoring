<#
本文件必须以「UTF-8 with BOM」保存。Windows PowerShell 5.1 会把没有 BOM 的 UTF-8
脚本按本地 ANSI 代码页解码，中文字符串会被错误解码并报出误导性的语法错误
（PowerShell 7 不受影响）。编辑后请确认文件仍以 EF BB BF 开头。

.SYNOPSIS
100 资产验收压测：跨租户播种、真实所有权验证、单轮 HTTP 检测吞吐与任务完整性断言。

.DESCRIPTION
只连接 Compose 内部的授权测试目标，不访问任何外部站点。每个资产生成一个独立的
主机名（load-001.webmonitoring.test …），由 smoke-target 容器的 Docker DNS 别名
解析到该容器，nginx 按 server_name 提供各自的文档根，因此每个资产都有一份独一无二
的 /.well-known/webmonitoring-verification.txt，走的是与生产完全相同的 FILE 验证路径。

压测断言：
  * 所有资产创建成功（租户内批量导入）；
  * 所有资产通过 FILE 所有权验证并自动启用；
  * 跨租户隔离：每个租户只看到自己的资产；
  * 单轮 HTTP 检测后全部任务到达终态且均为 SUCCEEDED（无丢失、无超时）；
  * 检测吞吐不低于 -MinimumJobsPerSecond；
  * 每个租户的证据条数不少于其资产数；
  * 未批准基线前不产生事件（无假阳性）。

测试数据写入 .data-loadtest（已 Git 忽略）。默认 100 资产、2 个租户。

.EXAMPLE
.\scripts\load-test.ps1 -KeepRunning
.EXAMPLE
.\scripts\load-test.ps1 -AssetCount 60 -Tenants 3 -MinimumJobsPerSecond 2
.EXAMPLE
# 浏览器模式：每个 http-worker 副本一次只消费一个任务，所以吞吐由副本数决定。
# 已实测：100 资产 / 2 租户 / 3 副本 -> 85.2 秒，1.17 任务/秒。
.\scripts\load-test.ps1 -Mode BROWSER -AssetCount 100 -Tenants 2 -HttpWorkers 3 `
    -MinimumJobsPerSecond 1 -TimeoutSeconds 1800
#>
param(
    [int]$AssetCount = 100,
    [int]$Tenants = 2,
    [int]$TimeoutSeconds = 300,
    [double]$MinimumJobsPerSecond = 1.0,
    # BROWSER drives a real Playwright context per check, so it must be run at a
    # much smaller -AssetCount with a much larger -TimeoutSeconds and a floor
    # measured for that mode rather than the HTTP default. The measured value is
    # recorded in docs/operations.md.
    [ValidateSet("HTTP", "BROWSER")][string]$Mode = "HTTP",
    # Each http-worker replica consumes one job at a time (XReadGroup Count:1,
    # processed synchronously), so the browser-mode wall clock is dominated by
    # this number and not by the browser worker. Measured 2026-09-20: a single
    # replica ran 12 browser checks in 309s, about 25s per check.
    [int]$HttpWorkers = 1,
    [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
if (-not $env:ALERT_SECRET_KEY) {
    $env:ALERT_SECRET_KEY = [Convert]::ToBase64String([byte[]](1..32))
}
if ($AssetCount -lt 1 -or $Tenants -lt 1 -or $Tenants -gt $AssetCount) {
    throw "参数无效：要求 AssetCount >= Tenants >= 1"
}

$projectName = if ($env:LOADTEST_PROJECT_NAME) { $env:LOADTEST_PROJECT_NAME } else { "webmonitoring-loadtest" }
$loadRoot = Join-Path (Join-Path (Join-Path (Get-Location) ".data-loadtest") "runs") "$PID"
$targetRoot = Join-Path $loadRoot "target"
$confPath = Join-Path $loadRoot "loadtest-nginx.conf"
$overlayPath = Join-Path $loadRoot "docker-compose.loadtest.yml"
$apiBase = "http://localhost:18081"
$adminPassword = "change-me-before-production"
# Tenant slugs must be unique even when a previous run left data behind.
$runID = [guid]::NewGuid().ToString("N").Substring(0, 8)

New-Item -ItemType Directory -Force -Path $targetRoot | Out-Null
$compose = @(
    "--project-name", $projectName,
    "-f", "deployments/compose/docker-compose.yml",
    "-f", $overlayPath
)

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

# Get-AllPages walks a keyset-paginated list endpoint to completion and returns
# every item, so a tenant with more than one page is never undercounted.
function Get-AllPages([string]$Uri, $WebSession) {
    $items = @()
    $cursor = ""
    while ($true) {
        $separator = if ($Uri -match "\?") { "&" } else { "?" }
        $target = "$Uri$separator" + "limit=100"
        if ($cursor) {
            $target = "$target&cursor=" + [uri]::EscapeDataString($cursor)
        }
        $page = Invoke-RestMethod -Method Get -Uri $target -WebSession $WebSession
        $items += @($page.items)
        $cursor = $page.nextCursor
        if (-not $cursor) {
            return $items
        }
    }
}

# --- 1. 生成 100 个独立主机名的文档根和 nginx 配置 -------------------------
$hosts = @()
$confLines = New-Object System.Collections.Generic.List[string]
for ($index = 1; $index -le $AssetCount; $index++) {
    $hostName = "load-{0:d3}.webmonitoring.test" -f $index
    $hosts += $hostName
    $hostRoot = Join-Path $targetRoot $hostName
    New-Item -ItemType Directory -Force -Path (Join-Path $hostRoot ".well-known") | Out-Null
    @"
<!doctype html>
<html><head><title>$hostName</title></head>
<body><main id="content">$hostName load page</main></body></html>
"@ | Set-Content -Path (Join-Path $hostRoot "index.html") -Encoding utf8
    $confLines.Add("server {")
    $confLines.Add("    listen 80;")
    $confLines.Add("    server_name $hostName;")
    $confLines.Add("    root /usr/share/nginx/html/$hostName;")
    $confLines.Add("    location / { try_files `$uri `$uri/ =404; }")
    $confLines.Add("}")
}
# A request with any other Host header still gets a defined answer instead of
# falling through to an unrelated asset's document root.
$fallbackRoot = Join-Path $targetRoot "default"
New-Item -ItemType Directory -Force -Path $fallbackRoot | Out-Null
@"
<!doctype html>
<html><head><title>load test fallback</title></head>
<body><main id="content">load test fallback</main></body></html>
"@ | Set-Content -Path (Join-Path $fallbackRoot "index.html") -Encoding utf8
$confLines.Add("server {")
$confLines.Add("    listen 80 default_server;")
$confLines.Add("    server_name _;")
$confLines.Add("    root /usr/share/nginx/html/default;")
$confLines.Add("    location / { try_files `$uri `$uri/ =404; }")
$confLines.Add("}")
# nginx and Compose both parse these files directly, so they are written without
# a UTF-8 BOM (Windows PowerShell 5.1 adds one for -Encoding utf8).
$utf8NoBom = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllLines($confPath, $confLines, $utf8NoBom)

$aliases = ($hosts | ForEach-Object { "          - $_" }) -join "`n"
$mountTarget = $targetRoot.Replace("\", "/")
$mountConf = $confPath.Replace("\", "/")
$overlay = @"
services:
  smoke-target:
    image: nginx:1.29-alpine
    volumes:
      - "${mountTarget}:/usr/share/nginx/html:ro"
      - "${mountConf}:/etc/nginx/conf.d/default.conf:ro"
    networks:
      default:
        aliases:
$aliases
    healthcheck:
      # 127.0.0.1 而不是 localhost：容器内 localhost 优先解析到 ::1，而挂载进
      # 只读 conf 后 nginx 入口脚本无法补上 IPv6 listen，因此只有 IPv4 在监听。
      test: ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1/"]
      interval: 5s
      timeout: 3s
      retries: 20

  minio:
    ports: !reset []

  # Same topology as the production Compose file (including the Playwright browser
  # worker and the /readyz dependency on it); only the host port and the SSRF
  # boundary differ, exactly as in the smoke overlay.
  api:
    ports: !override
      - "18081:8080"
    environment:
      ALLOW_PRIVATE_TARGETS: "true"

  http-worker:
    environment:
      ALLOW_PRIVATE_TARGETS: "true"
"@
[System.IO.File]::WriteAllText($overlayPath, $overlay, $utf8NoBom)

# --- 2. 启动 Compose -------------------------------------------------------
$slices = @()
$assetsByLabel = @{}
try {
    Invoke-Compose @("config") | Out-Null
    $upArguments = @("up", "-d", "--build",
        "postgres", "redis", "minio", "migrate", "browser-worker", "smoke-target", "api", "http-worker")
    if ($HttpWorkers -gt 1) {
        $upArguments += @("--scale", "http-worker=$HttpWorkers")
    }
    Invoke-Compose $upArguments | Out-Null
    foreach ($service in @("postgres", "redis", "minio", "browser-worker", "migrate", "smoke-target", "api", "http-worker")) {
        Wait-Healthy $service
    }
    Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null

    # --- 3. 建立租户会话 ---------------------------------------------------
    # 平台管理员只用它来建租户；每个分片都用一个本次运行新建的租户，而不是内置的
    # default 租户。Compose 具名卷会跨运行保留，复用 default 租户会让"每个租户只
    # 看到自己的资产"这类计数断言取决于上一次运行残留的数据，脚本就不再可重复。
    $adminSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $adminLogin = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $adminSession -ContentType "application/json" `
        -Body "{`"username`":`"admin`",`"password`":`"$adminPassword`"}"
    $adminCsrf = @{ "X-CSRF-Token" = $adminLogin.csrfToken }

    $perTenant = [math]::Ceiling($AssetCount / [double]$Tenants)
    for ($tenantIndex = 0; $tenantIndex -lt $Tenants; $tenantIndex++) {
        $tenantHosts = @($hosts | Select-Object -Skip ($tenantIndex * $perTenant) -First $perTenant)
        if ($tenantHosts.Count -eq 0) {
            continue
        }
        $slug = "loadtest-$runID-$tenantIndex"
        $tenantAdmin = "load-admin-$tenantIndex"
        $tenantPassword = "loadtest-password-$tenantIndex"
        Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/tenants" `
            -WebSession $adminSession -Headers $adminCsrf -ContentType "application/json" `
            -Body "{`"name`":`"Load test tenant $tenantIndex`",`"slug`":`"$slug`",`"adminUsername`":`"$tenantAdmin`",`"adminPassword`":`"$tenantPassword`"}" | Out-Null
        $tenantSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
        $tenantLogin = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
            -WebSession $tenantSession -ContentType "application/json" `
            -Body "{`"tenant`":`"$slug`",`"username`":`"$tenantAdmin`",`"password`":`"$tenantPassword`"}"
        $slices += [pscustomobject]@{
            Label = $slug; Session = $tenantSession; Csrf = @{ "X-CSRF-Token" = $tenantLogin.csrfToken }; Hosts = $tenantHosts
        }
    }

    # --- 4. 跨租户播种资产 -------------------------------------------------
    $created = 0
    foreach ($slice in $slices) {
        $rows = @()
        foreach ($tenantHost in @($slice.Hosts)) {
            $rows += [pscustomobject]@{ name = $tenantHost; url = "http://$tenantHost/"; type = "OFFICIAL" }
        }
        $payload = @{ assets = $rows } | ConvertTo-Json -Depth 5 -Compress
        $import = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/import" `
            -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body $payload
        if ($import.created -ne $rows.Count) {
            throw "租户 $($slice.Label) 仅创建 $($import.created) 个资产，期望 $($rows.Count) 个"
        }
        $assetsByLabel[$slice.Label] = [object[]]@($import.results)
        $created += $import.created
    }
    if ($created -ne $AssetCount) {
        throw "资产总数 $created，期望 $AssetCount"
    }

    # --- 5. 真实 FILE 所有权验证（每个资产一份独立令牌文件）----------------
    $verified = 0
    foreach ($slice in $slices) {
        foreach ($asset in $assetsByLabel[$slice.Label]) {
            $issued = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.assetId)/verification" `
                -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body '{"action":"ISSUE"}'
            $tokenPath = Join-Path (Join-Path (Join-Path $targetRoot $asset.name) ".well-known") "webmonitoring-verification.txt"
            [System.IO.File]::WriteAllText($tokenPath, $issued.content, [System.Text.UTF8Encoding]::new($false))
            Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.assetId)/verification" `
                -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body '{"action":"VERIFY"}' | Out-Null
            $verified++
        }
    }

    # --- 6. 验证结果与跨租户隔离 ------------------------------------------
    foreach ($slice in $slices) {
        $tenantAssets = @(Get-AllPages "$apiBase/api/v1/assets" $slice.Session)
        $expected = @($slice.Hosts).Count
        if ($tenantAssets.Count -ne $expected) {
            throw "租户 $($slice.Label) 可见 $($tenantAssets.Count) 个资产，期望 $expected 个（租户隔离或播种失败）"
        }
        $enabled = @($tenantAssets | Where-Object { $_.verificationStatus -eq "VERIFIED" -and $_.enabled }).Count
        if ($enabled -ne $expected) {
            throw "租户 $($slice.Label) 只有 $enabled/$expected 个资产通过验证并启用"
        }
        $foreign = @($tenantAssets | Where-Object { @($slice.Hosts) -notcontains ([uri]$_.url).Host })
        if ($foreign.Count -gt 0) {
            throw "租户 $($slice.Label) 列出了不属于自己的资产：$($foreign[0].url)"
        }
    }

    Write-Host "consumers: http-worker x$HttpWorkers, mode=$Mode"

    # --- 7. 单轮检测（-Mode 选择 HTTP 或 BROWSER）--------------------------
    $jobIds = @()
    foreach ($slice in $slices) {
        foreach ($asset in $assetsByLabel[$slice.Label]) {
            $job = $null
            try {
                $job = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/detection-jobs" `
                    -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" `
                    -Body "{`"assetId`":`"$($asset.assetId)`",`"mode`":`"$Mode`"}"
            } catch {
                # The control plane's scheduler also queues a check for every
                # asset that has none, on a 30s tick. On a long setup phase --
                # 100 assets take a while to verify -- it can win the race, and
                # this POST is then rejected by the active-job index from
                # migration 000012 with JOB_REJECTED. That is the same check
                # being queued, not a failure, so adopt the job already in
                # flight instead of treating the scheduler as an error.
                $inFlight = @(Get-AllPages "$apiBase/api/v1/detection-jobs" $slice.Session) |
                    Where-Object {
                        $_.assetId -eq $asset.assetId -and $_.mode -eq $Mode -and
                        $_.status -in @("QUEUED", "RUNNING")
                    } | Select-Object -First 1
                if (-not $inFlight) {
                    throw "排入 $($asset.assetId) 的 $Mode 检测任务失败，且找不到在途任务"
                }
                $job = $inFlight
            }
            $jobIds += $job.id
        }
    }
    if ($jobIds.Count -ne $AssetCount) {
        throw "只排入 $($jobIds.Count) 个检测任务，期望 $AssetCount 个"
    }

    $started = Get-Date
    $deadline = $started.AddSeconds($TimeoutSeconds)
    $terminal = @{}
    while ((Get-Date) -lt $deadline) {
        $terminal = @{}
        foreach ($slice in $slices) {
            foreach ($job in @(Get-AllPages "$apiBase/api/v1/detection-jobs" $slice.Session)) {
                if ($jobIds -contains $job.id -and $job.status -in @("SUCCEEDED", "FAILED", "TIMED_OUT", "CANCELLED")) {
                    $terminal[$job.id] = $job.status
                }
            }
        }
        if ($terminal.Count -ge $jobIds.Count) {
            break
        }
        Start-Sleep -Seconds 3
    }
    $elapsed = (Get-Date) - $started

    $unfinished = @($jobIds | Where-Object { -not $terminal.ContainsKey($_) })
    if ($unfinished.Count -gt 0) {
        throw "有 $($unfinished.Count) 个检测任务在 $TimeoutSeconds 秒内未进入终态（任务丢失或积压）"
    }
    $notSucceeded = @($terminal.GetEnumerator() | Where-Object { $_.Value -ne "SUCCEEDED" })
    if ($notSucceeded.Count -gt 0) {
        $summary = ($notSucceeded | Group-Object Value | ForEach-Object { "$($_.Name)=$($_.Count)" }) -join ", "
        throw "检测任务未全部成功：$summary"
    }
    $rate = [math]::Round($jobIds.Count / [math]::Max($elapsed.TotalSeconds, 1), 2)
    Write-Host "$Mode throughput: $rate jobs/s over $($jobIds.Count) assets"
    if ($rate -lt $MinimumJobsPerSecond) {
        throw "$Mode 检测吞吐 $rate 任务/秒，低于下限 $MinimumJobsPerSecond 任务/秒"
    }

    # --- 8. 证据完整性与无假阳性事件 ---------------------------------------
    $evidenceTotal = 0
    $incidentTotal = 0
    foreach ($slice in $slices) {
        $expected = @($slice.Hosts).Count
        $tenantEvidence = @(Get-AllPages "$apiBase/api/v1/evidence" $slice.Session)
        if ($tenantEvidence.Count -lt $expected) {
            throw "租户 $($slice.Label) 只有 $($tenantEvidence.Count) 条证据，少于 $expected 个资产"
        }
        $evidenceTotal += $tenantEvidence.Count
        $incidentTotal += @(Get-AllPages "$apiBase/api/v1/incidents" $slice.Session).Count
    }
    if ($incidentTotal -ne 0) {
        throw "未批准基线前产生了 $incidentTotal 个事件（假阳性）"
    }

    Write-Host "Load test passed"
    Write-Host ("  assets          : {0} across {1} tenant(s)" -f $AssetCount, $slices.Count)
    Write-Host ("  verified+enabled: {0}" -f $verified)
    Write-Host ("  detection jobs  : {0} SUCCEEDED" -f $terminal.Count)
    Write-Host ("  wall clock      : {0:n1}s" -f $elapsed.TotalSeconds)
    Write-Host ("  throughput      : {0:n2} jobs/s" -f $rate)
    Write-Host ("  evidence items  : {0}" -f $evidenceTotal)
    Write-Host ("  incidents       : {0} (expected 0 before baseline approval)" -f $incidentTotal)
}
finally {
    if (-not $KeepRunning) {
        Invoke-Compose @("down", "--remove-orphans")
    }
}
