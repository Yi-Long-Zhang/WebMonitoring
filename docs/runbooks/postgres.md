# 运行手册：PostgreSQL 不可用 / 连接池耗尽

**Q：我看到的什么现象指向这本手册？**

`/readyz` 返回 503 且 `dependencies` 里含 `"postgres":"unavailable"`；或在
PostgreSQL 尚可连接的情况下，接口大面积返回 500 `STORAGE_UNAVAILABLE`、
日志出现 `acquire` / 连接超时类错误。

**Q：这本手册里哪一步会改变状态？**

带 **【改变状态】** 标记的小节。只读诊断命令没有标记，可以先跑完再决定。

## 判断

### 1. 确认是 PostgreSQL 还是连接池

```bash
# 依赖分级：postgres 是否在失败列表里
curl -s http://localhost:8080/readyz

# 容器与健康检查状态
docker compose -f deployments/compose/docker-compose.yml ps postgres
docker inspect --format '{{.State.Health.Status}} restarts={{.RestartCount}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q postgres)
```

`postgres` 的 healthcheck 是 `pg_isready -U webmonitoring`（interval 5s、
timeout 3s、retries 20）。`unhealthy` 说明数据库进程本身没在正常应答，走
「处置 A」；`healthy` 但 `/readyz` 仍报 `postgres`，说明是**连接层**问题
（连接池取不到连接、DSN 或认证失败），走「处置 B」。

### 2. 直接连进数据库看实时状态

```bash
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT count(*) AS total,
          count(*) FILTER (WHERE state='active') AS active,
          count(*) FILTER (WHERE state='idle') AS idle,
          count(*) FILTER (WHERE wait_event_type='Lock') AS waiting_on_lock
     FROM pg_stat_activity WHERE datname='webmonitoring';"
```

```bash
# 按状态的连接分布：idle in transaction 堆积是最常见的「池子被占满」形态
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT state, count(*) FROM pg_stat_activity
    WHERE datname='webmonitoring' GROUP BY 1 ORDER BY 2 DESC;"
```

```bash
# 长事务与持锁查询：找占着连接不放的那个
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT pid, now()-xact_start AS xact_age, state, left(query,80) AS query
     FROM pg_stat_activity
    WHERE datname='webmonitoring' AND xact_start IS NOT NULL
    ORDER BY xact_start ASC LIMIT 10;"
```

```bash
# 数据库与卷大小（判断是不是磁盘写满导致的写入失败）
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT pg_size_pretty(pg_database_size('webmonitoring')) AS db_size;"
docker system df -v | grep -i postgres-data
```

### 3. 确认是不是「任务卡住」而不是「数据库坏了」

任务领取租约默认 5 分钟（`store.JobLease`，`internal/store/repository.go`；
生产用 `JOB_LEASE_SECONDS` 覆盖）。调度器每 30 秒调用 `ReapTimedOut` 把
`status='RUNNING' AND started_at < now()-租约` 的任务改成 `TIMED_OUT` /
`CHECK_TIMEOUT`，并打印 `reaped stale RUNNING detection jobs`
（`internal/detection/scheduler.go`、`internal/store/postgres.go`）。

```bash
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 api \
  | grep -i "reaped stale RUNNING"
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT status, count(*) FROM detection_jobs GROUP BY 1 ORDER BY 2 DESC;"
```

如果 `TIMED_OUT` 在涨、但 `postgres` 健康，**数据库本身不是故障点**：是
http-worker 没有在消费（见 [redis.md](redis.md)）或浏览器任务变慢（见
[browser-worker.md](browser-worker.md)）。不要因为「任务超时」去重启数据库。

### 4. 迁移 checksum 不一致时立即停下

```bash
docker compose -f deployments/compose/docker-compose.yml run --rm migrate up
```

`migrate` 按原始字节校验每个迁移文件。**checksum 不一致说明迁移文件或数据库
记录被改动过**——这是安全事件，不是运维故障。不要 `baseline`、不要删卷，
按「升级/上报」处理。

## 处置

### 处置 A：PostgreSQL 进程不健康

```bash
# 先取证，再动手：日志是重启后唯一的现场
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 postgres
```

**【改变状态】** 重启数据库容器。这会断开所有在途连接：

```bash
docker compose -f deployments/compose/docker-compose.yml restart postgres
```

**不要**用删除卷的方式「修复」任何问题。`postgres-data` 卷里是全部业务元
数据、基线、事件与审计记录；删除它是不可逆的数据销毁，且违反项目原则
（[operations.md](../operations.md) 明确写了「不得通过删除 PostgreSQL 数据卷
来完成升级」）。

### 处置 B：`pg_isready` 正常但连着取不到连接

1. 先按「判断 2」找出占着连接的会话。`idle in transaction` 长时间不动的会话
   通常来自一个卡死的调用方，而不是数据库。
2. **【改变状态，会造成该请求失败】** 终止个别卡死的后端会话，比重启整个
   数据库影响面小得多：

   ```bash
   docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
     psql -U webmonitoring -d webmonitoring -c \
     "SELECT pg_terminate_backend(<pid>) FROM pg_stat_activity WHERE pid=<pid>;"
   ```

   牺牲的是那个会话正在进行的事务；它的调用方会拿到一个错误并失败/重试。
