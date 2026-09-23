# WebMonitoring

面向私有化部署的网页篡改监测与安全事件处置平台。持续检测**已授权**的网页是否
发生异常变化，并给出可核查的证据、差异和处置记录。

本文用问答形式回答：**这是什么、现在能不能用、怎么跑起来**。
详细的文档索引见 [docs/README.md](docs/README.md)。

---

## 这是什么？

**Q：一句话说清它做什么？**

对已授权的网页做定时检测，发现异常变化时给出证据、差异信号和处置记录。

**Q：它不是什么？**

不是 WAF、不是主机入侵检测、不是漏洞扫描器，也不会自动修改你的网站。
它只做**监测与取证**。

**Q：主要能发现什么？**

新增外部脚本、恶意跳转、明显内容替换、表单/iframe/链接变化、响应头与 TLS 变化，
以及浏览器渲染后的弹窗、跳转和下载行为。

---

## 现在能用吗？

**Q：直接回答。**

**功能闭环能用，生产上线看场景。**

- **能用**：内网/私有化部署、**单租户或少量可信租户**、给运维或安全团队内部使用。
- **别用**：多租户 SaaS（无资源级授权）、强合规隔离场景（无双人审批、主密钥明文
  进环境变量）、依赖外部 AWS S3 的场景（未实机验证）。

**Q：已经实测通过的是什么？**

| 演练 | 结果 |
| --- | --- |
| 单机 Compose 验收（2026-09-15） | 通过（含登录锁定、keyset 分页、暂停/恢复、任务取消、控制台 Playwright E2E） |
| 100 资产 HTTP 压测 | 100 任务全部 SUCCEEDED，吞吐 32.8 任务/秒（下限 1.0），0 假阳性 |
| 100 资产浏览器压测（2026-09-20） | 100 个 BROWSER 任务全部成功，500 条证据，85.2 秒，1.17 任务/秒 |
| 多副本演练（2026-09-20） | `--scale api=2 --scale http-worker=3`，40 资产 40 任务，0 重复分组 |
| 备份恢复（2026-09-15） | 1198 个受校验文件，恢复后 `/readyz`、SHA-256、任务数、Redis pending 全通过 |

**Q：现在明确不能干什么？**

- **资源级授权与双人审批**：角色只有 ADMIN / OPERATOR / AUDITOR / SUPER_ADMIN
  四种，按端点做粗粒度校验。同租户内 ADMIN 能读写该租户全部资源。
- **多租户运营**：无配额计费、无租户自注册、无跨租户管理员视图。
- **外部 KMS/Vault**：主密钥以明文环境变量注入。版本化轮换和再加密扫描都已实现，
  但再加密**尚未在真实数据上执行过**。
- **外部对象存储**：MinIO 已跑通，但外部 S3 演练**尚未执行**。
- **Kubernetes**：只有单机 Docker Compose。

**Q：部署形态为什么是单机 Compose？**

这是**有意选择而非欠债**。`AGENTS.md` 的约束是「确有规模依据后使用 Kubernetes」，
本项目尚无该依据，因此不预先引入 K8s 或 Helm。Compose 已加固（长跑服务设 CPU/内存
限制、日志轮转、四个应用服务开 `no-new-privileges`、Redis 可设密码、生产拒绝 `sslmode=disable`），
并提供 nginx TLS 终止 overlay 与 `scripts/reload-tls.ps1`。故障处置见
[docs/runbooks/](docs/runbooks/README.md)。

**Q：可观测性怎么接？**

控制面按 Prometheus 文本格式暴露 `/metrics`，但**本仓库不含采集器**——
scrape 配置、告警规则与时序存储由部署方提供。需要指标时把采集器接入 Compose
网络抓取内部地址（`api:8080/metrics`），不要为此把 `/metrics` 重新发布到主机。

---

## 怎么跑起来？

### 只想调 API 和前端（无需基础设施）

用进程内存保存业务数据、本地目录存证据，**API 退出后业务数据丢失**：

```powershell
corepack pnpm install
go run ./apps/api
corepack pnpm dev:console
```

控制台在 `http://localhost:5173`，Vite 把 `/api` 代理到 `http://localhost:8080`。
默认本地账号 `admin`，口令取自 `deployments/compose/.env.default`
（**只允许用于开发**）。改这个默认口令 = 改那一个文件；未设 `ADMIN_PASSWORD`
时 API 回落到它，设了 `ADMIN_PASSWORD`（或 `ADMIN_PASSWORD_FILE`）则以环境
为准。`APP_ENV=production` 时既不允许用这个默认口令，也不允许不设口令就启动。

登录后可用顶栏用户菜单的「修改密码」自助改密——播种出来的平台超管不会被任何
管理员重置，自助入口是它唯一能改口令的通道。

### 完整 Compose 模式

需要 Docker Compose，会启用 PostgreSQL、Redis、MinIO、独立 HTTP Worker 和
Playwright Worker。首次启动前复制环境文件，并至少替换管理员密码、数据库密码、
对象存储密钥和 Worker Token：

```powershell
Copy-Item .env.example .env
docker compose -f deployments/compose/docker-compose.yml up --build
```

打开 `http://localhost:8088`。API 健康检查在 `http://localhost:8080/healthz`。
当前 Compose 默认使用 HTTP，因此 `SESSION_SECURE=false`；
**部署到 HTTPS 反向代理后必须改为 `true`**。

