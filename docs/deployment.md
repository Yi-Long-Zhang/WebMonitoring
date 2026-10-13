# 部署手册

**这一页是流程，不是参考。** 它把一套 WebMonitoring 从零装到可用、加固、上线、
运维，按可以照做的顺序排好；每一步都给出命令，以及**怎么知道这一步成功了**。

问答式参考在 [operations.md](operations.md)，出故障时进
[runbooks/](runbooks/README.md)。

> 状态：**部分实现 —— 单机 Compose 的「本地构建」路径已整条实测；生产 HTTPS 路径
> （`docker-compose.tls.yml`）从未演练过，仓库里没有任何一次运行记录。** 逐条依据见
> 文末「这份手册没覆盖的部分」。**首次在生产上启用 TLS，请当成一次演练来做**，不要
> 当成一条走过很多遍的路。
>
> 2026-10-13 修正了初稿据实的四处缺陷（1.2 的 13 条错误信息路径、3.2 的资产类型下拉、
> 3.3 的授权验证弹窗、4.6 的两个 `0` 开关）；相关小节都留了修改前后的说明。

---

## 第 0 部分：开工前

### 0.1 先确认你要的是哪条路

| 形态 | 清单 | 实测状态 | 什么时候用 |
| --- | --- | --- | --- |
| 无基础设施（内存 + 本地目录） | 不需要 Compose | 已实测，是日常开发路径 | 只想调 API 和前端；**API 退出后业务数据丢失**。见 [README.md](../README.md) 的「怎么跑起来」 |
| 单机 Compose，本地构建镜像 | `deployments/compose/docker-compose.yml` | **已整条实测**（见下方证据） | 默认路径，也是今天唯一确定能跑通的完整部署 |
| 单机 Compose，跑**发布产物** | 上者 + 一份 `image: …@digest` overlay | **缺件：overlay 尚未提供** | 目前做不到。登记在 [roadmap.md](roadmap.md) 的 2.9 判据 6 |
| 生产 HTTPS | 再加 `docker-compose.tls.yml` | **从未演练** | 上生产必须叠它，但先读第 4 部分与文末 |

「单机 Compose 本地构建」这一行的证据：CI 在**没有镜像缓存**的干净 runner 上执行
`compose-smoke.ps1` 与 `multi-replica-drill.ps1` 到底并全绿（run `38206727165`），本机
另有 100 资产 HTTP/浏览器压测、多副本演练、备份与恢复演练。见
[rehearsals.md](rehearsals.md)。

**这一页不教你选基础设施。** 数据库、Redis、对象存储都跑在同一个 Compose 项目里，
只把 API 与控制台发布到主机；这是单机拓扑的定义，不是可以逐项替换的默认值。

### 0.2 主机要多大

下表逐行来自 `deployments/compose/docker-compose.yml` 与
`deployments/compose/docker-compose.tls.yml`。`mem_limit` 是**上限而不是实测峰值**；
把它们相加是为了让你知道最坏情况下 Docker 会试图占用多少内存，而不是推荐配置。

| 服务 | 镜像 / 构建来源 | `mem_limit` | `cpus` | 发布到主机 | 容器内监听 | 具名卷 | 健康检查 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `postgres` | `postgres:17-alpine` | 2g | 2 | 否 | 5432 | `postgres-data` | `pg_isready` |
| `redis` | `redis:8-alpine` | 1g | 1 | 否 | 6379 | `redis-data` | `redis-cli ping` |
| `migrate` | 本地构建 | — | — | 否 | 不监听 | — | — |
| `objectstore` | `versity/versitygw:v1.8.0`（按 digest 钉死） | 1g | 1 | 否 | 9000 | `objectstore-data` | `nc` 探 HTTP 状态行 |
| `browser-worker` | 本地构建（Playwright 底座） | 2g | 2 | 否 | 8081 | —（`read_only` + tmpfs `/tmp`） | `fetch('/healthz')` |
| `api` | 本地构建 | 1g | 2 | **`8080:8080`** | 8080 | — | **无** |
| `http-worker` | 本地构建 | 1g | 2 | 否 | 不监听 | — | **无** |
| `console` | 本地构建（nginx） | 256m | 1 | **`8088:8080`** | 8080 | — | `wget /healthz` |
| `edge`（仅 TLS overlay） | `nginx:1.29-alpine` | 128m | 0.5 | **`80:80`、`443:443`** | 80 / 443 | — | **无** |

`rclone`（备份客户端）与 `rewrap`（密钥轮换）挂在 `tools` profile 上，只有被显式
`run` 时才存在，不占常驻内存。

**内存合计**：基础清单八个常驻服务中七个设了上限，相加 **8.25 GiB**；叠上 TLS overlay
的 `edge` 是 **8.375 GiB**。给主机留出余量——`postgres` 与 `browser-worker` 各要
2 GiB，而 `browser-worker` 还会按检测数派生 Chromium 进程。

**磁盘**：三个具名卷（`postgres-data`、`redis-data`、`objectstore-data`）是全部持久
状态，其中 `objectstore-data` 增长最快（每轮检测都落 HTML/截图证据）。没有可抄的
增长曲线——它等于「资产数 × 检测频率 × 保留天数」，只能在自己的规模上实测。两个可用
的实测锚点，来自一次真实演练（2026-09-15，见
[backup-restore-rehearsal.md](backup-restore-rehearsal.md)）：**1198 个受校验文件、
数据库 dump 386 KiB、证据 2.00 MiB**。另外按默认设置，备份要留 14 份全量，规划磁盘时
先按 14 倍算。容器日志有上限：每服务 `10m × 3`（`x-logging`），十一个服务最坏约
330 MB，不会无限增长。

### 0.3 网络与端口：主机上只看得到几个口

这是排查时最容易搞错的一件事，先记住：

- **不叠 TLS**：主机上只有 `8080`（API）与 `8088`（控制台）。PostgreSQL、Redis、
  对象存储**有意不发布端口**——所以主机上**没有**可用的对象存储端点，需要碰它的工具
  必须跑在 Compose 网络里（见 5.5）。
- **叠了 TLS**：`api` 的 `8080` 与 `console` 的 `8088` 被 `ports: !reset []` **摘掉**，
  主机上只剩 `edge` 的 `80`/`443`。此时 `curl http://localhost:8080` 不通，这是设计
  而不是故障。
- 控制台自己的 nginx 把 `/api/` 反代到 `api:8080`，所以整套是**单一来源**——
  `ALLOWED_ORIGINS` 留空（同源）就是正确配置，见 4.3。

### 0.4 软件前置

- **Docker 与 Compose v2**：`docker compose version` 必须答得上来（`docker-compose`
  这个带连字符的 v1 命令不被使用）。
- **仓库根有一份 `.env.example`**，它同时是模板和「变量有哪些」的清单。
- **`make` 不是必需的。** 本手册全部用底层 `docker compose` / `git` 命令写成，没有
  `make` 的 Windows 原生环境照做即可；有 `make` 的环境里 `make compose-up` /
  `make compose-smoke` 是同样的动作（全部目标见 `Makefile`，`make help` 列出来）。
- **首次构建要能拉基础镜像**（`golang`、`node`、`nginx`、`postgres`、`redis`、
  `versitygw`、`rclone`、distroless、Playwright）。受限网络下的备选变量（`GOPROXY`、
  `BUILD_NETWORK`、`CONSOLE_SOURCEMAP`）见 [operations.md](operations.md) 的「镜像准备」。

### 0.5 主机准备检查表

- [ ] Docker Compose v2 可用；Docker 守护进程对将要运行备份计划任务的账户可见
      （Windows 上计划任务以 `SYSTEM` 运行，单用户桌面安装的 Docker Desktop 可能不满足，
      见 `deployments/scheduling/README.md`）
- [ ] 磁盘容量按 0.2 估算过，且 `objectstore-data` 所在分区是容量最大的那个
- [ ] 决定好 TLS 终止在哪做：叠 `docker-compose.tls.yml`（本文 4.2）还是外部反代
- [ ] 决定好 `POSTGRES_SSLMODE` 怎么满足（**先读 4.4，这条不是照抄就能过的**）
- [ ] 有一个能收告警的出站通道（SMTP 或 Webhook），并确认主机能出去

