# 运行手册：MinIO 不可达 / 证据卷写满

## 症状

`/readyz` 里 `evidence` 报 `unavailable`；任务以 `EVIDENCE_WRITE_FAILED` /
`EVIDENCE_METADATA_FAILED` 失败并触发重试；证据下载返回 404
`EVIDENCE_MISSING`；或宿主的 `minio-data` 卷持续增长直至写满。

## 先理解对象存储的位置

Compose 里的 `minio` **不发布任何宿主机端口**。API、http-worker 和备份演练用
的 `mc` 客户端都在 Compose 网络内部通过 `http://minio:9000` 访问它。这有两个
直接后果：

- 宿主机上**没有** `http://localhost:9000`，也没有控制台端口。要手工访问
  MinIO，必须 `exec` 进容器，或起一个带 `profiles: ["tools"]` 的 `mc` 服务。
- `S3_ENDPOINT` 指向哪里决定了 API 用哪套存储。留空则退化为本地目录
  `DATA_DIR/evidence`（`apps/api/main.go` 的 `buildEvidenceStore`），**此时
  `evidence` 检查项走的是本地磁盘，不是 MinIO**。排查前先确认这一点。

证据的元数据在 PostgreSQL 的 `evidence` 表
（`id, tenant_id, asset_id, job_id, kind, media_type, size_bytes, sha256,
object_key, created_at, retain_until`），**大体积原始证据在对象存储**。两者
必须一致：对象在、元数据不在 → 孤儿对象；元数据在、对象不在 → 下载 404。

## 判断

### 1. 是对象存储不可达，还是只是某个对象丢了

```bash
curl -s http://localhost:8080/readyz

# MinIO 进程与健康检查（healthcheck 打的是容器内的
# http://localhost:9000/minio/health/live）
docker compose -f deployments/compose/docker-compose.yml ps minio
docker inspect --format '{{.State.Health.Status}} restarts={{.RestartCount}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q minio)
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 minio
```

`evidence` 不健康 → 处置 A。`/readyz` 健康但个别证据下载 404 → 处置 C
（对象与元数据不一致），范围完全不同。

### 2. 确认 API 到底在跟谁说话

```bash
# 只看 endpoint，不打印密钥
docker compose -f deployments/compose/docker-compose.yml config | grep -E 'S3_ENDPOINT|S3_BUCKET'

# MinIO 自身的健康端点（minio 镜像里有 curl，它自己的 healthcheck 就用它）
docker compose -f deployments/compose/docker-compose.yml exec -T minio \
  curl -sf http://localhost:9000/minio/health/live && echo OK
```

> **API 与 http-worker 的镜像是 distroless，没有 shell、没有 curl/wget**
> （`apps/api/Dockerfile`、`apps/http-worker/Dockerfile` 的最终阶段是
> `gcr.io/distroless/static-debian12:nonroot`）。所以「从 API 容器里 wget 一下
> MinIO」这条路走不通，不要在这上面浪费时间：用 `mc`（处置见下）或
> `minio` 容器自己验证连通性。

### 3. 桶是否存在、对象数是否合理

`mc` 在 `profiles: ["tools"]` 下，`up` 不会启动它，用 `run --rm` 临时起。
凭据通过 `MC_HOST_backup` 环境变量传给容器（这是 `scripts/compose-backup.ps1`
的 `Invoke-Mc` 用的方式），**不要**放进命令行参数——`mc` 会把 URL 里的凭据
写进进程参数，任何能读 `/proc` 的东西都看得到：

```bash
# MC_HOST_<alias> 的别名在这里叫 backup，与备份脚本保持一致
export MC_HOST_backup="http://$S3_ACCESS_KEY:$S3_SECRET_KEY@minio:9000"
COMPOSE="docker compose -f deployments/compose/docker-compose.yml"

$COMPOSE run --rm -T --no-deps --entrypoint sh -e MC_HOST_backup \
  mc -c 'mc ls backup'
$COMPOSE run --rm -T --no-deps --entrypoint sh -e MC_HOST_backup \
  mc -c 'mc du backup/evidence'
```

