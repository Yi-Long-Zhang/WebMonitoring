# 运行手册：告警渠道投递失败

**Q：我看到的什么现象指向这本手册？**

控制台渠道列表出现「投递熔断」标签；`GET /api/v1/alert-deliveries` 里出现
`status=FAILED` 的行（`last_error` 有值）；或 `/metrics` 里
`webmonitoring_alert_suppressed_total` 开始增长。

**Q：为什么这本手册开头就强调 `/readyz` 查不出问题？**

因为告警投递失败时 API 和 worker 都仍然就绪。一开始就往 `/readyz` 上找，
会得出"一切正常"的错误结论。

## 关键前提：`/readyz` 查不出这个问题

**告警投递失败时 API 和 worker 都仍然就绪。** `/readyz` 不覆盖出站通知链路，
告警问题只能从三个地方看出来：交付表、指标、控制台的渠道列表。一开始就往
`/readyz` 上找，会得出「一切正常」的错误结论。

## 判断

### 第 1 步：先做一次测试投递（不要先看熔断状态）

`POST /api/v1/alert-channels/{id}/test` 发送一条测试消息，**它故意不经过熔断器、
也不写交付记录**（`internal/notify/dispatcher.go` 的 `SendTest`，注释里说明
这是刻意设计）：

- 不查询 gate：熔断打开恰恰是运维最想测这个渠道的时候——「它恢复了没有」
  就是这个问题本身。会查熔断的测试等于答非所问。
- 不写交付记录：`alert_deliveries` 是「平台真的告了什么」的历史，一条手工测试
  会变成一行没有事件的幽灵记录，而且重试 worker 会把它当成待办。
- 不套重试阶梯：测试要现在告诉运维第一次尝试的结果。
- **但不跳过 SSRF 校验**：`sendForChannel` 照常校验目标，误填或恶意 URL
  在这里同样不能出网。

```bash
curl -s -X POST "http://localhost:8080/api/v1/alert-channels/<channelId>/test" \
  -H "X-CSRF-Token: $CSRF" -b "$COOKIE_JAR"
```

| 返回 | 含义 |
| --- | --- |
| `200 {"status":"DELIVERED"}` | 渠道本身是通的。问题在熔断/限流状态或积压，看第 2 步 |
| `502 CHANNEL_TEST_FAILED`（body 里是脱敏后的原因） | 渠道配置或目标侧问题，看第 3 步 |
| `404` | 渠道 id 不存在（或不属于当前租户） |
| 提示没有 inline notifier | 见下方「关于 alert worker 模式」 |

该接口需要 `ADMIN` 角色（`server.go` 的路由表），并且会写一条
`alert_channel.test` 审计，`result` 为 `SUCCESS` / `FAILURE`。

> **关于 alert worker 模式**：测试走的是本进程内联的 dispatcher
> （`internal/detection/service.go` 的 `TestChannel` → `notifier.SendTest`）。
> 正常投递则由 http-worker 上的 alert worker 完成，它在自己的进程里装了
> 一个 gate 并通过 Redis 与 API 共享。所以**测试通过 = 渠道通**，但不等于
> 「队列一定在消费」——队列侧的问题在 [redis.md](redis.md)（看
> `webmonitoring:alert-jobs`）。

### 第 2 步：读熔断/限流状态

两者的差别，决定了完全不同的处置：**熔断是「渠道坏了」，限流是「渠道被
用得太多」**（`internal/notifygate/gate.go` 的包注释）。

| 指标标签 | 含义 | 该做什么 |
| --- | --- | --- |
| `webmonitoring_alert_suppressed_total{reason="CIRCUIT_OPEN"}` | 连续失败达到阈值，渠道被暂停投递 | 修渠道（第 3 步），等冷却后自动探测恢复 |
| `webmonitoring_alert_suppressed_total{reason="LIMITED"}` | 单窗口内的投递量超过配额 | 不是故障。见下方「处置 B」 |

```bash
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep webmonitoring_alert_suppressed_total
```

