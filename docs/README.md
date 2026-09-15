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
- 登录态检测（HTTP 路径）：客户凭据 AES-GCM 加密、登录地址 SSRF 复核、登录后抓取
  受保护页面，API 响应不泄露凭据；浏览器模式暂不支持登录（失败关闭）。
- 内存/本地证据开发模式，以及 PostgreSQL、Redis、MinIO 的 Compose 路径。
- 生产配置门禁、90 天证据/365 天审计保留策略、dry-run 清理和备份恢复校验脚本。
- OpenTelemetry Trace（env 门控、默认 no-op）和补充的单元测试（含 miniredis）。
- Repository 错误传播、真实 PostgreSQL/Redis 集成测试和 100 资产调度合成测试。

当前明确不应描述为已完成或尚未完成部署验收：

- 租户和站点的完整管理界面（当前为站点/租户 CRUD 基础形态）。
- 细粒度权限、跨租户管理员、二次确认和双人审批。
- 完整 Secret Manager 集成（当前凭据用 ALERT_SECRET_KEY 派生密钥加密）与
  浏览器模式登录态检测。
- 完整多租户运营管理。
- 真实 100 资产 HTTP/Browser 压测和备份恢复实机演练；控制台 Playwright E2E
  已纳入 Compose smoke，尚未在本机实跑。
- Docker Compose smoke 和真实数据库/Redis 集成门禁尚未在本机实跑（本机已具备
  Docker 与 Compose，正在补做验收演练）。

功能状态以代码、测试结果和部署验证记录为准。通过本地内存模式不能推出
生产可用性；发布前必须单独完成 Compose、数据库迁移、队列恢复和安全验收。

## 当前验证基线

截至 2026-09-14，以下检查已在本地通过：

- `go test ./...`
- `go vet ./...`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker typecheck`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker test`
- `corepack pnpm --filter @webmonitoring/console lint`
- `corepack pnpm --filter @webmonitoring/console --filter @webmonitoring/browser-worker build`

前端构建存在 Element Plus 产物体积提示，但不是构建失败。本机现已具备 Docker 和
Compose，Compose smoke、真实数据库/队列集成和恢复演练正在补做；此前因缺少
Docker 由 CI 或具备基础设施的验收主机执行。
