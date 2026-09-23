# Compose 运维手册

本文用问答形式说明部署、升级、备份、恢复、验收和故障处理。命令都可直接复制。

---

## 部署和升级

**Q：首次部署前要准备什么？**

复制 `.env.example` 为 `.env`，替换管理员密码、数据库密码、对象存储密钥、
Worker Token 和 `ALERT_SECRET_KEY`。生产环境必须启用 HTTPS 并将 `SESSION_SECURE`
设置为 `true`。

**Q：管理员密码从哪来？装完之后怎么改？**

开发默认口令住在 `deployments/compose/.env.default`，这是它在仓库里的**唯一**
存放点；源码、compose 清单和脚本都不再自带这个口令。未设 `ADMIN_PASSWORD` 时
API 回落到该文件，因此开箱即用的栈仍能登录；想换默认值就改这一个文件，或用
`ADMIN_PASSWORD_FILE` 指向仓库外的自己的文件。

种子账号是 `default` 租户下的 `admin`（平台超管）。它**不会**被任何管理员重置
——`POST /api/v1/users/{id}/password` 对平台账号拒绝，这是有意的租户隔离。改它
的口令走**顶栏用户菜单 →「修改密码」**（`POST /api/v1/auth/password`，自助、
需输当前口令）。首次部署收尾时应当立刻改掉默认口令，改完新口令立即生效，且
重启不会把它重置回默认值（账号已存在时播种逻辑直接返回）。

`APP_ENV=production` 下：既拒绝使用这个公开默认口令，也拒绝不设 `ADMIN_PASSWORD`
就启动。

**Q：怎么启动或升级？**

```powershell
docker compose -f deployments/compose/docker-compose.yml build
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
docker compose -f deployments/compose/docker-compose.yml up -d
docker compose -f deployments/compose/docker-compose.yml ps
Invoke-WebRequest http://localhost:8080/readyz
```

**Q：可以删掉数据卷来重置升级吗？**

**不可以。** 不得通过删除 PostgreSQL 数据卷来完成升级。

**Q：迁移 checksum 不一致怎么办？**

**停止发布并调查**文件或数据库是否被篡改。

**Q：`baseline` 命令什么时候能用？**

迁移服务只执行向上迁移。`baseline` 只允许在**人工确认已有数据库结构与仓库版本
一致**后显式执行：

```powershell
docker compose -f deployments/compose/docker-compose.yml run --rm migrate baseline --version 3
```

---

## 备份和恢复

**Q：备份应该包含什么？**

不只是 PostgreSQL。**MinIO 证据对象不能只备份元数据**，必须同时备份：

1. PostgreSQL custom dump。
2. evidence bucket。
3. 逐文件 SHA-256 manifest。

**Q：用哪个脚本？**

```powershell
$env:S3_ACCESS_KEY = "..."
$env:S3_SECRET_KEY = "..."
$env:S3_ENDPOINT = "http://localhost:9000"
.\scripts\compose-backup.ps1 -OutputRoot .data-backups
```

脚本不会把密码写入 manifest。备份前应暂停会产生大量写入的升级或维护操作，
并记录备份时间、版本和校验值。

**Q：手工 dump 数据库怎么做？**

```powershell
docker compose -f deployments/compose/docker-compose.yml exec -T postgres `
  pg_dump -U webmonitoring -d webmonitoring --format=custom `
  > .data-dev\webmonitoring-$(Get-Date -Format yyyyMMdd-HHmmss).dump
```

**Q：手工备份 evidence bucket 怎么做？**

使用客户批准的 S3 兼容备份工具，并保留对象校验和。以 MinIO Client 为例：

```powershell
mc alias set local http://localhost:9000 $env:S3_ACCESS_KEY $env:S3_SECRET_KEY
mc mirror --overwrite local/evidence D:\backups\webmonitoring\evidence
```

**Q：恢复的顺序是什么？**

先停 API 和 Worker，恢复数据库和对象存储，再执行迁移并启动服务：

