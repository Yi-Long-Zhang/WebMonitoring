# WebMonitoring 项目文档

## 文档导航

| 文档 | 用途 |
| --- | --- |
| [product-scope.md](product-scope.md) | 产品目标、用户、MVP 边界和验收指标 |
| [architecture.md](architecture.md) | 技术选型、模块边界和部署拓扑 |
| [domain-model.md](domain-model.md) | 核心对象、状态机和数据契约 |
| [security-model.md](security-model.md) | 威胁模型、权限、SSRF 和证据安全 |
| [engineering.md](engineering.md) | 分支、提交、评审、测试、发布和回滚 |
| [adr/](adr/) | 需要长期保留的架构决策记录 |

## 文档规则

- `AGENTS.md` 是最高级项目级 Agent 规则，只放不可妥协约束。
- `docs/` 说明如何执行，不得降低 `AGENTS.md` 的安全要求。
- 代码行为改变时，同一提交应更新受影响的文档。
- 重大技术、数据模型和安全边界变化必须新增或更新 ADR。
- 事实、决定、假设和待确认事项必须明确区分。

## 当前状态

当前仓库已具备 MVP 基础实现：Go 控制面和 HTTP Worker、Playwright
浏览器 Worker、Vue 控制台、PostgreSQL/Redis/MinIO Compose 编排，以及
资产授权、基线、差异、事件、证据、告警和审计流程。

仍未完成的生产验收项包括 Docker 实测、Redis pending 消息恢复、真实
PostgreSQL 集成测试、Prometheus/OpenTelemetry 接入、游标分页和容量测试。
功能状态应以代码、测试结果和部署验证记录为准，不能把本地嵌入模式视为生产部署。
