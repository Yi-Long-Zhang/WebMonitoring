# 验收演练总账

本文是全部实测结果的**唯一事实来源**（append-only）。README 和 docs/README 的
实测速览只保留一行摘要并指向这里；`architecture.md` 与 `operations.md` 内嵌的
实测输出是设计论证证据，原地保留。

**追加约定**：每次演练后在下方新增一节，按时间倒序（最新在最前），不修改、
不删除历史小节。每节固定字段：日期、脚本、关键断言、结果、专项档案（如有）。
数字以脚本实际输出为准，不四舍五入、不补估算值。

---

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