---

## 第 1 部分：第一次部署

### 1.1 取代码

```powershell
git clone <仓库地址> WebMonitoring
cd WebMonitoring
```

### 1.2 准备 `deployments/compose/.env`

```powershell
Copy-Item .env.example deployments\compose\.env
```

**复制成 `deployments/compose/.env`，不是仓库根。**

> **别漏掉 `.env` 本身。** 忘记某个变量时，Compose 会打印
> `set X, or copy .env.example into deployments/compose/.env`，直接照做即可——那行字
> 与上面这条 `Copy-Item` 是同一件事。这 14 条错误信息
> （`docker-compose.yml:59,125,157,158,226,249,311,338,343,344,390,395,396,399`）
> 在 2026-10-13 之前指向的是一个**不存在的路径**（`deployments/compose/.env.example`，
> 仓库里唯一的模板始终是**根目录**的 `.env.example`），已修正；若你在旧版本的输出里
> 看到那个路径，按上面那行 `Copy-Item` 做。

为什么路径这么讲究：`-f` 指向 `deployments/compose/` 时，Compose 去**那个目录**找
`.env`；放在仓库根的文件根本不会被读到，栈会因为变量缺失而起不来（`config`、`ps`、
`logs`、`exec` 这些子命令同样要先解析清单，一样会失败）。仓库里不需要第二份 `.env`。

#### 必须显式提供的六个变量

清单里**没有任何口令或密钥字面量**。下面六个缺一个，`docker compose config` 就会
失败并点名是哪一个：

| 变量 | 生产要求 |
| --- | --- |
| `POSTGRES_PASSWORD` | 不得是开发默认值，且 ≥ 16 字节。各服务的 `POSTGRES_DSN` 由清单用它拼出，**不要再单独去改 DSN 里的口令** |
| `S3_ACCESS_KEY` | 不得是 `webmonitoring`，且 ≥ 8 字节 |
| `S3_SECRET_KEY` | 不得是 `replace-with-a-random-secret`，且 ≥ 16 字节 |
| `BROWSER_WORKER_TOKEN` | 不得是 `replace-with-a-random-token`，且 ≥ 16 字节 |
| `ADMIN_PASSWORD` | ≥ 12 字节，且不得等于 `deployments/compose/.env.default` 里的值 |
| `ALERT_SECRET_KEY` | base64 编码的 **32 字节**密钥，用 `openssl rand -base64 32` 生成 |

叠 TLS overlay 时还有**第七个**：`TLS_CERT_DIR`（见 4.2）。

#### 一条容易踩的转发规则

Compose 用两个 YAML 锚点（`x-postgres-pool-env`、`x-queue-alert-env`）把一批变量
**按原样**转发给进程，语法一律是 `${VAR:-}`：**没写进 `deployments/compose/.env`
的变量，转发过去就是「未设置」**，然后由代码里的默认值生效。也就是说，`.env.example`
里宣传的池参数、队列参数、告警闸参数，**只有真的写进 `.env` 才会到达进程**。想调它们
就写进去，别只是「知道有这么个变量」。

#### 完整变量清单

下表是部署时会用到的全部变量。分组括号里是**未设置时的真实行为**。

**应用与登录**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `APP_ENV` | `development` | 设为 `production` 开启全部生产校验（见 4.1） |
| `HTTP_ADDR` | `:8080` | API 监听地址 |
| `DATA_DIR` | `/data`（Compose） | 未配对象存储时的本地证据目录 |
| `SESSION_SECURE` | `false` | 生产必须 `true`；TLS overlay 会自动设 |
| `ADMIN_USERNAME` | `admin` | 播种的平台超管用户名 |
| `ADMIN_PASSWORD` | 回落到 `.env.default` | **必须改**；生产拒绝默认值 |
| `ADMIN_PASSWORD_FILE` | — | 指向自己的口令文件，替代回落 |
| `LOGIN_MAX_FAILURES` / `LOGIN_LOCKOUT_MINUTES` | 5 / 15 | 登录防爆破 |
| `ALLOW_PRIVATE_TARGETS` | `false` | 生产必须 `false`（拒绝检测私网） |
| `ALLOWED_ORIGINS` | 空 = 同源 | 单机部署保持空即可；非空时每项须 `https://` |
| `METRICS_TOKEN` | 空 = `/metrics` 开放 | 生产**必须**设置且 ≥ 16 字节 |

**PostgreSQL**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `POSTGRES_DSN` | 内存仓储 | 由清单拼出，不用自己写 |
| `POSTGRES_PASSWORD` | 无（必填） | 参与拼 DSN |
| `POSTGRES_SSLMODE` | `disable` | 拼进 DSN 的 `sslmode`；**生产不得为 `disable`——先读 4.4** |
| `POSTGRES_MAX_CONNS` 等六个池参数 | 10 / 1 / 3600 / 900 / 60 / 10 | 生产拒绝 `0`、负数、超过 1000 与 `MIN_CONNS > MAX_CONNS` |

**队列与告警投递**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `QUEUE_MAX_DELIVERIES` / `QUEUE_MIN_IDLE_SECONDS` | 5 / 120 | 检测队列重试 |
| `QUEUE_RETRY_BASE_MILLISECONDS` | 500 | 重试退避基数 |
| `ALERT_QUEUE_MAX_DELIVERIES` / `ALERT_QUEUE_MIN_IDLE_SECONDS` | 5 / 120 | 告警队列重试 |
| `ALERT_MAX_ATTEMPTS` / `ALERT_RETRY_BASE_MILLISECONDS` | 3 / 250 | 单条告警投递重试 |
| `ALERT_GATE_WINDOW_SECONDS` | 60 | 限额窗口；生产拒绝 `0` |
| `ALERT_MAX_PER_CHANNEL_PER_WINDOW` | 60 | 单渠道每窗口上限；生产拒绝 `0` |
| `ALERT_FAILURE_THRESHOLD` | 5 | 连续失败多少次熔断；生产拒绝 `0` |
| `ALERT_CIRCUIT_COOLDOWN_SECONDS` | 300 | 熔断后多久放探测请求（**不进生产校验**） |

**加密主密钥**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `ALERT_SECRET_KEY` | 无（必填） | 告警地址与签名密钥的 AES-256-GCM 主密钥 |
| `ALERT_SECRET_KEYS` | — | 版本化轮换形式 `v1:<key>,v2:<key>`，设置后**优先于**单数形式；每条必须带 `vN:` 标签 |

**对象存储**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `S3_ENDPOINT` / `S3_BUCKET` | 清单固定 `http://objectstore:9000` / `evidence` | 证据存储 |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | 无（必填） | 见上表 |
| `S3_REGION` | 空 = 自动发现 | 外部 S3 需要时可设 |
| `S3_INTERNAL_ENDPOINT` | `objectstore:9000` | **只被备份/恢复脚本读**，应用不读 |

**保留期与维护任务**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `JOB_LEASE_SECONDS` | 600（Compose）/ 300（裸跑） | 生产要求**严格大于 270** |
| `EVIDENCE_RETENTION_DAYS` / `AUDIT_RETENTION_DAYS` / `JOB_RETENTION_DAYS` | 90 / 365 / 30 | 证据 / 审计 / 终态任务行保留 |
| `RETENTION_INTERVAL_HOURS` | 24 | 后台保留任务间隔；**`0` = 关闭定时器**，手动入口仍在（见 4.6） |
| `EVIDENCE_SIGNED_URL_TTL_SECONDS` | 300 | 短时签名证据 URL 有效期 |
| `ESCALATION_INTERVAL_MINUTES` / `ESCALATION_UNCLAIMED_MINUTES` | 5 / 30 | 高危事件无人认领的升级扫描；第一个**`0` = 关闭定时器**，第二个不接受 0（见 4.6） |
| `MIGRATE_CONFIRM_DESTRUCTIVE` | 空 = 拒绝 | 升级窗口内确认破坏性迁移；**全新安装不需要** |

