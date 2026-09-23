# 领域模型与状态机

本文用问答形式说明核心对象、状态机和数据契约。查字段定义、查状态能怎么流转、
查"为什么这么设计"都在这里。

---

## 对象之间是什么关系？

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

---

## Tenant

**Q：租户隔离靠什么？**

除平台级资源外，所有业务资源必须携带 `tenant_id`。每个租户有唯一 `slug`
（登录解析用，大小写不敏感）。调度器遍历所有租户执行检测。

**Q：租户能删吗？**

**不做硬删除**（级联风险）。平台管理员权限仅限租户 CRUD 与播种，不做任意租户
数据全量可见。

---

## Site

**Q：站点是干什么的？**

同一域名或业务系统的归属集合，是租户内资产的 1:N 组织维度。租户内 `ADMIN`
可创建/编辑站点；资产通过 `site_id` 归属站点，资产列表可按站点过滤。

**Q：站点参与检测吗？**

**不参与。** 站点不进入检测/调度路径，纯粹是组织维度。

**Q：`site_id` 可以为空吗？**

可以，保持 nullable 以兼容存量资产。复合外键 `(tenant_id, site_id)` 与
`(tenant_id, owner_id)` 在数据库层强制站点与资产、负责人同属一个租户。

**Q：站点能删除吗？**

能，但站点下**仍有资产**时拒绝（`409 SITE_NOT_EMPTY`），调用方需先把这些资产的
`site_id` 置空再重试。失败关闭而非静默解除归属：把资产悄悄挪成「无所属」是没人
要求过的数据丢失。删除前先写审计记录，理由与租户删除相同——记录必须比它描述的
那一行活得久。

---

## Asset

**Q：什么时候一个目标才允许被检测？**

完成授权验证之后。一个资产是"已登记并完成授权验证后才允许检测的目标"。
未处于 `VERIFIED` 的资产不得进入生产检测队列。

**Q：最低字段有哪些？**

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

---

## MonitorPolicy

**Q：策略里配什么？**

检测频率、超时、动态忽略、可信域名、维护窗口和告警策略，以及可选的登录态凭据
配置（`AuthConfig`：登录地址、用户名字段、密码字段、附加字段和提交选择器）。

**Q：登录凭据在策略里怎么加密？**

登录态凭据以明文入参提交，落库前用 AES-256-GCM 加密
（`internal/security/secrets.go`），关联数据为 `tenantID:assetID:auth`，
**防止密文跨租户或跨资产重放**；读取策略时由 `redactPolicy` 脱敏，响应不返回密文。
登录地址在写入和检测两个阶段各做一次 SSRF 校验。

**Q：浏览器路径的凭据怎么传？**

机制与 HTTP 路径不同：控制面解密后经内部 HTTP 把明文凭据传给 Worker，Worker 在
同一个 `BrowserContext` 内先提交登录表单再访问目标页，使 session cookie 生效；
登录地址在 Worker 侧独立再复核一次 SSRF。传输约束、失败关闭条件和取舍见
[ADR-0004](adr/0004-browser-login-credentials.md)。

**Q：检测节点（多节点调度）能用了吗？**

**还不能**，检测节点配置仍未形成可用的领域流程。

---

## BaselineVersion

**Q：基线能改吗？**

不能。经批准的页面状态基线**不可覆盖**，只能产生新版本或废弃旧版本。

---

## DetectionJob

**Q：一次检测任务记录什么？**

执行状态、输入策略版本、重试次数、开始和结束时间。当前队列为 Redis Streams，
开发无 Redis 时可由 API 进程内执行。

---

## Signal

**Q：信号必须带什么？**

信号是原子检测信号（如状态码变化、正文差异、外部脚本新增、跳转或弹窗），
必须包含**类型、严重性、证据引用和规则版本**。

---

## Incident

**Q：事件是什么？**

对一个或多个相关信号的聚合。事件是**运营对象**，不是单次 HTTP 请求。

