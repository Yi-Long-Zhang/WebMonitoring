# 验收演练总账

本文是全部实测结果的**唯一事实来源**（append-only）。README 和 docs/README 的
实测速览只保留一行摘要并指向这里；`architecture.md` 与 `operations.md` 内嵌的
实测输出是设计论证证据，原地保留。

**追加约定**：每次演练后在下方新增一节，按时间倒序（最新在最前），不修改、
不删除历史小节。每节固定字段：日期、脚本、关键断言、结果、专项档案（如有）。
数字以脚本实际输出为准，不四舍五入、不补估算值。

---

## 0.4 会话路径静默失败实测 — 2026-10-01

- **脚本**：手工实测，真实 Compose 栈（分支 `codex/auth-failures` 构建的 api
  镜像），临时账号（argon2id，用后连同审计行删除）；`docker compose stop redis`
  制造中断，`start redis` 恢复。
- **关键断言**：(1) Redis 中断期间，带有效会话 Cookie 的请求必须得到 **5xx**
  而非 401（区分「不存在」与「后端故障」）；(2) 正确密码登录在会话写不进去时
  必须 **500** 而非 401，也不返回会被下一条请求拒绝的 Cookie；(3) Redis 恢复后
  原会话直接 200——抖动不得把全员登出。
- **结果**：
  - 中断前 `GET /api/v1/dashboard`：200。
  - `stop redis` 后同一 Cookie：500 `STORAGE_UNAVAILABLE`（非 401
    `SESSION_EXPIRED`）；正确密码登录：500 `SESSION_STORE_UNAVAILABLE`
    （非 401 `INVALID_CREDENTIALS`）。
  - `start redis` 后同一 Cookie 再请求：**200**（会话存活，无人被登出）；
    新登录 201。
  - 单元/接口回归：`internal/auth` 新增「Put 失败则登录失败」「Get 区分
    ErrSessionNotFound 与后端错误」（miniredis 模拟 Redis 宕机），
    `internal/api` 新增「会话后端故障 → 500」「登录写会话失败 → 500」；
    全部通过，19 包全绿。
- **修法摘要**：`SessionStore.Get` 签名从 `(record, bool)` 改为
  `(record, error)`，新增 `ErrSessionNotFound` 哨兵；Redis 实现把 `redis.Nil`
  映射为 NotFound、其余错误原样包装；`Service.Login` 的 `store.Put` 错误
  从忽略改为失败关闭；`authenticate` 中间件按错误类型分 401/500，登录
  handler 对后端故障返回 500 且不计入登录失败锁定计数。
- **未覆盖**：会话存储故障期间登出（`DELETE` 同样 best-effort）无单独断言；
    http-worker 消费端重试行为在 0.3 已实测，本次未重复。

---

## 0.3 worker 优雅停机与消费重试实测 — 2026-10-01

- **脚本**：手工实测，在真实 Compose 栈（分支 `codex/worker-shutdown` 构建的镜像）
  上加一次性 overlay（compose 内网慢目标：node HTTP 服务固定 20 秒才回响应；
  `ALLOW_PRIVATE_TARGETS=true` 仅供本网段目标可达，用完即删），检测任务经
  `POST /api/v1/detection-jobs` 用临时账号（argon2id，用后连同资产/策略/任务/
  证据/审计行一并删除）触发。
- **关键断言**：(1) http-worker 在检测在途时收到 SIGTERM，须等该任务跑完再退出，
  任务不丢、状态落库为 SUCCEEDED；(2) Redis 中断 ~10 秒，worker 打退避重试日志
  且进程不退出，恢复后仍能消费；(3) browser-worker 在快照在途时收到 SIGTERM，
  打排水日志、在途检查完成、退出码 0、不触发 60 秒强制退出；(4) 所有服务
  PID 1 为 init。