**事件与限速**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RATE_LIMIT_READ_PER_WINDOW` / `_WRITE_` / `_BULK_` | 600 / 120 / 12 | 生产拒绝 `0`，上限 100000 / 20000 / 2000 |
| `RATE_LIMIT_WINDOW_SECONDS` | 60 | 窗口长度 |
| `WORKER_DRAIN_TIMEOUT_SECONDS` | 60 | SIGTERM 后等在途检测跑完的上限 |
| `WORKER_ID` | 按 hostname 生成 | **不要设**，见 4.1 |

**通知与可观测性**

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USERNAME` / `SMTP_PASSWORD` / `SMTP_FROM` | 空 = EMAIL 渠道关闭 | `SMTP_HOST` 设了就必须齐备 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 空 = Trace 关闭 | 设了才导出 |
| `OTEL_TRACES_SAMPLER_ARG` | 1（全采样） | 仅在 Trace 开启时有效 |
| `APP_VERSION` / `APP_COMMIT` / `APP_BUILD_TIME` | `dev` / `unknown` / `unknown` | 构建时注入，见 1.4 |

**仅构建期**：`GOPROXY`、`BUILD_NETWORK`、`CONSOLE_SOURCEMAP`（后者默认空，
即不产 sourcemap——产了就等于把前端源码发给任何能打开控制台的人）。

### 1.3 校验清单能被解析

不需要 Docker 起栈，几秒钟就有结论：

```powershell
docker compose -f deployments/compose/docker-compose.yml config --quiet
```

**没有输出就是成功。** 它点名缺哪个变量，比等到 `up` 才炸快得多。

### 1.4 首次构建

```powershell
docker compose -f deployments/compose/docker-compose.yml build
```

单独一步是为了让构建失败在这里停下，而不是混在 `up` 的输出里。

想让 `/api/v1/version` 与控制台「关于」显示真实版本而不是 `dev`，构建时带上：

```powershell
$env:APP_VERSION = "v0.1.0"
$env:APP_COMMIT  = (git rev-parse --short HEAD)
$env:APP_BUILD_TIME = (git show -s --format=%cI HEAD)
docker compose -f deployments/compose/docker-compose.yml build
```

不设就如实自称 `dev`——**这是有意的**，不是缺陷。`BUILD_TIME` 取 commit 时间而不是
`date`，是为了让同一次发布的所有镜像报同一个时间。

### 1.5 跑迁移

```powershell
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
```

**它与第 1.6 步的关系要说清**：`api` 与 `http-worker` 都声明了
`depends_on: migrate: {condition: service_completed_successfully}`，所以单独执行
`up -d` **也会**先跑迁移，迁移不成功就起不来。这一步是提前把这件事做掉——迁移的输出
会直接打在你眼前，出错时你能立刻看到是哪一条，而不是从一个「服务起不来」的现象往回猜。

### 1.6 起栈

```powershell
docker compose -f deployments/compose/docker-compose.yml up -d
docker compose -f deployments/compose/docker-compose.yml ps
Invoke-WebRequest http://localhost:8080/readyz
```

**怎么知道成功了**：`ps` 里 `postgres`、`redis`、`objectstore`、`browser-worker`、
`api`、`console` 六个常驻服务都是 `Up`（除 `api` 外都配了健康检查，会显示
`(healthy)`），`migrate` 是 `Exited (0)`。`/readyz` 返回 HTTP 200。

> `api` 没有健康检查，而 `console` 声明了 `depends_on: api: {condition: service_healthy}`
> ——这两个事实放在一起是矛盾的（没有健康检查就没有 `healthy` 状态）。`docker compose
> config --quiet` 通过、CI 也绿，所以它被容忍至今，但**别把它当成「api 是被验证过的」**：
> 判断 API 是否真的可用，看 `/readyz`，不要看容器状态。

`rewrap` 与 `rclone` 挂在 `tools` profile 上，`up -d` **不会**启动它们，只在被显式
`run` 时才存在。这是有意的。

打开 `http://localhost:8088`，看到登录页即可。

**启动失败最常见的原因**是 `APP_ENV=production` 下的配置门禁——它**拒绝启动**而不是
只打印警告，见 4.1。

### 1.7 首次登录，以及装完立刻要做的事

默认账号是 `default` 租户下的 `admin`（平台超管）。它的口令住在
`deployments/compose/.env.default`，那是这个口令在仓库里的**唯一**存放点；未设
`ADMIN_PASSWORD` 时 API 回落到该文件。（走 Compose 清单时用不到这条回落——清单用
`:?` 要求这个变量**非空**；回落管的是不带清单的开发运行方式，以及改默认值这个入口。）

**第一件事就是改掉它**：顶栏用户菜单 →「修改密码」（`POST /api/v1/auth/password`，
自助、需输当前口令）。走这条路而不是用户管理，是因为**平台超管不会被任何管理员重置**
——`POST /api/v1/users/{id}/password` 对平台账号拒绝，自助入口是它唯一能改口令的通道。
改完立即生效，重启不会把它重置回默认值。

---

## 第 2 部分：验收——怎么知道这套栈是好的

按从便宜到贵的顺序做。**前四条是只读的**，任何时候都可以跑。

```powershell
# 1. 进程存活（不检查依赖）
Invoke-WebRequest http://localhost:8080/healthz

# 2. 依赖就绪：PostgreSQL、Redis、对象存储、Browser Worker
Invoke-WebRequest http://localhost:8080/readyz

# 3. 现在跑的是哪个版本的二进制（在会话门禁之后；不读数据库）
Invoke-WebRequest http://localhost:8080/api/v1/version

# 4. 指标（设了 METRICS_TOKEN 时带 Bearer 头，否则 401）
Invoke-RestMethod http://localhost:8080/metrics -Headers @{ Authorization = "Bearer $env:METRICS_TOKEN" }
```

`/readyz` 不健康时返回 503 **并点名是哪个依赖**——所以故障排查的第一步永远是它，
比逐个容器猜快得多（详见 [runbooks/README.md](runbooks/README.md)）。

**第 4 步里有两组指标决定「监测本身是不是还活着」**，值得在验收时就看一眼：
`webmonitoring_last_successful_detection_timestamp_seconds`（最近一次成功检测的时刻，
它停止前进就是有人该被叫醒的信号，哪怕控制台上一条事件都没有）与
`webmonitoring_detection_checks_last_hour{result}`（最近一小时的完成/失败数）。

最后跑一次端到端 smoke。它使用 Compose 内部的授权测试目标，**不连接外部网站**：

```powershell
.\scripts\compose-smoke.ps1
```

覆盖登录、资产验证、基线审批、HTTP/Browser 检测、事件、证据、报告、控制台 E2E 以及
API/Worker 重启后的持久化。

**它跑的是自己的一套栈，不是你刚起的那一套**：项目名、端口、数据卷都是独立的
（smoke 用 `webmonitoring-smoke` / API `18080` / 控制台 `18088`；压测用
`webmonitoring-loadtest` / `18081`；多副本演练用 `webmonitoring-mrdrill` / `18082`）。
所以**不要拿 8080/8088 去核对它的结果**，也不要以为它读的是你的数据；它有自己的一份
（`.data-smoke`，已被 Git 忽略）。**两者只共享源码与镜像构建。**

**脚本是 `.ps1`**，执行策略是 `Restricted` 时会被拦下且一行都不执行——用
`powershell -ExecutionPolicy Bypass -File .\scripts\compose-smoke.ps1`，
或先 `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`。

其余门禁与它们的覆盖范围、参数：

