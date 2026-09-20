# 技术架构与选型

## 1. 总体决策

当前采用“模块化单体控制面 + 独立检测 Worker”的架构。

原因：

- 当前处于 MVP，业务模型和规则仍在快速演进。
- 控制面需要事务一致性和快速迭代。
- HTTP 检测和浏览器检测有不同资源模型，适合独立扩缩容。
- 通过队列解耦任务，不需要一开始拆成大量微服务。

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

## 2. 前端

### 选型

- Vue 3 + TypeScript + Vite。
- Composition API 和 `<script setup>`。
- Element Plus：适合密集型安全运营后台。
- Pinia：只保存跨页面客户端状态，不把所有服务端数据塞入全局 Store。
- TanStack Query for Vue：请求缓存、重试、失效和加载状态。
- ECharts 已作为依赖预留给总览统计；当前页面以摘要和列表为主。
- 差异详情当前使用结构化信号和证据下载，Monaco Diff Editor 尚未接入。
- 前端当前有 API client 和 Worker 测试，并有控制台 Playwright E2E（`console-smoke`，纳入 Compose smoke）。

### 前端分层

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

当前实现已按上述边界组织。OpenAPI 类型生成到
`apps/console/src/types/generated/`，HTTP 调用集中在
`services/api/`，服务端状态集中在 Pinia Store，页面组件通过
`features/` 暴露的动作完成写操作。不要在组件中直接拼接后端 URL、
重复定义 API 类型或实现绕过服务端的权限判断。

### 前端安全

- 采集 HTML 默认以转义文本展示。
- 禁止无审查使用 `v-html`。
- 必须预览时使用独立 origin 和严格 `sandbox` iframe。
- 当前证据下载由已认证 API 直接返回，并校验对象大小和 SHA-256；
  另支持短期签名 URL。
- 菜单隐藏不等于权限控制，后端必须鉴权。

## 3. Go 控制面

目标模块边界：

```text
internal/
├── auth/           # 认证、授权、租户上下文
├── asset/          # 资产与授权验证
├── policy/         # 检测策略、白名单、维护窗口
├── baseline/       # 基线版本和批准
├── detection/      # 任务编排和结果接收
├── analysis/       # 信号、规则和风险评分
├── incident/       # 事件状态、指派和时间线
├── alert/          # 去重、渠道和重试
├── evidence/       # 对象存储、摘要和下载
├── audit/          # 审计事件
└── platform/       # 数据库、队列、配置和观测
```

推荐使用标准库配合轻量路由和数据库驱动；引入框架必须说明收益和退出成本。
业务层不应直接依赖 HTTP 请求对象、具体消息队列或对象存储 SDK。

## 4. 检测 Worker

### HTTP Worker

适合高频、低成本检测：

- DNS 和 TLS 基础信息。
- HTTP 状态、响应头、响应大小和响应时间。
- 重定向链与最终地址。
- HTML、脚本、链接、表单和 iframe 的静态解析。
- 内容规范化、摘要和差异信号。

HTTP 无渲染，快照中的 `popups`、`dialogs`、`downloads`、`screenshotHash` 明确留空
（`resourceRequests` 由静态资源清单近似，仅脚本/样式表/图片/iframe 引用）；这些
字段只有浏览器 Worker 才产出，跨模式对比时应忽略缺失侧的空值。

### Browser Worker

只在页面需要执行 JavaScript、截图或行为分析时使用：

- Playwright 页面加载和截图。
- 最终导航和跨域导航记录。
- 新窗口、弹窗、下载和表单行为观察。
- 资源请求清单和脚本变化。
- 登录态流程只使用专门的密钥管理和客户授权。

浏览器 Worker 必须容器隔离、限制出口网络、限制 CPU/内存/时间和响应体大小。

## 5. 数据和基础设施

| 组件 | 用途 |
| --- | --- |
| PostgreSQL | 租户、资产、策略、基线、任务、信号、事件、审计 |
| Redis Streams | 检测任务、结果消费、重试和削峰 |
| S3/MinIO | HTML、截图、HAR、差异文件等大对象 |
| Prometheus | 指标 |
| OpenTelemetry | Trace 和跨 Worker 关联 |
| 结构化日志 | 运行排障，不保存敏感原文 |

数据库保存证据元数据和摘要；对象存储保存原始文件。证据对象命名不得包含
客户敏感信息。当前访问必须经过控制面认证、租户校验和完整性校验；生产阶段
仍需补充短期签名或等价的短时访问凭证。

### 可观测性