```powershell
docker compose -f deployments/compose/docker-compose.yml stop api http-worker
Get-Content .data-dev\webmonitoring-backup.dump | `
  docker compose -f deployments/compose/docker-compose.yml exec -T postgres `
  pg_restore -U webmonitoring -d webmonitoring --clean --if-exists
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
docker compose -f deployments/compose/docker-compose.yml up -d api http-worker
```

**Q：恢复后要检查什么？**

必须检查 `/readyz`、事件证据下载、SHA-256 校验和、队列 pending 数量以及最近任务状态。
**数据库恢复不能替代证据对象恢复。**

**Q：恢复脚本会直接改数据吗？**

默认不会。恢复脚本默认只校验备份文件和校验值，不执行任何修改。正式恢复必须显式确认：

```powershell
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000 `
  -Execute -ConfirmText RESTORE
```

恢复脚本会停止 API/Worker、恢复数据库和 evidence bucket、执行迁移并重新启动
API/Worker。**生产环境执行前必须记录审批、备份版本、RPO/RTO 和回滚方案。**

---

## 运行验收脚本前的准备

**Q：脚本说禁止运行脚本怎么办？**

所有验收脚本都是 `.ps1`，需要在允许执行本地脚本的 PowerShell 中运行。Windows
客户端版的默认策略是 `Restricted`，此时 `powershell -File scripts\...` 会报
「在此系统上禁止运行脚本」并直接退出，**脚本一行都不会执行**：

```powershell
Get-ExecutionPolicy   # 返回 Restricted 说明需要先处理
```

两种处理方式任选其一：

```powershell
# 只对当前用户放开本地脚本（推荐，一次设置长期生效）
Set-ExecutionPolicy -Scope CurrentUser RemoteSigned

