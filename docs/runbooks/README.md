# 故障处置手册（Runbooks）

本目录按**故障类别**组织处置手册。

**Q：怎么用这个目录？**

先回答"是谁坏了"这个问题——`GET /readyz` 会直接告诉你是哪个依赖。拿到组件名后
进对应的手册，按手册里的"判断"小节逐条确认，再按"处置"小节动手。

**Q：手册的结构是什么？**

每个手册的结构固定：

| 小节 | 含义 |
| --- | --- |
| 症状 | 一句话描述这类故障长什么样 |
| 判断 | 只读的诊断命令，用来确认或排除该诊断 |
| 处置 | 具体动作；任何会改变状态或造成损失的步骤都显式标注 |
| 恢复验证 | 怎么确认已经恢复健康，而不是「看起来没报错」 |
| 升级/上报 | 什么情况下必须停下来找人 |

**Q：处置命令里有危险操作吗？**

有，且都会显式标注。带 **【改变状态】** 标记的小节会改动运行中的系统或删除数据，
执行前先读该小节说明。**只读诊断命令没有任何标记**，可以放心先跑。

## 手册索引

| 手册 | 覆盖的故障类别 |
| --- | --- |
| [postgres.md](postgres.md) | PostgreSQL 不可用，或连接池被占满 |
| [redis.md](redis.md) | Redis 内存耗尽，或任务/告警队列堆积 |
| [object-storage.md](object-storage.md) | 对象存储（VersityGW）不可达，或证据卷写满 |
| [browser-worker.md](browser-worker.md) | 浏览器 Worker 崩溃，或 Chromium 需要更新 |
| [alert-delivery.md](alert-delivery.md) | 告警渠道投递失败 |

## 先看这里：谁在故障

**Q：第一步做什么？**

**第一步永远是 `GET /readyz`。** 它逐个检查依赖，并在不健康时返回 503 和
依赖名，这比逐个容器猜要快得多（`apps/api/main.go` 的 `SetReadiness`）：

```bash
curl -s -i http://localhost:8080/readyz
```

返回 `200 {"status":"ready"}` 说明下列依赖全部就绪。返回
`503 {"status":"not_ready","dependencies":{...}}` 时，key 就是故障组件：

| `/readyz` 的 key | 实际组件 | 对应手册 |
| --- | --- | --- |
| `postgres` | PostgreSQL 连接池（`Ping`） | [postgres.md](postgres.md) |
| `redis` | Redis detection Stream | [redis.md](redis.md) |
| `alert_redis` | Redis alert Stream | [redis.md](redis.md) |
| `evidence` | 对象存储（Compose 下即 VersityGW） | [object-storage.md](object-storage.md) |
| `browser_worker` | 浏览器 Worker `/healthz` | [browser-worker.md](browser-worker.md) |

> `/readyz` **不覆盖**告警投递链路：投递失败时 API 和 worker 都仍然就绪。
> 告警问题只能从交付表、指标和控制台的渠道列表看出来，见
> [alert-delivery.md](alert-delivery.md)。

### 症状 → 手册对照表

| 你看到的症状 | 最可能的原因 | 去哪 |
| --- | --- | --- |
| `/readyz` 的 `postgres` 不可用；列表接口大面积 500 `STORAGE_UNAVAILABLE` | PostgreSQL 停机或连接池满 | [postgres.md](postgres.md) |
| API/worker 启动即失败，日志里有 `connect postgres`；`docker compose ps` 里 postgres 反复重启 | PostgreSQL 起不来 | [postgres.md](postgres.md) |
| 迁移报 checksum 不一致 | 迁移文件或数据库被改动 | [postgres.md](postgres.md) |
| 检测任务全部卡在 `RUNNING`，之后被 `TIMED_OUT` 收掉，日志里 `reaped stale RUNNING detection jobs` | 任务领取租约到期，worker 没在跑 | [postgres.md](postgres.md)（先确认库没挂）、[browser-worker.md](browser-worker.md) |
| `/readyz` 的 `redis` 或 `alert_redis` 不可用；`/metrics` 里 `webmonitoring_queue_read_failures_total` 上涨 | Redis 停机或达到 `maxmemory` | [redis.md](redis.md) |
| `/metrics` 的 `webmonitoring_queue_pending` 持续增长、不再回落 | 队列堆积（消费者不够或单个任务变慢） | [redis.md](redis.md) |
| `/metrics` 里 `webmonitoring_queue_dead_letters_total` 增长 | 消息耗尽重试次数被丢弃 | [redis.md](redis.md) |
| `/readyz` 的 `evidence` 不可用；`webmonitoring_evidence_failures_total` 上涨；任务以 `EVIDENCE_WRITE_FAILED` 失败 | 对象存储不可达 | [object-storage.md](object-storage.md) |
| 宿主或卷使用率持续上涨，`objectstore-data` / `postgres-data` 卷写满 | 保留任务没跑或跑失败 | [object-storage.md](object-storage.md)、[postgres.md](postgres.md) |
| 证据下载 404 `EVIDENCE_MISSING`，但事件里仍有该证据 | 对象与元数据不一致 | [object-storage.md](object-storage.md) |
| `/readyz` 的 `browser_worker` 不可用；只有 `BROWSER` 模式任务失败 `CHECK_FAILED` | 浏览器 Worker 崩溃 | [browser-worker.md](browser-worker.md) |
| 浏览器 Worker 反复 OOM（`exit 137`） | `mem_limit` / `shm_size` 不足或页面过大 | [browser-worker.md](browser-worker.md) |
| 浏览器任务全部 `CHECK_TIMEOUT`，HTTP 任务正常 | Chromium 启动慢/失败或页面超时 | [browser-worker.md](browser-worker.md) |
| 控制台渠道列表出现「投递熔断」标签；`webmonitoring_alert_suppressed_total{reason="CIRCUIT_OPEN"}` 增长 | 渠道被熔断 | [alert-delivery.md](alert-delivery.md) |
| 控制台渠道出现「投递熔断」且 `reason="LIMITED"`；`RATE_LIMITED` 大量出现 | 渠道被限流，不是渠道坏了 | [alert-delivery.md](alert-delivery.md) |
| `alert_deliveries` 里出现 `FAILED` 行、`last_error` 有值 | 渠道配置或网络问题 | [alert-delivery.md](alert-delivery.md) |
| 控制台能打开但接口返回 429 `RATE_LIMITED` | 请求配额被打满，不是依赖故障 | 先看 `/metrics` 的 `webmonitoring_rate_limited_total{class}`，再回到对应组件的 `/readyz` |

