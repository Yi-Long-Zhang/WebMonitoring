# WebMonitoring 项目文档

## 文档导航

| 文档 | 用途 |
| --- | --- |
| [product-scope.md](product-scope.md) | 产品目标、用户、MVP 边界和验收指标 |
| [architecture.md](architecture.md) | 技术选型、模块边界和部署拓扑 |
| [domain-model.md](domain-model.md) | 核心对象、状态机和数据契约 |
| [security-model.md](security-model.md) | 威胁模型、权限、SSRF 和证据安全 |
| [engineering.md](engineering.md) | 分支、提交、评审、测试、发布和回滚 |
| [operations.md](operations.md) | Docker Compose 部署、迁移、备份、恢复、TLS 终止和验收 |
| [runbooks/](runbooks/README.md) | 故障处置手册：按 `/readyz` 依赖定位 Postgres/Redis/对象存储/浏览器 Worker/告警投递 |
| [backup-restore-rehearsal.md](backup-restore-rehearsal.md) | 备份恢复演练记录模板和验收门禁 |
| [object-storage-rehearsal.md](object-storage-rehearsal.md) | 外部 S3 验证步骤（状态：尚未执行） |
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
- 事件时间线（`incident_events`，append-only）：每轮复现各自的信号集、状态流转、
  负责人变更和备注。事件行上的 `signals` 每轮被覆盖，复现明细只存在于时间线里。
  时间线条目随事件生命周期，**不参与保留期清理**（`docs/security-model.md` 把变更与
  处置归为不可静默修改的审计数据）。
- HTML、快照和截图证据保存；下载和报告导出校验证据大小及 SHA-256；短时签名证据 URL。
- Webhook/企业微信/DingTalk/邮件通知、投递记录和事件级去重。
- 登录防爆破与账户锁定、Redis 会话持久化、用户/角色管理、keyset 真分页、
  任务取消/超时终态、优雅停机和批量导入/导出。
- 登录态检测（HTTP 与浏览器两条路径）：客户凭据 AES-GCM 加密、登录地址在控制面和
  Worker 两侧各做一次 SSRF 复核、登录后抓取受保护页面，API 响应不泄露凭据。
  浏览器模式下凭据由控制面解密后经内部 HTTP 传给 Worker，生产门禁要求该端点使用
  https 或私网地址（见 [adr/0004](adr/0004-browser-login-credentials.md)）。
- 内存/本地证据开发模式，以及 PostgreSQL、Redis、MinIO 的 Compose 路径。
- 生产配置门禁、90 天证据/365 天审计保留策略、dry-run 清理和备份恢复校验脚本；
  定时清理会为实际发生的删除补写 `maintenance.retention.scheduled` 审计事件。
- 可重复的验收演练脚本：Compose smoke（含登录锁定、keyset 分页、资产暂停/恢复、
  任务取消与迟到结果不覆盖）、100 资产跨租户压测（逐资产真实 FILE 所有权验证、
  吞吐下限与任务完整性断言）、备份/恢复（MinIO 客户端在 Compose 网络内运行，
  不依赖宿主机 `mc` 或 PowerShell 7）。
- OpenTelemetry Trace（env 门控、默认 no-op）和补充的单元测试（含 miniredis）。
- 出站告警的配额与熔断（`internal/notifygate`）、通用请求限速（读/写/重操作三类
  配额，生产门禁拒绝 0 与「等同于无限制」的高值）、CORS 与 HSTS，以及带
  `method`/`status`/`path` 标签的请求级指标（路径按集合归并，签名令牌不进入标签）。
- Repository 错误传播、真实 PostgreSQL/Redis 集成测试和 100 资产调度合成测试。

当前明确不应描述为已完成或尚未完成部署验收：

- **多副本（HA）已于 2026-09-20 实机演练通过**，数字与变异检查记录在
  [architecture.md](architecture.md)。以下三处跨副本阻塞点已经修复 —— 检测任务的重复排队由迁移
  `000012` 的部分唯一索引 `detection_jobs_active_uidx` 兜底，Worker 消费者身份由
  `config.WorkerIdentity` 生成每进程身份，保留策略清理由 `store.ReplicaLease` 的 PostgreSQL
  会话级 advisory lock 串行化，并会为定时删除补写审计事件。演练以
  `--scale api=2 --scale http-worker=3` 实跑，40 资产对应 40 个任务、0 个重复分组。
  `ReapTimedOut` 的租约已改为 `JOB_LEASE_SECONDS` 可配，并由生产门禁强制
  `> 270` 秒（策略超时上限 90 秒 × BROWSER 花两次）；它仍不校验任务持有者，
  保证是「没有合法检测能超过租约」而非「reaper 知道谁持有任务」。