控制台侧的呈现（`apps/console/src/pages/ChannelsPage.vue`）：渠道「状态」列
按优先级显示「停用」→「投递熔断」→「启用」。该标签来自
`GET /api/v1/alert-channels` 响应里的 `suspended` 字段，由
`alertChannelView`（`internal/api/server.go`）派生——它读的是 gate 的实时状态，
**不是**渠道表里的一个字段（`internal/detection/service.go` 的
`SuspendedChannels` 逐 id 调 `gate.Open`，只读、不消耗配额、不探测）。

熔断参数（`internal/notifygate/gate.go`，默认值）：

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `ALERT_FAILURE_THRESHOLD` | 5 | 连续失败多少次后开路。≤0 关闭熔断 |
| `ALERT_CIRCUIT_COOLDOWN_SECONDS` | 300 | 开路后多久放一个探测请求 |
| `ALERT_MAX_PER_CHANNEL_PER_WINDOW` | 60 | 单渠道单窗口的投递配额。≤0 关闭限流 |
| `ALERT_GATE_WINDOW_SECONDS` | 60 | 上面那个窗口的长度 |

行为细节（决定了「为什么没人收到告警」的答案）：

- 一个渠道连续失败 `ALERT_FAILURE_THRESHOLD` 次后开路；开路期间投递被**跳过**
  而不是重试。冷却期满后**放行恰好一个探测请求**，由它的结果决定关闭还是
  重新开路。
- **成功即关闭熔断**并清零失败计数（`RecordOutcome`）。
- 失败计数（Redis key `notifygate:fail:<channelId>`）**24 小时**不失效，远长于
  冷却期：一个渠道哪怕 90 分钟才失败一次，也是「坏了」，不该因为间隔长而
  重置计数。它靠**下一次成功**清零，而不是靠时钟。
- 开路记录（key `notifygate:open:<channelId>`）也保留 24 小时，因为冷却期
  结束后到来的探测请求必须能读到「什么时候开的」。
- **两个控制都 fail open**：读不到状态（Redis 不可达）时放行通知。告警路径
  的意义就是在出事时能发出去，Redis 故障不是停止通知篡改的理由。

> Redis 里的实际状态可以直接看（key 前缀就是这一组，便于核对阈值）：
> ```bash
> docker compose -f deployments/compose/docker-compose.yml exec -T redis \
>   redis-cli --scan --pattern 'notifygate:*'
> docker compose -f deployments/compose/docker-compose.yml exec -T redis \
>   redis-cli GET notifygate:open:<channelId>
> docker compose -f deployments/compose/docker-compose.yml exec -T redis \
>   redis-cli GET notifygate:fail:<channelId>
> ```
> key 里的 channelId 被 `NormalizeChannelID` 统一成小写去空格后的形式。

### 第 3 步：读交付表

投递记录在 PostgreSQL 的 `alert_deliveries` 表（`db/migrations/000001_initial.up.sql`）：

| 列 | 含义 |
| --- | --- |
| `status` | `PENDING` / `DELIVERED` / `FAILED` |
| `attempts` | 已尝试次数 |
| `last_error` | 脱敏、截断到 240 字符的失败原因 |
| `next_attempt_at` | 下一次计划尝试时间（终态时为 NULL） |
| `dedup_key` | 去重键 = `incidentId:channelId:reason` |
| `incident_id` / `channel_id` | 关联的事件与渠道 |

```bash
# 通过接口读（推荐：自动按租户隔离、分页、脱敏）
curl -s "http://localhost:8080/api/v1/alert-deliveries?limit=50" -b "$COOKIE_JAR"
# 按事件过滤
curl -s "http://localhost:8080/api/v1/alert-deliveries?incidentId=<id>" -b "$COOKIE_JAR"
```

```bash
# 直接查库（跨租户统计，排查用；注意这是绕过租户隔离的运维视图）
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT status, count(*) FROM alert_deliveries GROUP BY 1;"
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT channel_id, status, attempts, left(coalesce(last_error,''),80) AS err,
          max(updated_at) AS last_try
     FROM alert_deliveries
    WHERE status <> 'DELIVERED'
    GROUP BY 1,2,3,4 ORDER BY last_try DESC LIMIT 20;"
```

