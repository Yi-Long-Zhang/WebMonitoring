# WebMonitoring

WebMonitoring 是一个面向私有化部署的网页篡改监测与安全事件处置平台。

## 当前阶段

本项目处于 **MVP 可运行、安全边界收敛和部署路径验证阶段**。当前可运行的
主流程是：在默认租户下登记资产，完成 HTTP 文件授权验证，启用 HTTP 或
浏览器检测，审批候选基线，查看差异事件、证据、通知投递和审计记录。

已具备：

- Go 控制面、Go HTTP Worker、Playwright 浏览器 Worker 和 Vue 3 控制台。
- URL 安全校验、重定向复核、检测超时和响应大小限制。
- 候选基线、批准/拒绝/废弃、差异信号、事件聚合和处置状态。
- HTML、快照和截图证据保存；下载和报告导出会校验大小及 SHA-256。
- Webhook/企业微信通知、投递记录、重试和事件级去重。
- 内存/本地证据开发模式，以及 PostgreSQL、Redis、MinIO Compose 路径。

当前不应视为生产完成：

- 完整租户/站点管理、细粒度权限、跨租户管理员和审批工作流。
- DNS 或 HTML 元标签授权验证、登录态检测和 Secret Manager 集成。
- 短时签名 URL、保留期管理、邮件/钉钉适配器和批量导入导出。
- 游标分页、Prometheus/OpenTelemetry、容量测试和完整 PostgreSQL 集成测试。
- Docker Compose 实测、Redis pending 消息恢复和生产升级演练。

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