指标由自研 registry 提供，控制面和 Worker 各自暴露 Prometheus 抓取端点
`/metrics`；这是文档化的运维接口，不随 OpenTelemetry 的引入而改变。

Trace 是**附加且默认关闭**的（`internal/telemetry`）。只有设置
`OTEL_EXPORTER_OTLP_ENDPOINT` 才创建导出器；未设置时全局 provider 保持 SDK
no-op，所有 span 调用退化为几个纳秒，行为与引入前完全一致。端点、头部、TLS
和压缩由 OTLP/HTTP 导出器自行读取标准 `OTEL_*` 环境变量，代码内不硬编码采集
地址；采样率由 `OTEL_TRACES_SAMPLER_ARG` 控制，缺省全采。初始化失败只记录
告警，不阻止进程启动。

已埋点位置：

- HTTP 中间件 server span，覆盖整个请求（方法 + 路径）。
- `detection.process`：任务消费，带 `tenant_id`/`job_id`/`asset_id`/`mode`。
- `checker.http`：HTTP 检测出站请求。
- `notify.send`：单个告警渠道投递。

`X-Request-ID` 与导出的 trace ID 一致（未启用追踪时为本地生成），审计记录沿用
同一 ID，因此日志、审计和 trace 可以互相定位。

span 属性只记录租户、任务、资产、渠道类型这类标识。客户 URL、DingTalk 签名、
Webhook 凭据、邮件收件人和页面内容都不写入 span——trace 会导出到外部采集器。
`GET /api/v1/evidence/signed/{token}` 的 token 属于凭据，因此该路由的 span 名
使用路由占位符而不是真实路径。

**已知缺口**：队列消息体只包含任务 JSON，不携带 W3C `traceparent`。Worker 的
span 与产生该任务的 API 请求因此不是同一个 trace，只能通过 `job_id`/`asset_id`
属性关联。跨进程 trace 关联需要把 trace 上下文放进队列信封，属于后续工作。

## 6. API 约定

- API 契约使用 OpenAPI，前端客户端和类型由契约生成。
- 时间使用 UTC 存储，展示层转换为用户时区。
- 列表接口必须支持分页、排序和明确的最大页大小。
- 写操作返回稳定资源 ID 和状态，不返回内部存储路径。
- 异步操作返回任务 ID，使用查询或 SSE 获取进度。
- 错误返回稳定错误码、可读消息和追踪 ID。
- 事件状态流转由后端校验，前端不能自行改变状态。

## 7. 实时和异步

管理操作使用 REST。告警和任务状态推送优先使用 SSE；只有需要双向实时交互
时才考虑 WebSocket。

所有队列消费者必须：

- 具备幂等键。
- 区分可重试和不可重试错误。
- 使用指数退避和最大重试次数。
- 将最终失败送入死信或失败记录。
- 记录任务、租户、资产和追踪 ID。

## 8. 多副本支持状态

横向扩容必须逐项满足以下条件。任一未满足时，扩容都会在不报错的情况下重复执行检测
或重复删除证据：

| 位置 | 机制 | 状态 |
| --- | --- | --- |
| `internal/maintenance/retention.go` | 无锁，且 `RunDaily` 启动时立即执行一次全量清理 | **未解决**：每个副本重启都触发完整清扫，重复删除证据对象与元数据、重复计数 |
| `internal/detection/scheduler.go` | 每个副本各自跑 30 秒 tick，`due()` 先读任务表再决定是否入队 | 已解决：迁移 `000012` 的部分唯一索引 `detection_jobs_active_uidx` 限制每个 `(tenant_id, asset_id, mode)` 最多一个 `QUEUED`/`RUNNING` 任务，竞争失败方由 `store.ErrJobAlreadyActive` 静默吞掉 |
| `apps/http-worker` 的消费者身份 | `WORKER_ID` 曾被 Compose 固定为 `http-worker-1` | 已解决：`config.WorkerIdentity` 在未显式配置时按 hostname + 随机后缀生成每进程身份，生产门禁拒绝两个历史固定值 |

调度器和 Worker 的幂等已经具备，**但在保留策略加上跨副本租约之前，仍然只支持单个控制面
副本**。对比之下 `internal/migrate/runner.go` 使用 `pg_advisory_lock` 串行化迁移，保留策略
可以直接沿用该模式。

扩容前还必须补充一项验证：多副本下 `ReapTimedOut` 会把其他副本仍在执行的 `RUNNING` 任务
单方面置为 `TIMED_OUT`（它只按 `started_at` 与租约判断，不校验持有者），需要确认任务租约
长于最慢的浏览器检测时长，或改为带持有者校验的回收。