---

## IncidentEvent（事件时间线）

**Q：为什么事件行上的 `signals` 不够用？**

事件行的 `signals` 在每次复现时被**整体覆盖**，因此它只描述**最近一轮**看到的内容；
`occurrence_count` 说明复现了几次，但第二、第三次分别看到了什么，只存在于时间线里。

**Q：时间线里有哪些条目类型？**

append-only（`incident_events`，迁移 `000013`）：

```text
CREATED          检测首次产生该事件，signals 为首轮信号集
RECURRED         同一 fingerprint 再次命中，signals 为该轮信号集
REOPENED         命中一个已 CLOSED/FALSE_POSITIVE 的事件，事件回到 OPEN，
                 状态回到 OPEN 与 occurrence_count 自增在同一条记录里完成
STATUS_CHANGED   状态流转，记录 from/to、操作者，负责人与处置说明在 details
OWNER_ASSIGNED   预留：仅负责人变更（当前 API 不产生，指派随流转一并提交）
COMMENTED        人工备注，正文在 details.note
```

**Q：谁负责写时间线？**

是 **store** 而不是调用方。只有它知道这一轮是「创建」还是「并入既有事件」，
也只有它持有合并后事件的真实 ID（每轮生成的 ID 在冲突时被丢弃）。CREATED/RECURRED
与 `occurrence_count` 的自增在同一条语句（PostgreSQL）或同一临界区（内存）内完成，
中间崩溃不会丢掉条目。

**Q：已有事件是怎么回填的？**

迁移为已有事件补一条 `CREATED` 锚点，`signals` 留空并在 `details` 标记
`backfilled` 与 `unrecordedOccurrences`。**不能**把幸存的信号集写进首次槽位——
那是伪造一个从未发生过的记录，而且事后与真实记录无法区分。

**Q：时间线会被保留期清掉吗？**

**不会。** 时间线条目随事件生命周期，不参与保留期清理。`docs/security-model.md`
把「变更、处置操作」归为不可静默修改的审计数据，而时间线记录的正是这些。
需要控制体量时应使用**写入侧上限**（每事件保留最新 N 条），不要做按时间扫描的
维护任务——那会把一个仍然开着的事件的中间历史挖掉。

**Q：时间线怎么排序？**

按 `(created_at DESC, id DESC)`。同一毫秒内的多条记录之间顺序**不保证**，
因为 `id` 是随机 UUIDv4；生产环境的复现间隔是分钟级，不构成实际问题。

---

## Evidence

**Q：原始证据必须带什么？**

对象摘要、媒体类型、大小、生成时间、存储位置和保留策略。原始证据写入后不可覆盖。

---

## AuditEvent

记录谁在什么时间，以什么身份，对什么资源执行了什么操作以及结果。

---

## 状态机

### 授权状态

```text
PENDING
  -> VERIFIED
  -> REJECTED

VERIFIED -> SUSPENDED
SUSPENDED -> VERIFIED
```

**Q：授权失效后已有证据怎么办？**

仍按留存策略保留，但**不能继续生成新的检测请求**。

### 资产状态

```text
DISABLED -> ENABLED
ENABLED  -> PAUSED
PAUSED   -> ENABLED
ENABLED  -> DELETED
```

**Q：删除资产会把证据一起删掉吗？**

不会。删除是逻辑删除或进入保留期，**不得直接物理删除证据**。

### 基线状态

```text
CANDIDATE -> APPROVED
CANDIDATE -> REJECTED
APPROVED  -> RETIRED
```

**Q：能有多个生效基线吗？**

不能。同一资产同一时刻只能有一个有效批准基线。替换基线必须保留旧版本和操作审计。

### 检测任务状态

```text
QUEUED -> RUNNING
RUNNING -> SUCCEEDED
RUNNING -> FAILED
RUNNING -> TIMED_OUT
QUEUED -> CANCELLED
```