# 或不改机器配置，单次运行
powershell -ExecutionPolicy Bypass -File .\scripts\compose-smoke.ps1
```

**Q：这和执行策略以外的东西有关吗？**

无关：Docker 正常、镜像齐备时脚本依然会被策略拦下，因此「脚本没输出」先查这一项。

---

## 验收和故障处理

**Q：怎么跑 smoke test？**

```powershell
.\scripts\compose-smoke.ps1
```

脚本使用 Compose 内部的授权测试目标，**不连接外部网站**；它覆盖登录、资产验证、
基线审批、HTTP/Browser 检测、事件、证据、报告、控制台关键页面 E2E 以及
API/Worker 重启。测试数据保存在 `.data-smoke`（已被 Git 忽略）。

### 验收清单

| 门禁 | 覆盖内容 | 命令 |
| --- | --- | --- |
| 主链路 smoke | 登录/资产验证/基线审批/HTTP+Browser 检测/事件/证据/报告/控制台 E2E/重启持久化 | `.\scripts\compose-smoke.ps1` |
| 安全边界 smoke | P0-1 登录锁定（N 次失败后即使密码正确也返回 `ACCOUNT_LOCKED`，且不影响其他账户）、P1-5 keyset 分页（`limit`+`cursor` 无重复无遗漏）、P1-6 资产暂停/恢复（暂停后排队返回 `JOB_REJECTED`，恢复不自动启用）、P1-7 任务取消（终态为 `CANCELLED`，迟到结果不得覆盖） | 同上，`compose-smoke.ps1` 已内置 |
| 浏览器登录态 smoke | P4-06/P4-07 浏览器模式登录态：smoke-target 上另起一个 cookie 门控的 `smoke-login` host，同一资产在**无** `AuthConfig` 时快照须是登录页（负控），**有**时须是受保护页 | 同上，`compose-smoke.ps1` 已内置 |
| 100 资产压测 | 跨租户播种、真实 FILE 所有权验证、单轮检测吞吐、任务无丢失、证据完整、未批准基线无假阳性。`-Mode BROWSER` 为浏览器模式 | `.\scripts\load-test.ps1`（HTTP）、`-Mode BROWSER -HttpWorkers 3`（浏览器） |
| 多副本演练 | 调度器跨副本排队不重复（任务数等于资产数）、无重复 `(tenant, asset, mode)`、每个 worker 副本一个唯一消费者身份。`-Mode BROWSER` 时断言的是浏览器任务数（余量最薄，需小规模并放宽超时） | `.\scripts\multi-replica-drill.ps1`；变异检查 `-DropActiveJobIndex` 必须失败 |
| 备份恢复演练 | 数据库 dump、evidence bucket 清单、恢复后 `/readyz` 与证据校验 | `.\scripts\compose-backup.ps1`、`.\scripts\compose-restore.ps1`（见 [backup-restore-rehearsal.md](backup-restore-rehearsal.md)） |

**Q：这些门禁在 CI 里跑哪些？**

四项，见 `.github/workflows/ci.yml`：

| CI job | 跑什么 |
| --- | --- |
| `backend` | `gofmt` 校验、`go vet`、`go test`（连真 Postgres 和 Redis 服务容器）、`golangci-lint` |
| `node` | 契约生成一致性（`generate:api` 后 `git diff --exit-code` 生成类型）、`lint`、`typecheck`、`test`、`build` |
| `compose-smoke` | `compose-smoke.ps1` 全链路 |
| `multi-replica-drill` | `multi-replica-drill.ps1`（默认 2 个 api 副本 + 3 个 http-worker） |

**`load-test.ps1` 不在 CI 里**：它要跑满 100 资产并断言吞吐，在共享 runner 上
既慢又不稳，保留为手工验收。`multi-replica-drill` 进 CI 是因为它测的东西没有替代
路径——`compose-smoke` 走的是 API 逐资产建任务那条与调度器无关的路径，碰不到
迁移 000012 的部分唯一索引。

`golangci-lint` 锁到具体版本（`v1.64.8`）而不是 `latest`：上游发布新 linter 规则
不应让历史提交凭空变红。

**Q：浏览器登录态断言是怎么做的？为什么不能用简单办法？**

用的是 `smoke-target` 容器上的第二个 nginx `server_name`（`smoke-login`）和独立
文档根，**没有**给 `location /` 加 cookie 门控：那样会让每次未认证检测都看到登录页，
上面的内容篡改断言就会因为错误的原因通过。cookie 故意不带 `Secure`（该栈是明文
http，Chromium 会丢弃 Secure cookie），变量名必须是 `session` 以对上 nginx 的
`$cookie_session`。

**Q：这条断言证明了什么？**

证明的是**链路**而不是凭据正确性：nginx 不校验用户名密码，所以它证明的是控制面
解密了绑定到该租户+资产的凭据块、送给了 worker、worker 填进了表单、cookie 存活到
目标页导航、抓到的快照是受保护页。真正让第二步有意义的是第一步的负控——两步只差
一个 `AuthConfig`，结果却相反。这一点已实测：把 `AuthConfig` 从策略写入里去掉后
脚本以退出码 1 失败并报 `authenticated browser check saw 'Smoke login'`。

**Q：环境变量会影响 smoke 的断言吗？**

不会。`compose-smoke.ps1` 会固定 `LOGIN_MAX_FAILURES=5` 后再断言锁定行为，因此本机
环境变量不会影响该断言。

**Q：压测会复用 `default` 租户吗？**

**不会。** 压测脚本的每个资产生成一个独立主机名（`load-NNN.webmonitoring.test`，
通过 smoke-target 容器的 Docker DNS 别名解析）和独立文档根，从而对每个资产执行一次
真实的 FILE 验证，而不是批量伪造验证状态。每次运行都为每个分片新建租户（slug 带
运行 ID）：Compose 具名卷会跨运行保留，复用 default 会让「每个租户只看到自己的资产」
这类计数断言取决于上一次运行的残留数据，脚本就不再可重复。

### 浏览器模式压测

```powershell
.\scripts\load-test.ps1 -Mode BROWSER -AssetCount 100 -Tenants 2 -HttpWorkers 3 `
    -MinimumJobsPerSecond 1 -TimeoutSeconds 1800
```

2026-09-20 实测结果：

```
assets          : 100 across 2 tenant(s)
verified+enabled: 100
detection jobs  : 100 SUCCEEDED
wall clock      : 85.2s
throughput      : 1.17 jobs/s
evidence items  : 500
incidents       : 0 (expected 0 before baseline approval)
```

