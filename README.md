# WebMonitoring

WebMonitoring 是一个面向私有化部署的网页篡改监测与安全事件处置平台。

## 当前阶段

本项目处于 **MVP 可运行、安全边界收敛和单机 Compose 正式验收加固阶段**。当前可运行的
主流程是：在默认租户下登记资产，完成 HTTP 文件授权验证，启用 HTTP 或
浏览器检测，审批候选基线，查看差异事件、证据、通知投递和审计记录。

已具备：

- Go 控制面、Go HTTP Worker、Playwright 浏览器 Worker 和 Vue 3 控制台。
- URL 安全校验、重定向复核、检测超时和响应大小限制。
- 三种所有权验证：HTTP 文件令牌（FILE）、DNS TXT（DNS）和 HTML meta 标签（META）。
- 候选基线、批准/拒绝/废弃、差异信号、事件聚合和处置状态。
- 事件时间线：每轮复现各自的信号集、状态流转、负责人变更和备注均可追溯。
- HTML、快照和截图证据保存；下载和报告导出会校验大小及 SHA-256。
- 短时签名证据 URL、Webhook/企业微信/钉钉/邮件通知、投递记录、重试和事件级去重。
- 登录态检测（HTTP 路径）：客户凭据 AES-256-GCM 加密、登录地址 SSRF 复核、
  登录后抓取受保护页面；API 响应不泄露凭据。
- 批量导入导出（JSON/CSV，逐行校验与部分失败）、用户/角色管理和受保护的租户删除。
- 内存/本地证据开发模式，以及 PostgreSQL、Redis、MinIO Compose 路径。
- 生产配置门禁、证据和审计保留策略、dry-run 清理以及备份恢复校验脚本。
- 多租户：登录按租户 slug 进入对应租户、session 携带租户、调度器遍历所有租户，
  复合外键在数据库层强制租户一致性。
- 站点（Site）CRUD 与资产站点归属/按站点过滤。
- 平台管理员（SUPER_ADMIN）：租户 CRUD，创建时播种该租户首个管理员账号。
- PostgreSQL/Redis 集成测试、100 资产调度合成测试和 Compose smoke CI 门禁。

单机 Compose 的验收演练已于 2026-09-15 在本机实跑通过（Compose smoke 含控制台
Playwright E2E、100 资产跨租户 HTTP 压测、备份恢复校验），记录见
[docs/operations.md](docs/operations.md) 和
[docs/backup-restore-rehearsal.md](docs/backup-restore-rehearsal.md)。

当前不应视为生产完成：

- 细粒度权限、资源级授权与双人审批。角色矩阵已收敛到单一来源，详见
  [docs/README.md](docs/README.md)。
- 外部 KMS/Vault 集成与密钥再加密扫描。凭据加密已支持密钥版本与轮换
  （`ALERT_SECRET_KEYS` + `security.Keyring`），**仍缺**把存量密文重写为最新版本的
  那次扫描。
- 控制台接线：资产导入导出、用户管理、证据短时签名 URL 和租户删除仍只在 API 层；
  keyset 分页、任务取消、钉钉/邮件渠道、租户启停、权限收口、基线快照预览与审计筛选
  已于 2026-09-20 接入界面。
- 部署形态：只有单机 Docker Compose，没有 Kubernetes。Compose 已加固（全服务
  CPU/内存限制、日志轮转、`no-new-privileges`、Redis 可设密码、生产拒绝
  `sslmode=disable`），并提供了 nginx TLS 终止 overlay。
- **外部对象存储验证尚未执行**：需要真实 S3 凭据，步骤见
  [docs/object-storage-rehearsal.md](docs/object-storage-rehearsal.md)。

已经实测通过的部分（2026-09-20）：

- **多副本演练已跑通**：`--scale api=2 --scale http-worker=3` 下 40 资产对应 40 个检测任务、
  0 个重复的 `(tenant, asset, mode)` 分组；同一次运行删掉活跃任务唯一索引后演练如期失败。
