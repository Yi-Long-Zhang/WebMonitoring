# 技术架构与选型

本文用问答形式说明技术选型、模块边界和部署拓扑。**为什么这么选**的问题，
能答的都答了；只记录结论、不记录取舍的地方会明确标注。

---

## 总体架构

**Q：整体是什么结构？**

「模块化单体控制面 + 独立检测 Worker」。通过队列解耦任务，不拆成大量微服务。

```text
Vue Console
     |
Go Control Plane
  |  Asset / Policy / Baseline / Incident / Alert / Audit
  |
Redis Streams
  |                         |
Go HTTP Worker       Playwright Browser Worker
  |                         |
  +------ Signal & Evidence Result ------+
                    |
          PostgreSQL + S3/MinIO
```

**Q：为什么不直接上微服务？**

- 业务模型已收敛（事件去重与时间线语义稳定），但运维面仍在按场景补齐。
- 控制面需要事务一致性和快速迭代。
- HTTP 检测和浏览器检测有不同资源模型，适合独立扩缩容——**这两个**才需要拆。
- 队列解耦已足够，再拆只会增加跨服务事务和排障成本。

**Q：为什么控制面不直接执行不可信网页脚本？**

这是不可妥协的隔离边界：控制面不执行客户页面的 JS。HTTP Worker 和 Browser Worker
隔离运行，各有超时、大小和网络边界。浏览器 Worker 必须容器隔离、限制出口网络、
限制 CPU/内存/时间和响应体大小。

---

## 前端

**Q：前端选型是什么？**

- Vue 3 + TypeScript + Vite，Composition API 和 `<script setup>`。
- Element Plus：适合密集型安全运营后台。
- Pinia：只保存跨页面客户端状态，不把所有服务端数据塞入全局 Store。
- TanStack Query for Vue：请求缓存、重试、失效和加载状态。
- ECharts：总览页的三张图（事件趋势折线、风险分布饼图、检测成功率仪表盘），
  封装在 `components/monitoring/EChart.vue`，配置与数据变换在
  `features/monitoring/charts.ts`。

**Q：总览页有图表吗？**

有，三张。趋势图按天统计（未指定区间时为最近 30 天），风险分布按严重度聚合，
检测成功率是仪表盘。数据来自 `GET /api/v1/reports/summary` 和仪表盘接口，
没有事件时趋势图与饼图显示空态而不是空坐标轴。

**Q：差异对比用 Monaco Diff Editor 吗？**

**没有。** 差异详情当前使用结构化信号和证据下载，Monaco Diff Editor 尚未接入。

**Q：怎么组织前端代码？**

```text
src/
├── app/          # 应用初始化、路由、权限和全局配置
├── pages/        # 页面级组合
├── features/     # 资产、基线、事件、报告等业务功能
├── components/   # 无业务或低业务复用组件
├── services/     # API client、SSE、下载和通知
├── stores/       # 少量客户端状态
├── types/        # API 生成类型和领域类型
└── styles/
```

**Q：前端代码有什么硬约束？**

OpenAPI 类型生成到 `apps/console/src/types/generated/`，HTTP 调用集中在
`services/api/`，服务端状态集中在 Pinia Store，页面组件通过 `features/`
暴露的动作完成写操作。**不要**在组件中直接拼接后端 URL、重复定义 API 类型，
或实现绕过服务端的权限判断。

**Q：有什么测试？**

有 API client 和 Worker 单元测试，以及控制台 Playwright E2E
（`console-smoke`，已纳入 Compose smoke）。

**Q：展示采集到的 HTML 安全吗？**

采集 HTML 默认以转义文本展示；禁止无审查使用 `v-html`；必须预览时使用独立 origin
和严格 `sandbox` iframe。证据下载由已认证 API 直接返回并校验对象大小和 SHA-256，
另支持短期签名 URL。

**Q：菜单里隐藏了入口，就等于没权限了吗？**