- **结果**：
  - 合并前整体回归（2026-10-01，`codex/worker-shutdown` 镜像）：
    `compose-smoke.ps1` 通过（`Compose smoke test passed`，退出码 0，含控制台
    Playwright E2E）；`multi-replica-drill.ps1` 通过（`Multi-replica drill passed`，
    40 任务、duplicate groups 0、退出码 0）。 drill 首跑失败系其复用的
    postgres 卷存着 CRLF 时期的 checksum 记录，属同一行尾问题的既有库症状，
    `down -v` 清掉一次性演练卷后通过。
  - http-worker 排水：任务 14:56:26 进入 RUNNING（慢目标 20 秒），`stop` 发送
    SIGTERM，日志 `shutdown signal received; draining in-flight work` → 约 19 秒后
    `in-flight work drained`，容器退出码 0；任务 `finished_at` 14:56:46，
    状态 SUCCEEDED。
  - Redis 中断：`stop redis` 后两条消费线各打一条
    `consumer stopped; retrying ... backoff=500ms`，进程存活；`start redis` 后
    再触发任务，正常消费至 SUCCEEDED（期间无进程重启）。
  - browser-worker 排水：BROWSER 任务 RUNNING 后 `stop`，日志
    `received SIGTERM; draining in-flight checks`，容器约 33 秒后退出码 0
    （未触发 `drain timeout exceeded; forcing exit`），任务 SUCCEEDED。
  - `docker top compose-browser-worker-1`：PID 1 为 `/sbin/docker-init`，
    node 为其子进程；api `stop` 退出码 0。
- **顺带修复（独立提交 `641ed60`）**：migrate 按原始字节校验迁移文件，
  autocrlf 曾把 worktree 副本翻成 CRLF 导致重建镜像校验和不符、migrate 拒绝
  启动。已加 `.gitattributes`（`db/migrations/*.sql text eol=lf`），并把
  schema_migrations 里 11 条按 CRLF 记录的 checksum 修回 LF  canonical 值
  （逐文件核对 worktree 字节与 git blob 一致后才更新）。
- **未覆盖**：浏览器 worker 排水期间 chromium 子进程的逐一存活清点（以容器
  整体退出、退出码 0 与 init PID 1 为证据）；api 在途请求排水的精确时序。

---

## 0.2 有界查询与任务保留实测 — 2026-10-01

- **脚本**：手工实测，在真实 Compose 栈（`deployments/compose/docker-compose.yml`，
  分支 `codex/bounded-queries` 构建的镜像）上执行：psql 灌入 100,000 行
  `detection_jobs`（`generate_series`，40 天均匀散布，初始全部 SUCCEEDED，
  其中 25,069 行早于 30 天窗口）+ 临时测量账号（argon2id，用后删除）。
- **关键断言**：(1) dashboard 在 10 万行任务下仍为聚合查询延迟；(2) 保留
  dry-run 报出的 `expiredJobs` 与窗口内行数一致且 execute 恰好删除这些行；
  (3) 调度器跨副本不重复分组；(4) 主链路 smoke 回归通过。
- **结果**：
  - dashboard（登录会话实测，20 次采样）：p50 = 1ms、p95 = 2ms，
    返回体正确；把 100 行改为 FAILED 后 p50 = 2ms、p95 = 3ms 且
    `failedJobs = 100`，证明数字来自 `FailedJobCount` 聚合而非分页内容。
  - 调度聚合查询 `EXPLAIN (ANALYZE)`（74,916 行上）：Seq Scan +
    HashAggregate，执行 13.6ms，每组一行输出；单租户场景下计划器选择
    顺序扫描是正确选择，成本由任务保留（30 天窗口）封顶，不随明细增长。
  - 保留清理：dry-run `expiredJobs=25084 deletedJobs=0`；execute
    `deletedJobs=25084` 且 `failureReasons` 为空；二次执行又清掉恰好
    3 行（种子 `created_at` 随机散布在窗口边界上的行），无多删。
  - 迁移 `000016`（`detection_jobs_reap_idx ON (tenant_id, status, started_at)`）
    已应用，`pg_indexes` 可见。
  - `multi-replica-drill.ps1`：40 资产 40 任务、0 重复分组、40 成功
    （调度器改写后跨副本语义不变）；`compose-smoke.ps1`：
    `Compose smoke test passed`，退出码 0。