- **100 资产浏览器压测已跑通**：2 租户、100 个 BROWSER 任务全部成功、500 条证据、
  85.2 秒、1.17 任务/秒（3 个 http-worker 副本）。
- 冒烟栈内覆盖浏览器登录态检测（cookie 门控 fixture 的正负两步断言）。

后续：`ReapTimedOut` 不校验任务持有者，当前靠 `JOB_LEASE_SECONDS > 270` 的强制不等式
保证安全。若策略超时将来变成无上限，应改成 worker 心跳 + 按 `last_heartbeat_at` 回收。

## 本地嵌入模式

仅调试 API 和控制台时，可不启动基础设施。该模式使用进程内存保存业务
数据，使用本地目录保存证据，API 退出后业务数据会丢失：

```powershell
corepack pnpm install
go run ./apps/api
corepack pnpm dev:console
```

控制台位于 `http://localhost:5173`，Vite 将 `/api` 代理到
`http://localhost:8080`。默认本地账号为 `admin` /
`change-me-before-production`，只允许用于开发。

## 完整 Compose 模式

完整模式需要 Docker Compose，并启用 PostgreSQL、Redis、MinIO、独立 HTTP
Worker 和 Playwright Worker。首次启动前复制环境文件，并至少替换管理员
密码、数据库密码、对象存储密钥和 Worker Token：

```powershell
Copy-Item .env.example .env
docker compose -f deployments/compose/docker-compose.yml up --build
```

打开 `http://localhost:8088`。API 健康检查位于
`http://localhost:8080/healthz`。当前 Compose 默认使用 HTTP，因此
`SESSION_SECURE=false`；部署到 HTTPS 反向代理后必须改为 `true`。

环境变量职责：

| 变量 | 用途 |
| --- | --- |
| `POSTGRES_DSN` | 业务元数据持久化；未设置时使用进程内存 |
| `REDIS_ADDR` | 检测任务队列；未设置时 API 进程内执行 |
| `S3_ENDPOINT`、`S3_*` | MinIO/S3 证据存储；未设置时写入 `DATA_DIR` |
| `BROWSER_WORKER_URL`、`BROWSER_WORKER_TOKEN` | 浏览器检测服务与认证 |
| `ALERT_SECRET_KEY` | 告警地址和签名密钥的 AES-256-GCM 主密钥（32 字节 Base64） |
| `ALLOW_PRIVATE_TARGETS` | 是否允许检测私网地址，默认 `false` |
| `SESSION_SECURE` | 是否只通过 HTTPS 发送会话 Cookie |

## Go 依赖

本项目使用 Go Modules。首次拉取代码或 `go.mod` 发生变化后，使用以下命令
同步依赖：

```powershell
go mod download
go mod tidy
```

不要使用 `go get -u all` 作为项目初始化或 IDE 同步命令。该命令会尝试升级
所有依赖，并可能把依赖模块中的内部目录当作独立包解析，产生诸如
`minio-go/v7/internal/json` 或 `go-redis/v9/internal/rand` 不存在的错误。
需要升级依赖时，应指定明确的模块，例如：

```powershell
go get github.com/minio/minio-go/v7@latest
go get github.com/redis/go-redis/v9@latest
go mod tidy
```

本机若需要代理下载依赖，可在当前 PowerShell 会话执行：

```powershell
$env:HTTP_PROXY="http://127.0.0.1:7890"
$env:HTTPS_PROXY="http://127.0.0.1:7890"
$env:http_proxy=$env:HTTP_PROXY
$env:https_proxy=$env:HTTPS_PROXY
```

开发环境可生成主密钥：

```powershell
$bytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToBase64String($bytes)
```

## 验证

```powershell
go test ./...
go vet ./...
corepack pnpm typecheck
corepack pnpm test
corepack pnpm build
```

产品和工程约束参见 [docs/README.md](docs/README.md)。
