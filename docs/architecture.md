# 技术架构与选型

## 1. 总体决策

采用“模块化单体控制面 + 独立检测 Worker”的架构。

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
- ECharts：总览统计和趋势。
- Monaco Diff Editor：源码、HTML 和文本差异。
- Playwright：前端 E2E。

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
- 证据下载使用短期签名 URL。
- 菜单隐藏不等于权限控制，后端必须鉴权。

## 3. Go 控制面

建议模块：

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
客户敏感信息，访问必须经过授权检查和短期签名。

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