**Q：吞吐由什么决定？加 browser-worker 副本有用吗？**

**吞吐由 `-HttpWorkers` 决定，不是由 browser-worker 决定。** 每个 http-worker 副本
一次只消费一个任务：`internal/jobqueue/redis.go` 用 `XReadGroup Count: 1` 并在循环里
同步处理，所以吞吐约等于副本数除以单次检测耗时。**扩容 `browser-worker` 不会提高吞吐。**

（单副本 12 资产的一次小规模测量是 0.04 任务/秒，但那个数字被固定开销主导，
不适合当作单次检测耗时来推算。）

**Q：浏览器压测为什么有时会报 `JOB_REJECTED`？**

`-Mode BROWSER` 下脚本会容忍调度器抢先排队：控制面每 30 秒给没有在途任务的资产排
一次检测，长准备阶段里它会赢得竞争，此时手工 POST 会因迁移 000012 的活跃任务唯一
索引返回 `JOB_REJECTED`。**那是同一个检测被排队，不是失败**，脚本改为接管已经在途
的任务。

```powershell
# 默认 100 资产 / 2 租户 / 吞吐下限 1 任务每秒 / 超时 300 秒
.\scripts\load-test.ps1
.\scripts\load-test.ps1 -AssetCount 100 -Tenants 4 -MinimumJobsPerSecond 2 -KeepRunning
```

```powershell
# 默认 40 资产 / 1 租户 / api=2 / http-worker=3
.\scripts\multi-replica-drill.ps1
# 变异检查：先删掉活跃任务唯一索引，演练应当失败并报出重复任务数
.\scripts\multi-replica-drill.ps1 -DropActiveJobIndex
```

**Q：多副本演练和压测的区别是什么？**

区别在于**谁创建任务**：压测通过 API 逐资产手动创建，走不到调度器；演练不创建任何
任务，让每个控制面副本的调度器自己排队，然后断言任务数恰好等于资产数 —— 这正是
`due()` 先读后判无法保证、由迁移 `000012` 的部分唯一索引兜住的那件事。竞态窗口是
每个副本启动时的第一次 `scheduleAll()`，因此脚本在验证完成后会 `restart api`，
让所有副本同时重启把这个窗口放大。

**Q：扩容 api 之后为什么访问不了宿主端口了？**

`api` 被扩容后不能再占用宿主端口，脚本为此在 overlay 里加了一个按请求解析 DNS 的
nginx 入口（`api-entry`，宿主 18082）。用变量写 `proxy_pass` 才会走 resolver，否则
nginx 只在启动时解析一次，`restart api` 换了容器 IP 之后就指向已经不存在的副本。

**Q：三个脚本能同时跑吗？**

能。测试数据保存在 `.data-smoke`、`.data-loadtest` 与 `.data-multireplica`，三个目录
已被 Git 忽略。三个脚本使用不同的 Compose 项目名和主机端口（smoke：API 18080 /
控制台 18088；压测：API 18081；多副本演练：API 18082），可以并存运行。

### 镜像准备

**Q：首次运行要拉哪些镜像？**

`golang`、`node`、`nginx`、`postgres`、`redis`、`minio`、`gcr.io/distroless` 和
`mcr.microsoft.com/playwright` 基础镜像。

**Q：拉 playwright 镜像失败怎么办？**

受限网络中 MCR 的 IPv6 路由可能被重置，表现为
`docker pull mcr.microsoft.com/playwright:...` 连接失败；**重试即可**（Docker 会在
IPv6 失败后回退 IPv4），也可在具备网络的机器上预先拉取并导入镜像。基础镜像缺失时
`docker compose up` 会在构建 `browser-worker` 阶段失败，**这是构建环境问题，
不是应用缺陷**。

**Q：构建时依赖下载很慢怎么办？**

构建阶段的 npm/Go 依赖下载走的是 buildkit 的默认网桥，带宽可能远低于宿主机直连
（实测同一 tarball 25 秒内默认网桥约 0.5 MB、host 网络约 3 MB，且默认网桥更容易被
重置）。需要时可切换到 host 网络：

