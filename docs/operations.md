# Compose 运维手册

## 部署和升级

首次部署前复制 `.env.example` 为 `.env`，替换管理员密码、数据库密码、
对象存储密钥、Worker Token 和 `ALERT_SECRET_KEY`。生产环境必须启用 HTTPS
并将 `SESSION_SECURE` 设置为 `true`。

启动或升级：

```powershell
docker compose -f deployments/compose/docker-compose.yml build
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
docker compose -f deployments/compose/docker-compose.yml up -d
docker compose -f deployments/compose/docker-compose.yml ps
Invoke-WebRequest http://localhost:8080/readyz
```

迁移服务只执行向上迁移。`baseline` 只允许在人工确认已有数据库结构与仓库
版本一致后显式执行：

```powershell
docker compose -f deployments/compose/docker-compose.yml run --rm migrate baseline --version 3
```

不得通过删除 PostgreSQL 数据卷来完成升级。迁移 checksum 不一致时必须停止
发布并调查文件或数据库是否被篡改。

## 备份和恢复

备份前暂停会产生大量写入的升级或维护操作，并记录备份时间、版本和校验值。

仓库提供备份脚本，会同时保存 PostgreSQL custom dump、evidence bucket 和
逐文件 SHA-256 manifest。脚本不会把密码写入 manifest：

```powershell
$env:S3_ACCESS_KEY = "..."
$env:S3_SECRET_KEY = "..."
$env:S3_ENDPOINT = "http://localhost:9000"
.\scripts\compose-backup.ps1 -OutputRoot .data-backups
```

```powershell
docker compose -f deployments/compose/docker-compose.yml exec -T postgres `
  pg_dump -U webmonitoring -d webmonitoring --format=custom `
  > .data-dev\webmonitoring-$(Get-Date -Format yyyyMMdd-HHmmss).dump
```

MinIO 证据对象不能只备份 PostgreSQL 元数据。使用客户批准的 S3 兼容备份工具
复制 `evidence` bucket，并保留对象校验和；以 MinIO Client 为例：

```powershell
mc alias set local http://localhost:9000 $env:S3_ACCESS_KEY $env:S3_SECRET_KEY
mc mirror --overwrite local/evidence D:\backups\webmonitoring\evidence
```

恢复时先停止 API 和 Worker，恢复数据库和对象存储，再执行迁移并启动服务：

```powershell
docker compose -f deployments/compose/docker-compose.yml stop api http-worker
Get-Content .data-dev\webmonitoring-backup.dump | `
  docker compose -f deployments/compose/docker-compose.yml exec -T postgres `
  pg_restore -U webmonitoring -d webmonitoring --clean --if-exists
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
docker compose -f deployments/compose/docker-compose.yml up -d api http-worker
```

恢复后必须检查 `/readyz`、事件证据下载、SHA-256 校验和、队列 pending 数量
以及最近任务状态。数据库恢复不能替代证据对象恢复。

恢复脚本默认只校验备份文件和校验值，不执行任何修改。正式恢复必须显式确认：

```powershell
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000 `
  -Execute -ConfirmText RESTORE