### 通用只读诊断入口

下面这几条在任何一个手册里都用得上，先跑一遍能把范围缩小很多。

```bash
# 1. 依赖分级：哪个组件不健康
curl -s http://localhost:8080/readyz
# 进程存活（不检查依赖）
curl -s http://localhost:8080/healthz

# 2. 容器状态与重启次数（RestartCount 高 = 反复崩溃）
docker compose -f deployments/compose/docker-compose.yml ps
docker compose -f deployments/compose/docker-compose.yml logs --tail=200 <service>

# 3. 依赖自身的健康检查
docker inspect --format '{{.State.Health.Status}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q postgres)
# 同理替换为 redis / objectstore / browser-worker / console

# 4. 指标（Prometheus 文本格式；设了 METRICS_TOKEN 时需带 Bearer 头，否则 401。
#    生产校验强制要求设置它，下面的命令假定它已导出到环境）
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics
# 其中两组最能说明「监测本身是不是还活着」：
#   webmonitoring_last_successful_detection_timestamp_seconds  最近一次成功检测的时刻
#   webmonitoring_detection_checks_last_hour{result}           最近一小时的完成/失败数
# 前者停止推进就是信号，哪怕控制台上一条事件都没有。

# 5. 现在跑的是哪个版本（在会话门禁之后，需要登录后的 Cookie；控制台总览页也显示）。
#    它不读数据库，库正是坏掉的那一环时也答得上来——而那时恰恰是有人要问它的时候。
curl -s -b "webmonitoring_session=<登录后的 Cookie 值>" http://localhost:8080/api/v1/version
```

> **日志只保留最近 3×10MB。** 所有服务用 `json-file` 驱动、`max-size: 10m`、
> `max-file: 3`（见 `deployments/compose/docker-compose.yml` 的 `x-logging`）。
> 这意味着「上周的日志」通常已经不在了：**先取证再重启**，尤其是容器已经
> 崩溃重启过的场景。重启会清掉 stdout 日志、临时文件和内存状态，是本手册
> 里最常见的不可逆动作之一。

### 关于本手册使用的命令

- 命令均以 `deployments/compose/docker-compose.yml` 为准。叠加了 TLS overlay
  时再加 `-f deployments/compose/docker-compose.tls.yml`；但要注意 **TLS overlay
  会摘掉 API 的 `8080` 与控制台的 `8088` 端口**（`ports: !reset []`），此时
  宿主机上 `curl http://localhost:8080` 不通，要从边缘走 HTTPS 或在容器内
  `exec` 执行。
- 验收脚本（`scripts/*.ps1`）跑起来的栈用的是**不同的 Compose 项目名和端口**：
  smoke 是 `webmonitoring-smoke` / API `18080`、控制台 `18088`；压测是
  `webmonitoring-loadtest` / API `18081`；多副本演练是 API `18082`。排查时先
  确认自己在看哪个项目，否则读的是另一个栈的状态。
- Windows 上 shell 是 PowerShell：Docker 命令本身通用，但 `curl` 在
  PowerShell 5.1 里是 `Invoke-WebRequest` 的别名，想用真 curl 要写
  `curl.exe`。执行 `.ps1` 还需要放开执行策略（见
  [operations.md](../operations.md) 的「运行验收脚本的前置条件」）。