- **未覆盖**：`reports/summary` 仍是全表搬运（本次未改，其成本现由任务保留
  封顶）；10 万行是在单租户单资产下灌入的，未测多租户大表下计划器是否仍选
  顺序扫描（语义不受计划影响）。
- **专项档案**：无。

## 合并后主链路回归（smoke）— 2026-09-26

- **脚本**：`scripts/compose-smoke.ps1`（`-ExecutionPolicy Bypass`；策略探测本身
  在本机不可用，故直接绕过）
- **关键断言**：脚本 `$ErrorActionPreference = "Stop"` 且全程 `throw` 即中止，
  因此「通过」等于全部断言为真：登录锁定（`ACCOUNT_LOCKED`）、keyset 分页、
  资产暂停后的 `JOB_REJECTED`、任务取消、浏览器登录态的正负控
  （无 `AuthConfig` 见登录页 / 有则见受保护页）、控制台 Playwright E2E。
- **结果**：`Compose smoke test passed`，退出码 0，容器与网络全部正常回收。
  本次是 `codex/fix-error-leak` 与 `codex/pool-timeouts` 合并进 `main`
  （`c136530`、`7f0d7df`）后的回归，覆盖前面那条「未跑 `compose-smoke`」的缺口。
- **未覆盖**：仍未做计划里 `pg_sleep(120)` 的慢查询用例（池的建连/查询边界）；
  S3 超时仍未对真实 MinIO 造过中途停住的对象。
- **专项档案**：无。

## 连接池与配置转发实测 — 2026-09-26

- **脚本**：无（手工实测；`docker compose config --format json` + 真实 Compose 栈
  上的一次性 `webmonitoring-verify`，用完即 `down -v` 拆除）
- **关键断言**：
  - 池上限对真实连接生效：给 40 路并发 `/readyz` 打压力时，用
    `pg_stat_activity` 采样应用侧连接数，上限 2 的那次峰值恰好为 2，上限 10 的
    对照那次峰值为 3（本轮压力只需 3 条，上限 10 不构成约束）。
  - 连接池是逐副本的：两个副本各自 `POSTGRES_MAX_CONNS=2`，`pg_stat_activity`
    里出现 4 条应用连接，即 2×2 —— 与「扩容副本要同步下调单副本上限」这条
    运维要求一致。
  - 生产门禁在容器内生效（`APP_ENV=production`）：`POSTGRES_MAX_CONNS=0`
    与 `ALERT_FAILURE_THRESHOLD=0` 都让进程以退出码 1 失败，且分别点名该变量。
  - `.env` 里声明的键真的进到服务：改动前三个服务的 environment 里这些键是
    `<absent>`，改动后 `rewrap` 拿到池上限，`api`/`http-worker` 拿到池上限与
    全部 8 个队列/告警键。
- **结果**：
  - 池上限：`POSTGRES_MAX_CONNS=2` → `/metrics`
    `webmonitoring_postgres_pool_max_connections 2`，采样峰值 2；对照 10 → 峰值 3。
  - 逐副本：2 副本 × 2 = 4 条应用连接。
  - 门禁 A：`invalid production configuration: … POSTGRES_MAX_CONNS must be
    positive in production: 0 leaves the pool on its own default instead of the
    limit you asked for`（退出码 1；同一次还报了 S3_ACCESS_KEY 与 sslmode，
    说明门禁把本条与既有的几条一起判了）。
  - 门禁 B：`ALERT_FAILURE_THRESHOLD must be positive in production: 0 disables
    that notification control`（退出码 1）——这正是 `.env.example` 上写的那句
    承诺，改动前那个 0 根本进不了容器（`validateAlertGate` 对空值 `continue`，
    而容器的 `os.Getenv` 只会返回空）。
  - 转发前后（`docker compose config`，shell 里给 `POSTGRES_MAX_CONNS=42`、
    `ALERT_FAILURE_THRESHOLD=9`）：改动前 `rewrap`/`api`/`http-worker` 三项
    都是 `<absent>`；改动后 `rewrap` 得 `42`，`api`/`http-worker` 得 `42` 与
    `9`，`migrate` 两项都不出现（它用自建的一次性池，有意不转发）。
