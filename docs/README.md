# WebMonitoring 项目文档

## 文档导航

| 文档 | 用途 |
| --- | --- |
| [product-scope.md](product-scope.md) | 产品目标、用户、MVP 边界和验收指标 |
| [architecture.md](architecture.md) | 技术选型、模块边界和部署拓扑 |
| [domain-model.md](domain-model.md) | 核心对象、状态机和数据契约 |
| [security-model.md](security-model.md) | 威胁模型、权限、SSRF 和证据安全 |
| [engineering.md](engineering.md) | 分支、提交、评审、测试、发布和回滚 |
| [operations.md](operations.md) | Docker Compose 部署、迁移、备份、恢复和验收 |
| [backup-restore-rehearsal.md](backup-restore-rehearsal.md) | 备份恢复演练记录模板和验收门禁 |
| [adr/](adr/) | 需要长期保留的架构决策记录 |

## 文档规则

- `AGENTS.md` 是最高级项目级 Agent 规则，只放不可妥协约束。
- `docs/` 说明如何执行，不得降低 `AGENTS.md` 的安全要求。
- 代码行为改变时，同一提交应更新受影响的文档。
- 重大技术、数据模型和安全边界变化必须新增或更新 ADR。
- 事实、决定、假设和待确认事项必须明确区分。

## 状态约定

文档中的功能状态使用以下含义：

- **已实现**：代码、接口和自动化测试已经覆盖，仍需按发布门禁验证部署环境。
- **部分实现**：主流程可运行，但存在明确的范围、环境或安全限制。
- **规划中**：产品或架构目标，当前代码不应被视为已经提供。

## 当前状态

当前仓库处于 **单机 Compose 正式验收加固阶段**。已实现或部分实现：

- Go 控制面、Go HTTP 检测 Worker、Playwright 浏览器 Worker 和 Vue 控制台。
- 多租户按 slug 登录、SUPER_ADMIN 平台管理员、站点/租户 CRUD 与租户级隔离。
- 资产登记、负责人、三种所有权验证（HTTP 文件 / DNS TXT / HTML meta 标签）、
  启停控制、暂停/恢复和监测策略。
- HTTP/浏览器检测、SSRF 防护、响应和渲染大小限制、重定向复核及行为记录。
- 候选基线、批准/拒绝/废弃、差异信号（解析 IP/TLS 指纹/资源/弹窗等）、事件聚合、
  处置状态机和审计记录。
- HTML、快照和截图证据保存；下载和报告导出校验证据大小及 SHA-256；短时签名证据 URL。
- Webhook/企业微信/DingTalk/邮件通知、投递记录和事件级去重。
- 登录防爆破与账户锁定、Redis 会话持久化、用户/角色管理、keyset 真分页、
  任务取消/超时终态、优雅停机和批量导入/导出。
- 登录态检测（HTTP 与浏览器两条路径）：客户凭据 AES-GCM 加密、登录地址在控制面和
  Worker 两侧各做一次 SSRF 复核、登录后抓取受保护页面，API 响应不泄露凭据。
  浏览器模式下凭据由控制面解密后经内部 HTTP 传给 Worker，生产门禁要求该端点使用
  https 或私网地址（见 [adr/0004](adr/0004-browser-login-credentials.md)）。
- 内存/本地证据开发模式，以及 PostgreSQL、Redis、MinIO 的 Compose 路径。
- 生产配置门禁、90 天证据/365 天审计保留策略、dry-run 清理和备份恢复校验脚本。
- 可重复的验收演练脚本：Compose smoke（含登录锁定、keyset 分页、资产暂停/恢复、
  任务取消与迟到结果不覆盖）、100 资产跨租户压测（逐资产真实 FILE 所有权验证、
  吞吐下限与任务完整性断言）、备份/恢复（MinIO 客户端在 Compose 网络内运行，
  不依赖宿主机 `mc` 或 PowerShell 7）。
- OpenTelemetry Trace（env 门控、默认 no-op）和补充的单元测试（含 miniredis）。
- Repository 错误传播、真实 PostgreSQL/Redis 集成测试和 100 资产调度合成测试。

当前明确不应描述为已完成或尚未完成部署验收：