```

恢复脚本会停止 API/Worker、恢复数据库和 evidence bucket、执行迁移并重新启动
API/Worker。生产环境执行前必须记录审批、备份版本、RPO/RTO 和回滚方案。

## 验收和故障处理

本机安装 Docker 后执行可重复 smoke test：

```powershell
.\scripts\compose-smoke.ps1
```

脚本会使用 Compose 内部的授权测试目标，不连接外部网站；它覆盖登录、资产
验证、基线审批、HTTP/Browser 检测、事件、证据、报告、控制台关键页面 E2E
以及 API/Worker 重启。
测试数据保存在 `.data-smoke`，该目录已被 Git 忽略。

### 验收清单

| 门禁 | 覆盖内容 | 命令 |
| --- | --- | --- |
| 主链路 smoke | 登录/资产验证/基线审批/HTTP+Browser 检测/事件/证据/报告/控制台 E2E/重启持久化 | `.\scripts\compose-smoke.ps1` |
| 安全边界 smoke | P0-1 登录锁定（N 次失败后即使密码正确也返回 `ACCOUNT_LOCKED`，且不影响其他账户）、P1-5 keyset 分页（`limit`+`cursor` 无重复无遗漏）、P1-6 资产暂停/恢复（暂停后排队返回 `JOB_REJECTED`，恢复不自动启用）、P1-7 任务取消（终态为 `CANCELLED`，迟到结果不得覆盖） | 同上，`compose-smoke.ps1` 已内置 |
| 100 资产压测 | 跨租户播种、真实 FILE 所有权验证、单轮 HTTP 检测吞吐、任务无丢失、证据完整、未批准基线无假阳性 | `.\scripts\load-test.ps1` |
| 备份恢复演练 | 数据库 dump、evidence bucket 清单、恢复后 `/readyz` 与证据校验 | `.\scripts\compose-backup.ps1`、`.\scripts\compose-restore.ps1`（见 [backup-restore-rehearsal.md](backup-restore-rehearsal.md)） |

`compose-smoke.ps1` 会固定 `LOGIN_MAX_FAILURES=5` 后再断言锁定行为，因此本机
环境变量不会影响该断言。压测脚本的每个资产生成一个独立主机名（`load-NNN.webmonitoring.test`，
通过 smoke-target 容器的 Docker DNS 别名解析）和独立文档根，从而对每个资产执行一次真实
的 FILE 验证，而不是批量伪造验证状态。

压测脚本每次运行都为每个分片新建租户（slug 带运行 ID），不复用内置的 `default`
租户：Compose 具名卷会跨运行保留，复用 default 会让"每个租户只看到自己的资产"这类
计数断言取决于上一次运行的残留数据，脚本就不再可重复。

```powershell
# 默认 100 资产 / 2 租户 / 吞吐下限 1 任务每秒 / 超时 300 秒
.\scripts\load-test.ps1
.\scripts\load-test.ps1 -AssetCount 100 -Tenants 4 -MinimumJobsPerSecond 2 -KeepRunning
```

测试数据保存在 `.data-smoke` 与 `.data-loadtest`，两个目录已被 Git 忽略。两个脚本
使用不同的 Compose 项目名和主机端口（smoke：API 18080 / 控制台 18088；压测：API 18081），
可以并存运行。

### 镜像准备

首次运行需要拉取 `golang`、`node`、`nginx`、`postgres`、`redis`、`minio`、`gcr.io/distroless`
和 `mcr.microsoft.com/playwright` 基础镜像。受限网络中 MCR 的 IPv6 路由可能被重置，
表现为 `docker pull mcr.microsoft.com/playwright:...` 连接失败；重试即可（Docker 会在
IPv6 失败后回退 IPv4），也可在具备网络的机器上预先拉取并导入镜像。基础镜像缺失时
`docker compose up` 会在构建 `browser-worker` 阶段失败，这是构建环境问题，不是应用缺陷。

构建阶段的 npm/Go 依赖下载走的是 buildkit 的默认网桥，带宽可能远低于宿主机直连
（实测同一 tarball 25 秒内默认网桥约 0.5 MB、host 网络约 3 MB，且默认网桥更容易被重置）。
镜像构建因此需要时可切换到 host 网络：

```powershell
$env:BUILD_NETWORK = "host"
docker compose -f deployments/compose/docker-compose.yml build
```

`BUILD_NETWORK` 默认是 `default`，即 compose 原本的行为；只有显式设置才会改变构建网络。

若 registry 连元数据都频繁重置，可以在宿主机预热 pnpm store 再灌进构建缓存，
让镜像里的 `pnpm install --frozen-lockfile` 基本走离线。`console` 与
`browser-worker` 的 Dockerfile 共用 `webmonitoring-pnpm-store` 这个 buildkit
缓存挂载，因此只需灌一次（在仓库外的临时目录执行，不污染工作区）：

```powershell
mkdir D:\pnpm-seed\work\apps\console, D:\pnpm-seed\work\apps\browser-worker
Copy-Item package.json,pnpm-workspace.yaml,pnpm-lock.yaml D:\pnpm-seed\work
Copy-Item apps\console\package.json D:\pnpm-seed\work\apps\console
Copy-Item apps\browser-worker\package.json D:\pnpm-seed\work\apps\browser-worker
cd D:\pnpm-seed\work
pnpm fetch --store-dir D:\pnpm-seed\v11 `
  --fetch-retries=20 --fetch-retry-maxtimeout=180000 --fetch-timeout=900000 --network-concurrency=6
```

