# ADR-0006：事件流的跨进程投递

状态：Accepted
日期：2026-10-10

## 背景

控制台通过 `GET /api/v1/events/stream`（SSE）接收实时更新。这条流订阅的是**本进程**的
broker（`internal/events/broker.go`），而 broker 自项目开始就是纯进程内的：一个
`map[chan []byte]struct{}` 加一次非阻塞扇出，没有任何跨进程传输。

单进程时这没问题。但 Compose 恒设 `REDIS_ADDR`，检测因此跑在 http-worker 进程
（`apps/api/main.go` 投队列、`apps/http-worker/main.go` 消费），而那是**另一个进程**。
worker 的 broker 没有任何订阅者——它不跑 `api.NewServer`，也不服务 SSE 端点。于是：

- **到不了控制台**：`detection.job.running/succeeded/retrying/timed_out/failed`、
  `baseline.candidate.created`，以及最要紧的检测产生的新事件 `incident.created` /
  `incident.updated`。
- **能到**：调度器排队的 `detection.job.queued`（由 API 进程的 scheduler 发布），以及
  事件转移与备注的 `incident.updated`（`internal/api/server.go` 内联发布）。

后果是运营视角的一个空洞：**检测发现了新事件，控制台不会自己冒出来**，得手动刷新。
它还特别难在测试里发现——无 `REDIS_ADDR` 的嵌入式模式在 API 进程内
`go s.Process(...)` 执行检测，所有事件都到达，所以单测与嵌入式路径永远看不到这个缺口。

同一次排查还发现这条流**没有租户隔离**（任意已登录用户都能收到其他租户的事件）。那一条
作为缺陷单独修复，不在本 ADR 范围内；但两者共用同一个 broker，租户作用域是本决策的前提。

## 决策

1. **relay 是可插拔的传输**。`Broker` 持有一个可选的 `Relay`；`Publish` 在本地扇出之后把
   事件**非阻塞入队**到一个有界队列（深度 256），由唯一的 sender goroutine 排空。relay
   卡住或不存在只会丢事件，**绝不拖慢发布它的调用方**——那是检测 worker 的请求路径。
2. **每租户一个 Redis channel**（`webmonitoring:events:<tenantID>`），每个进程一条
   `PSubscribe("webmonitoring:events:*")`。连接数与租户数无关，每副本 O(1)。否决「全局单
   channel + 进程内过滤」：那样每次投递要把全部租户的载荷搬进每个副本的内存，租户隔离退化
   成「一个 `if` 写对没写对」，一处逻辑错即跨租户泄漏。
3. **接收路径永不回发**。`Broker.DeliverRemote` 只做本地扇出——这才是排除环路的机制。
   envelope 里的 `origin` 管的是**去重**（不把本进程自己的回显投递第二次），不是防环。
4. **冗余校验失败关闭**：channel 名里的租户与 envelope 里的租户必须一致，不一致即丢。
   将来有人把其中一个算错，结果是一个事件被丢掉，而不是被投给错误的租户。
5. **接受尽力而为（lossy）投递，不做补偿。**

## 后果

**收益**：检测产生的事件在任何副本的控制台上都能实时出现；横向扩容不再让控制台的实时性
随副本数衰减。

**投递语义（有前提，与前端契约绑定）**：Redis pub/sub 不持久化——进程重启、订阅重建与
队列溢出都会丢事件。这成立的前提是**事件只是建议性的**：前端 `onUpdate` 不收参数、从不读
`event.data`，只把它当「有变化，去重新拉取」的信号（`apps/console/src/services/events/
stream.ts`），真正的数据永远来自随后的 REST 拉取。**若将来前端改成从 `event.data` 直接
渲染，此结论必须重评**——那时事件变成权威数据源，需要持久化投递（Redis Streams + 每副本
消费组与 ack），是另一个量级的改动。

**故障模式**：

- 启动时 Redis 不可达 → `NewRedisRelay` 返回 error，两个 main 都 `log.Fatal`。这与既有契约
  一致（队列在同条件下已经 `log.Fatal`），不新增失败模式。**可回退点**：改成 `logger.Warn`
  并跳过 relay，即只降级为本地投递——租户隔离不受影响，因为隔离在 broker 里。
- 运行时 Redis 挂掉 → `Publish` 只记日志与计数，请求路径不阻塞；订阅以指数退避重建
  （500ms → 30s）。这段时间控制台只能靠手动刷新。
- 事件量：每个检测任务约 4 条事件，每条经每个订阅进程一次。有界队列溢出即丢并计数。

**不新增 readiness 检查**：事件流是建议性的，把它挂进 `/readyz` 会让 Redis 一抖就把整个
API 踢出轮转，代价远大于「UI 少刷一次」。Redis 健康已由队列覆盖。

**指标**：丢弃与失败只记固定 reason 集合（`webmonitoring_events_relay_dropped_total`、
`webmonitoring_events_relay_failures_total`）。**不得挂租户标签**——那会把租户名泄进
`/metrics`，并让 series 数随租户数增长（0.8 那轮确立的纪律）。

**单一 Redis 假设**：channel 前缀不做环境化，与队列 stream 名
（`webmonitoring:detection-jobs`）一样假定「一个部署一个 Redis 实例」。

## 验证

- **红→绿**：三处 mutation 逐个注入，对应测试都失败（去掉 origin 回显抑制、去掉
  channel/tenant 一致性检查、把投递租户写死）。
- **miniredis**：跨 broker 投递、线上租户隔离、回显只收到一次、Redis 关掉后不阻塞不 panic、
  publish-only relay。
- **真实 Redis**（`REDIS_TEST_ADDR` 门控）：同一组不变量在真实服务器上跑一遍，覆盖 go-redis
  自身的协议处理与重订阅。
- **部署拓扑**：`scripts/compose-smoke.ps1` 与 `scripts/multi-replica-drill.ps1`，结果记入
  [rehearsals.md](../rehearsals.md)。

## 迁移方式

无需迁移。未配 `REDIS_ADDR` 的部署行为逐字节不变；配了的部署下次启动即生效。

回滚：去掉两处 `broker.SetRelay(...)` 即回到进程内投递，事件流退化为只在 API 进程内可见。