| 脚本 | 关键参数（默认） | 说明 |
| --- | --- | --- |
| `scripts/integration-test.ps1` | `-PostgresImage`、`-RedisImage`、`-ReadyTimeoutSeconds`(90)、`-Race` | 起两个**一次性容器**（随机回环端口）跑真实 PostgreSQL/Redis 集成用例；不复用 Compose 栈（rewrap 用例会 `CREATE DATABASE`） |
| `scripts/compose-smoke.ps1` | `-KeepRunning` | 端到端；自己的栈（18080/18088） |
| `scripts/load-test.ps1` | `-AssetCount`(100)、`-Tenants`(2)、`-Mode`(HTTP\|BROWSER)、`-MinimumJobsPerSecond`(1.0) | 100 资产压测；自己的栈（18081） |
| `scripts/multi-replica-drill.ps1` | `-AssetCount`(40)、`-ApiReplicas`(2)、`-HttpWorkers`(3)、`-DropActiveJobIndex` | 多副本演练；自己的栈（18082）。`-DropActiveJobIndex` 会**故意**删掉自己的索引，此时脚本**应当失败**——那是变异检查 |
| `scripts/compose-backup.ps1` | `-OutputRoot`(.data-backups)、`-KeepBackups`(14)、`-EnvFile` | 备份 + 轮转。**会删**输出目录里匹配 `^\d{8}-\d{6}$` 的旧备份 |
| `scripts/compose-restore.ps1` | `-BackupRoot`（必填）、`-Execute`、`-ConfirmText` | 默认**只读校验**；`-Execute` 才动手，且必须 `-ConfirmText RESTORE` |
| `scripts/backup-rehearsal.ps1` | `-RehearsalRoot`(.data-backup-rehearsal) | 跑两个备份周期 + restore dry-run，不动运行数据 |
| `scripts/reload-tls.ps1` | `-CertDir`（必填）、`-CheckOnly` | `nginx -t` 后发 `SIGHUP` 热重载；`-CheckOnly` 只读 |

所有脚本的退出码语义一致：成功 `0`，失败经未捕获异常得到非零（无自定义退出码）。

---

## 第 3 部分：把平台用起来

装完之后，按这条链路把它跑起来。**顺序不能乱：没有完成所有权验证的资产不会进入检测。**

> 登录 → 登记资产 → 完成所有权验证 → 启用检测 → 审批基线 → 出差异事件 →
> 看证据 / 通知 / 审计

登录时「租户」一行填**租户标识（slug）**，留空回落到 `default` 租户。

### 3.1 租户与用户

角色只有四种：**平台管理员（ADMIN）、安全运营（OPERATOR）、审计只读（AUDITOR）、
超级管理员（SUPER_ADMIN）**。按端点做粗粒度校验：**同一租户内 ADMIN 能读写该租户全部
资源**——没有资源级授权与双人审批，这是产品边界（见
[product-scope.md](product-scope.md)）。

- **只有 SUPER_ADMIN 能建租户**（控制台「租户管理」）。建租户时**同时播种该租户的第一个
  ADMIN**，字段是「租户名称」「租户标识（slug）」「初始管理员账号」「初始管理员密码」。
- **建用户只能建 OPERATOR 或 AUDITOR**（`POST /users`；给别的角色会得到 400
  「仅可创建 OPERATOR 或 AUDITOR 用户」）。
- 有几道保护会拒绝你的操作，看到它们是正常的：不能修改 SUPER_ADMIN（403）、不能停用或
  降级自己（409）、租户必须保留至少一名启用的 ADMIN（409）。
- 停用用户 = 编辑用户时把状态开关拨到「停用」；下一次请求即结束其会话。
- 用户的 `email` 是可选的，但**只有填了 email 的启用 ADMIN 会收到升级通知**——没人填就
  等于没人被通知。

> SUPER_ADMIN 只负责租户 CRUD 与播种租户管理员，**不是「能看到所有租户数据」的神**。

### 3.2 登记资产

控制台资产页「新增资产」，字段是「资产名称」「监测地址」「资产类型」「所属站点」。

**资产类型只能从四个值里选**，下拉给的就是这四个，与后端一致
（`internal/domain/models.go:23-28`、`server.go:3513-3516`）：

| 下拉项 | 提交的值 |
| --- | --- |
| 官方网站 | `OFFICIAL` |
| 业务系统 | `BUSINESS` |
| 登录页面 | `LOGIN` |
| 其他 | `OTHER` |

> 2026-10-13 之前这个下拉给的是「官方网站 / 监测站点 / 第三方」（`OFFICIAL` /
> `MONITORING` / `THIRD_PARTY`），后两项提交必然得到 **`400 INVALID_ASSET`「资产名称或
> 类型无效」**（`server.go:525-527`），而且 `BUSINESS` / `LOGIN` 从控制台根本选不到。
> 已修正。若你在旧版本上遇到这个 400，改用本节下面的批量导入，或直接调 API。

批量登记走「批量导入」：可以粘贴 JSON 或 CSV（CSV 每行 `name,url,type`，最多 500 行），
先「预检」再「确认导入」；逐行独立校验，**允许部分失败**，每行结果是
`CREATED` / `VALIDATED` / `REJECTED`。

### 3.3 完成所有权验证

**这是整个平台的门槛：没完成所有权验证的资产不会进入检测队列。** 方法在**创建资产时
选定，之后不能改**；不指定时默认 `FILE`。

> **控制台建出来的资产永远是 `FILE`。** 新增资产弹窗只有「资产名称」「监测地址」「资产
> 类型」「所属站点」，**没有选择验证方法的地方**（`ConsoleLayout.vue:66`），所以后端拿到
> 空方法后按默认走 `FILE`。想用 `DNS` 或 `META`，只能走批量导入或 API。

在资产行点「授权验证」→ 弹窗「完成站点授权」，它会签发一个 30 分钟有效的令牌
（`webmonitoring-verification=<uuid>`），把它按所选方法放上去，再点「已完成，开始验证」：

| 方法 | 放什么 | 校验什么 |
| --- | --- | --- |
| `FILE`（默认，也是控制台唯一能建的） | 站点根下的 `/.well-known/webmonitoring-verification.txt`，内容为令牌值 | HTTP 200 且正文匹配 |
| `DNS` | TXT 记录 `_webmonitoring-verify.<资产主机名>`，内容为令牌值 | `LookupTXT` 记录内容匹配 |
| `META` | 页面里 `<meta name="webmonitoring-verification" content="<令牌值>">` | 标签内容匹配 |

**三种方法都能在控制台上走完**：弹窗会按资产的方法给出对应的指令——`FILE` 报文件路径、
`DNS` 报要加的 TXT 记录名、`META` 直接把整条标签给你（接口按方法换字段名
`path` / `record`+`recordType` / `tag`，见 `server.go:3177-3194`）。
2026-10-13 之前弹窗一律读 `path`，`DNS` / `META` 资产会看到**「创建文件 undefined」**，
这两种方法只能走 API；若你在旧版本上遇到，按上表手工放置即可。

失败与过期的确切行为：

| 情况 | 结果 |
| --- | --- |
| 令牌过期 | 资产置为 `REJECTED`、`enabled=false`，返回 **422 `VERIFICATION_EXPIRED`**「验证令牌已过期，请重新签发」——重新点「授权验证」即可 |
| 没找到 / 不匹配 | 同样置 `REJECTED` 并停用，返回 **422 `VERIFICATION_FAILED`**「未在目标站点找到有效令牌」 |
| 没先签发就验证 | **400 `INVALID_ACTION`**「请先签发验证令牌」 |
| 成功 | `VERIFIED` + `enabled=true`，令牌清空 |

验证通过后资产才可被「暂停」与「恢复」；失效后已有证据按保留策略留着，但**不再产生新的
检测请求**。

### 3.4 配置检测策略

资产行的「策略」按钮打开「监测策略 · <资产名>」。可配项与**服务端校验的真实范围**：

| 字段（控制台文案） | 默认 | 允许范围 |
| --- | --- | --- |
| 「HTTP 间隔（秒）」 | 300 | ≥ 60 |
| 「浏览器间隔（秒）」 | 1800 | ≥ 300 |
| 「超时（秒）」 | 30 | 1–90 |
| 「最大响应字节」 | 5 MiB | 1024–10485760 |
| 「忽略 CSS Selector」 | 空 | 动态内容用，避免每次都判为变化 |
| 「可信域名」 | 空 | 来自这些域的资源变化不单独报 |
| 「周期性维护窗口」 / 「一次性维护区间」 | 空 | 星期 0=周日…6=周六（留空=每天），分钟 0–1440，`utcOffsetMinutes` 默认 0 |

