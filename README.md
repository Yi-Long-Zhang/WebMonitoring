# WebMonitoring

WebMonitoring 是一个面向私有化部署的网页篡改监测与安全事件处置平台。

## 当前实现

- Go 控制面和 HTTP 检测器
- Vue 3 管理控制台
- Playwright 浏览器检测 Worker
- 资产授权、基线、差异、事件、证据、告警和审计
- PostgreSQL、Redis、MinIO 的 Docker Compose 部署基线

## 本地嵌入模式

仅调试 API 和控制台时，可不启动基础设施。该模式使用进程内存保存业务
数据，使用本地目录保存证据，API 退出后业务数据会丢失：

```powershell
pnpm install
go run ./apps/api
pnpm dev:console
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
pnpm typecheck
pnpm test
pnpm build
```

产品和工程约束参见 [docs/README.md](docs/README.md)。