- **控制台接线**：keyset 分页、任务取消、钉钉/邮件渠道创建、租户启停、权限收口、
  审计筛选和资产导入导出、用户管理、证据短时签名 URL、租户删除、基线快照预览、
  告警渠道编辑与删除均已接入界面。其中「基线快照预览」此前记为 2026-09-20 已接入，
  实际当次提交（`9d578a1`）只暴露了 API 契约与后端处理器，没有任何前端文件；
  界面部分于 2026-09-21 补齐，本节在此更正。

  告警渠道的编辑有两条**必须**保持的约束，都来自密文与行的绑定关系：
  更新必须是同一行原地重写（`AlertChannelUpdate` 走 `PUT`），因为密文的关联数据
  含 `<tenantID>:<channelID>`，重建行会让存量凭据永久解不开；且界面**不回显**
  `url` 与 `secret` —— 列表返回的 `url` 是脱敏值（路径与查询变成 `...`），
  `secret` 从不返回，把这两个字段预填再保存会把脱敏值写回真实地址。
  因此编辑时二者都要求重新填写，`secret` 留空表示「保持原值」。
- 租户和站点的完整管理界面（当前为站点/租户 CRUD 基础形态）。
- 细粒度权限、跨租户管理员、二次确认和双人审批。前端权限只到角色级；
  角色矩阵已于 2026-09-20 收敛到 `apps/console/src/app/permissions.ts` 单一来源
  （此前 `Sidebar.vue` 漏了 `canOperate`，五个导航项对只读角色也可见），
  但仍没有资源级或字段级授权。
- 完整 Secret Manager 集成。凭据加密已于 2026-09-20 支持密钥版本：`ALERT_SECRET_KEYS`
  按 `v1:,v2:` 显式标注，`security.Keyring` 实现 `security.SecretBox` 接口，密文带版本
  前缀，主密钥取最高版本。存量密文的再加密扫描**已于 2026-09-22 实现**
  （`internal/rewrap` + `apps/rewrap`，支持 `--dry-run`、advisory lease 串行化与审计），
  但**尚未在真实数据上执行过** —— 它是一次需要授权的维护操作，未接入外部 KMS/Vault。
- 完整多租户运营管理。
- 部署形态：只有单机 Compose，无 Kubernetes。**TLS 终止已具备**：叠加
  `docker-compose.tls.yml` 后由 nginx 边缘终止 TLS 且只由边缘发布端口，配套
  `scripts/reload-tls.ps1` 在证书续期后 SIGHUP 重载（不重启容器，不断开 SSE 流）；
  仓库不签发证书，ACME 客户端由部署方选择。Compose 已于 2026-09-20
  加固：全部服务有 CPU/内存限制、日志轮转和 `no-new-privileges`，MinIO 不再向宿主机
  暴露端口，`REDIS_PASSWORD` 可启用 `requirepass`（应用侧已有对应支持），
  `POSTGRES_SSLMODE` 控制数据库 TLS 且生产环境拒绝 `disable`。
  仍未做的是外部 KMS；Kubernetes 依据 `AGENTS.md` 待有规模依据后再上。
- 外部对象存储验证（见 [object-storage-rehearsal.md](object-storage-rehearsal.md)）尚未执行，
  需要真实 S3 凭据。100 资产 **浏览器** 压测已于 2026-09-20 跑通（100 资产 / 2 租户 /
  100 个 BROWSER 任务全部成功 / 85.2 秒 / 1.17 任务/秒 / 500 条证据），多副本演练同日跑通
  并含变异检查；备份恢复实机演练；控制台 Playwright E2E
  已纳入 Compose smoke，并已于 2026-09-15 在本机实跑通过（见
  [operations.md](operations.md) 的验收清单和 [backup-restore-rehearsal.md](backup-restore-rehearsal.md)）。
  浏览器模式**登录态**检测已于 2026-09-20 纳入同一 smoke（cookie 门控 fixture
  的正负两步断言），不再只有单元覆盖。
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