**不等于。** 菜单隐藏不是权限控制，后端必须鉴权。前端权限只到角色级，
单一来源见 `apps/console/src/app/permissions.ts`。

---

## Go 控制面

**Q：模块怎么划分？**

按**技术职责**分包，不按领域对象分包。`internal/` 下实际存在的包：

```text
internal/
├── api/            # HTTP 处理器、路由、中间件、权限校验（最大，25 个文件）
├── store/          # 仓储接口与 PostgreSQL / 内存实现（15 个文件）
├── detection/      # 检测编排、HTTP/浏览器检查、差异信号、事件聚合（14 个文件）
├── security/       # 加密、密钥环、签名 URL、脱敏（12 个文件）
├── config/         # 配置解析与生产门禁
├── auth/           # 认证、会话、登录限流
├── browser/        # 浏览器 Worker 客户端与登录态凭据传递
├── evidence/       # 证据元数据与对象存储访问
├── notify/         # 告警渠道与投递
├── notifygate/     # 出站告警的滑动窗口配额与熔断
├── ratelimit/      # 按租户的读/写/重操作配额（600/120/12 每分钟）
├── jobqueue/       # Redis Streams 队列
├── maintenance/    # 保留期清理
├── metrics/        # 指标注册表与路径归并
├── telemetry/      # OpenTelemetry（默认 no-op）
├── rewrap/         # 存量密钥密文再加密
├── events/         # SSE 广播
├── identity/       # 标识生成
├── migrate/        # 迁移执行器
└── domain/         # 领域类型与枚举（仅 models.go）
```

**Q：资产、策略、基线、事件这些领域逻辑在哪个包？**

**没有各自独立的包。** 领域逻辑直接挂在 `api.Server` 的方法上，用动词命名
（`createAsset`、`approveBaseline`、`transitionIncident`、`cancelJob`…），
持久化落在 `store/`。设计文档早期画过一张按领域对象分包的图
（`asset/`、`policy/`、`baseline/`、`analysis/`、`incident/`、`alert/`、`audit/`、
`platform/`），**那 8 个目录从未存在**，不要按它去找代码。

**Q：差异信号和规则评分在哪个包？**

都在 `detection/`：`diff.go` 定义 16 种信号类型和 `IncidentFingerprint`，
`checker.go` 执行 HTTP 检测并构造 Snapshot，`html.go` 做静态解析。
**没有独立的 `analysis/` 包，也没有独立的"风险评分"计算**——事件的风险级别来自
信号严重性的聚合，不是单独的打分函数。

**Q：依赖方向有什么要求？**

用标准库配合轻量路由和数据库驱动；引入框架必须说明收益和退出成本。业务层
**不应**直接依赖 HTTP 请求对象、具体消息队列或对象存储 SDK。

---

## 检测 Worker

**Q：HTTP Worker 和 Browser Worker 分别负责什么？**

**HTTP Worker**（高频、低成本）：

- DNS 和 TLS 基础信息。
- HTTP 状态、响应头、响应大小和响应时间。
- 重定向链与最终地址。
- HTML、脚本、链接、表单和 iframe 的静态解析。
- 内容规范化、摘要和差异信号。

**Browser Worker**（需要执行 JS、截图或行为分析）：

- Playwright 页面加载和截图。
- 最终导航和跨域导航记录。
- 新窗口、弹窗、下载和表单行为观察。
- 资源请求清单和脚本变化。

**Q：两种模式的快照字段一样吗？**

不一样。HTTP 无渲染，快照中的 `popups`、`dialogs`、`downloads`、`screenshotHash`
明确留空（`resourceRequests` 由静态资源清单近似，仅脚本/样式表/图片/iframe 引用）；
这些字段只有浏览器 Worker 才产出，**跨模式对比时应忽略缺失侧的空值**。

**Q：登录态检测的凭据怎么保证不泄露？**