**Q：启动失败最常见的原因？**

`APP_ENV=production` 下的配置门禁会**拒绝启动**而不是仅打印警告。见下面的环境变量表。

---

## 环境变量

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
| `APP_ENV` | 设为 `production` 时启动前校验生产门禁 |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | 首次启动播种的平台管理员；密码至少 12 字节。`ADMIN_PASSWORD` 留空时回落到 `deployments/compose/.env.default`，另可用 `ADMIN_PASSWORD_FILE` 指向自定义文件 |
| `POSTGRES_SSLMODE` | 写入 `POSTGRES_DSN` 的 `sslmode`；生产不得为 `disable` |
| `HTTP_ADDR` | API 监听地址（http-worker 用 `WORKER_HTTP_ADDR`） |
| `DATA_DIR` | 未配 S3 时本地证据目录 |
| `WORKER_TOKEN` / `WORKER_ID` | worker 向控制面认证的凭据，以及消费者身份（未设时按 hostname 生成） |
| `EVIDENCE_SIGNED_URL_TTL_SECONDS` | 短时签名证据 URL 的有效期，默认 300 |
| `EVIDENCE_RETENTION_DAYS`、`AUDIT_RETENTION_DAYS` | 保留期，默认 90 / 365 |
| `RETENTION_INTERVAL_HOURS` | 后台保留任务间隔，默认 24；设 0 则只保留手动入口 |
| `LOGIN_MAX_FAILURES`、`LOGIN_LOCKOUT_MINUTES` | 登录失败多少次后锁定，以及锁定时长 |
| `ALERT_GATE_WINDOW_SECONDS`、`ALERT_QUEUE_MIN_IDLE_SECONDS` | 告警限额窗口与队列重领最短空闲 |
| `SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM` | EMAIL 渠道的出站邮件中继；`SMTP_HOST` 留空则关闭 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` 等 `OTEL_*` | Trace 导出，默认 no-op |
| `BUILD_NETWORK` | 仅影响 `docker compose build` 的构建网络 |

限速与告警投递的配额（不设置则用内置默认）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RATE_LIMIT_READ_PER_WINDOW` / `_WRITE_` / `_BULK_` | 600 / 120 / 12 | 每窗口的读、写与重操作配额 |
| `RATE_LIMIT_WINDOW_SECONDS` | 60 | 上述窗口长度 |
| `ALERT_MAX_PER_CHANNEL_PER_WINDOW` | 60 | 单渠道每窗口投递上限 |
| `ALERT_FAILURE_THRESHOLD` | 5 | 连续失败多少次后熔断该渠道 |
| `ALERT_CIRCUIT_COOLDOWN_SECONDS` | 300 | 熔断后多久放一个探测请求 |

**Q：为什么生产环境设成 0 会被拒绝启动？**

`APP_ENV=production` 会校验这些配额：设为 0 直接拒绝启动（关掉的控制和正常工作的
控制在出事那天之前无法区分）。读/写/重操作三项还各有上限（100000 / 20000 / 2000），
超过即视为「等同于无限制」而拒绝。告警投递的两项只拒绝 0，不设上限——繁忙的安装
可能需要很大的额度。

**Q：`JOB_LEASE_SECONDS` 为什么不能随手调低？**

策略超时上限 90 秒，BROWSER 检测要花两次，生产门禁因此要求它严格大于 270 秒
（3 × 90）。调低会让仍在执行的任务被同伴副本标记 `TIMED_OUT`。

**Q：怎么生成主密钥？**

```powershell
$bytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToBase64String($bytes)
```

---

## Go 依赖

**Q：怎么拉依赖？**

```powershell
go mod download
go mod tidy
```

**Q：为什么不能用 `go get -u all`？**

该命令会尝试升级所有依赖，并可能把依赖模块中的内部目录当作独立包解析，产生诸如
`minio-go/v7/internal/json` 或 `go-redis/v9/internal/rand` 不存在的错误。
升级依赖应指定明确模块：

```powershell
go get github.com/minio/minio-go/v7@latest
go get github.com/redis/go-redis/v9@latest
go mod tidy
```

需要在当前 PowerShell 会话走代理下载时：

```powershell
$env:HTTP_PROXY="http://127.0.0.1:7890"
$env:HTTPS_PROXY="http://127.0.0.1:7890"
$env:http_proxy=$env:HTTP_PROXY
$env:https_proxy=$env:HTTPS_PROXY
```

---

## 验证

**Q：改完之后应该跑什么？**

```powershell
go build ./...
go vet ./...
go test ./...

corepack pnpm typecheck
corepack pnpm lint
corepack pnpm test
corepack pnpm build
```

**Q：改了 API 契约有什么额外门禁？**

改动了 `api/openapi.yaml` 后必须重新生成前端类型并确认没有漂移，这是 CI 的硬门禁：

```powershell
corepack pnpm generate:api
git diff --exit-code apps/console/src/types/generated/api.d.ts
```

**Q：还有别的验收演练吗？**

有。Compose smoke、100 资产压测、多副本演练、备份恢复的完整清单和运行前提见
[docs/operations.md](docs/operations.md)，故障处置见
[docs/runbooks/](docs/runbooks/README.md)。

---

产品和工程约束参见 [docs/README.md](docs/README.md)。
