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
- 凭据加密的密钥版本与轮换：`ALERT_SECRET_KEYS="v1:<key>,v2:<key>"` 多版本共存，
  新密文盖最高版本；`apps/rewrap`（`--dry-run` / advisory lease / 审计）把存量
  密文重写为最新版本。
- 出站告警的双重保护：每渠道滑动窗口配额（`ALERT_MAX_PER_CHANNEL_PER_WINDOW`）
  与连续失败熔断（`ALERT_FAILURE_THRESHOLD` + `ALERT_CIRCUIT_COOLDOWN_SECONDS`），
  熔断状态在控制台渠道列表可见；`POST /alert-channels/{id}/test` 供投递前验证渠道。
- 通用 API 限速（读/写/重操作三类配额）、CORS（`ALLOWED_ORIGINS`）与 HSTS；
  请求级指标带 `method`/`status`/`path` 标签（路径按集合归并，签名令牌不落入标签）。
- PostgreSQL/Redis 集成测试、100 资产调度合成测试和 Compose smoke CI 门禁。

单机 Compose 的验收演练已于 2026-09-15 在本机实跑通过（Compose smoke 含控制台
Playwright E2E、100 资产跨租户 HTTP 压测、备份恢复校验），记录见
[docs/operations.md](docs/operations.md) 和
[docs/backup-restore-rehearsal.md](docs/backup-restore-rehearsal.md)。

当前不应视为生产完成：

- **资源级授权与双人审批**：角色固定为 `ADMIN` / `OPERATOR` / `AUDITOR` /
  `SUPER_ADMIN` 四种，按端点做粗粒度校验，没有 `asset:read` 这类资源级权限集，
  也没有高风险操作的双人审批。角色矩阵的单一来源见 [docs/README.md](docs/README.md)。
- **外部 KMS/Vault 集成**：主密钥仍以明文环境变量注入（`ALERT_SECRET_KEY` /
  `ALERT_SECRET_KEYS`）。版本化轮换和再加密扫描都已具备，缺的是把主密钥交给
  外部密钥托管，使进程环境里不再出现明文密钥。
- **外部对象存储验证尚未执行**：`docs/object-storage-rehearsal.md` 记录的状态是
  **尚未执行**，需要一组真实 S3 凭据和一个已存在的 bucket。MinIO 上已跑通，
  但区域解析与预签名 URL 这两条路径在 AWS S3 上可能不同，未验证前该结论保持有效。
- `ReapTimedOut` 不校验任务持有者，当前靠 `JOB_LEASE_SECONDS > 270` 的强制不等式
  保证安全。若策略超时将来变成无上限，应改成 worker 心跳 + 按 `last_heartbeat_at` 回收。

部署形态：目前是**单机 Docker Compose**，这是有意选择而非欠债。`AGENTS.md`
的约束是「确有规模依据后使用 Kubernetes」，本项目尚无该依据，因此不预先引入
K8s 或 Helm。Compose 已加固（全服务 CPU/内存限制、日志轮转、
`no-new-privileges`、Redis 可设密码、生产拒绝 `sslmode=disable`），并提供 nginx
TLS 终止 overlay 与 `scripts/reload-tls.ps1`（证书续期后 SIGHUP 重载，不重启容器）；
故障处置见 [docs/runbooks/](docs/runbooks/README.md)。

关于可观测性：控制面按 Prometheus 文本格式暴露 `/metrics`，但**本仓库不含采集器**，
scrape 配置、告警规则与时序存储由部署方提供。需要指标时把采集器接入 Compose
网络抓取内部地址，不要为此把 `/metrics` 重新发布到主机。

已经实测通过的部分（2026-09-20）：

- **多副本演练已跑通**：`--scale api=2 --scale http-worker=3` 下 40 资产对应 40 个检测任务、
  0 个重复的 `(tenant, asset, mode)` 分组；同一次运行删掉活跃任务唯一索引后演练如期失败。
- **100 资产浏览器压测已跑通**：2 租户、100 个 BROWSER 任务全部成功、500 条证据、
  85.2 秒、1.17 任务/秒（3 个 http-worker 副本）。
- 冒烟栈内覆盖浏览器登录态检测（cookie 门控 fixture 的正负两步断言）。

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
| `ALERT_SECRET_KEYS` | 版本化轮换形式（`v1:<key>,v2:<key>`），设置后优先于上一项 |
| `ALLOWED_ORIGINS` | 跨域控制台来源；留空即同源（单机部署默认） |
| `ALLOW_PRIVATE_TARGETS` | 是否允许检测私网地址，默认 `false` |
| `SESSION_SECURE` | 是否只通过 HTTPS 发送会话 Cookie |
| `JOB_LEASE_SECONDS` | 检测任务领取租约，默认 600；不得低于 270（见下方说明） |

限速与告警投递的配额（不设置则用内置默认）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RATE_LIMIT_READ_PER_WINDOW` / `_WRITE_` / `_BULK_` | 600 / 120 / 12 | 每窗口的读、写与重操作配额 |
| `RATE_LIMIT_WINDOW_SECONDS` | 60 | 上述窗口长度 |
| `ALERT_MAX_PER_CHANNEL_PER_WINDOW` | 60 | 单渠道每窗口投递上限 |
| `ALERT_FAILURE_THRESHOLD` | 5 | 连续失败多少次后熔断该渠道 |
| `ALERT_CIRCUIT_COOLDOWN_SECONDS` | 300 | 熔断后多久放一个探测请求 |

`APP_ENV=production` 会校验这些配额：设为 0 直接拒绝启动（关掉的控制和正常工作的
控制在出事那天之前无法区分），读/写/重操作三项还各有上限（100000 / 20000 / 2000），
超过即视为「等同于无限制」而拒绝。告警投递的两项只拒绝 0，不设上限——繁忙的安装
可能需要很大的额度。

`JOB_LEASE_SECONDS` 不是随手可调的：策略超时上限 90 秒，BROWSER 检测要花两次，
生产门禁因此要求它严格大于 270 秒。调低会让仍在执行的任务被同伴副本标记
`TIMED_OUT`。

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
go build ./...
go vet ./...
go test ./...

corepack pnpm typecheck
corepack pnpm lint
corepack pnpm test
corepack pnpm build
```

改动了 `api/openapi.yaml` 后，必须重新生成前端类型并确认没有漂移，这是 CI 的
硬门禁：

```powershell
corepack pnpm generate:api
git diff --exit-code apps/console/src/types/generated/api.d.ts
```

产品和工程约束参见 [docs/README.md](docs/README.md)。