- **未覆盖（不得当作已验）**：`compose-smoke.ps1` 本轮未跑；计划里那条
  `pg_sleep(120)` 慢查询用例未执行（本轮只证了池上限与逐副本，未证
  `POSTGRES_CONNECT_TIMEOUT_SECONDS` 对卡死查询的边界）；S3 超时只在单测里用
  「接受连接但永不回响应头」的 httptest 服务证过，未对真实 MinIO 造过中途停住的
  对象。
- **专项档案**：无（本条为手工实测记录）。

## 多副本（HA）演练 — 2026-09-20

- **脚本**：`scripts/multi-replica-drill.ps1`（`--scale api=2 --scale http-worker=3`）
- **关键断言**：调度器自行排队的任务数恰好等于资产数；无重复的
  `(tenant, asset, mode)` 分组；每个 worker 副本有唯一消费者身份。
- **结果**：40 资产 40 任务，0 重复分组，40 全部成功，wall clock 12.2 秒。
  同次运行的两个变异检查如期失败（`-DropActiveJobIndex` 使任务数变 66、
  重复组 26；固定 `WORKER_ID` 使三副本注册成同一名字），证明断言非空转。
- **专项档案**：设计论证见 [architecture.md](architecture.md) 多副本一节。

## 100 资产浏览器压测 — 2026-09-20

- **脚本**：`scripts/load-test.ps1 -Mode BROWSER -AssetCount 100 -Tenants 2 -HttpWorkers 3`
- **关键断言**：100 个 BROWSER 任务全部 SUCCEEDED；基线审批前 0 事件
  （无假阳性）。
- **结果**：100/100 成功，500 条证据，wall clock 85.2 秒，吞吐 1.17 任务/秒
  （下限 1.0）。
- **专项档案**：吞吐模型分析见 [operations.md](operations.md) 浏览器模式压测一节。

## 备份恢复演练 — 2026-09-15

- **脚本**：`scripts/compose-backup.ps1` + `scripts/compose-restore.ps1`
- **关键断言**：manifest 中每个文件 SHA-256 校验一致；恢复后 `/readyz` 200、
  证据 SHA-256 逐对象一致、任务数一致、Redis pending 归零。
- **结果**：1198 个受校验文件全部通过；冷启动恢复约 7 分钟，重复恢复 29 秒。
  首次恢复暴露「目标 bucket 不存在时 `mc mirror` 拒绝写入」，已修复并有
  回归保护。
- **专项档案**：[backup-restore-rehearsal.md](backup-restore-rehearsal.md)
  （完整字段表与暴露问题清单）。

## 100 资产 HTTP 压测 — 2026-09-15

- **脚本**：`scripts/load-test.ps1`（HTTP 模式，100 资产 / 2 租户）
- **关键断言**：100 任务全部 SUCCEEDED；吞吐不低于 1.0 任务/秒；基线审批前
  0 事件（无假阳性）。
- **结果**：吞吐 32.8 任务/秒，0 假阳性事件。

## 单机 Compose 验收（smoke）— 2026-09-15

- **脚本**：`scripts/compose-smoke.ps1`（含控制台 Playwright E2E）
- **关键断言**：登录锁定、keyset 分页、暂停/恢复、任务取消、控制台核心流程
  全通过。
- **结果**：通过。

---

## 尚未执行的演练

- **外部对象存储（S3）演练**：状态：部分实现 —— MinIO 已实测，外部 S3
  演练尚未执行。方案与回填步骤见
  [object-storage-rehearsal.md](object-storage-rehearsal.md)。