**Q：任务失败等于网页被篡改吗？**

**不等于。** 网络失败、Worker 失败和页面风险必须在领域层区分。

### 事件状态

```text
OPEN -> ACKNOWLEDGED
OPEN -> FALSE_POSITIVE
ACKNOWLEDGED -> INVESTIGATING
INVESTIGATING -> RESOLVED
INVESTIGATING -> FALSE_POSITIVE
RESOLVED -> CLOSED
RESOLVED -> INVESTIGATING
CLOSED -> OPEN              （检测复现，非人工流转）
FALSE_POSITIVE -> OPEN      （检测复现，非人工流转）
```

前七条是人工流转（`internal/detection/diff.go` 的 `ValidateTransition`）；后两条
由检测复现触发，不接受人工调用——见下面「已关闭/误报的事件再次命中会怎样」。
注意 `RESOLVED -> OPEN` **不在**其中：只有 `CLOSED` 与 `FALSE_POSITIVE` 会被
复现重开。

**Q：关闭高风险的流程是自由的吗？**

高风险事件关闭前**必须**存在处置说明和验证证据。已关闭事件不删除，只能追加更正记录。

---

## 风险级别

| 级别 | 典型信号 |
| --- | --- |
| 提示 | 内容轻微变化、单次网络抖动 |
| 低 | 非关键文本变化、可信资源调整 |
| 中 | 未知脚本、iframe、外链或关键区域变化 |
| 高 | 恶意关键词、异常表单、明显跳转或登录页结构替换 |
| 严重 | 多个独立高风险信号同时命中，且影响关键资产 |

**Q：风险分数能直接当结论吗？**

不能。风险分数是**排序和解释辅助**，不得替代原子信号和人工研判。

---

## 事件去重

**Q：去重键是什么？**

```text
tenant_id
asset_id
baseline_version
normalized_signal_fingerprint
```

告警投递使用 `incident_id:channel_id:reason` 作为租户内幂等键。

**Q：已关闭/误报的事件再次命中会怎样？**

**重开原事件，不新建。** 去重键上的唯一索引（迁移 `000015` 起
`active_incident_fingerprint_uidx`）对每个 fingerprint **恒唯一**，所以复现一定
落在原来那一行上：状态回到 `OPEN`、`occurrence_count` 自增、时间线追加一条
`REOPENED`，事件 ID 与既有时间线全部保留。运营侧因此能看出「这是我关掉的那个
问题回来了」，而不是看到两条 fingerprint 相同却毫不相干的记录。

**只有 `CLOSED` 与 `FALSE_POSITIVE` 会重开。** `RESOLVED` 复现后状态不变，只是
`occurrence_count` 与时间线继续增长——它仍然显示为「已恢复」，但计数在涨。
`resolved_at`、`acknowledged_at` 也**不复位**：重开只改状态与计数，不抹掉上一轮
的处置时间，否则「上次什么时候恢复的」就查不到了。这是当前实现的实际边界，
不是「已解决就一定重开」的语义。

这一点是**行为修正**而非新能力：迁移 `000015` 之前 `active_incident_dedup_idx`
是**部分**唯一索引（`WHERE status NOT IN ('CLOSED','FALSE_POSITIVE')`），终态行
不参与匹配，于是「关闭后复现」会插出一条全新的重复行，且 `DO UPDATE` 从不写
`status`。修正后的重开条件沿用了同一个集合，所以 `RESOLVED` 的行为与迁移前
一致——变的只是 `CLOSED`/`FALSE_POSITIVE` 两行。

告警理由同时新增 `REOPENED`：投递幂等键含理由，而重开的事件 ID 没变，沿用
`RECURRED` 会被既有键静默挡掉，运营收不到重开通知。

**Q：能按时间窗口拆分事件吗？**

当前事件去重**未引入时间桶**。后续如需按时间窗口拆分事件，必须新增规则和回归
测试，**不能直接改变现有 fingerprint 语义**。
