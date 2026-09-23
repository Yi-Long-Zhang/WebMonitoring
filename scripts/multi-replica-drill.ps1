<#
本文件必须以「UTF-8 with BOM」保存。Windows PowerShell 5.1 会把没有 BOM 的 UTF-8
脚本按本地 ANSI 代码页解码，中文字符串会被错误解码并报出误导性的语法错误
（PowerShell 7 不受影响）。编辑后请确认文件仍以 EF BB BF 开头。

.SYNOPSIS
多副本演练：横向扩容时检测任务不被重复排队，队列消息不被重复消费。

.DESCRIPTION
load-test.ps1 用 API 逐资产手动创建任务，那条路径与调度器无关；本脚本反过来，
**不手动创建任何任务**，让每个控制面副本的调度器自己去排队，然后断言任务数恰好
等于资产数。这正是 `due()` 先读后判所无法保证、而迁移 000012 的部分唯一索引
`detection_jobs_active_uidx` 兜住的那件事。

竞态窗口是每个副本启动时的第一次 `scheduleAll()`：所有副本在同一瞬间读到"没有在途
任务"，然后各自插入。因此验证完成后脚本会 `restart api`，让全部副本同时重启，
把这个窗口放大成可复现的。

断言：
  * 指定模式的检测任务数恰好等于资产数（没有重复排队）；
  * 不存在任何 (tenant, asset, mode) 有多于一条任务；
  * 全部任务进入 SUCCEEDED；
  * Redis 消费组里的消费者数量等于 http-worker 副本数，且名字互不相同。

-DropActiveJobIndex 是变异检查：先删掉那个唯一索引再演练。此时脚本**应当失败**并
报出重复任务数 —— 如果它照样通过，说明这个演练测不出它声称要测的东西。

只连接 Compose 内部的授权测试目标，不访问任何外部站点。测试数据写入
.data-multireplica（已 Git 忽略）。

-Mode 默认 HTTP。BROWSER 模式余量最薄：每次检测都要经 http-worker 调 Playwright，
所以浏览器变体要小规模跑，并相应放宽 -TimeoutSeconds：
  .\scripts\multi-replica-drill.ps1 -Mode BROWSER -AssetCount 6 -TimeoutSeconds 900

