<#
.SYNOPSIS
    在一次性 Postgres/Redis 上运行需要真实服务的 Go 集成测试。

.DESCRIPTION
    store / jobqueue / rewrap / migrate 里有一批用例在未设置 POSTGRES_TEST_DSN 或
    REDIS_TEST_ADDR 时会 t.Skip，所以 `go test ./...` 在本地全绿并不代表它们跑过。
    本脚本起两个一次性容器、把 DSN 指过去、跑测试、然后删掉容器。

    用一次性容器而不是复用 Compose 栈，有三个具体理由：

      * Compose 的 postgres/redis **不发布主机端口**，宿主的 go test 连不上；
      * rewrap 的集成测试会 CREATE DATABASE / DROP DATABASE，指向运维在用的库
        正是那个文件自己警告过的爆炸半径；
      * 每次都是空库，结果与上一个用例留下的数据无关。

    端口用临时回环端口（127.0.0.1::5432），因此不会和本地已有的 Postgres、
    另一次 Agent 运行或 smoke 栈相撞。

.EXAMPLE
    .\scripts\integration-test.ps1
    .\scripts\integration-test.ps1 -KeepRunning    # 排查用：跑完保留容器与 DSN
    .\scripts\integration-test.ps1 -Race           # 需要 C 工具链（Windows 上通常没有）
#>
param(
    [string]$PostgresImage = "postgres:17-alpine",
    [string]$RedisImage = "redis:8-alpine",
    [int]$ReadyTimeoutSeconds = 90,
    [switch]$KeepRunning,
    [switch]$Race
)

$ErrorActionPreference = "Stop"
$started = Get-Date

$dbName = "webmonitoring"
$dbUser = "webmonitoring"
$dbPassword = "webmonitoring"
$pgName = "webmonitoring-itest-pg-$PID"
$redisName = "webmonitoring-itest-redis-$PID"

# 容器没起时要在 1 秒内说清楚，而不是让后面每一步各自报一个费解的错。
# `docker version` 只有客户端也能成功，取出服务端版本才证明守护进程可达。
function Assert-DockerRunning {
    $server = $null
    try {
        $server = (& docker version --format '{{.Server.Version}}' 2>&1 | Out-String).Trim()
    } catch {
        $server = $null
    }
    if (-not $server -or $server -match "error|cannot|refused|not running") {
        throw ("Docker 守护进程不可达（docker version 取不到服务端版本）。" +
               "请先启动 Docker Desktop 再重跑。集成测试需要本机有一台 Postgres 17 与 Redis 8；" +
               "若本机确实没有 Docker，`make test` 仍会跑内存实现的那部分用例。")
    }
}

function Get-PublishedPort([string]$Container, [string]$ContainerPort) {
    $line = (& docker port $Container "$ContainerPort/tcp" 2>&1 | Out-String).Trim()
    if ($line -notmatch ':(\d+)\s*$') {
        throw "取不到 $Container 的 $ContainerPort 映射端口：$line"
    }
    return $Matches[1]
}

function Wait-ContainerHealthy([string]$Container) {
    for ($attempt = 0; $attempt -lt $ReadyTimeoutSeconds; $attempt++) {
        $state = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $Container 2>&1 | Out-String).Trim()
        if ($state -eq "healthy") { return }
        if ($state -eq "exited" -or $state -eq "dead") {
            $logs = (& docker logs --tail 20 $Container 2>&1 | Out-String)
            throw "$Container 启动即退出（$state）：`n$logs"
        }
        Start-Sleep -Seconds 1
    }
    $logs = (& docker logs --tail 20 $Container 2>&1 | Out-String)
    throw "$Container 在 $ReadyTimeoutSeconds 秒内未就绪：`n$logs"
}

Assert-DockerRunning

# 上一次异常退出留下的同名容器会让 docker run 直接失败，先清掉。
& docker rm -f $pgName $redisName 2>&1 | Out-Null