登录态流程只使用专门的密钥管理和客户授权。控制面解密凭据后经内部 HTTP 传给
Browser Worker（[ADR-0004](adr/0004-browser-login-credentials.md)）：凭据只出现在
请求体，不写日志、不回显；登录页的请求同样走上下文级 SSRF 拦截，一旦被拦截即
**失败关闭**，避免把渲染失败的登录页当成受保护页面存成快照。生产门禁要求该端点
使用 https 或私网地址。

---

## 数据和基础设施

| 组件 | 用途 |
| --- | --- |
| PostgreSQL | 租户、资产、策略、基线、任务、信号、事件、审计 |
| Redis Streams | 检测任务、结果消费、重试和削峰 |
| S3/MinIO | HTML、截图、HAR、差异文件等大对象 |
| Prometheus | 指标 |
| OpenTelemetry | Trace 和跨 Worker 关联 |
| 结构化日志 | 运行排障，不保存敏感原文 |

**Q：元数据和证据分别放哪？**

数据库保存证据元数据和摘要；对象存储保存原始文件。证据对象命名不得包含客户
敏感信息。访问经过控制面认证、租户校验和完整性校验，并支持短期签名访问凭证。

---

### 可观测性

**Q：指标怎么暴露？**

指标由自研 registry 提供，控制面和 Worker 各自暴露 Prometheus 抓取端点 `/metrics`。
请求级指标带 `method`/`status`/`path` 标签，路径按集合归并
（`/api/v1/assets` 而非 `/api/v1/assets/{id}`），**签名令牌不进入标签**。

**Q：本仓库带采集器吗？**

**不带。** `/metrics` 是文档化的运维接口，scrape 配置、告警规则和时序存储由部署方
提供。边缘 nginx 对外屏蔽 `/metrics`，正是为了让内部采集器成为唯一消费者。

**Q：Trace 默认开启吗？**

**默认关闭**（`internal/telemetry`）。只有设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 才创建
导出器；未设置时全局 provider 保持 SDK no-op，所有 span 调用退化为几个纳秒。
端点、头部、TLS 和压缩由 OTLP/HTTP 导出器读取标准 `OTEL_*` 环境变量，代码内不
硬编码采集地址；采样率由 `OTEL_TRACES_SAMPLER_ARG` 控制，缺省全采。初始化失败
只记录告警，不阻止进程启动。

**Q：哪些地方埋了点？**

- HTTP 中间件 server span，覆盖整个请求（方法 + 路径）。
- `detection.process`：任务消费，带 `tenant_id`/`job_id`/`asset_id`/`mode`。
- `checker.http`：HTTP 检测出站请求。
- `notify.send`：单个告警渠道投递。

`X-Request-ID` 与导出的 trace ID 一致（未启用追踪时为本地生成），审计记录沿用同一
ID，因此日志、审计和 trace 可以互相定位。

**Q：trace 里会出现客户敏感数据吗？**

不会。span 属性只记录租户、任务、资产、渠道类型这类标识。客户 URL、DingTalk 签名、
Webhook 凭据、邮件收件人和页面内容都不写入 span——trace 会导出到外部采集器。
`GET /api/v1/evidence/signed/{token}` 的 token 属于凭据，因此该路由的 span 名
使用路由占位符而不是真实路径。

**Q：日志、审计和 trace 能串起来吗？**

能，通过同一个请求 ID。但有一个**已知缺口**：队列消息体只包含任务 JSON，不携带
W3C `traceparent`，Worker 的 span 与产生该任务的 API 请求因此不是同一个 trace，
只能通过 `job_id`/`asset_id` 属性关联。

---

## API 约定

- API 契约使用 OpenAPI，前端客户端和类型由契约生成。
- 时间使用 UTC 存储，展示层转换为用户时区。
- 列表接口必须支持分页、排序和明确的最大页大小。
- 写操作返回稳定资源 ID 和状态，不返回内部存储路径。
- 异步操作返回任务 ID，使用查询或 SSE 获取进度。
- 错误返回稳定错误码、可读消息和追踪 ID。
- 事件状态流转由后端校验，**前端不能自行改变状态**。