```powershell
$env:BUILD_NETWORK = "host"
docker compose -f deployments/compose/docker-compose.yml build
```

`BUILD_NETWORK` 默认是 `default`，即 compose 原本的行为；**只有显式设置才会改变
构建网络**。

**Q：registry 连元数据都频繁重置怎么办？**

可以在宿主机预热 pnpm store 再灌进构建缓存，让镜像里的
`pnpm install --frozen-lockfile` 基本走离线。`console` 与 `browser-worker` 的
Dockerfile 共用 `webmonitoring-pnpm-store` 这个 buildkit 缓存挂载，因此只需灌一次
（在仓库外的临时目录执行，不污染工作区）：

```powershell
mkdir D:\pnpm-seed\work\apps\console, D:\pnpm-seed\work\apps\browser-worker
Copy-Item package.json,pnpm-workspace.yaml,pnpm-lock.yaml D:\pnpm-seed\work
Copy-Item apps\console\package.json D:\pnpm-seed\work\apps\console
Copy-Item apps\browser-worker\package.json D:\pnpm-seed\work\apps\browser-worker
cd D:\pnpm-seed\work
pnpm fetch --store-dir D:\pnpm-seed\v11 `
  --fetch-retries=20 --fetch-retry-maxtimeout=180000 --fetch-timeout=900000 --network-concurrency=6
```

`pnpm fetch` 必须在一个没有 `node_modules` 的目录里跑：pnpm 检测到 store 目录变化时
会要求删除 modules 目录，在交互式终端之外会直接中止。

```dockerfile
FROM node:24-alpine
COPY v11 /seed
RUN --mount=type=cache,id=webmonitoring-pnpm-store,target=/root/.local/share/pnpm/store/v11,sharing=locked \
    cp -a /seed/. /root/.local/share/pnpm/store/v11/
```

```powershell
docker build -f D:\pnpm-seed\seed.Dockerfile -t pnpm-store-seed D:\pnpm-seed
```

之后 `docker compose build` 中 `pnpm install` 会报告 `reused <N>, downloaded 0`。
构建镜像时无论是否走离线 store，仍会向 registry 拉取 lockfile 元数据做 supply-chain
校验（约 2 分钟），这是 pnpm 的校验步骤，**不是下载失败**。

`console` 与 `browser-worker` 的 `pnpm <workspace> build` 步骤复用了同一个
`webmonitoring-corepack` 缓存挂载，因此不会在每次构建时重新下载 corepack 固定的
pnpm 版本。

---

## 升级失败怎么办？

**Q：回滚策略是什么？**

保留日志和审计记录，回滚 API/Worker 镜像版本；**不要回滚已经执行的破坏性数据库迁移**。

**Q：从哪里看状态？**

- `GET /healthz`：进程是否存活。
- `GET /readyz`：PostgreSQL、Redis、MinIO 和 Browser Worker 是否就绪。
- `GET /metrics`：请求、任务、告警和事件指标。
- Redis detection/alert Stream 的 pending 和 dead-letter 状态。

**Q：队列的重试参数怎么调？**

默认任务领取租约为 5 分钟，pending 重领最短空闲时间为 120 秒。**不要把
`QUEUE_MIN_IDLE_SECONDS` 调低到单次浏览器检测的最大执行时间以下**，否则仍在执行的
消息可能被重复领取。可通过 `QUEUE_MAX_DELIVERIES`、`QUEUE_RETRY_BASE_MILLISECONDS`、
`ALERT_QUEUE_MAX_DELIVERIES`、`ALERT_MAX_ATTEMPTS` 和
`ALERT_RETRY_BASE_MILLISECONDS` 调整重试策略。

**Q：请求限速怎么调？**

按 `(租户, 端点类别)` 分三桶滑动窗口限速，默认每租户每分钟：

| 变量 | 类别 | 默认 | 生产上限 |
| --- | --- | --- | --- |
| `RATE_LIMIT_READ_PER_WINDOW` | 读（列表、详情） | 600 | 100000 |
| `RATE_LIMIT_WRITE_PER_WINDOW` | 写（登记、流转、编辑） | 120 | 20000 |
| `RATE_LIMIT_BULK_PER_WINDOW` | 重操作（导入、检测触发、保留执行） | 12 | 2000 |

超限返回 `429 RATE_LIMITED`。生产配置校验（`internal/config/production.go`）
把这三个值卡在**区间的两端**：超过上限会被拒，理由是「等于没有限速」；设为 `0`
或负数也会被拒，因为 `0` 意味着给那一类请求**关掉**限速。两者都是启动失败，
不是静默钳制——一个只看上限的校验会放过后者。

冒烟脚本（`compose-smoke.ps1`）在几分钟内从一个租户跑完整个流程，远超生产配额，
因此在 smoke overlay 里把三类都抬高——配额约束的是真实租户的爆炸半径，对一次性
的临时栈没有意义。

---

## 保留期维护

默认证据保留 90 天、审计保留 365 天。

**Q：保留任务什么时候跑？**

控制面进程同时运行后台保留任务（`RETENTION_INTERVAL_HOURS`，默认 24 小时，设为 0
则只保留手动入口）。后台任务在启动时先跑一次，此后按间隔逐租户执行；
**已停用（disabled）的租户会被跳过**，其证据不会在人工复核期间被清理。

**Q：怎么安全地手工执行？**

手动入口与后台任务共用同一段逻辑（`internal/maintenance`），因此 dry-run 的结果
就是下一次后台执行会做的操作。管理员应先调用 dry-run，确认过期数量和事件引用，
再显式执行：

```powershell
$headers = @{ "X-CSRF-Token" = $csrfToken }
Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/maintenance/retention `
  -Headers $headers -WebSession $session -ContentType "application/json" `
  -Body '{"execute":false}'
