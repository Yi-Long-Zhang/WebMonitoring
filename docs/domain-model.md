# 领域模型与状态机

## 1. 核心对象

```text
Tenant
 └── Site
      └── Asset
           ├── MonitorPolicy
           ├── BaselineVersion
           ├── DetectionJob
           └── Incident
                  ├── Evidence
                  ├── AlertDelivery
                  └── AuditEvent
```

### Tenant

租户隔离边界。除平台级资源外，所有业务资源必须携带 `tenant_id`。当前代码
使用固定的默认租户上下文，租户创建、切换和平台级管理员尚未实现。

### Site

同一域名或业务系统的归属集合，是目标数据模型的一部分；当前数据库和 API
尚未提供完整的站点管理流程，资产可暂时直接归属于租户。

### Asset

一个已登记并完成授权验证后才允许检测的目标。当前实现的最低字段为：

```text
id
tenant_id
site_id
url
asset_type
verification_status
owner_id
enabled
created_at
updated_at
```

### MonitorPolicy

检测频率、超时、动态忽略、可信域名、维护窗口和告警策略。检测节点和登录态
凭据配置尚未形成可用的领域流程。

### BaselineVersion

经批准的页面状态基线。不可覆盖，只能产生新版本或废弃旧版本。

### DetectionJob

一次检测执行及其状态、输入策略版本、重试次数、开始和结束时间。当前队列为
Redis Streams，开发无 Redis 时可由 API 进程内执行。

### Signal

原子检测信号，例如状态码变化、正文差异、外部脚本新增、跳转或弹窗。
信号必须包含类型、严重性、证据引用和规则版本。

### Incident

对一个或多个相关信号的聚合。事件是运营对象，不是单次 HTTP 请求。

### Evidence

原始或派生证据的元数据。原始证据必须具备对象摘要、媒体类型、大小、生成时间、
存储位置和保留策略。

### AuditEvent

记录谁在什么时间，以什么身份，对什么资源执行了什么操作以及结果。

## 2. 授权状态

```text
PENDING
  -> VERIFIED
  -> REJECTED

VERIFIED -> SUSPENDED
SUSPENDED -> VERIFIED
```

未处于 `VERIFIED` 的资产不得进入生产检测队列。授权失效时，已有证据仍按
留存策略保留，但不能继续生成新的检测请求。

## 3. 资产状态

```text
DISABLED -> ENABLED
ENABLED  -> PAUSED
PAUSED   -> ENABLED
ENABLED  -> DELETED
```

删除是逻辑删除或进入保留期，不得直接物理删除证据。

## 4. 基线状态

```text
CANDIDATE -> APPROVED
CANDIDATE -> REJECTED
APPROVED  -> RETIRED
```

同一资产同一时刻只能有一个有效批准基线。替换基线必须保留旧版本和操作审计。

## 5. 检测任务状态

```text
QUEUED -> RUNNING
RUNNING -> SUCCEEDED
RUNNING -> FAILED
RUNNING -> TIMED_OUT
QUEUED -> CANCELLED
```

任务失败不等于网页被篡改。网络失败、Worker 失败和页面风险必须在领域层区分。

## 6. 事件状态

```text
OPEN -> ACKNOWLEDGED
OPEN -> FALSE_POSITIVE
ACKNOWLEDGED -> INVESTIGATING
INVESTIGATING -> RESOLVED
INVESTIGATING -> FALSE_POSITIVE
RESOLVED -> CLOSED
```

高风险事件关闭前必须存在处置说明和验证证据。已关闭事件不删除，只能追加更正记录。

## 7. 风险级别

| 级别 | 典型信号 |
| --- | --- |
| 提示 | 内容轻微变化、单次网络抖动 |
| 低 | 非关键文本变化、可信资源调整 |
| 中 | 未知脚本、iframe、外链或关键区域变化 |
| 高 | 恶意关键词、异常表单、明显跳转或登录页结构替换 |
| 严重 | 多个独立高风险信号同时命中，且影响关键资产 |

风险分数是排序和解释辅助，不得替代原子信号和人工研判。

## 8. 事件去重

当前事件使用以下字段构造稳定去重键：

```text
tenant_id
asset_id
baseline_version
normalized_signal_fingerprint
```

告警投递使用 `incident_id:channel_id` 作为租户内幂等键。当前事件去重未引入
时间桶，后续如需按时间窗口拆分事件必须新增规则和回归测试，不能直接改变
现有 fingerprint 语义。
