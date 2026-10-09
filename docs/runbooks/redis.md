# 运行手册：Redis 内存耗尽 / 队列堆积

**Q：我看到的什么现象指向这本手册？**

`/readyz` 里 `redis` 或 `alert_redis` 报 `unavailable`；或 Redis 仍可连接，
但 `/metrics` 的 `webmonitoring_queue_pending` 持续增长不再回落、
`webmonitoring_queue_dead_letters_total` 开始增长、检测任务迟迟不进入终态。

**Q：为什么这里要先讲队列的形状？**

因为两个 Stream 有两套独立的消费者组和重试语义，"堆积"出现在哪一个上面，
处置完全不同。先看懂它们再动手。

## 先理解队列的形状

队列是 **Redis Streams**，不是列表。两个独立的 Stream，各有自己的消费者组
（`internal/jobqueue/redis.go`）：

| Stream | 消费者组 | 写的一方 | 读的一方 |
| --- | --- | --- | --- |
| `webmonitoring:detection-jobs` | `detection-workers` | API（入队） | http-worker |
| `webmonitoring:alert-jobs` | `alert-workers` | API / detection service | http-worker |

两者的死信 Stream 分别是上面两个名字加 `:dead-letter` 后缀。消费者用
`XReadGroup Count: 1` **同步**处理，**一个副本一次只处理一个任务**——所以吞吐
≈ 副本数 ÷ 单次检测耗时，扩容 `browser-worker` 不会提高吞吐
（[operations.md](../operations.md) 已实测记录）。

重试阶梯与上限（`QUEUE_*` / `ALERT_QUEUE_*`）：

- `QUEUE_MAX_DELIVERIES`（默认 5）：**单条消息**被投递的次数上限。超过就进
  死信：detection 侧把任务标成 `FAILED` / `DEAD_LETTER` 并写审计
  `detection.dead_letter`；alert 侧把该事件的投递记录标成 `FAILED`、写审计
  `alert.dead_letter`（`apps/http-worker/main.go`）。
- `QUEUE_MIN_IDLE_SECONDS`（默认 120）：滞留消息被 `XAutoClaim` 重新领取前
  必须空闲的时间。**不要把它调到单次浏览器检测的最大耗时以下**，否则仍在
  执行的消息会被重复领取。
- `QUEUE_RETRY_BASE_MILLISECONDS`（默认 500）：检测任务层重试的退避基数
  （`internal/detection/service.go` 的 `enqueueRetry`，延迟 = `2^retryCount × base`，
  最多 3 次）。

注意这里有**两层**重试，不要混淆：

1. **Stream 投递层**：消息处理失败时不 ACK，靠 `XAutoClaim` 重新投递，上限
   `QUEUE_MAX_DELIVERIES`。
2. **检测任务层**：任务以可重试错误码（`CHECK_FAILED`、`EVIDENCE_WRITE_FAILED`、
   `EVIDENCE_METADATA_FAILED`、`BASELINE_READ_FAILED`、`BASELINE_CREATE_FAILED`、
   `INCIDENT_WRITE_FAILED`、`ALERT_QUEUE_FAILED`）失败时，`RetryCount++` 后重新
   入队，最多 3 次；其余错误码直接进入终态。

## 判断

### 1. 是 Redis 挂了，还是队列堆积

```bash
curl -s http://localhost:8080/readyz
docker compose -f deployments/compose/docker-compose.yml ps redis
docker inspect --format '{{.State.Health.Status}} restarts={{.RestartCount}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q redis)
```

`unhealthy`（healthcheck 是 `redis-cli ping`，带 `REDISCLI_AUTH` 时用密码）
说明是连接问题，走「处置 A」。`healthy` 但 `pending` 在涨，走「处置 B」。

### 2. 队列深度与滞留消息

先读指标——它比手工算 Stream 长度更直接，且与调度器看到的是同一份数字：

```bash
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep -E \
  'webmonitoring_queue_pending|webmonitoring_queue_enqueues_total|webmonitoring_queue_deliveries_total|webmonitoring_queue_dead_letters_total|webmonitoring_queue_read_failures_total|webmonitoring_queue_claim_failures_total|webmonitoring_queue_processing_failures_total'
```

`webmonitoring_queue_pending{stream=...}` 由消费者每次循环后 `Set` 写入，因此
**只在有消费者在跑时才会更新**。数值停滞不动本身就是信号：消费者可能已经
退出循环——消费过程本身仍会重试（Redis 读失败后按 500ms→30s 退避重入，不退出
进程，见 `consumeWithRetry`），所以停滞更可能是消费真的不干了、或 worker 已经
不在，而不是一次读抖动。

```bash
# 两个 Stream 的长度与消费者组状态
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:detection-jobs
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:alert-jobs
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XINFO GROUPS webmonitoring:detection-jobs
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XINFO GROUPS webmonitoring:alert-jobs
```