```

**Q：什么不会被删？**

被事件引用的证据不会删除；对象删除、元数据删除、审计删除和失败原因都会记录。

---

## Trace 采集（可选）

**Q：默认采集 trace 吗？**

不采集。需要时把 API 和 http-worker 的 `OTEL_EXPORTER_OTLP_ENDPOINT` 指向
OTLP/HTTP 采集器（例如 `http://otel-collector:4318`），**两个服务必须配同一地址**，
否则同一任务的 span 会落到不同后端。采样率用 `OTEL_TRACES_SAMPLER_ARG`
（0.0-1.0，默认 1）。

**Q：采集器挂了会影响检测吗？**

不会。导出失败只记录在 SDK 内部，控制面和 Worker 照常运行。停用采集只需清空该
变量并重启；span 立即退回 no-op。

**Q：导出的内容里有敏感数据吗？**

导出内容不含客户 URL 凭据、通知签名、收件人和页面内容（见
`docs/architecture.md` 的可观测性一节）。**采集器本身仍应视为敏感基础设施。**

**Q：span 之间能串成一条 trace 吗？**

消费者 span 与 API span 目前**只通过 `job_id`/`asset_id` 属性关联，不是同一 trace**。

---

## TLS 终止（生产）

**Q：默认部署是 HTTPS 吗？**

不是。单机 Compose 默认把 API 发布在 `8080`、控制台发布在 `8088`，都是明文 HTTP。
生产部署叠加 TLS overlay，由 nginx 边缘终止 TLS，并且**只由边缘发布端口**：

```bash
TLS_CERT_DIR=/etc/letsencrypt/live/example.com \
  docker compose -f deployments/compose/docker-compose.yml \
                 -f deployments/compose/docker-compose.tls.yml up -d
```

`TLS_CERT_DIR` 必须包含 `fullchain.pem` 和 `privkey.pem`。证书不随仓库分发，
`deployments/.gitignore` 已排除 `deployments/nginx/certs/`、`*.pem` 和 `*.key`。

**Q：为什么 overlay 要摘掉 API 的 8080 和控制台的 8088？**

因为留着它们就存在一条**绕过边缘的明文路径**：既绕开安全响应头，也绕开边缘对
`/metrics` 的屏蔽，还会让标记为 secure 的会话 Cookie 有机会走 http 发出。这不是洁癖。

**Q：生产环境有哪些强制条件？**