超出范围会得到 **`400 INVALID_POLICY`**「监测策略超出允许范围」。「检测模式」（HTTP /
浏览器）不是策略字段，是**任务级**的，用资产行的「HTTP 检测」/「浏览器检测」触发。

登录态站点可以在策略里配 `auth`（登录地址、用户名/密码字段、额外字段、提交选择器）；
凭据以 AES-GCM 加密存储，两侧都做 SSRF 复核。

### 3.5 审批基线

**首次检测只产生「候选基线」，不会自动生效，也不产生差异事件**——必须先有人批准。
控制台「基线审批」页的说明写得很直白：「候选基线必须人工确认，批准后才参与篡改判断；
旧版本只允许显式废弃。」

- 状态机：`CANDIDATE → APPROVED` / `REJECTED`；`APPROVED → RETIRED`。
- **同一资产同一时刻只能有一个有效批准基线**；已批准的基线**不可覆盖**，只能出新版本或
  显式废弃。
- 批准 / 拒绝 / 废弃需要 ADMIN 或 OPERATOR。

差异信号带类型、严重性、**证据引用和规则版本**（当前 `builtin-2026-09-15.1`）——这就是
「证据先于结论」在数据结构上的体现。

### 3.6 处置事件

控制台「事件中心」。**允许的人工流转**：

```
OPEN → ACKNOWLEDGED → INVESTIGATING → RESOLVED → CLOSED
  └────────────→ FALSE_POSITIVE（可从 OPEN 或 INVESTIGATING）
RESOLVED ⇄ INVESTIGATING
```

除了上面这些，**复现也会自动改状态**（不是人工流转）：`CLOSED → OPEN`、
`FALSE_POSITIVE → OPEN`。

要点：

- **恢复、关闭或误报必须填写处置说明**，否则返回 `RESOLUTION_REQUIRED`
  「恢复、关闭或误报状态必须填写说明」。
- 认领一个未指派的事件时，默认指派给操作者本人；指派随流转一并提交。
- **已恢复（`RESOLVED`）的事件不会被复现重开**——只有已关闭 / 误报的会重开原事件
  （`REOPENED`），不会插重复行。去重键是
  `tenant_id + asset_id + baseline_version + 规范化信号指纹`。
- **事件时间线**（append-only）记录每一轮复现的信号集、状态流转、负责人变更和备注；
  事件行上的 `signals` 每轮被覆盖，**复现明细只存在于时间线里**。时间线条目随事件生命
  周期，**不参与保留期清理**。
- 批量流转最多 200 条，逐行独立结果。

### 3.7 配置通知渠道

控制台侧边栏「告警渠道」（仅可操作角色可见）。四种类型：

| 类型 | 需要填 |
| --- | --- |
| 企业微信（`WECHAT_WORK`） | 通知地址 |
| Webhook（`WEBHOOK`） | 通知地址 + 签名密钥 |
| 钉钉（`DINGTALK`） | 通知地址 + 签名密钥 |
| 邮件（`EMAIL`） | 收件人（逗号分隔）；**依赖 1.2 里的 `SMTP_*` 配置**，`SMTP_HOST` 留空即邮件渠道整体关闭 |

- **地址与密钥都以 AES-256-GCM 加密入库**，关联数据绑定 `tenant + channelID + 字段名`；
  接口**从不回传明文密钥**（编辑时留空 = 保留原值）。这就是为什么 `ALERT_SECRET_KEY`
  丢了就解不开存量渠道配置——**把它和其它两个密钥一起进你的密钥管理**。
- 每行有「测试」：发一条测试消息，**绕过熔断、不写投递记录**——用来区分「渠道配错了」和
  「渠道被熔断了」。
- 状态列会显示「启用 / 停用 / **投递熔断**」。看到熔断先看指标里的 `reason`：是
  `CIRCUIT_OPEN`（真坏了）还是 `LIMITED`（只是被限流），处置完全不同——见
  [runbooks/alert-delivery.md](runbooks/alert-delivery.md)。
- **告警只在证据写入成功之后才发**；Webhook 签名覆盖「UTC Unix 时间戳 + 正文」，
  **接收方要自己实现时间窗校验与去重**。

### 3.8 证据、报告与导出

- **单事件证据包**：`GET /api/v1/reports/{incidentId}` → ZIP（`incident.json` +
  `evidence/<id><ext>`）。读取证据时做完整性校验，失败返回 **424 `REPORT_INCOMPLETE`**
  ——这是「原始证据不可覆盖」在接口上的样子：拿不到就报错，不给你一个残缺的包。
- **单事件打印报告**：`GET /api/v1/reports/{incidentId}/html`（自包含 HTML，只列证据、
  不内嵌内容）。
- **汇总报告**：`GET /api/v1/reports/summary`（含 MTTD / MTTA / MTTR 与趋势）与
  `GET /api/v1/reports/summary/html`。
- **控制台没有独立的「报告中心」**：入口散在两处——总览页的「打印汇总报告」，以及事件
  中心行内的「导出证据」「打印报告」。打印就绪的 HTML 直接一键打印成 PDF，不需要内嵌
  中文字体。
- 其它导出：资产（`/assets/export`，JSON/CSV，ADMIN）、事件清单
  （`/incidents/export`）、审计（`/audit-events/export`，ADMIN）。**站点没有导入/导出**。

### 3.9 保留期与升级扫描（只有 API，没有界面）

这两件事**控制台上没有入口**，只能调 API：`POST /api/v1/maintenance/retention` 与
`POST /api/v1/maintenance/escalation`。

两个都是**写操作**，所以除了会话 Cookie 还必须带 **`X-CSRF-Token`** 头（`openapi.yaml:1317-1321`），
令牌就是登录响应里的 `csrfToken`。完整的最小调用长这样：

```bash
# 1) 登录：响应头带回会话 Cookie，响应体里的 csrfToken 就是下一步要用的值
curl -s -c cookies.txt -X POST http://localhost:8080/api/v1/auth/session \
  -H 'Content-Type: application/json' \
  -d '{"tenant":"default","username":"admin","password":"<你的口令>"}'

# 2) 保留期清理：先 dry-run 看会删什么，再执行
curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/maintenance/retention \
  -H 'Content-Type: application/json' -H 'X-CSRF-Token: <csrfToken>' \
  -d '{"execute": false}'
curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/maintenance/retention \
  -H 'Content-Type: application/json' -H 'X-CSRF-Token: <csrfToken>' \
  -d '{"execute": true}'

# 3) 升级扫描：把超过阈值仍无人认领的高危事件报告给租户管理员
curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/maintenance/escalation \
  -H 'X-CSRF-Token: <csrfToken>'
```

`execute` 是必填字段。保留结果里有 `expiredEvidence`、`deletedEvidence`、
`protectedEvidence`、`failedEvidence`、`deletedAuditEvents`、`failureReasons`
——**`protectedEvidence` 非零是正常的**，那正是被事件引用的证据。dry-run 的结果就是
下一次后台执行会做的操作。

升级扫描与定时扫描是同一段逻辑，手工入口存在的意义就是「不等到下一个周期就能核对它会报
出什么」。它返回的 `reported` 是本次真正上报的条数——**「跑了」和「发现了东西」是两个
不同的事实**，别把前者当成后者。

> 口令写在命令行里会进 shell 历史。这是 API-only 的运维动作，建议用 API 客户端而不是
> 留在 `history` 里的 `curl`。

> `RETENTION_INTERVAL_HOURS=0` 与 `ESCALATION_INTERVAL_MINUTES=0` 会把对应的后台定时器
> **真的关掉**，手动入口不受影响、始终可用。这条在 2026-10-13 之前是假的（设 0 会被
> 静默改成 24 / 5，见 4.6），现在与注释一致。

---

## 第 4 部分：生产加固

### 4.1 `APP_ENV=production` 会强制什么

设成 `production` 之后，启动前的校验**让进程启动失败**，而不是打印警告。下表逐条来自
`internal/config/production.go` 与 `internal/config/pool.go`，是校验器的真实行为：

