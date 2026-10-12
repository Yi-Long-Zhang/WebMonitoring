# 运行手册：对象存储不可达 / 证据卷写满

**Q：我看到的什么现象指向这本手册？**

`/readyz` 里 `evidence` 报 `unavailable`；任务以 `EVIDENCE_WRITE_FAILED` /
`EVIDENCE_METADATA_FAILED` 失败并触发重试；证据下载返回 404
`EVIDENCE_MISSING`；或宿主的 `objectstore-data` 卷持续增长直至写满。

**Q：又要删数据又要改配置，先看哪一节？**

先看"先理解对象存储的位置"——排查前必须先确认 API 到底在跟对象存储还是本地
目录说话，否则所有后续判断都建立在错误前提上。

## 先理解对象存储的位置

Compose 里的 `objectstore` 是 **VersityGW**（`versity/versitygw`，`posix` 后端，
证据以普通文件落在卷里）。它**不发布任何宿主机端口**。API、http-worker 和备份
演练用的 `rclone` 客户端都在 Compose 网络内部通过 `http://objectstore:9000`
访问它。这有两个直接后果：

- 宿主机上**没有** `http://localhost:9000`，也没有任何管理控制台——VersityGW
  不提供控制台。要手工访问对象存储，必须 `exec` 进容器，或起一个带
  `profiles: ["tools"]` 的 `rclone` 服务。
- `S3_ENDPOINT` 指向哪里决定了 API 用哪套存储。留空则退化为本地目录
  `DATA_DIR/evidence`（`apps/api/main.go` 的 `buildEvidenceStore`），**此时
  `evidence` 检查项走的是本地磁盘，不是对象存储**。排查前先确认这一点。

服务端跑在**单账户模式**下（启动参数不含 `--iam-dir`），凭据来自
`ROOT_ACCESS_KEY` / `ROOT_SECRET_KEY`，由 `.env` 的 `S3_ACCESS_KEY` /
`S3_SECRET_KEY` 供给。**没有 IAM，因此不存在"每租户一个 S3 凭据"**——租户隔离
完全由应用层的 `tenant_id` 对象键前缀承担（见 AGENTS.md 的架构不变量）。

证据的元数据在 PostgreSQL 的 `evidence` 表
（`id, tenant_id, asset_id, job_id, kind, media_type, size_bytes, sha256,
object_key, created_at, retain_until`），**大体积原始证据在对象存储**。两者
必须一致：对象在、元数据不在 → 孤儿对象；元数据在、对象不在 → 下载 404。

## 判断

### 1. 是对象存储不可达，还是只是某个对象丢了

```bash
curl -s http://localhost:8080/readyz

# 进程与 healthcheck。VersityGW 没有专用健康端点（/health、/healthz 都是 404），
# 容器里的探针是读一次原始 HTTP 状态行：未认证的 GET / 返回 403，
# 而"有完整状态行"就证明监听进程在应答。该镜像没有 curl，探针用 nc 手写请求。
docker compose -f deployments/compose/docker-compose.yml ps objectstore
docker inspect --format '{{.State.Health.Status}} restarts={{.RestartCount}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q objectstore)
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 objectstore
```

`evidence` 不健康 → 处置 A。`/readyz` 健康但个别证据下载 404 → 处置 C
（对象与元数据不一致），范围完全不同。

### 2. 确认 API 到底在跟谁说话

```bash
# 只看 endpoint，不打印密钥
docker compose -f deployments/compose/docker-compose.yml config | grep -E 'S3_ENDPOINT|S3_BUCKET'

# 从对象存储容器内部确认它在应答（该镜像有 nc，没有 curl）
docker compose -f deployments/compose/docker-compose.yml exec -T objectstore \
  sh -c 'printf "GET / HTTP/1.0\r\n\r\n" | nc -w 2 127.0.0.1 9000 | head -n1'
# 期望看到一行 HTTP/1.1 403 Forbidden：403 是"服务在跑"的正常答复，
# 不是错误。拿不到任何状态行才是故障。
```