控制台在事件详情里展示这些字段（`apps/console/src/pages/IncidentsPage.vue`：
`status`、`attempts 次尝试`、`lastError`）。

**投递失败/被丢弃的两种终局：**

- 重试阶梯耗尽：投递内部按 `2^attempt × ALERT_RETRY_BASE_MILLISECONDS` 退避，
  最多 `ALERT_MAX_ATTEMPTS`（默认 3）次，之后 `status=FAILED`、`last_error`
  写原因、`next_attempt_at` 清空，并把**一次**失败反馈给熔断器
  （`internal/notify/dispatcher.go`）。注意是每条投递失败记一次，不是每次
  尝试记一次——熔断器数的是「坏掉的渠道」，一条需要试三次的消息仍然是一个
  渠道的一次失败。
- 队列层消息耗尽：`ALERT_QUEUE_MAX_DELIVERIES`（默认 5）用尽后进死信，
  http-worker 调用 `MarkDeadLetter` 把该事件所有非 `DELIVERED` 的投递记录改成
  `FAILED`、`last_error = "alert message exceeded the maximum delivery count"`，
  并写审计 `alert.dead_letter`（`apps/http-worker/main.go`）。**这类消息不会
  自动补发**。

```bash
# 死信与对应的审计
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:alert-jobs:dead-letter
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT action, resource_id, result, created_at FROM audit_events
    WHERE action LIKE 'alert.%' ORDER BY created_at DESC LIMIT 20;"
```

### 第 4 步：按渠道类型定位

渠道类型与必填字段（`apps/console/src/app/channels.ts`，也和服务端校验一致）：

| 类型 | 需要什么 | 常见失败原因 |
| --- | --- | --- |
| `WEBHOOK` | URL + 签名 secret | 目标退役、签名不匹配、非 2xx |
| `WECHAT_WORK` | URL | webhook key 被重置 |
| `DINGTALK` | URL + 签名 secret | 机器人 secret 被轮换（gate 包注释举的正是这个例子） |
| `EMAIL` | 收件人列表 | `SMTP_HOST` 未配置或被拒；发件人被 relay 拒绝 |

EMAIL 渠道依赖 SMTP 配置：`SMTP_HOST` / `SMTP_PORT`（默认 25）/
`SMTP_USERNAME` / `SMTP_PASSWORD` / `SMTP_FROM`。**`SMTP_HOST` 留空即禁用
邮件渠道**，此时 EMAIL 渠道必然失败：

```bash
docker compose -f deployments/compose/docker-compose.yml config | grep -E '^ +SMTP_' | sed 's/PASSWORD.*/PASSWORD=[REDACTED]/'
```

## 处置

### 处置 A：渠道配置问题（测试投递就失败）

按第 1 步返回的错误定位。**改渠道配置是需要权限的动作**：创建/修改/删除
渠道都需要 `ADMIN` 角色，且渠道 URL 在控制台只展示脱敏结果、secret 永不回传。

**【改变状态，需授权】** 更新渠道后立刻用 `POST .../test` 复验。如果轮换了
钉钉/企业微信的 secret 或 webhook URL，**同时**要确认熔断状态：渠道恢复后
需要一次成功投递才会关闭熔断，而冷却期内只会放行一个探测请求。

### 处置 B：限流（`reason="LIMITED"`）

**这不是故障。** 一次检测跑出上千个变更页面就会产生上千个事件，一个渠道全部
收到就是没人会读的渠道。熔断包注释把这称为「失控保护，不是预算」。

可选动作（都需要判断，不要默认全做）：

- **【改变状态】** 提高 `ALERT_MAX_PER_CHANNEL_PER_WINDOW` /
  `ALERT_GATE_WINDOW_SECONDS`。这是**降低告警抑制**，代价是渠道可能被刷爆、
  被对方限流甚至封禁。
- 更好的方向是降低**事件噪声**（聚合、忽略选择器、维护窗口），让渠道收到的
  都是能处置的。这属于策略调整，需要和运维一起决定。