| 变量 | 生产要求 |
| --- | --- |
| `POSTGRES_DSN` / `REDIS_ADDR` / `S3_ENDPOINT` / `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `BROWSER_WORKER_URL` / `BROWSER_WORKER_TOKEN` | 必须非空（API 与 Worker 都要求） |
| `ADMIN_PASSWORD` | 只有 API 要求；必须非空、≥ 12 字节，且不得等于 `.env.default` 里的值 |
| `POSTGRES_DSN` 里的口令 | 不得是 `webmonitoring`，且 ≥ 16 字节 |
| `POSTGRES_DSN` 的 `sslmode` | **不得为空，也不得是 `disable`**。由 `.env` 的 `POSTGRES_SSLMODE` 拼进去——**注意这条有别的问题，见 4.4** |
| `SESSION_SECURE` | 必须是 `true`；API 与 http-worker 都受此约束 |
| `ALLOW_PRIVATE_TARGETS` | 必须是 `false`（生产拒绝 `true`） |
| `METRICS_TOKEN` | 必须非空且 ≥ 16 字节。清单把 API 端口发布到主机，没有令牌等于把进程、连接池和事件内部数据对任何能访问该主机的人开放 |
| `BROWSER_WORKER_URL` | `https` 一律可以；`http` 只允许指向私网地址、回环地址或不含点的服务名。**这是个纯词法检查**，一个带点的域名走 `http` 会被直接拒绝——正是「解密后的客户凭据被送到公网」的那一种错 |
| `JOB_LEASE_SECONDS` | 设了就必须**严格大于 270**。一条检测受策略超时（上限 90 秒）约束，而浏览器模式要花掉两次，所以更短的租约会让另一个副本把仍在跑的检测标成 `TIMED_OUT` |
| `ALERT_SECRET_KEY` 或 `ALERT_SECRET_KEYS` | 至少一个必须设置。每个条目都必须是 base64 的 32 字节密钥；用复数形式时**每条都必须带 `vN:` 标签**，且版本号不得重复 |
| `POSTGRES_MAX_CONNS` / `POSTGRES_MIN_CONNS` | 设了就必须为正、不超过 1000，且 `MIN_CONNS` 不得大于 `MAX_CONNS` |
| `POSTGRES_MAX_CONN_LIFETIME_SECONDS` 等四个池参数 | 设了就必须是正的秒数 |
| `RATE_LIMIT_{READ,WRITE,BULK}_PER_WINDOW` | 设了就必须为正（`0` 是「给这一类关掉限速」，不是「用默认值」），且不超过 100000 / 20000 / 2000 |
| `ALERT_MAX_PER_CHANNEL_PER_WINDOW`、`ALERT_FAILURE_THRESHOLD`、`ALERT_GATE_WINDOW_SECONDS` | 设了就必须为正（`0` 会关掉那道通知控制） |
| `ALLOWED_ORIGINS` | 留空即同源（单机默认）。非空时不得含 `*`，且每个来源必须是 `https://` |
| `SMTP_*` | 只在 `SMTP_HOST` 非空时校验完整性：`SMTP_HOST`/`SMTP_USERNAME`/`SMTP_PASSWORD`/`SMTP_FROM` 必须齐备，`SMTP_PORT` 在 1–65535 之间 |
| `WORKER_ID` | 只有 Worker 校验，且只拒绝 `worker-1` 与 `http-worker-1` 这两个固定名——它们会被 `--scale` 的每个副本共用，于是副本互相抢占在途任务。**不要设它**，留空即由进程生成唯一身份 |

这些是**区间**校验，不是「设了就信」：设 `0` 或负数同样启动失败，理由和上限一样——一个
被关掉的控制和一个正常工作的控制，在出问题那天之前看起来是一样的。

### 4.2 叠加 TLS 终止

```bash
TLS_CERT_DIR=/etc/letsencrypt/live/example.com \
  docker compose -f deployments/compose/docker-compose.yml \
                 -f deployments/compose/docker-compose.tls.yml up -d
```

`TLS_CERT_DIR` 必须含 `fullchain.pem` 和 `privkey.pem`（证书不随仓库分发，
`deployments/.gitignore` 已排除它们）。这一层做的事：

- 边缘 nginx 独占 `80`/`443`，80 上的请求返回 **308**（保留方法与正文，不会把 POST 悄悄
  降级成 GET）；`api` 的 `8080` 与 `console` 的 `8088` 被 `!reset []` 摘掉，并给 API
  打开 `SESSION_SECURE=true`。
- **摘掉那两个端口不是洁癖**：留着它们就存在一条绕过边缘的明文路径，既绕开安全响应头，
  也绕开边缘对 `/metrics` 的屏蔽，还会让标记为 secure 的会话 Cookie 有机会走 http 发出。
- `/metrics` 在边缘**返回 404**。它不带鉴权，对外发布等于把资产数、任务吞吐、错误率和
  连接池占用交给任何能访问该主机的人。API 仍在内部网络里提供它，供采集器抓取；
  **不要为此把 `/metrics` 重新发布到主机**，也不要指望 `METRICS_TOKEN` 就能把它挂公网。
- `/healthz`、`/readyz` 保留对外（外部负载均衡需要）。`/api/` 关掉
  `proxy_buffering`，否则 `GET /api/v1/events/stream` 这条 SSE 会成批到达或干脆不到。
- 边缘显式传 `X-Forwarded-Proto: https`——这才是让 API 的 `SESSION_SECURE=true` Cookie
  端到端真的被当作 secure 的那一步。

**续期之后要显式 reload**：nginx 只在启动时读一次证书，之后一直用内存里的副本。

```powershell
.\scripts\reload-tls.ps1 -CertDir C:\certs\example.com            # 装入新证书
.\scripts\reload-tls.ps1 -CertDir C:\certs\example.com -CheckOnly # 只看边缘现在提供哪张
```

脚本先 `nginx -t` 校验配置再发 `SIGHUP`；**不重启容器**，避免丢掉在途请求（包括控制台
挂着的 SSE 流）。把它接到已经在做续期的定时任务上，别指望有人记得手动跑。仓库**不**
替你签发、续期或准备证书。

### 4.3 安全响应头与 CORS

边缘固定发送 `HSTS`（`max-age=31536000; includeSubDomains`）、`X-Content-Type-Options:
nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: same-origin`。

`ALLOWED_ORIGINS` **单机部署保持留空**：控制台的 nginx 把 `/api/` 反代到 `api:8080`，
页面与接口同源，留空即正确。只有当控制台与 API 真的分处不同来源时才需要设置，且生产
要求每项都是 `https://`、不含 `*`。

### 4.4 已知未结清的一件事：`POSTGRES_SSLMODE`

生产门禁要求 `sslmode` 不是 `disable`（4.1），而 **Compose 里的 `postgres:17-alpine`
没有配置任何 TLS**：它不挂证书，也没有打开 `ssl`。三处放在一起看：

1. `TLS_CERT_DIR` 那份 overlay **只设了 `SESSION_SECURE`**，没有设 `POSTGRES_SSLMODE`；
   把 `sslmode` 拼进 DSN 的 `POSTGRES_SSLMODE` 默认值是 `disable`，所以**必须由运维在
   `.env` 里自己设**，否则 API 在生产模式下直接拒绝启动。
2. `internal/config/production.go:306-307` 那句注释说「Compose 为这件事设了
   `POSTGRES_SSLMODE`，而 TLS overlay 会把它变成 `verify-full`」——**后半句与文件的实际
   内容不符**：overlay 里没有这一项，而 base 清单只有 `disable` 这个默认值。
3. 门禁只拒绝空值和 `disable`，所以 `prefer` 这类取值能通过校验；而 pgx 的 `prefer` 语义
   是「先试 TLS，连不上就退回明文」（`pgconn` 用 `Fallbacks` 实现），对面这台 Postgres
   又不提供 TLS——**门禁会通过，链路依然是明文**。一个通过了的校验并不等于加密生效。

所以这条要求目前**没有一条在仓库内可验证的满足方式**：要么由部署方在 Compose 之外给
PostgreSQL 终止 TLS（或用带 TLS 的外部实例），要么明确接受「校验通过但传输未加密」。
**这不是本手册能替你决定的部署决策**，也不要在没有核实之前照抄 `verify-full` 到
`.env` 里——那会让 API 连不上库。这一条与第 4 部分整体「从未演练」是同一件事的两面。