两个**已经被强制**的条件（`internal/config/production.go`，不满足则**进程启动失败**，
而不是仅打印警告）：

| 变量 | 要求 | 原因 |
|---|---|---|
| `SESSION_SECURE` | `true` | 会话 Cookie 只在 HTTPS 上发送；`APP_ENV=production` 时非 `true` 直接启动失败，API 与 http-worker 都受此约束 |
| `POSTGRES_SSLMODE` | 非 `disable` | `POSTGRES_DSN` 里写入 `sslmode=${POSTGRES_SSLMODE:-disable}`；生产环境仍为 `disable` 会启动失败并指出该变量 |

**Q：`sslmode` 这条门是破坏性变更吗？**

**是。** 既有 `APP_ENV=production` 部署在升级后若未设置 `POSTGRES_SSLMODE`，会拒绝
启动。这是有意为之——把传输加密从文档建议变成不满足就不跑。

**Q：为什么边缘对 `/metrics` 返回 404？**

控制面的 `/metrics` 挂在不带认证的根 mux 上，**对外发布等于把资产数、任务吞吐和
错误率暴露给任何能访问该主机的人**。`/healthz` 与 `/readyz` 保留对外，外部负载
均衡需要它们。

**Q：为什么 `/api/` 要关掉 `proxy_buffering`？**

因为 `GET /api/v1/events/stream` 是 SSE，缓冲会让控制台的实时刷新成批到达或干脆不到。

**Q：本仓库带采集器吗？**

**不带。** 控制面按 Prometheus 文本格式暴露 `/metrics`，但 scrape 配置、告警规则和
时序存储都由部署方提供——采集器跑在 Compose 网络的内部即可读到 `/metrics`
（边缘对外屏蔽了它，正是为了让采集器成为唯一消费者）。需要指标时，自行加一个
Prometheus/Grafana Agent 服务挂到同一网络，按 `api:8080/metrics` 与 `http-worker`
对应的地址抓取；**不要为此把 `/metrics` 重新发布到主机**。

### 证书续期

**Q：仓库会帮我签发或续期证书吗？**

**不会。** `TLS_CERT_DIR` 是部署方准备的目录，里面放 `fullchain.pem` 和
`privkey.pem`；至于证书从哪来——主机上的 certbot、别处的 ACME 客户端、企业 CA，
还是采购来的——是部署决策，本仓库不替部署方做。把某个 ACME 客户端烤进 overlay
就等于替所有人做了这个决定。

**Q：续期之后边缘会自己发现吗？**

**不会。** nginx 在启动时读一次证书，之后一直用内存里的副本，磁盘上换了新证书也
看不见，直到收到 reload 信号。`scripts/reload-tls.ps1` 补的就是这一段：

```powershell
# 续期完成后，把新证书装进运行中的边缘（不重启容器，不断开已有连接）
.\scripts\reload-tls.ps1 -CertDir C:\certs\example.com

# 只确认边缘当前在对外提供哪张证书，不做任何改动
.\scripts\reload-tls.ps1 -CertDir C:\certs\example.com -CheckOnly
```

脚本先用 `nginx -t` 校验配置，再发 `SIGHUP`。

**Q：为什么用 reload 而不是 `docker compose restart edge`？**

**不重启容器是有意的**：重启会丢掉所有在途请求，包括控制台正挂着的 SSE 流；
而 reload 是 nginx 在请求之间处理的信号，新老 worker 平滑交接。

**Q：怎么保证续期后一定有人 reload？**

把它接到已经在做续期的定时任务上（cron、systemd timer、或 ACME 客户端的续期后
钩子），让 reload 跟着续期走，而不是指望有人记得手动跑。

**Q：`-CheckOnly` 读的是磁盘上的文件吗？**

不是。它通过**实际 TLS 握手**读边缘对外提供的证书，而不是读磁盘上的文件——
文件是新的和边缘已经加载了它，不是同一件事，而后者才是要确认的。拿它报告的到期
时间和 `fullchain.pem` 里的 not-after 时间对比；如果还是旧的那张，说明 reload
没生效。