> **API 与 http-worker 的镜像是 distroless，没有 shell、没有 curl/wget**
> （`apps/api/Dockerfile`、`apps/http-worker/Dockerfile` 的最终阶段是
> `gcr.io/distroless/static-debian12:nonroot`）。所以「从 API 容器里 wget 一下
> 对象存储」这条路走不通，不要在这上面浪费时间：用 `rclone`（处置见下）或
> `objectstore` 容器自己验证连通性。

### 3. 桶是否存在、对象数是否合理

`rclone` 在 `profiles: ["tools"]` 下，`up` 不会启动它，用 `run --rm` 临时起。
凭据以**变量名**而非**值**的形式交给容器（`-e RCLONE_CONFIG_BACKUP_...`），
所以它们不出现在命令行参数里，任何能读 `/proc` 的东西都看不到——这是
`scripts/compose-backup.ps1` 的 `Invoke-Rclone` 用的同一套做法：

```bash
# remote 完全由环境变量描述，与备份脚本建立的是同一个 remote。
# FORCE_PATH_STYLE 是必需的：Compose 网络里没有 bucket.objectstore 这个
# 虚拟主机形式的 DNS 名。不要设 RCLONE_CONFIG_BACKUP_NO_CHECK_BUCKET——
# 见处置 A 的说明。
export RCLONE_CONFIG_BACKUP_TYPE=s3
export RCLONE_CONFIG_BACKUP_PROVIDER=Other
export RCLONE_CONFIG_BACKUP_ENDPOINT=http://objectstore:9000
export RCLONE_CONFIG_BACKUP_ACCESS_KEY_ID="$S3_ACCESS_KEY"
export RCLONE_CONFIG_BACKUP_SECRET_ACCESS_KEY="$S3_SECRET_KEY"
export RCLONE_CONFIG_BACKUP_FORCE_PATH_STYLE=true

rclone_in_compose() {
  docker compose -f deployments/compose/docker-compose.yml run --rm -T --no-deps \
    -e RCLONE_CONFIG_BACKUP_TYPE -e RCLONE_CONFIG_BACKUP_PROVIDER \
    -e RCLONE_CONFIG_BACKUP_ENDPOINT -e RCLONE_CONFIG_BACKUP_ACCESS_KEY_ID \
    -e RCLONE_CONFIG_BACKUP_SECRET_ACCESS_KEY -e RCLONE_CONFIG_BACKUP_FORCE_PATH_STYLE \
    rclone "$@"
}

rclone_in_compose lsd backup:              # 有哪些桶
rclone_in_compose size backup:evidence     # 对象数与总字节
rclone_in_compose lsjson backup:evidence   # 逐对象列出
```

（rclone 官方镜像的 entrypoint 就是 `rclone` 本身，所以子命令直接跟在
`run --rm ... rclone` 后面，不需要像 `mc` 时期那样套一层 `sh -c`。）

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
docker system df -v | grep -iE 'objectstore-data|postgres-data|redis-data'
docker compose -f deployments/compose/docker-compose.yml ps --format \
  'table {{.Service}}\t{{.Status}}'
