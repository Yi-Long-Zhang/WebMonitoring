# ADR-0007：对象存储与备份客户端的替换

状态：Accepted
日期：2026-10-12

## 背景

`deployments/compose/docker-compose.yml` 把对象存储钉在
`quay.io/minio/minio@sha256:14cea493…`，备份/恢复演练的客户端钉在
`quay.io/minio/mc@sha256:a7fе349e…`。2026-10-10 发现两者在公网都取不到了：quay.io
返回 `401 UNAUTHORIZED`，Docker Hub 的 `minio/minio` 与 `minio/mc` 仓库 API 返回
`{"message": "object not found"}`。上游 README 声明社区版此后仅以源码分发。

后果不是「少了一颗可选组件」，而是**这一整套栈在干净机器上起不来**：
`docker compose up` 停在 `minio Error unauthorized`，于是 `compose-smoke` 与
`multi-replica-drill` 两个演练作业一次都执行不了。而这两个脚本是投产前唯一的端到端
验证手段，也是备份/恢复演练的前提。这是一个投产阻塞项。

`internal/evidence/s3.go` 用的是 `github.com/minio/minio-go/v7`，那是**客户端库**，与
服务端镜像的下架无关，本决策不涉及它，也不涉及 `go.mod`。

## 选项

只列做过实测评估的三个。评估方法统一：在真实容器上跑仓库自己的代码路径，不看文档宣称。

| 选项 | 结论 | 理由 |
| --- | --- | --- |
| 自建冻结的 MinIO 二进制 | 否决 | 对安全产品是净降级。上游已停止发版，等于把一个**不会再修的安全组件**钉进部署。 |
| Garage v2.4.1 | 否决 | 运行时镜像 `FROM scratch`（无 shell、无 curl、无 nc），且实例可用前必须手工执行 `garage layout assign` + `layout apply --version 1`，没有配置文件替代。这跟「干净机器上一条 `docker compose up`」的验收前提直接冲突。 |
| **VersityGW v1.8.0** | **采纳** | Apache-2.0，运行时是 `alpine`（有 busybox 的 `sh`/`nc`/`head`/`grep`），`posix` 后端把证据存成卷里的普通文件，启动无需引导步骤。 |

客户端同理：`mc` 随 MinIO 一起下架，替代品选 **rclone 1.75.2**（官方镜像同样是
`alpine`，`RCLONE_CONFIG_<REMOTE>_*` 环境变量可以完全描述一个 remote，无需写配置文件）。

## 决策

1. **服务端**：`versity/versitygw:v1.8.0`，按 digest 钉死。`posix` 后端，
   命令行 `--port :9000 --region us-east-1 posix /data`。
2. **单账户模式**：不给 `--iam-dir`。VersityGW 这时从 `ROOT_ACCESS_KEY` /
   `ROOT_SECRET_KEY` 取凭据，而这两个仍由既有的 `${S3_ACCESS_KEY}` /
   `${S3_SECRET_KEY}` 供给——`.env` 契约不变，运维不需要学新变量名。
3. **客户端**：`rclone/rclone:1.75.2`，按 digest 钉死。凭据仍以**变量名**而非**值**
   的形式交给容器（`docker compose run -e NAME`），与 `mc` 时期的
   `-e MC_HOST_backup` 同样是"不进 argv、不出现在进程列表"。
4. **服务改名** `minio` → `objectstore`。留着旧名会让「这个栈依赖某个具体厂商」的
   假设继续传播，而它本来只是个 S3 端点。
5. **healthcheck 换成原始 HTTP 状态行**（见下）。

## 后果

### 收益

- 干净机器上 `docker compose up` 重新可用，两个此前从未执行过的 CI 演练作业因此才
  跑得起来。
- 证据以普通文件落在 `objectstore-data` 卷里，可以直接用文件系统工具查看与计容，
  不必再起一个客户端容器。
- 备份工具从"随厂商下架"变成一个有独立维护节奏的项目。

### 故障模式

- **单进程、单后端**。VersityGW 挂掉时 `/readyz` 的 evidence 项转红，证据写入以
  `EVIDENCE_WRITE_FAILED` 失败。这与 MinIO 时期一致，不新增失败模式。