（`--entrypoint sh -e MC_HOST_backup` 是为了让环境变量进入容器；
`mc -c '<命令>'` 才是真正执行的 `mc` 子命令。这是脚本里已验证过的用法。）

> 桶缺失是**启动失败**而不是检测失败：`NewS3Store` 会尝试 `MakeBucket`，
> 最小权限凭据下会走「竞态回退」分支重新检查桶是否存在
> （`docs/object-storage-rehearsal.md`）。所以「API 起不来」先看桶。

### 4. 数据库与对象数量对不上时，谁多谁少

```bash
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT count(*) AS rows, pg_size_pretty(sum(size_bytes)) AS logical_bytes
     FROM evidence;"

docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT retain_until IS NULL AS never_expires, count(*)
     FROM evidence GROUP BY 1;"
```

`retain_until IS NULL` 的证据**永远不会被保留任务清理**。如果这个数字很大，
卷会无限增长，且这不是故障而是数据/策略问题（见处置 D）。

### 5. 写满的是哪个卷

```bash
docker system df -v | grep -iE 'minio-data|postgres-data|redis-data'
docker compose -f deployments/compose/docker-compose.yml ps --format \
  'table {{.Service}}\t{{.Status}}'
```

同时排除容器日志：每个服务 `json-file`、`max-size: 10m`、`max-file: 3`，
正常情况下不会成为主因，但 3 个副本 × 各服务累加仍值得一看。

### 6. 保留任务是否真的在跑

证据清理由控制面托管的保留任务执行（`internal/maintenance/retention.go`），
间隔 `RETENTION_INTERVAL_HOURS`（默认 24，0 表示只保留手工入口）。它持
`pg_try_advisory_lock` 的 `maintenance.retention` 租约，**多副本下只有一个会跑**；
抢不到租约的副本打印 `retention pass skipped; another replica holds the lease`
然后什么都不做——这在多副本下是**正常**的，不是故障。

```bash
docker compose -f deployments/compose/docker-compose.yml logs api \
  | grep -iE "retention|sweep"
```

## 处置

### 处置 A：MinIO 不可达

**【改变状态】** 先取证，再重启：

```bash
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 minio
docker compose -f deployments/compose/docker-compose.yml restart minio
```

`https://` 证据下载会 404；受影响的任务以 `EVIDENCE_WRITE_FAILED` 失败，
`EVIDENCE_WRITE_FAILED` 在 `isRetryable` 列表里，因此任务会**自动重试**
（最多 3 次，退避 `2^n × QUEUE_RETRY_BASE_MILLISECONDS`）。所以短时中断通常
会自愈，不需要人工重排。超过重试次数后任务进入终态，需要人工重跑检测——
重跑会重新抓取目标页面，属于对外部目标的动作。

MinIO 起不来时优先检查：

- `S3_ACCESS_KEY` / `S3_SECRET_KEY` 与 API/worker 侧的取值是否一致。生产门禁
  会拒绝默认值 `webmonitoring` / `replace-with-a-random-secret`
  （`internal/config/production.go` 的 `validateCommon`）。
- `minio-data` 卷是否可写、宿主是否已满。

### 处置 B：卷写满

**先减少写入，再考虑删数据。** 顺序很重要，因为删除是不可逆的。

1. **【改变状态：降低检测频率】** 临时把受影响资产的 `policy` 里
   `httpIntervalSeconds` / `browserIntervalSeconds` 调大，减少证据产生速率。
   代价：降低监测覆盖密度——这是**安全能力上的让步**，需要有人知道并同意。
   恢复容量后要记得调回来。
2. **【改变状态：会删除数据】** 执行保留清理。先 dry-run，再执行：

   ```bash
   curl -s -X POST http://localhost:8080/api/v1/maintenance/retention \
     -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
     -b "$COOKIE_JAR" -d '{"execute":false}'
   ```

   返回体的 `expiredEvidence` 是候选数，`protectedEvidence` 是被事件/基线引用
   而**不会**被删的数量。确认后把 `execute` 改成 `true`。

   不可放宽的边界：被事件或基线引用的证据永不删除；**先删对象再删元数据行**，
   所以部分失败留下的是可发现的记录而不是孤儿对象；**已停用的租户被跳过**。