$postgresStarted = $false
$redisStarted = $false
try {
    Write-Host "starting throwaway services (images from CI, ports ephemeral)"

    & docker run -d --name $pgName `
        -e "POSTGRES_DB=$dbName" -e "POSTGRES_USER=$dbUser" -e "POSTGRES_PASSWORD=$dbPassword" `
        -p 127.0.0.1::5432 `
        --health-cmd "pg_isready -U $dbUser -d $dbName" --health-interval 2s --health-timeout 3s --health-retries 30 `
        $PostgresImage | Out-Null
    $postgresStarted = $true

    & docker run -d --name $redisName `
        -p 127.0.0.1::6379 `
        --health-cmd "redis-cli ping" --health-interval 2s --health-timeout 3s --health-retries 30 `
        $RedisImage | Out-Null
    $redisStarted = $true

    Wait-ContainerHealthy $pgName
    Wait-ContainerHealthy $redisName

    $pgPort = Get-PublishedPort $pgName 5432
    $redisPort = Get-PublishedPort $redisName 6379

    $env:POSTGRES_TEST_DSN = "postgres://${dbUser}:${dbPassword}@127.0.0.1:$pgPort/${dbName}?sslmode=disable"
    $env:REDIS_TEST_ADDR = "127.0.0.1:$redisPort"
    # 让「所有集成用例都被跳过」变成失败而不是绿：运行器没能把 DSN 传下去时，
    # 测试会静默 skip、退出码仍是 0，那正是门禁最不能有的结果。
    $env:WEBMONITORING_TEST_REQUIRE_SERVICES = "1"

    if ($KeepRunning) {
        Write-Host "postgres: $($env:POSTGRES_TEST_DSN)"
        Write-Host "redis   : $($env:REDIS_TEST_ADDR)"
    }

    # 预检一次迁移：把「迁移坏了」从几十个费解的用例失败变成一条清楚的错误，
    # 同时顺带覆盖一次全新库的引导路径（runner 的 bootstrapping 分支）。
    Write-Host ""
    Write-Host "preflight: applying migrations to the empty database"
    # apps/migrate reads POSTGRES_DSN, the tests read POSTGRES_TEST_DSN; both point
    # at the throwaway database so either reader is correct.
    $env:POSTGRES_DSN = $env:POSTGRES_TEST_DSN
    & go run ./apps/migrate up
    if ($LASTEXITCODE -ne 0) {
        throw "迁移预检失败（go run ./apps/migrate up 退出码 $LASTEXITCODE）"
    }

    Write-Host ""
    Write-Host "running integration tests"
    $testArgs = @("test", "-count=1")
    if ($Race) { $testArgs += "-race" }
    $testArgs += "./..."
    & go @testArgs
    if ($LASTEXITCODE -ne 0) {
        throw "集成测试失败（退出码 $LASTEXITCODE）"
    }

    # 覆盖率只报数、不设阈值：阈值属于 2.2，且应以 CI 实测值为准。
    Write-Host ""
    Write-Host "coverage for the packages that were previously skipped"
    & go test -count=1 -cover `
        ./internal/store/... ./internal/jobqueue/... ./internal/rewrap/... ./internal/migrate/...
    if ($LASTEXITCODE -ne 0) {
        throw "覆盖率统计失败（退出码 $LASTEXITCODE）"
    }

    Write-Host ""
    Write-Host ("integration tests passed in {0:n1}s" -f ((Get-Date) - $started).TotalSeconds)
}
finally {
    if ($KeepRunning) {
        Write-Host ""
        Write-Host "leaving containers running (-KeepRunning):"
        Write-Host "  docker rm -f $pgName $redisName"
    } else {
        if ($postgresStarted) { & docker rm -f $pgName 2>&1 | Out-Null }
        if ($redisStarted) { & docker rm -f $redisName 2>&1 | Out-Null }
    }
}