- **无 IAM，因此无"每租户一个 S3 凭据"**。租户隔离完全由应用层的 `tenant_id` 前缀
  承担——这本来就是架构不变量（见 AGENTS.md），存储层从来没有参与过隔离。
  推论：**密钥轮换只能是"改 `ROOT_*` 并重启两个 worker"**，没有多用户模型可做灰度
  发布。轮换必须在这个前提下降级为一次短暂停机。
- **不开 bucket versioning**。「原始证据不可覆盖」仍由应用层的追加语义承担，不靠
  存储层。这个不变量在 MinIO 时期也不是靠 versioning 实现的，但值得显式记录，
  因为换存储时它很容易被误认为"新存储能兜底"。

### 与 MinIO 的语义差异（逐条实测，非推测）

- **`mc mirror --overwrite` ≙ `rclone copy --ignore-times`**。`--ignore-times` 不是
  风格选择：rclone 默认按"大小 + mtime 相同即跳过"启发式，实测中一个同大小、同 mtime、
  内容不同的对象确实被跳过了，加上该标志后才覆盖。
- **`mc mirror --overwrite --remove` ≙ `rclone sync --ignore-times`**。实测 `sync`
  会删掉桶里多出来的对象，同时保留其余对象。
- **`mc mb --ignore-existing` ≙ `rclone mkdir`——前提是不带 `--s3-no-check-bucket`**。
  该标志会让 `mkdir` 退化成静默空操作，于是"目标桶不存在时先建桶"这条保证会无声消失，
  直到更晚的 `NoSuchBucket` 才暴露。因此脚本**不设** `NO_CHECK_BUCKET`。
- **备份指向不存在的桶时，rclone 报错退出（exit 3），而不再静默产出空备份**。这比
  `mc` 时期更严，正是备份工具应有的方向。
- **healthcheck**：VersityGW 没有专用健康端点（`/health`、`/healthz` 均 404），未认证的
  `GET /` 返回 403。因此探针改为读原始状态行
  （`printf "GET / HTTP/1.0\r\n\r\n" | nc -w 2 127.0.0.1 9000 | head -n1 | grep -qE "HTTP/1\.[01] [234]"`）。
  两个细节是刻意的：该镜像没有 `curl`；且要求"有 HTTP 应答"而非仅 `nc -z` 的端口可连，
  拒绝 5xx——因此不弱于它替换掉的 `curl -f`。

### 回滚

应用只说 S3，所以回滚不是"退回 MinIO"（镜像已经拿不到，那条路是断的），而是
**把 `S3_ENDPOINT` 指向另一个 S3 兼容存储，并用 rclone 迁移数据**。仓库自带的
`scripts/compose-backup.ps1` / `compose-restore.ps1` 就是这个迁移工具，无需新写。

## 验证

全部在真实容器上、走仓库自己的代码路径（`internal/evidence`），不留推测项：

- `NewS3Store`（含**新建桶**的全新安装路径）、`Ready`（HeadBucket）、`Put`
  （PutObject + Content-MD5）、`Open`（GetObject + Stat）、`Delete`——`Region` 留空
  （生产的实际设置，走 `GetBucketLocation`）与显式 `us-east-1` 两种都通过。
- **预签名 URL 真的把对象字节取回来了**（不是"URL 长得像"）。
- **分片上传**：40 MiB 载荷逐字节往返一致。服务器端访问日志确认走的是真分片路径
  （`POST ?uploads=` → 三次 `PUT ?partNumber=N` → `POST ?uploadId=`），不是单片 PUT——
  否则这条对分片什么也没证明。
- healthcheck 的**红绿两侧**都在真实容器上验过：健康时 exit 0，服务停止后 exit 1。
- rclone 的四条语义等价值，每条都带对照实验（见"语义差异"）。
- 端到端：`compose-smoke`、备份/恢复演练结果记入 [rehearsals.md](../rehearsals.md)。

## 迁移方式

已有 MinIO 部署的数据迁移：

1. 旧栈仍在运行时，用 `scripts/compose-backup.ps1` 备份（它会带上 manifest 与逐文件
   SHA-256）。
2. 换到新栈，用 `scripts/compose-restore.ps1` 恢复；它对每个证据文件重算 SHA-256 并与
   manifest 比对，不匹配即失败。
3. 对象存储不在本 Compose 项目内时，用 `S3_INTERNAL_ENDPOINT` 指定可达端点
   （默认 `objectstore:9000`）。

回滚见上一节。两条路径都不需要新的工具。
