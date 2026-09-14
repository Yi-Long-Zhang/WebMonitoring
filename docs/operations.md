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

默认证据保留 90 天、审计保留 365 天。管理员应先调用 dry-run，确认过期数量和
事件引用，再显式执行：

```powershell
$headers = @{ "X-CSRF-Token" = $csrfToken }
Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/maintenance/retention `
  -Headers $headers -WebSession $session -ContentType "application/json" `
  -Body '{"execute":false}'
```

被事件引用的证据不会删除；对象删除、元数据删除、审计删除和失败原因都会记录。