.EXAMPLE
.\scripts\multi-replica-drill.ps1 -KeepRunning
.EXAMPLE
.\scripts\multi-replica-drill.ps1 -DropActiveJobIndex
#>
param(
    [int]$AssetCount = 40,
    [int]$Tenants = 1,
    [int]$ApiReplicas = 2,
    [int]$HttpWorkers = 3,
    [int]$TimeoutSeconds = 240,
    [ValidateSet("HTTP", "BROWSER")][string]$Mode = "HTTP",
    [switch]$DropActiveJobIndex,
    [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
if (-not $env:ALERT_SECRET_KEY) {
    $env:ALERT_SECRET_KEY = [Convert]::ToBase64String([byte[]](1..32))
}
if ($AssetCount -lt 1 -or $Tenants -lt 1 -or $Tenants -gt $AssetCount) {
    throw "参数无效：要求 AssetCount >= Tenants >= 1"
}
if ($ApiReplicas -lt 2) {
    throw "参数无效：ApiReplicas 至少为 2，单副本测不出跨副本竞态"
}

$projectName = if ($env:MRDRILL_PROJECT_NAME) { $env:MRDRILL_PROJECT_NAME } else { "webmonitoring-mrdrill" }
$dbName = "webmonitoring"
$dbUser = "webmonitoring"
$drillRoot = Join-Path (Join-Path (Join-Path (Get-Location) ".data-multireplica") "runs") "$PID"
$targetRoot = Join-Path $drillRoot "target"
$confPath = Join-Path $drillRoot "target-nginx.conf"
$entryConfPath = Join-Path $drillRoot "api-entry-nginx.conf"
$overlayPath = Join-Path $drillRoot "docker-compose.mrdrill.yml"
$apiBase = "http://localhost:18082"
# The stack's admin password. The base compose file requires ADMIN_PASSWORD to be
# set (":?"), so it is exported before `up` and pinned to a known value here; the
# overlay forwards it to the api service and the script logs in with it. The stack
# is torn down at the end of the run, so the value needs no secrecy -- it only has
# to be consistent, and it is deliberately not the repository's published default.
$env:ADMIN_PASSWORD = "mrdrill-admin-password"
$adminPassword = $env:ADMIN_PASSWORD
$runID = [guid]::NewGuid().ToString("N").Substring(0, 8)
$slugPrefix = "mrdrill-$runID"

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

# Invoke-Sql returns the scalar result of one query against the Compose database.
# Assertions read the job table directly: the API paginates and filters, and the
# point here is what actually landed in the table.
function Invoke-Sql([string]$Sql) {
    $result = & docker compose @compose exec -T postgres `
        psql -U $dbUser -d $dbName -t -A -v ON_ERROR_STOP=1 -c $Sql
    if ($LASTEXITCODE -ne 0) {
        throw "psql failed: $Sql"
    }
    return ($result | Out-String).Trim()
}

function Wait-Healthy([string]$Service) {
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        # -aq, not -q: `docker compose ps` hides exited containers by default, and
        # migrate is a one-shot that exits 0. Without -a it is never seen, the
        # loop never returns, and the drill times out on a migration that in fact
        # succeeded.
        $containers = @(& docker compose @compose ps -aq $Service)
        if ($containers.Count -gt 0) {
            $allHealthy = $true
            foreach ($container in $containers) {
                $state = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $container).Trim()
                if ($Service -eq "migrate" -and $state -eq "exited") {
                    if ((& docker inspect --format '{{.State.ExitCode}}' $container).Trim() -ne "0") {
                        throw "migrate exited non-zero"
                    }
                    continue
                }
                if ($state -ne "healthy") {
                    $allHealthy = $false
                }
            }
            if ($allHealthy) {
                return
            }
        }
        Start-Sleep -Seconds 2
    }
    throw "$Service did not become healthy"
}

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

# --- 1. 每个资产一份独立主机名和令牌文件 ---------------------------------
$hosts = @()
$confLines = New-Object System.Collections.Generic.List[string]
for ($index = 1; $index -le $AssetCount; $index++) {
    $hostName = "mrdrill-{0:d3}.webmonitoring.test" -f $index
    $hosts += $hostName
    $hostRoot = Join-Path $targetRoot $hostName
    New-Item -ItemType Directory -Force -Path (Join-Path $hostRoot ".well-known") | Out-Null
    @"
<!doctype html>
<html><head><title>$hostName</title></head>
<body><main id="content">$hostName drill page</main></body></html>
"@ | Set-Content -Path (Join-Path $hostRoot "index.html") -Encoding utf8
    $confLines.Add("server {")
    $confLines.Add("    listen 80;")
    $confLines.Add("    server_name $hostName;")
    $confLines.Add("    root /usr/share/nginx/html/$hostName;")
    $confLines.Add("    location / { try_files `$uri `$uri/ =404; }")
    $confLines.Add("}")
}
$fallbackRoot = Join-Path $targetRoot "default"
New-Item -ItemType Directory -Force -Path $fallbackRoot | Out-Null
"<!doctype html><html><body><main id=""content"">drill fallback</main></body></html>" |
    Set-Content -Path (Join-Path $fallbackRoot "index.html") -Encoding utf8
$confLines.Add("server {")
$confLines.Add("    listen 80 default_server;")
$confLines.Add("    server_name _;")
$confLines.Add("    root /usr/share/nginx/html/default;")
$confLines.Add("    location / { try_files `$uri `$uri/ =404; }")
$confLines.Add("}")

# nginx 和 Compose 直接解析这些文件，因此不带 BOM 写入。
$utf8NoBom = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllLines($confPath, $confLines, $utf8NoBom)

# API 入口。`api` 被扩容后不能再占用宿主端口，所以这里放一个按请求解析 DNS 的
# 反向代理：proxy_pass 用变量时 nginx 会走 resolver，否则它只在启动时解析一次，
# 后面 restart api 换了容器 IP 就会指向已经不存在的副本。
$entryConf = @"
server {
    listen 8080;
    resolver 127.0.0.11 valid=1s ipv6=off;
    access_log off;
    location / {
        set `$api_backend http://api:8080;
        proxy_pass `$api_backend;
        proxy_http_version 1.1;
        proxy_set_header Host `$host;
        proxy_set_header X-Forwarded-For `$proxy_add_x_forwarded_for;
        proxy_buffering off;
        proxy_read_timeout 120s;
    }
}
"@
[System.IO.File]::WriteAllText($entryConfPath, $entryConf, $utf8NoBom)

$aliases = ($hosts | ForEach-Object { "          - $_" }) -join "`n"
$mountTarget = $targetRoot.Replace("\", "/")
$mountConf = $confPath.Replace("\", "/")
$mountEntryConf = $entryConfPath.Replace("\", "/")
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
      test: ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1/"]
      interval: 5s
      timeout: 3s
      retries: 20

  api-entry:
    image: nginx:1.29-alpine
    volumes:
      - "${mountEntryConf}:/etc/nginx/conf.d/default.conf:ro"
    ports:
      - "18082:8080"
    depends_on:
      - api

  minio:
    ports: !reset []

  api:
    # 多副本时宿主端口只能给一个容器，因此由 api-entry 承担入口。
    ports: !reset []
    environment:
      ALLOW_PRIVATE_TARGETS: "true"
      # 基础 compose 文件现在要求 ADMIN_PASSWORD 必须给值（":?"），因此在 up
      # 之前就从脚本导出；本栈运行结束即拆除，口令无需保密，只要前后一致，
      # 且刻意不等于仓库公开的开发默认口令。
      ADMIN_PASSWORD: "$env:ADMIN_PASSWORD"

  http-worker:
    environment:
      ALLOW_PRIVATE_TARGETS: "true"
"@
[System.IO.File]::WriteAllText($overlayPath, $overlay, $utf8NoBom)

try {
    Invoke-Compose @("config") | Out-Null
    $upArguments = @("up", "-d", "--build")
    foreach ($service in @("postgres", "redis", "minio", "migrate", "browser-worker", "smoke-target", "api", "api-entry", "http-worker")) {
        $upArguments += $service
    }
    $upArguments += "--scale", "api=$ApiReplicas", "--scale", "http-worker=$HttpWorkers"
    Invoke-Compose $upArguments | Out-Null
    foreach ($service in @("postgres", "redis", "minio", "browser-worker", "migrate", "smoke-target", "api", "http-worker")) {
        Wait-Healthy $service
    }

    # api-entry 没有健康检查（nginx 镜像里的 wget 是 busybox 版，行为不一致），
    # 直接从宿主机重试直到代理后面的 API 应答。
    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    if (-not $ready) {
        throw "api-entry 后面的 API 未就绪"
    }

    $runningApi = @(& docker compose @compose ps -q api).Count
    $runningWorkers = @(& docker compose @compose ps -q http-worker).Count
    Write-Host "replicas: api=$runningApi http-worker=$runningWorkers"

    if ($DropActiveJobIndex) {
        Invoke-Sql "DROP INDEX IF EXISTS detection_jobs_active_uidx;" | Out-Null
        Write-Host "已删除 detection_jobs_active_uidx（变异检查）"
    }

    # --- 2. 租户、资产、真实 FILE 验证 ------------------------------------
    $adminSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $adminLogin = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
        -WebSession $adminSession -ContentType "application/json" `
        -Body "{`"username`":`"admin`",`"password`":`"$adminPassword`"}"
    $adminCsrf = @{ "X-CSRF-Token" = $adminLogin.csrfToken }

    $perTenant = [math]::Ceiling($AssetCount / [double]$Tenants)
    $slices = @()
    for ($tenantIndex = 0; $tenantIndex -lt $Tenants; $tenantIndex++) {
        $tenantHosts = @($hosts | Select-Object -Skip ($tenantIndex * $perTenant) -First $perTenant)
        if ($tenantHosts.Count -eq 0) { continue }
        $slug = "$slugPrefix-$tenantIndex"
        $tenantAdmin = "mrdrill-admin-$tenantIndex"
        $tenantPassword = "mrdrill-password-$tenantIndex"
        Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/tenants" `
            -WebSession $adminSession -Headers $adminCsrf -ContentType "application/json" `
            -Body "{`"name`":`"Drill tenant $tenantIndex`",`"slug`":`"$slug`",`"adminUsername`":`"$tenantAdmin`",`"adminPassword`":`"$tenantPassword`"}" | Out-Null
        $tenantSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
        $tenantLogin = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/auth/session" `
            -WebSession $tenantSession -ContentType "application/json" `
            -Body "{`"tenant`":`"$slug`",`"username`":`"$tenantAdmin`",`"password`":`"$tenantPassword`"}"
        $slices += [pscustomobject]@{
            Label = $slug; Session = $tenantSession; Csrf = @{ "X-CSRF-Token" = $tenantLogin.csrfToken }; Hosts = $tenantHosts
        }
    }

    $assetsByLabel = @{}
    foreach ($slice in $slices) {
        $rows = @()
        foreach ($tenantHost in @($slice.Hosts)) {
            $rows += [pscustomobject]@{ name = $tenantHost; url = "http://$tenantHost/"; type = "OFFICIAL" }
        }
        $payload = @{ assets = $rows } | ConvertTo-Json -Depth 5 -Compress
        $import = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/import" `
            -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body $payload
        if ($import.created -ne $rows.Count) {
            throw "租户 $($slice.Label) 仅创建 $($import.created)/$($rows.Count) 个资产"
        }
        $assetsByLabel[$slice.Label] = [object[]]@($import.results)
    }

    $verified = 0
    foreach ($slice in $slices) {
        foreach ($asset in $assetsByLabel[$slice.Label]) {
            $issued = Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.assetId)/verification" `
                -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body '{"action":"ISSUE"}'
            $tokenPath = Join-Path (Join-Path (Join-Path $targetRoot $asset.name) ".well-known") "webmonitoring-verification.txt"
            [System.IO.File]::WriteAllText($tokenPath, $issued.content, $utf8NoBom)
            Invoke-RestMethod -Method Post -Uri "$apiBase/api/v1/assets/$($asset.assetId)/verification" `
                -WebSession $slice.Session -Headers $slice.Csrf -ContentType "application/json" -Body '{"action":"VERIFY"}' | Out-Null
            $verified++
        }
    }
    Write-Host "verified+enabled: $verified"

    # --- 3. 让所有控制面副本同时重启 --------------------------------------
    # 竞态窗口是启动时的第一次 scheduleAll()：全部副本同时读到"无在途任务"。
    # 不重启的话各副本的 30 秒 tick 是错开的，读-写窗口几乎不会重叠。
    Write-Host "restarting api replicas to align their first scheduleAll()"
    Invoke-Compose @("restart", "api") | Out-Null
    foreach ($service in @("api")) { Wait-Healthy $service }
    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            Invoke-RestMethod -Method Get -Uri "$apiBase/readyz" | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    if (-not $ready) { throw "重启后的 API 未就绪" }

    # --- 4. 等待调度器排队并跑完 ------------------------------------------
    $scope = "t.slug LIKE '$slugPrefix-%' AND j.mode = '$Mode'"
    $started = Get-Date
    $deadline = $started.AddSeconds($TimeoutSeconds)
    $jobs = 0
    while ((Get-Date) -lt $deadline) {
        $jobs = [int](Invoke-Sql "SELECT count(*) FROM detection_jobs j JOIN tenants t ON t.id = j.tenant_id WHERE $scope;")
        $pending = [int](Invoke-Sql "SELECT count(*) FROM detection_jobs j JOIN tenants t ON t.id = j.tenant_id WHERE $scope AND j.status IN ('QUEUED','RUNNING');")
        if ($jobs -ge $AssetCount -and $pending -eq 0) {
            break
        }
        Start-Sleep -Seconds 3
    }
    $elapsed = (Get-Date) - $started

    $duplicateGroups = [int](Invoke-Sql @"
SELECT count(*) FROM (
    SELECT j.tenant_id, j.asset_id, j.mode
    FROM detection_jobs j JOIN tenants t ON t.id = j.tenant_id
    WHERE $scope
    GROUP BY 1, 2, 3 HAVING count(*) > 1
) d;
"@)
    $succeeded = [int](Invoke-Sql "SELECT count(*) FROM detection_jobs j JOIN tenants t ON t.id = j.tenant_id WHERE $scope AND j.status = 'SUCCEEDED';")

    # 消费者身份：同名消费者会让 XAutoClaim 把对方的在途消息当滞留消息回收。
    # 逐个副本按自己的容器主机名匹配，而不是数总数 —— 消费组会跨运行保留旧的
    # 消费者条目，数总数会让这一次的断言取决于上一次跑过什么。
    $consumerLines = @(& docker compose @compose exec -T redis `
        redis-cli XINFO CONSUMERS webmonitoring:detection-jobs detection-workers)
    $consumerNames = @()
    for ($index = 0; $index -lt $consumerLines.Count; $index++) {
        if ($consumerLines[$index].Trim() -eq "name" -and ($index + 1) -lt $consumerLines.Count) {
            $consumerNames += $consumerLines[$index + 1].Trim()
        }
    }
    $workerHostnames = @()
    foreach ($container in @(& docker compose @compose ps -q http-worker)) {
        $workerHostnames += (& docker inspect --format '{{.Config.Hostname}}' $container).Trim()
    }
    $badConsumers = @()
    foreach ($hostname in $workerHostnames) {
        $matched = @($consumerNames | Where-Object { $_ -like "$hostname-*" }).Count
        if ($matched -ne 1) {
            $badConsumers += "$hostname=$matched"
        }
    }
    $distinctHostnames = @($workerHostnames | Sort-Object -Unique).Count

    Write-Host ""
    Write-Host ("  assets            : {0}" -f $AssetCount)
    Write-Host ("  detection jobs    : {0} (expected {1})" -f $jobs, $AssetCount)
    Write-Host ("  duplicate groups  : {0}" -f $duplicateGroups)
    Write-Host ("  succeeded         : {0}" -f $succeeded)
    Write-Host ("  wall clock        : {0:n1}s" -f $elapsed.TotalSeconds)
    Write-Host ("  worker consumers  : {0}" -f ($workerHostnames | ForEach-Object { "$_-" } | Out-String).Trim())

    if ($duplicateGroups -gt 0) {
        throw "有 $duplicateGroups 个 (tenant, asset, mode) 存在多条任务；实际任务数 $jobs，期望 $AssetCount"
    }
    if ($jobs -ne $AssetCount) {
        throw "检测任务数 $jobs，期望 $AssetCount（重复排队或漏排）"
    }
    if ($succeeded -ne $AssetCount) {
        throw "只有 $succeeded/$AssetCount 个任务成功"
    }
    if ($distinctHostnames -ne $workerHostnames.Count) {
        throw "有 http-worker 副本共用主机名，无法区分消费者身份"
    }
    if ($badConsumers.Count -gt 0) {
        throw "副本与消费者身份不是一一对应：$($badConsumers -join ', ')"
    }

    Write-Host ""
    Write-Host "Multi-replica drill passed"
}
finally {
    if (-not $KeepRunning) {
        Invoke-Compose @("down", "--remove-orphans")
    }
}