### 4.5 指标怎么接

控制面按 Prometheus 文本格式暴露 `/metrics`，但**本仓库不含采集器**——scrape 配置、
告警规则与时序存储由部署方提供。

采集器要跑在 Compose 网络里，抓 `api:8080/metrics`（`http-worker` 在自己的
`WORKER_HTTP_ADDR` 上也暴露一份）。**不要**为此把 `/metrics` 重新发布到主机；叠了
TLS 时边缘对它是 404。

值得告警的几组（症状与处置见 [runbooks/README.md](runbooks/README.md) 的对照表）：

- `webmonitoring_last_successful_detection_timestamp_seconds` **停止前进**——最该被叫醒
  的信号，哪怕一条事件都没有；
- `webmonitoring_detection_checks_last_hour{result}` 失败占比；
- `webmonitoring_queue_pending` 持续增长不回落、`queue_dead_letters_total` 增长；
- `webmonitoring_evidence_failures_total`、`webmonitoring_alert_suppressed_total`；
- `webmonitoring_rate_limited_total{class}`（区分「被打满配额」与「依赖坏了」）。

### 4.6 两个后台扫描的开关

`RETENTION_INTERVAL_HOURS=0` 与 `ESCALATION_INTERVAL_MINUTES=0` 会**真的关掉**对应的
定时器，两个手动入口（`POST /api/v1/maintenance/retention`、
`POST /api/v1/maintenance/escalation`）始终可用。这两个变量走
`config.NonNegativeInt`（`internal/config/intervals.go`）：`0` 原样返回，未设置/非法/
负数才回落默认值。

> **这条在 2026-10-13 之前是假的。** 两者当时都经由一个「`<= 0` 一律回落默认值」的
> 解析器（旧的 `envInt64`），`0` 被静默改成 24 / 5，`apps/api/main.go` 里用来关闭的两
> 个 `else` 分支是不可达代码。安全影响是直接的：一个以为「已经关掉了升级通知」的运维，
> 实际每 5 分钟仍在扫描、仍可能给管理员发信，而日志和界面上没有任何东西说相反的话。

关掉之后，启动日志里会有下面两行之一——那是**唯一**的提示，请确认它出现过：

```text
background retention disabled; only the manual endpoint will run it
incident escalation is disabled; unclaimed high-risk incidents will not be reported
```

**一个仍然存在的不对称：`ESCALATION_UNCLAIMED_MINUTES` 不接受 `0`。** 它表示「多久算
无人认领」，设 `0` 会回落成 30——一个事件不可能「无人认领了零分钟」，这里没有「关闭」
这个语义，所以它保留 `config.PositiveInt` 的旧行为。

**生产门禁不检查这两个变量。** 4.1 的表里，限速与告警闸设 `0` 会被判为「关掉了一道
控制」而**拒绝启动**；保留与升级扫描不在这张表里——`APP_ENV=production` 下设 `0` 不会
报错，只会静默关掉。要关闭就在变更记录里写明，并以上面那行 Warn 为准。

### 4.7 密钥轮换

主密钥（`ALERT_SECRET_KEY`）用版本化形式轮换，把新版本加进 `ALERT_SECRET_KEYS`
（`v1:…,v2:…`），再用 `tools` profile 里的 `rewrap` 服务把存量密文重新加密：

```powershell
docker compose -f deployments/compose/docker-compose.yml run --rm rewrap --dry-run
docker compose -f deployments/compose/docker-compose.yml run --rm rewrap
```

**先跑 `--dry-run` 看扫描结果，再执行。** 轮换与存量再加密的扫描（dry-run）与执行路径
都已实现并在一次性容器上验证过，**但尚未在真实部署的数据上执行过再加密**
（`状态：部分实现`，见 [README.md](../README.md) 的「现在明确不能干什么」）。

### 4.8 审计与隐私边界

- 证据保留 90 天、审计 365 天（默认，见 1.2）。审计记录不可变；`incident_events`
  append-only，且**不参与**保留期清理。被事件或基线引用的证据永不删除。
- 告警只在证据写入成功之后才发；Webhook 签名覆盖「UTC Unix 时间戳 + 正文」，
  **接收方必须自己实现时间窗校验与去重**。
- 登录态检测的凭据以 AES-GCM 加密存储。
- 日志与模型上下文里不得出现密码、Cookie、Token 或敏感表单值——这是项目的不可妥协项，
  也意味着**不要把 `.env` 或凭据贴进工单、聊天或任何模型上下文**。

---

## 第 5 部分：日常运维

### 5.1 常规升级

```powershell
git pull
docker compose -f deployments/compose/docker-compose.yml build
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
docker compose -f deployments/compose/docker-compose.yml up -d
docker compose -f deployments/compose/docker-compose.yml ps
Invoke-WebRequest http://localhost:8080/readyz
```

升级重建容器时，`up -d`/`stop` 发给 worker 的是 SIGTERM：http-worker 停止领新任务并等
在途检测跑完（上限 `WORKER_DRAIN_TIMEOUT_SECONDS`，默认 60 秒），在途任务不会被硬杀丢失；
browser-worker 走 `server.close()` 等待在途快照。**不要在这段时间里反复重启**。

四件升级时必须知道的事：

- **不得通过删除 PostgreSQL 数据卷来完成升级。** 没有「重置一下」这条路。
- **迁移只前进**。`db/migrations/*.down.sql` 存在但没有执行器。
- **破坏性迁移需要显式确认**：对已有数据的库应用带 `-- migrate:destructive` 标记的迁移
  前，必须在 `.env` 设 `MIGRATE_CONFIRM_DESTRUCTIVE=1` 并重建 migrate 服务；**全新安装
  不需要它**。日志里出现 `refusing to apply ... marked destructive` 时，先读那个迁移
  文件的头部注释确认影响面，不要顺手把变量设上不管。用完就撤掉这个变量。
- **迁移 checksum 不一致时停止发布并调查**文件或数据库是否被篡改，不要绕过。

有一类升级会让在线用户被登出（一次性的全体重新登录）：带密码指纹的版本、以及此后一直
如此的改密码操作。停用用户或租户同样会在下一次请求上结束其会话。

`migrate: status`、`unknown migration version` 的救援步骤、以及「升级失败怎么办」的完整
问答在 [operations.md](operations.md)。

### 5.2 备份

**备份必须同时包含三样**，缺一样就不算备份：PostgreSQL custom dump、evidence bucket
**里的对象**（不能只备份元数据）、以及逐文件 SHA-256 manifest。

```powershell
$env:S3_ACCESS_KEY = "..."
$env:S3_SECRET_KEY = "..."
.\scripts\compose-backup.ps1 -OutputRoot .data-backups
```

脚本自己从 `deployments/compose/.env` 读 S3 凭据（`-EnvFile` 可改），**凭据不出现在
任何任务定义里**。默认保留 14 份（`-KeepBackups` 或 `BACKUP_KEEP_COUNT` 可调），规划
磁盘时按 14 份全量估算。轮转**会删除**输出目录里匹配 `^\d{8}-\d{6}$` 的旧备份目录。

**排进计划任务**，别靠人记：

```powershell
powershell -ExecutionPolicy Bypass -File deployments/scheduling/register-windows-task.ps1
```

默认任务名 `WebMonitoring-Backup`、每日 `02:30`、保留 14 份、输出 `.data-backups`。
手工触发与删除：

```powershell
schtasks /Run /TN WebMonitoring-Backup
schtasks /Delete /TN WebMonitoring-Backup /F
```

Linux 用 `deployments/scheduling/` 里的 systemd `.service` / `.timer` 模板（注意把
`WorkingDirectory` 换成绝对路径，且 `ExecStart` 假定 `powershell` 在 `/usr/bin/powershell`）。

随时可以手工验证整条备份链（跑两个备份周期 + restore dry-run，**不动运行数据**）：

```powershell
.\scripts\backup-rehearsal.ps1
```

### 5.3 恢复