- **多副本（HA）尚未实机演练**：三处跨副本阻塞点已经修复 —— 检测任务的重复排队由迁移
  `000012` 的部分唯一索引 `detection_jobs_active_uidx` 兜底，Worker 消费者身份由
  `config.WorkerIdentity` 生成每进程身份，保留策略清理由 `store.ReplicaLease` 的 PostgreSQL
  会话级 advisory lock 串行化，并会为定时删除补写审计事件。但尚未在 `--scale` 拓扑上实跑
  验证，演练通过前不要按多副本部署。另需确认 `ReapTimedOut` 的 5 分钟租约长于最慢的一次
  检测，它不校验任务持有者。
- **保留策略清理缺少审计**：定时清理（`RunAllTenants`）只记录日志，不写审计事件，
  只有手动端点 `POST /maintenance/retention` 会写。自动删除证据因此没有审计留痕。
- **完整事件时间线**：`UpsertIncident` 的 `signals = EXCLUDED.signals` 会覆盖上一轮信号集，
  事件只保留 `occurrence_count` 和 `last_seen_at`；事件由检测 Worker 创建时不写审计，
  `ListAudits` 没有资源过滤，审计记录本身还会被保留策略删除。历史无法事后重建，需要
  新增事件时间线存储。
- **控制台接线**：keyset 分页（前端不传 `limit`/`cursor` 且丢弃 `nextCursor`，所有列表
  只显示第一页）、任务取消、钉钉/邮件渠道创建、租户启停、基线快照预览等后端已具备的
  能力尚未接入界面。
- 租户和站点的完整管理界面（当前为站点/租户 CRUD 基础形态）。
- 细粒度权限、跨租户管理员、二次确认和双人审批。前端权限只到角色级，且
  `permissions.ts`、`stores/session.ts` 和 `Sidebar.vue` 三处重复定义同一策略。
- 完整 Secret Manager 集成（当前凭据用 `ALERT_SECRET_KEY` 派生密钥加密，没有密钥版本
  和轮换路径，`SecretCipher` 也没有可替换的接口抽象）。
- 完整多租户运营管理。
- 部署形态：只有单机 Compose，无 Kubernetes、无 TLS 终止、除 browser-worker 外无
  CPU/内存限制，Compose 中 Redis 未设密码、PostgreSQL 使用 `sslmode=disable`。
- 真实 100 资产 **浏览器** 压测和备份恢复实机演练；控制台 Playwright E2E
  已纳入 Compose smoke，并已于 2026-09-15 在本机实跑通过（见
  [operations.md](operations.md) 的验收清单和 [backup-restore-rehearsal.md](backup-restore-rehearsal.md)）。
- Docker Compose smoke 和真实数据库/Redis 集成门禁已于 2026-09-15 在本机实跑
  通过；演练记录覆盖单机 Compose 拓扑，尚未覆盖多副本、外部对象存储和真实生产网络。

功能状态以代码、测试结果和部署验证记录为准。通过本地内存模式不能推出
生产可用性；发布前必须单独完成 Compose、数据库迁移、队列恢复和安全验收。

## 当前验证基线

截至 2026-09-15，以下检查已在本地通过：

- `go test ./...`（含 `POSTGRES_TEST_DSN` 指向真实 PostgreSQL 的集成测试）
- `go vet ./...`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker typecheck`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker test`
- `corepack pnpm --filter @webmonitoring/console lint`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker build`
- `.\scripts\compose-smoke.ps1`（含 P0 登录锁定、P1 keyset 分页、资产暂停/恢复、
  任务取消、控制台 Playwright E2E）
- `.\scripts\load-test.ps1`：100 资产 / 2 租户全部验证启用，100 个检测任务
  全部 SUCCEEDED，吞吐 32.8 任务/秒（下限 1.0），0 假阳性事件；同一数据卷上
  重复运行同样通过
- `.\scripts\compose-backup.ps1` + `.\scripts\compose-restore.ps1`：备份
  1198 个受校验文件，恢复后 `/readyz`、证据 SHA-256、任务数与 Redis pending
  全部通过（见 [backup-restore-rehearsal.md](backup-restore-rehearsal.md)）

前端构建存在 Element Plus 产物体积提示，但不是构建失败。以上演练均在单机
Docker Compose 拓扑上完成，尚未覆盖多副本、外部对象存储和真实生产网络。