3. 如果连接数确实被打满且没有长事务，重启**应用侧**（`api`、`http-worker`）
   比重启数据库更合适：连接池属于应用进程，重启它们会释放整池连接。
   ```bash
   docker compose -f deployments/compose/docker-compose.yml restart api http-worker
   ```
   代价：在途的检测任务会失去连接，随后被租约机制回收并重排（见上面的
   `ReapTimedOut`）；API 重启期间控制台不可用。**没有丢失已保存证据的风险。**

### 处置 C：磁盘/卷写满

先确认是哪个卷满（`docker system df -v`）。若是 `postgres-data` 满，通常不是
「数据库变大」而是**保留任务没有执行**——证据元数据行、审计行、检测任务行只增不减。
保留任务由控制面进程托管（`internal/maintenance`），按
`RETENTION_INTERVAL_HOURS`（默认 24h）执行，并持 `pg_try_advisory_lock` 的
`maintenance.retention` 租约，**多个副本同时只有一个会跑**。检测任务行由
`JOB_RETENTION_DAYS`（默认 30 天）清理，只动终态行。

```bash
# 是否有人持有保留租约 / 是否有副本在跳过
docker compose -f deployments/compose/docker-compose.yml logs api \
  | grep -i "retention"
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT count(*) FROM pg_locks WHERE locktype='advisory';"
```

**【改变状态：会删除数据】** 只有确认过期数量和事件引用之后才执行保留清理。
先跑 dry-run，它和后台任务共用同一段逻辑，因此 dry-run 的结果就是下一次
后台执行的打算：

```bash
# 需要管理员会话与 CSRF token；execute:false 是 dry-run
curl -s -X POST http://localhost:8080/api/v1/maintenance/retention \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -b "$COOKIE_JAR" -d '{"execute":false}'
```

返回体的 `expiredEvidence` / `protectedEvidence` / `deletedAuditEvents` 就是
将要发生的量。确认无误后再用 `{"execute":true}`。

牺牲与边界（`internal/maintenance/retention.go`，不可放宽）：

- 被事件或基线引用的证据**不会**被删除，即使已过期。
- 先删对象存储里的对象、再删元数据行；部分失败留下的是「可发现的记录」而
  不是孤儿对象。
- **已停用（disabled）的租户会被跳过**，其证据在人工复核期间不会被清理。
- 一键删除是不可逆的。审计保留默认 365 天（`AUDIT_RETENTION_DAYS`），
  证据保留默认 90 天（`EVIDENCE_RETENTION_DAYS`）。

如果清完仍然满，说明基础容量不足，属于容量规划问题，按「升级/上报」处理。

### 处置 D：DSN 或认证失败

`POSTGRES_DSN` 由 `POSTGRES_PASSWORD` 拼接
（`postgres://webmonitoring:${POSTGRES_PASSWORD}@postgres:5432/webmonitoring?sslmode=...`）。
改密码需要**同时**改数据库角色和 `.env`，只改一边的结果就是认证失败。

```bash
# 确认当前 DSN 里的库名与用户（不要打印密码）
docker compose -f deployments/compose/docker-compose.yml config | grep -i POSTGRES_DSN
```

注意生产门禁：`APP_ENV=production` 时 `POSTGRES_DSN` 的 `sslmode=disable` 会
**直接拒绝启动**（`internal/config/production.go`），这是有意的破坏性变更，
不是 bug。

## 恢复验证

```bash
# 1. 依赖就绪
curl -s http://localhost:8080/readyz

# 2. 连接数回到正常区间，没有 idle in transaction 堆积
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT state, count(*) FROM pg_stat_activity
    WHERE datname='webmonitoring' GROUP BY 1 ORDER BY 2 DESC;"

# 3. 任务重新流动：pended 回落、新任务进入终态
curl -s http://localhost:8080/metrics | grep 'webmonitoring_detection_jobs_total'
curl -s http://localhost:8080/metrics | grep 'webmonitoring_queue_pending'

# 4. 一次端到端写入：触发一个资产的检测，确认证据落库且可下载
```

判据不是「`/readyz` 返回 200」，而是**一次真实检测跑完并写出证据**。
`/readyz` 只证明 `Ping` 通了。

## 升级/上报

- 迁移 checksum 不一致：**停止发布并调查**，视为可能的篡改，不要继续。
- `pg_isready` 健康但持续报错、且找不到具体会话：可能是数据损坏，需要有人
  决定是否进入恢复流程（见 [backup-restore-rehearsal.md](../backup-restore-rehearsal.md)）。
- 需要执行恢复（`scripts/compose-restore.ps1 -Execute -ConfirmText RESTORE`）：
  这是**带审批的高风险操作**。恢复会停止 API/Worker、覆盖数据库与 evidence
  bucket。执行前必须记录审批、备份版本、RPO/RTO 和回滚方案
  （见 [operations.md](../operations.md)）。
- 卷空间在不清理数据的前提下无法容纳增长：容量规划问题，需要人工决策。
- 任何需要删除卷、删除表或改写审计记录的动作：项目原则要求授权与审计，
  必须先上报。