### 处置 C：熔断打开（`reason="CIRCUIT_OPEN"`）

1. 先按第 3 步找出 `last_error`，修根因（渠道侧或网络侧）。**不要先清 Redis
   状态**：用 `DEL notifygate:open:<id>` / `DEL notifygate:fail:<id>` 强行清除
   只会让平台立刻再去打同一个坏渠道，并把「渠道已停止工作」这个事实从证据里
   抹掉。
2. 修好后用 `POST .../test` 验证渠道通了。
3. **让熔断自然恢复**：冷却期（`ALERT_CIRCUIT_COOLDOWN_SECONDS`，默认 300 秒）
   到期后，下一个真实告警会作为探测请求放行；成功即关闭熔断。也可以主动
   制造一次投递（触发一条测试事件）来触发探测。

   **【改变状态，需说明代价】** 如果需要立刻恢复告警，改用缩短
   `ALERT_CIRCUIT_COOLDOWN_SECONDS` 并重启 http-worker 的方式（比删 key 可追溯：
   配置变更是可见的，删 key 不是）。代价：冷却变短会更快地对坏渠道重试。

### 处置 D：邮件渠道整体不可用

确认 `SMTP_HOST` 已配置。`SMTP_PORT` 默认 25，多数云环境与办公网络会拦截
出站 25 端口；被拦截的表现是连接超时，`last_error` 里是连接类错误。
**【改变状态】** 换到 587/465 或经批准的 relay，需要同步调整 TLS 相关配置，
属于变更动作，需要验证。

### 处置 E：告警延迟（队列堆积）

投递正常但「告警来得太晚」时，问题在队列而不是渠道：见
[redis.md](redis.md) 的 `webmonitoring:alert-jobs`。判断指标是
`webmonitoring_queue_pending{stream="webmonitoring:alert-jobs"}`。

## 恢复验证

```bash
# 1. 测试投递返回 DELIVERED
curl -s -X POST "http://localhost:8080/api/v1/alert-channels/<id>/test" \
  -H "X-CSRF-Token: $CSRF" -b "$COOKIE_JAR"

# 2. 控制台渠道列表标签回到「启用」（suspended 为 false）
curl -s http://localhost:8080/api/v1/alert-channels -b "$COOKIE_JAR" \
  | grep -o '"suspended":[a-z]*'

# 3. 不再有新的抑制
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep webmonitoring_alert_suppressed_total

# 4. 一次真实投递成功：触发一条会产生事件的变更，确认
#    - 出现 status=DELIVERED 的投递行
#    - 目标渠道真的收到了消息（这是唯一算数的证据）
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT status, count(*) FROM alert_deliveries WHERE created_at > now() - interval '10 minutes' GROUP BY 1;"
```

判据是「**渠道真的收到了消息**」，不是「测试接口返回 200」，也不是「熔断
标签消失了」。熔断标签消失只说明状态被清了。

## 升级/上报

- **有 `FAILED` 投递或死信被丢弃**：这些告警**不会自动补发**。需要记录并向
  责任人明说「这段时间的事件没有通知出去」——静默丢失告警是安全事件的一部分，
  不能只修好链路就算完。补发的动作本身是**对外发送告警/客户数据**，属于
  高风险操作，需要授权。
- **熔断打开超过一个巡检周期**：意味着平台在静默期间没有告警能力，需要上报。
- 需要删除/覆盖 `alert_deliveries` 记录、审计记录，或清 Redis 的
  `notifygate:*` 状态：都属于「删除或覆盖证据/审计」类操作，需要授权与审计。
- 修改 `ALERT_FAILURE_THRESHOLD` / `ALERT_MAX_PER_CHANNEL_PER_WINDOW` /
  `ALERT_CIRCUIT_COOLDOWN_SECONDS`：生产门禁会校验这几个值（
  `internal/config/production.go` 的 `validateAlertGate`），把它们设成 0
  等于关闭控制，生产会拒绝。需要变更就走正式发布。