恢复**默认只校验、不改数据**，必须显式确认才动手：

```powershell
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000                 # dry-run
.\scripts\compose-restore.ps1 -BackupRoot .data-backups\20260914-120000 -Execute -ConfirmText RESTORE
```

`-Execute` 是**破坏性**的：它会停掉 `api` 与 `http-worker`、用
`pg_restore --clean --if-exists` **覆盖式**恢复数据库、用 `rclone sync` **清空并覆盖**
桶内容，然后跑迁移并重启服务。`-ConfirmText` 必须精确等于 `RESTORE`（大小写敏感）。

**生产环境执行前必须记录审批、备份版本、RPO/RTO 和回滚方案。** 目标 RPO ≤ 24 小时、
RTO ≤ 4 小时；那次演练的实测是冷启动恢复约 7 分钟（见
[backup-restore-rehearsal.md](backup-restore-rehearsal.md)）。

### 5.4 回滚

**回滚策略是「只前进、不回滚」**：回滚 = 换回上一版镜像 + 用备份把数据库恢复到升级前，
**不是**逆向执行迁移。

### 5.5 手工碰对象存储

主机上**没有**对象存储端口，客户端要跑在 Compose 网络里、端点写 `objectstore:9000`：

```powershell
docker compose -f deployments/compose/docker-compose.yml run --rm rclone lsf :s3:evidence
```

用 rclone 时必须带 `--ignore-times`——默认的「大小 + mtime 相同就跳过」实测会跳过一个
内容不同的对象。**成品备份一律用 `scripts/compose-backup.ps1`，不要手工拼装。**

### 5.6 扩容与多副本

`api` 与 `http-worker` 可以横向扩：任务领取靠数据库租约与唯一索引，多副本不会重复分组
（已由 `multi-replica-drill.ps1` 验证：`--scale api=2 --scale http-worker=3`，40 资产
40 任务、0 重复）。**不要设 `WORKER_ID`**，否则副本共用一个身份而互相抢占在途任务（4.1）。

### 5.7 迁机与卸载

全部持久状态在三个具名卷里（`postgres-data`、`redis-data`、`objectstore-data`）。迁机 =
在新机按本手册部署 + 用 5.3 恢复备份，**不要**直接搬卷。

卸载并**删除全部数据**（不可逆）：

```powershell
docker compose -f deployments/compose/docker-compose.yml down --volumes
```

---

## 第 6 部分：排障入口

**第一步永远是 `GET /readyz`**：它逐个检查依赖，不健康时返回 503 和依赖名。

| `/readyz` 的 key | 实际组件 | 对应手册 |
| --- | --- | --- |
| `postgres` | PostgreSQL 连接池 | [postgres.md](runbooks/postgres.md) |
| `redis` | Redis detection Stream | [redis.md](runbooks/redis.md) |
| `alert_redis` | Redis alert Stream | [redis.md](runbooks/redis.md) |
| `evidence` | 对象存储（Compose 下即 VersityGW） | [object-storage.md](runbooks/object-storage.md) |
| `browser_worker` | 浏览器 Worker `/healthz` | [browser-worker.md](runbooks/browser-worker.md) |

> `/readyz` **不覆盖**告警投递链路：投递失败时 API 和 worker 都仍然就绪。告警问题只能
> 从交付表、指标和控制台的渠道列表看出来，见
> [alert-delivery.md](runbooks/alert-delivery.md)。

完整的「症状 → 手册」对照表（含每条症状最可能的原因）在
[runbooks/README.md](runbooks/README.md)，这里不重复。**通用只读诊断**先跑这几条：

```powershell
# 容器状态与重启次数（RestartCount 高 = 反复崩溃）
docker compose -f deployments/compose/docker-compose.yml ps
docker compose -f deployments/compose/docker-compose.yml logs --tail=200 <service>
```

> **日志只保留最近 3×10MB。** 所有服务用 `json-file` 驱动、`max-size: 10m`、
> `max-file: 3`。这意味着「上周的日志」通常已经不在了：**先取证再重启**，尤其是容器
> 已经崩溃重启过的场景。重启会清掉 stdout 日志、临时文件和内存状态，是最常见的
> 不可逆动作之一。

排查前先确认**你在看哪个 Compose 项目**——验收脚本各有自己的栈与端口（第 2 部分），
否则读的是另一个栈的状态。

---

## 交付前清单

装完或升级完，逐条打勾：

- [ ] `docker compose -f deployments/compose/docker-compose.yml config --quiet` 无输出
      （清单可解析、变量齐备）
- [ ] `docker compose -f deployments/compose/docker-compose.yml ps` 六个常驻服务 `Up`，
      `migrate` 为 `Exited (0)`
- [ ] `GET /readyz` 返回 200
- [ ] 默认管理员口令**已改**，且不是从 `.env.default` 继承来的值
- [ ] `.env` 里六个无回落变量全部替换过开发值
- [ ] `APP_ENV=production` 时，4.1 表里每一条都过了一遍（进程能起来本身就是证据）
- [ ] HTTPS 已启用、`SESSION_SECURE=true`、80 端口返回 308、`/metrics` 在边缘 404
- [ ] `GET /metrics` 需要 `Bearer` 才能读
- [ ] `POSTGRES_SSLMODE` 的去向已明确（4.4：要么外部 TLS，要么书面接受明文）
- [ ] `.\scripts\compose-smoke.ps1` 退出码 0
- [ ] 备份脚本跑通过至少一次，且恢复脚本的 dry-run 校验通过
- [ ] 备份已排进计划任务，并手工触发过一次确认落地
- [ ] 有人知道 `webmonitoring_last_successful_detection_timestamp_seconds` 该往哪看
- [ ] 证书续期接上了 `reload-tls.ps1`（或至少有人负责这件事）

---

## 这份手册没覆盖的部分

写在这里不是因为不重要，而是因为它们**确实没做过**，而「文档说可以」与「跑通过」在
这个仓库里是两件事。

- **生产 HTTPS 整条路径从未演练。** 4.2 是从清单和 nginx 配置读出来的，不是从一次成功
  的运行里读出来的——`docker-compose.tls.yml`、`edge`、`TLS_CERT_DIR`、`reload-tls.ps1`
  在 [rehearsals.md](rehearsals.md) 与 [roadmap.md](roadmap.md) 的「从未实测的场景」里
  **都没有任何记录**。这一条本身也还没登记进去。
- **`POSTGRES_SSLMODE` 与仓库内 PostgreSQL 的 TLS 能力对不上**，见 4.4。
- **第 1.2、3.2、3.3、4.6 节记的四条缺陷已修（2026-10-13），但都还没有经过一次真实的
  部署运行**：改的是据实核对的代码与文案（`docker-compose.yml` 的 13 条错误信息、
  两个扫描间隔的 `0`、控制台的资产类型下拉、授权验证弹窗），验证方式是单元测试与
  类型检查，不是一次端到端装机。第一次照本手册部署时，这四处值得多看一眼。
- **用发布产物起整套栈做不到**：GHCR 上的镜像已发布、已签名、已扫描，但把 `build` 换成
  `image: …@sha256:…` 的 overlay 尚未提供（[roadmap.md](roadmap.md) 2.9 判据 6）。所以
  「本地 `docker compose up` 跑通」**不等于**「发布产物跑通」。
- **发布是不完整的**：只出 amd64；没有 GitHub Release 或可下载产物，因此「迁移顺序与
  回滚策略」「配置与密钥变更清单」在 tag 上没有载体；`console` 与 `browser-worker`
  被门禁按 44 + 10 条 HIGH（均来自底座镜像）拦下，**没有当前的发布 tag**。
- **外部依赖全都没上过实机**：外部 S3（bucket 创建、预签名 URL、真实读写）、真实 SMTP
  收信、Prometheus 真实抓取。采集器要自己加，仓库不带。
- **多租户 SaaS 与强合规隔离不在范围内**：没有资源级授权与双人审批，主密钥以明文环境
  变量注入，未接外部 KMS。见 [product-scope.md](product-scope.md)。
- **只有单机 Docker Compose**：没有 K8s 清单，没有 Helm chart。