3. 如果 dry-run 的 `expiredEvidence` 很小，说明多数证据还没到期
   （`EVIDENCE_RETENTION_DAYS` 默认 90 天）：清不出空间，是**容量规划问题**，
   按「升级/上报」处理。

### 处置 C：对象与元数据不一致（404 `EVIDENCE_MISSING`）

这是**证据完整性问题**，不是可用性问题，处置原则是先留证、不覆盖。

```bash
# 1. 确认是单个对象还是成片缺失
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT object_key, kind, size_bytes, sha256, created_at
     FROM evidence WHERE id = '<evidenceId>';"

# 2. 该对象在对象存储里是否存在
export MC_HOST_backup="http://$S3_ACCESS_KEY:$S3_SECRET_KEY@minio:9000"
docker compose -f deployments/compose/docker-compose.yml run --rm -T --no-deps \
  --entrypoint sh -e MC_HOST_backup mc -c 'mc stat backup/evidence/<object_key>'
```

- 对象存在但下载失败：可能是对象内容损坏，用 `mc` 取回后比对元数据里的
  `sha256`。**校验不一致就是证据被改动过的信号**，按「升级/上报」处理，
  不要重新生成或覆盖它。
- 对象不存在：只有从备份恢复才能找回（`scripts/compose-backup.ps1` 会同时
  备份 PostgreSQL dump、evidence bucket 和逐文件 SHA-256 manifest）。**不要**
  用新抓取的页面顶替历史证据——那会破坏「原始证据不可覆盖」。

### 处置 D：`retain_until IS NULL` 的证据过多

这些证据永远不会过期。它们要么来自早期没有设置保留期的写入路径，要么来自
一个本该写保留期的 bug。**删除它们是高风险操作**（删除证据需要授权和审计）。
不要自行 `DELETE`；按「升级/上报」处理，由人工决定逐条设定期限还是保留。

## 恢复验证

```bash
# 1. 就绪
curl -s http://localhost:8080/readyz

# 2. 一次真实写入 + 一次真实读取，并且校验一致
#    触发一次检测，然后对生成的证据签发短时 URL 并下载，比对 sha256
curl -s -X POST "http://localhost:8080/api/v1/evidence/<id>/signed-url" \
  -H "X-CSRF-Token: $CSRF" -b "$COOKIE_JAR"
# 下载到的字节的 SHA-256 必须等于 evidence.sha256
```

**只比「URL 返回 200」是不够的**：签名错误同样可能返回 200 而内容是错误对象
或错误分片（`docs/object-storage-rehearsal.md` 明确写了这条）。

```bash
# 3. 指标不再增长
curl -s http://localhost:8080/metrics | grep -E \
  'webmonitoring_evidence_writes_total|webmonitoring_evidence_failures_total'

# 4. 卷使用率回落并留出余量
docker system df -v | grep -i minio-data
```

## 升级/上报

- 证据的 SHA-256 校验不一致：可能是证据被篡改或损坏。**先停手、保留现场**，
  这属于安全事件。
- 需要从备份恢复 evidence bucket：`scripts/compose-restore.ps1 -Execute
  -ConfirmText RESTORE` 是**带审批的高风险操作**，会停止 API/Worker 并覆盖
  evidence bucket。需要审批、备份版本、RPO/RTO 与回滚方案。
- 需要在 dry-run 清不出空间的前提下扩容卷/迁移存储。
- 删除任何证据元数据行或对象（含 `retain_until IS NULL` 的清理）：需要授权
  与审计。
- 更换 `S3_*` 凭据或把存储迁到外部 S3：属于配置变更，且外部对象存储路径
  在本仓库中**尚未实机演练过**（`docs/object-storage-rehearsal.md` 状态为
  「尚未执行」），迁移前需要先补做该演练。