---

## 实时和异步

**Q：什么时候用 SSE，什么时候用 WebSocket？**

管理操作使用 REST。告警和任务状态推送**优先使用 SSE**；只有需要双向实时交互时
才考虑 WebSocket。

**Q：队列消费者必须满足什么？**

- 具备幂等键。
- 区分可重试和不可重试错误。
- 使用指数退避和最大重试次数。
- 将最终失败送入死信或失败记录。
- 记录任务、租户、资产和追踪 ID。

---

## 多副本（HA）支持状态

**Q：能横向扩容吗？**

**能，且已实测。** 但横向扩容需要逐项满足条件，否则扩容会**在不报错的情况下**
重复执行检测或重复删除证据。三处阻塞点都已解决：

| 位置 | 机制 | 状态 |
| --- | --- | --- |
| `internal/detection/scheduler.go` | 每个副本各自跑 30 秒 tick，`due()` 先读任务表再决定是否入队 | 已解决：迁移 `000012` 的部分唯一索引 `detection_jobs_active_uidx` 限制每个 `(tenant_id, asset_id, mode)` 最多一个 `QUEUED`/`RUNNING` 任务，竞争失败方由 `store.ErrJobAlreadyActive` 静默吞掉 |
| `apps/http-worker` 的消费者身份 | `WORKER_ID` 曾被 Compose 固定为 `http-worker-1` | 已解决：`config.WorkerIdentity` 在未显式配置时按 hostname + 随机后缀生成每进程身份，生产门禁拒绝两个历史固定值 |
| `internal/maintenance/retention.go` | 无锁，且 `RunDaily` 启动时立即执行一次全量清理 | 已解决：`store.ReplicaLease` 提供 PostgreSQL 会话级 advisory lock，未取得租约的副本直接跳过本轮；定时清理会为实际发生的删除补写审计事件 |

**Q：怎么验证扩容是安全的？**

`scripts/multi-replica-drill.ps1` 是这三项的验收脚本：它不通过 API 创建任务，而是让
各副本调度器自己排队，再断言任务数等于资产数、没有重复的 `(tenant, asset, mode)`、
每个 worker 副本对应一个唯一消费者身份。

2026-09-20 在真实 `--scale api=2 --scale http-worker=3` 上首次跑通
（验收总账：[rehearsals.md](rehearsals.md)）：

```
assets            : 40
detection jobs    : 40 (expected 40)
duplicate groups  : 0
succeeded         : 40
wall clock        : 12.2s
worker consumers  : 3 个互不相同的名字
```

**Q：这些断言会不会是空转的？**

不是。同一次运行里做了两个变异检查，都如期失败：`-DropActiveJobIndex`（先删掉迁移
000012 的唯一索引）使任务数变 66、重复组 26，脚本以退出码 1 报错；更早的 `WORKER_ID`
变异（把消费者名钉回固定值）使三个副本注册成同一名字。

**Q：任务租约和超时是怎么保证的？**

`ReapTimedOut` 会把所有 `started_at` 早于租约的 `RUNNING` 任务单方面置为 `TIMED_OUT`，
`ClaimJob` 也允许抢占超过租约的任务，**两者都不校验持有者** —— 所以保证不是
「reaper 知道谁持有任务」，而是「**没有合法检测能超过租约**」。租约由
`JOB_LEASE_SECONDS` 配置（`store.JobLease()` 暴露给调度器，避免 reap 截止点与
reclaim 截止点漂移），并且生产门禁要求它**严格大于** `3 × 90s = 270s`：
一次检测受策略超时约束（上限 90 秒），而一次 BROWSER 检测会把这份预算花两次
（登录页加载 + 目标页导航）。

**Q：以后策略超时无上限了怎么办？**

当前的不等式就不再成立。正确做法是改成 worker 周期性写 heartbeat、reaper 按
`last_heartbeat_at` 回收 —— 那是构造上正确，但需要迁移、仓储方法和 worker 管线。