`XINFO GROUPS` 的 `pending` 是「已投递未 ACK」的数量，`last-delivered-id` 与
`entries-read` 能看出组是否还在推进。**`pending` 高且不降 = 有消息在被反复
重投或卡在某个消费者手里**。

```bash
# 哪个消费者在积压（消费者名 = 各副本的主机名，因为 WORKER_ID 故意留空）
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XINFO CONSUMERS webmonitoring:detection-jobs detection-workers
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XPENDING webmonitoring:detection-jobs detection-workers
```

> Redis 设置了 `REDIS_PASSWORD` 后所有 `redis-cli` 都要认证。用
> `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ...`（**不要**用 `-a`：那会把
> 口令放进进程参数，任何能读 `/proc` 的东西都看得到——compose 文件的
> healthcheck 就是这么做的）。

### 3. 死信：确认是不是在丢消息

```bash
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:detection-jobs:dead-letter
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:alert-jobs:dead-letter
# 最近几条死信的原始错误（payload 里含任务内容，按敏感数据处理）
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XREVRANGE webmonitoring:detection-jobs:dead-letter + - COUNT 3
```

死信里的 `error` / `attempts` 字段是脱敏后的原因与投递次数
（`internal/jobqueue/redis.go` 的 `deadLetter`、`safeError`）。同时对照数据库：

```bash
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT status, count(*) FROM detection_jobs GROUP BY 1 ORDER BY 2 DESC;"
```

`DEAD_LETTER` 出现在 `error_code` 上，且有一条 `detection.dead_letter` 审计。

### 4. 内存：确认是 maxmemory 还是真的写满了

compose 里 Redis 是 `redis-server --appendonly yes --requirepass ...`，
`mem_limit: 1g`，卷 `redis-data:/data`。**仓库没有配置 `maxmemory` 或
`maxmemory-policy`**，所以「内存耗尽」在本项目里有两种形态，处置完全不同：

- 容器被 cgroup 限制触发 OOM：`docker inspect` 的 `OOMKilled`、退出码 137。
- 宿主磁盘被 AOF 写满：`redis-data` 卷增长。

```bash
docker inspect --format 'OOMKilled={{.State.OOMKilled}} exit={{.State.ExitCode}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q redis)
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli INFO memory
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli INFO persistence
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli INFO keyspace
docker system df -v | grep -i redis-data
```

## 处置

### 处置 A：Redis 不可达

**【改变状态】** 先取证再重启：

```bash
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 redis
docker compose -f deployments/compose/docker-compose.yml restart redis
```

队列是 Stream + AOF 持久化的：**Redis 重启不会丢已 ACK 之外的消息**，但
**会丢在途消息的 ACK 状态**——重启后它们仍是 pending，会被 `XAutoClaim` 在
`QUEUE_MIN_IDLE_SECONDS` 之后重新领取。这就是「任务幂等」这条架构不变量存在
的原因，也因此**重复处理一次是可接受的**，不需要人工修数据。

三种修不掉的启动失败，需要先改配置：

- 口令不一致：`REDIS_ADDR` 必须写成 `redis://:password@redis:6379` 以携带同一
  个 `REDIS_PASSWORD`。不一致时**启动就失败**（fail closed），而不是静默降级。
- 生产门禁：`APP_ENV=production` 时 `REDIS_ADDR` 的口令不得为空、不得等于
  默认值、并且**至少 16 字符**（`internal/config/production.go` 的
  `validateRedisCredentials`）。
- 遗留的 `WORKER_ID`：生产会拒绝已知的固定名字，因为多副本 `--scale` 下
  固定名会让副本互相回收在途消息。保持 `WORKER_ID` 为空，让
  `config.WorkerIdentity` 生成每进程唯一身份。

### 处置 B：队列堆积但 Redis 健康

按「消费者不够 → 单个任务变慢 → 消息在反复失败」的顺序排查。

1. **消费者还在跑吗**

   ```bash
   docker compose -f deployments/compose/docker-compose.yml ps http-worker
   docker compose -f deployments/compose/docker-compose.yml logs --tail=200 http-worker
   # "detection worker started" 后面如果紧跟错误，说明消费循环已经退出
   docker compose -f deployments/compose/docker-compose.yml logs http-worker \
     | grep -E "read jobs from|claim pending jobs from|redis"
   ```

   Redis 读失败**不会**结束消费：`consumeWithRetry` 记日志、退避（500ms 起，翻倍
   至 30s 上限）后重入 `Consume`/`ConsumeAlerts`，直到进程收到信号。因此不存在
   「重启风暴」这条路径；Redis 长时间不可达的表现是**队列持续堆积**（消费者在
   退避等待），而不是 worker 反复重启——**先修 Redis，不要先扩容 worker**。