```

对象存储的数据就是 `objectstore-data` 卷里的普通文件，需要时可以直接在宿主机上
用 `du` 复核，不必经过客户端容器：

```bash
docker run --rm -v compose_objectstore-data:/data alpine du -sh /data/evidence
```

> 卷名前缀是 Compose 项目名（默认取目录名，这里是 `compose`）；用
> `docker volume ls | grep objectstore` 确认真实名字。在 Git Bash 里跑这条要加
> `MSYS_NO_PATHCONV=1`，否则 MSYS 会把容器内的 `/data` 改写成
> `C:/Program Files/Git/data`，命令会以 `invalid volume specification` 或"目录不存在"
> 失败——**报错指向路径，不指向卷**，很容易误判成卷坏了。

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

### 处置 A：对象存储不可达

**【改变状态】** 先取证，再重启：

```bash
docker compose -f deployments/compose/docker-compose.yml logs --tail=300 objectstore
docker compose -f deployments/compose/docker-compose.yml restart objectstore
```

证据下载会 404；受影响的任务以 `EVIDENCE_WRITE_FAILED` 失败，
`EVIDENCE_WRITE_FAILED` 在 `isRetryable` 列表里，因此任务会**自动重试**
（最多 3 次，退避 `2^n × QUEUE_RETRY_BASE_MILLISECONDS`）。所以短时中断通常
会自愈，不需要人工重排。超过重试次数后任务进入终态，需要人工重跑检测——
重跑会重新抓取目标页面，属于对外部目标的动作。

起不来时优先检查：

- `S3_ACCESS_KEY` / `S3_SECRET_KEY` 与 API/worker 侧的取值是否一致。生产门禁
  会拒绝默认值 `webmonitoring` / `replace-with-a-random-secret`
  （`internal/config/production.go` 的 `validateCommon`）。
- `objectstore-data` 卷是否可写、宿主是否已满。VersityGW 用 `posix` 后端，
  卷不可写会直接体现为后端初始化失败（日志里是
  `failed to init posix backend`）。
- **不要设 `RCLONE_CONFIG_BACKUP_NO_CHECK_BUCKET`**（`--s3-no-check-bucket`）。
  它是省一次 HeadBucket 的优化，但代价是 `rclone mkdir` 退化成静默空操作——
  "目标桶不存在时先建桶"这条保证会无声消失，直到更晚的 `NoSuchBucket` 才暴露。
  备份/恢复脚本因此刻意不设它；手工排障时也别加。副作用是好的：备份指向一个
  不存在的桶会**直接报错退出**，而不是静默产出一个空备份。

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

# 2. 该对象在对象存储里是否存在（rclone_in_compose 的定义见"判断"第 3 节）
rclone_in_compose lsjson "backup:evidence/<object_key>"
```

> **看输出，不要看退出码。** rclone 把"对象不存在"当作**空结果**而不是错误：实测
> `lsjson`、`lsf`、`cat`、`size` 四条命令在对象不存在时**全部退出 0**，和对象存在时
> 一样。判据是输出——`lsjson` 返回 `[]` 即不存在，`cat` 无输出即不存在。
> （这与"桶不存在"不同：备份指向一个不存在的**桶**会 exit 3 报错退出，见处置 A。
> 区别在于桶是路径解析的一部分，对象不是。）

- 对象存在但下载失败：可能是对象内容损坏，用
  `rclone_in_compose cat "backup:evidence/<object_key>"` 取回后比对元数据里的
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
或错误分片（`docs/object-storage-rehearsal.md` 明确写了这条）。这条在
VersityGW 上已实测过一轮：一个 40 MiB 的对象经预签名 URL 取回的字节与写入时
逐字节一致，且服务端日志确认走的是分片上传（`POST ?uploads=` → 多个
`PUT ?partNumber=N` → `POST ?uploadId=`），不是单片 PUT。

```bash
# 3. 指标不再增长
curl -s -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep -E \
  'webmonitoring_evidence_writes_total|webmonitoring_evidence_failures_total'

# 4. 卷使用率回落并留出余量
docker system df -v | grep -i objectstore-data
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
- **更换 `S3_ACCESS_KEY` / `S3_SECRET_KEY`**：对象存储跑在单账户模式下，没有
  IAM，所以轮换没有灰度路径——它必然是一次「改 `.env` 并重启 `objectstore`、
  api、http-worker」的短暂停机。轮换前先备份，轮换后按上面的"恢复验证"跑一遍。
- 把存储迁到外部 S3：对象存储路径本身已在 Compose 上实测跑通（服务端换成
  VersityGW，见 `docs/adr/0007`），但**外部 S3 路径尚未实机演练过**
  （`docs/object-storage-rehearsal.md` 状态为「尚未执行」），迁移前需要先补做
  该演练。