`pnpm fetch` 必须在一个没有 `node_modules` 的目录里跑：pnpm 检测到 store 目录
变化时会要求删除 modules 目录，在交互式终端之外会直接中止。

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
构建镜像时无论是否走离线 store，仍会向 registry 拉取 lockfile 元数据做
supply-chain 校验（约 2 分钟），这是 pnpm 的校验步骤，不是下载失败。

`console` 与 `browser-worker` 的 `pnpm <workspace> build` 步骤复用了同一个
`webmonitoring-corepack` 缓存挂载，因此不会在每次构建时重新下载 corepack 固定
的 pnpm 版本。

升级失败时保留日志和审计记录，回滚 API/Worker 镜像版本；不要回滚已经执行
的破坏性数据库迁移。检查：

- `GET /healthz`：进程是否存活。
- `GET /readyz`：PostgreSQL、Redis、MinIO 和 Browser Worker 是否就绪。
- `GET /metrics`：请求、任务、告警和事件指标。
- Redis detection/alert Stream 的 pending 和 dead-letter 状态。

默认任务领取租约为 5 分钟，pending 重领最短空闲时间为 120 秒。不要把
`QUEUE_MIN_IDLE_SECONDS` 调低到单次浏览器检测的最大执行时间以下，否则仍在
执行的消息可能被重复领取。可通过 `QUEUE_MAX_DELIVERIES`、
`QUEUE_RETRY_BASE_MILLISECONDS`、`ALERT_QUEUE_MAX_DELIVERIES`、
`ALERT_MAX_ATTEMPTS` 和 `ALERT_RETRY_BASE_MILLISECONDS` 调整重试策略。

## 保留期维护

默认证据保留 90 天、审计保留 365 天。

控制面进程同时运行后台保留任务（`RETENTION_INTERVAL_HOURS`，默认 24 小时，
设为 0 则只保留手动入口）。后台任务在启动时先跑一次，此后按间隔逐租户执行；
**已停用（disabled）的租户会被跳过**，其证据不会在人工复核期间被清理。

手动入口与后台任务共用同一段逻辑（`internal/maintenance`），因此 dry-run 的
结果就是下一次后台执行会做的操作。管理员应先调用 dry-run，确认过期数量和
事件引用，再显式执行：

```powershell
$headers = @{ "X-CSRF-Token" = $csrfToken }
Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/maintenance/retention `
  -Headers $headers -WebSession $session -ContentType "application/json" `
  -Body '{"execute":false}'
```

被事件引用的证据不会删除；对象删除、元数据删除、审计删除和失败原因都会记录。

## Trace 采集（可选）

默认不采集 trace。需要时把 API 和 http-worker 的 `OTEL_EXPORTER_OTLP_ENDPOINT`
指向 OTLP/HTTP 采集器（例如 `http://otel-collector:4318`），两个服务必须配同一
地址，否则同一任务的 span 会落到不同后端。采样率用
`OTEL_TRACES_SAMPLER_ARG`（0.0-1.0，默认 1）。

- 采集器不可达不会影响检测：导出失败只记录在 SDK 内部，控制面和 Worker 照常运行。
- 停用采集只需清空该变量并重启；span 立即退回 no-op。
- 导出内容不含客户 URL 凭据、通知签名、收件人和页面内容（见
  `docs/architecture.md` 的「可观测性」）。采集器本身仍应视为敏感基础设施。
- 消费者 span 与 API span 目前只通过 `job_id`/`asset_id` 属性关联，不是同一 trace。