2. **【改变状态：增加并发】扩容 http-worker**

   吞吐由副本数决定，不是一个能无限加的旋钮：每个副本同步处理一个任务，
   浏览器任务单次实测约 25 秒（`docs/operations.md`）。

   ```bash
   docker compose -f deployments/compose/docker-compose.yml up -d --scale http-worker=4
   ```

   代价：更多并发浏览器任务会同时打向 `browser-worker`（`mem_limit: 2g`、
   `cpus: 2`）和目标站点；对客户站点来说这是**扩大检测压力**，属于需要知情的
   动作。若目标是内部/测试站点可以直接做；若是客户生产站点，先确认窗口。

3. **消费者身份是否唯一**（只在手工改过 `WORKER_ID` 时才怀疑）

   同名消费者会让 `XAutoClaim` 把对方的在途消息当成滞留消息回收。用
   `XINFO CONSUMERS` 对照各副本容器主机名——这正是不设 `WORKER_ID` 的原因。
   诊断思路见 `scripts/multi-replica-drill.ps1`（它逐个副本按容器主机名匹配）。

4. **单个任务是不是变慢了**

   浏览器模式变慢 → [browser-worker.md](browser-worker.md)；HTTP 模式变慢
   通常是目标站点慢或超时。看任务分布：

   ```bash
   curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep 'webmonitoring_detection_jobs_total'
   docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
     psql -U webmonitoring -d webmonitoring -c \
     "SELECT status, error_code, count(*) FROM detection_jobs
       WHERE created_at > now() - interval '1 hour' GROUP BY 1,2 ORDER BY 3 DESC;"
   ```

5. **消息在反复失败**

   如果 `webmonitoring_queue_deliveries_total{result="failed"}` 和
   `webmonitoring_queue_dead_letters_total` 同步上涨，说明不是慢，是**坏**。
   按死信里的 `error` 定位根因（对象存储？浏览器？），修根因**之后**再考虑
   是否需要重排这些任务。

### 处置 C：内存 / 体积

- **容器 OOM**：**【改变状态：改配置并重启】** 提高 `redis:mem_limit`
  （当前 1g）或减少 `max-file`/日志占用。重启代价同处置 A。
- **AOF 卷增长**：`--appendonly yes` 意味着写入量直接落到 `redis-data` 卷。
  如果卷满，先在宿主上确认不是容器日志（每个服务 3×10MB）或证据卷占的。
  **不要**手工删除 AOF 文件：那等于丢队列状态，且 Redis 可能拒绝启动。
- **STREAM 无限增长**：Stream 本身不会因为 ACK 而缩短，只会被 `XTRIM`/
  `XADD MAXLEN` 截断——**本仓库没有配置任何 Stream 裁剪**。如果
  `XLEN` 长期线性增长，这是需要**上报的容量问题**，不是可以自行 `XTRIM` 的
  操作：截断 Stream 会丢掉尚未处理的检测任务与死信记录（即审计线索）。

## 恢复验证

```bash
# 1. 就绪
curl -s http://localhost:8080/readyz

# 2. pending 回落而不是持平：前后各取一次，间隔大于 QUEUE_MIN_IDLE_SECONDS
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep webmonitoring_queue_pending

# 3. 消费组在推进（last-delivered-id 变化、pending 下降）
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XINFO GROUPS webmonitoring:detection-jobs
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XINFO GROUPS webmonitoring:alert-jobs

# 4. 死信不再增长（数值稳定，而不是清零——死信不会自动消失）
docker compose -f deployments/compose/docker-compose.yml exec -T redis \
  redis-cli XLEN webmonitoring:detection-jobs:dead-letter

# 5. 端到端：触发一次检测并确认进入 SUCCEEDED
```

判据是「**消费组在推进 + 死信不再增长**」，不是「Redis 能 ping 通」。

## 升级/上报

- **确认有消息进入死信（被丢弃）**：`QUEUE_MAX_DELIVERIES` 用尽意味着这些
  检测/告警**不会自动补做**。这属于需要记录并决定是否人工重排的事件；
  重排动作本身会重新触发检测，属于对外部目标的动作，先上报。
- 需要 `XTRIM`/删除 Stream 或死信、需要删除 `redis-data` 卷：**不可逆的数据
  销毁**，必须先授权。
- `webmonitoring:alert-jobs` 堆积导致告警延迟：告警延迟是安全事件的一部分
  （发现到通知的时间），见 [alert-delivery.md](alert-delivery.md)。
- `REDIS_PASSWORD` 轮换：涉及 Redis、API、http-worker 三方同时变更，属于
  需要排期的变更，不要在故障中顺手做。
- 提高 `--scale` 会扩大对客户站点的检测并发：客户生产站点需要事先知情。
