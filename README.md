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

- **多副本（HA）不支持**：调度器 30 秒轮询、保留策略清理和 Worker 消费者身份
  都没有跨副本协调，多副本会重复排队检测任务、重复清扫证据。当前只支持单个
  控制面副本和单个 HTTP Worker 实例。
- **完整事件时间线**：事件只保留发生次数和最后发生时间，每次复现会覆盖上一轮
  信号集；事件由检测 Worker 创建时不写审计记录，无法事后重建历史。
- 细粒度权限、任意租户数据的跨租户可见与审批工作流（super-admin 当前仅限
  租户 CRUD 与播种租户管理员）。
- Secret Manager 集成：当前凭据由 `ALERT_SECRET_KEY` 单一主密钥加密，没有密钥
  版本和轮换路径，轮换该密钥会导致已存密文不可解密。
- 控制台接线：keyset 分页、任务取消、钉钉/邮件渠道创建、租户启停、基线快照
  预览等后端已具备的能力尚未接入界面；列表默认只显示第一页。
- 部署形态：只有单机 Docker Compose，没有 Kubernetes；无 TLS 终止、除
  browser-worker 外无 CPU/内存限制；Compose 中 Redis 未设密码、PostgreSQL 使用
  `sslmode=disable`。
- 真实 100 资产浏览器压测、多副本演练和外部对象存储验证尚未执行。

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
