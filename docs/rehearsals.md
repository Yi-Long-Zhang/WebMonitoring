# 验收演练总账

本文是全部实测结果的**唯一事实来源**（append-only）。README 和 docs/README 的
实测速览只保留一行摘要并指向这里；`architecture.md` 与 `operations.md` 内嵌的
实测输出是设计论证证据，原地保留。

**追加约定**：每次演练后在下方新增一节，按时间倒序（最新在最前），不修改、
不删除历史小节。每节固定字段：日期、脚本、关键断言、结果、专项档案（如有）。
数字以脚本实际输出为准，不四舍五入、不补估算值。

---

## 发布链路接通：两处静默缺陷与发布规则对照 — 2026-10-12

- **脚本**：本节**不是**一次演练通过，因此没有脚本成绩可记。材料是
  `.github/workflows/release.yml`（**尚未真实运行**）、本机 `docker build` /
  `docker create` / `docker cp` / `grep -qaF`，判据来自 `docs/operations.md`「发版」一节
  自身与 `docs/engineering.md` §8。
- **关键断言**：读配置能发现的缺陷与跑一遍才能发现的缺陷是**两类**。本节两处都属于
  前者，而且都属于同一条纪律——**「门禁红了」与「门禁根本没跑」是两种状态**，后者更
  隐蔽：这两处都不会因为「功能不对」而变红（第 1 处**红了，但红在上游一层**；第 2 处
  按最坏情况也不会红在推镜像那一步）。

### 1. `migrate` 与 `rewrap` 的镜像从 `7ba1839` 起根本构建不出来

- **现象**：`main` 上的 `7ba1839`，run `38212063384`，四个作业里两个红：

| 作业 | 结论 | 起止（UTC） |
| --- | --- | --- |
| `backend` | success | 06:20:43 → 06:23:13 |
| `node` | success | 06:20:43 → 06:21:47 |
| `compose-smoke` | **failure** | 06:23:16 → 06:24:41 |
| `multi-replica-drill` | **failure** | 06:23:16 → 06:23:50 |

- **根因（具体行，不猜）**：`compose-smoke` 作业日志第 1037 行原文——

  ```
  apps/migrate/main.go:12:2: package webmonitoring/internal/version is not in std
  (/usr/local/go/src/webmonitoring/internal/version)
  ```

  失败步骤是 `[migrate build 8/8] RUN CGO_ENABLED=0 go build … ./apps/migrate`。
  `apps/migrate/Dockerfile` 在 `7ba1839` 时只有三行 `COPY`：`apps/migrate`、
  `db/migrations`、`internal/migrate`——**这种窄式 COPY 列表就是该二进制的构建输入
  集合**（`go build` 只看得见被 COPY 进来的东西，这也是本仓库两种 Dockerfile 写法的
  分野）。同一次改动让 `apps/migrate/main.go:12` 开始 import `internal/version`，
  却没有把该包加进列表。`api` 与 `http-worker` 用的是 `COPY internal ./internal`，
  所以同一处缺陷在它们身上不出现。
- **它为什么没在上一轮暴露**：上一轮的本机构建只覆盖了 `http-worker` ——**恰好是两种
  写法里不会出事的那一种**。缺陷因此从本机溜过去，直到干净 runner 上才显形。
- **`rewrap` 是同一处缺陷的同一形态，但 CI 上没有观察到**：`apps/rewrap/main.go:32`
  同样 import 了 `internal/version`，它的 COPY 列表同样没有这一行。CI 上 `migrate`
  先失败，`rewrap` 根本没轮到。**这一条是读代码判定的，不是观察到的**——修法与验证
  一并做过，但「CI 上 rewrap 曾经红过」不是事实。
- **修法**：两个 Dockerfile 各加一行 `COPY internal/version ./internal/version`
  （现为 `apps/migrate/Dockerfile:25`、`apps/rewrap/Dockerfile:32`），并在注释里写明
  规则：**窄式 COPY 列表必须覆盖 `main.go` 的每一个 `internal/*` 依赖**。
- **验证（两条，都通过）**：
  - **CI**：`3c3f44b`，run `38212963700`，四个作业全 success——原先红的两个都转绿
    （`compose-smoke` 06:34:15 → 06:38:26，`multi-replica-drill` 06:34:17 → 06:37:24）。
  - **本机**：用真实值构建**四个** Go 镜像（`api`/`http-worker`/`migrate`/`rewrap`），
    逐个 `docker create` → `docker cp` 取出二进制，`grep -qaF` 版本号、完整 commit SHA、
    构建时间三个串**都能找到**；`migrate`/`rewrap`/`http-worker` 的启动首行确实打印该
    标识。镜像都是 distroless（没有 shell），所以「取出来读」是唯一可靠的读法；
    commit SHA 是其中最硬的一条：`.dockerignore` 排除了 `.git`，`-X` 是这些字节唯一
    的来源。
  - 顺带结掉 1.5 与 2.6 里那条「没有实测过带真实版本号的镜像构建」（见下方两处回填）。

### 2. `release.yml` 里有四处拼出了一个 docker 不接受的名字

- **现象**：从未运行过，是读配置读出来的。
- **判定（离线，一行命令就够了）**：

  ```
  docker create ghcr.io/Yi-Long-Zhang/WebMonitoring/api@sha256:000…0
  → invalid reference format: repository name (Yi-Long-Zhang/WebMonitoring/api@sha256)
    must be lowercase
  ```

  同一串改成全小写则能通过解析（随后在注册表侧被拒，说明「解析」这一关过了）。仓库
  owner 是混合大小写，而工作流四处直接拼 `${{ github.repository }}`。
- **为什么必须在上路前修**：按最坏情况，即使 `docker/metadata-action` 自己会把名字转
  小写、**推镜像那一步照旧成功**，红的是它下游两步（「验证产物」与 `cosign sign`）
  ——而那时镜像**已经推到公开注册表，收不回来**。这不是「等第一次跑报错再修」的场景。
- **修法**：新增一个 step，用 bash 的 `${GITHUB_REPOSITORY,,}` 生成一次镜像名（GitHub
  表达式没有小写函数），Image metadata、验证产物、cosign 签名、Trivy 扫描四处共用同一个
  output；发布标识（版本 / commit / 提交时间）由同一个 step 一并产出，`BUILD_TIME` 取
  **commit 时间**而非 `date`（六个并行作业各取 `date` 会给同一次发布盖上六个时间戳）。
- **验证**：该 step 的两个分支（`BINARY` 有值 / 无值）在本机以真实环境变量跑过一遍，
  确认小写化与 `GITHUB_OUTPUT` 的多行 heredoc 输出正确；CI `824c3b2`，run
  `38213618721`，四个作业全 success。**这次绿证明的是「没碰坏别的」，不是「发布链路
  已验证」**——本次改动只碰 `release.yml`，而 `ci.yml` 根本不跑它。

### 3. `docs/engineering.md` §8 发布规则的对照（自检）

§8 要求一次发布具备六项材料。逐条找仓库里现有的材料，并标明它**是否随发布物走**：

| §8 要求 | 仓库里的材料 | 随发布物走？ |
| --- | --- | --- |
| 明确版本或构建标识 | `internal/version` + `GET /api/v1/version`（`operations.md:624`）+ 控制台总览页 + 四个 Go 镜像的构建参数与启动日志 | **本次接上一半**：`release.yml` 从 tag/commit/提交时间注入，并在推送前 grep 二进制确认；尚未真实运行 |
| 数据库迁移顺序和回滚策略 | `db/migrations/`；`migrate up/status/baseline`；`operations.md:580`「升级失败怎么办」（只前进不回滚）与总账 0.6 | ❌ 没有任何载体挂在 tag 上 |
| 配置和密钥变更清单 | `.env.example`；`operations.md:783`「密钥轮换（`ALERT_SECRET_KEYS`）」 | ❌ |
| 关键健康检查 | `GET /healthz`、`GET /readyz`（`operations.md:619-620`，`/readyz` 查 PG / Redis / 对象存储 / Browser Worker） | 部分：镜像自带 `HEALTHCHECK` |
| 监控和告警确认 | `GET /metrics` 与 `operations.md` 的运行验收一节；`docs/runbooks/alert-delivery.md` | ❌ |
| 失败后的停止和回滚方案 | `operations.md:580`；`docs/backup-restore-rehearsal.md` | ❌ |

**结论**：六项材料在仓库里都能找到，但**没有一项挂在发布物上**——没有 GitHub Release、
没有 release notes、没有可下载产物（工作流是 `permissions: contents: read`，也没有
release 步骤）。「这个 tag 对应哪几步迁移、哪几处配置改动、怎么回滚」目前只能靠 tag
指向的 commit 去人肉回溯。**登记在案，本轮不做。**

### 未覆盖

- **发布工作流本身一次都没有真实运行过**：`git tag` 一直是空的，
  `on: push: tags: ['v*']` 从未匹配；六个镜像的构建与推送、`cosign` 无密钥签名、Trivy
  扫描、SARIF 上传**全部是纸面**。本节两条验证覆盖的是「构建这两个二进制的 Dockerfile」
  与「解析镜像名的那段 bash」，**不是工作流整体**。
- **`make` 在开发机上没有安装**：文档里的 `make release-check` 本轮用等价的
  `git describe --tags --exact-match` 跑（`Makefile:59-60` 就是这个命令）。命令能跑通
  不等于「照 Makefile 跑过」。
- **只出 amd64**：工作流没有 `platforms:`，也没有 QEMU。
- **action 的 pin 是浮动 tag**（`actions/checkout@v4`、`aquasecurity/trivy-action@0.28.0`
  等），而本仓库对对象存储镜像按 digest 钉死（ADR 0007）——两套标准不一致。
- **GHCR 首次创建的 package 默认私有**，需要在包设置里改成 public，未登录的机器才拉得
  到；这是网页操作，工作流代劳不了。
- **发布产物起栈的 overlay 不存在**：`docker-compose.yml` 默认本地构建，所以「本地
  `docker compose up` 跑通」目前仍不等于「发布产物跑通」。

### 回填

- **1.5「版本与构建标识」的未覆盖**（原「没有实测过带真实版本号的镜像构建……release
  流程也未接上这些构建参数」）：前半句 2026-10-12 已实测（四个 Go 镜像、真实值、读二进制
  确认三个串都在）；后半句已修（构建参数接进 `release.yml` 与本地 compose）。**仍未
  实测的是「按 tag 发布的产物」**。
- **2.6 的未覆盖**（原「`APP_VERSION` 等构建参数仍未实测注入真实版本号」）：同上。

---

## CI 首次全绿：两个演练作业第一次真正执行完 — 2026-10-12

- **脚本**：`.github/workflows/ci.yml`（推送触发，非手动）。
- **触发**：推送 `codex/object-store-swap`，SHA `ab18f43`，run `38206727165`。
- **关键断言**：四个作业全部 `success`，其中 `compose-smoke` 与 `multi-replica-drill`
  是**本仓库历史上第一次真正执行到底**——此前它们只有两类结局：从未被触发，或启动后
  因拉镜像失败在 32s 内退出。
- **结果**：

| 作业 | 结论 | 起止（UTC） | 日志里的关键输出 |
| --- | --- | --- | --- |
| `backend` | success | 05:09:35 → 05:11:55 | — |
| `node` | success | 05:09:35 → 05:11:44 | — |
| `compose-smoke` | success | 05:11:58 → 05:16:21 | `Compose smoke test passed` |
| `multi-replica-drill` | success | 05:11:59 → 05:15:08 | `replicas: api=2 http-worker=3`、`verified+enabled: 40`、`detection jobs : 40 (expected 40)`、`duplicate groups : 0`、`succeeded : 40`、`wall clock : 3.6s`、`Multi-replica drill passed` |

### 这一节结掉了什么

- **roadmap 2.8 的判据 1 与判据 5**：两者本来就是同一个实验的两面——GitHub runner 正是
  那台「没有镜像缓存」的机器。两个作业的日志里都有 `objectstore Pulling` →
  `objectstore Pulled`，即**在无缓存的干净机器上确实从公开注册表拉到了镜像**。在此之前，
  本仓库全部 Compose 通关记录都跑在本机 13 个月前的镜像缓存上。
- **「CI 门禁自身」整条未实测项**（`roadmap.md`「从未实测的场景」）：两个演练作业内的
  断言终于被真实执行过一次，不再是「一条都没验证过」。
- **「本地演练的可复现性」**：镜像换成公开可拉取的 VersityGW + rclone 之后，本机缓存
  不再是被依赖的前提。

### 顺带得到的一条新观察

`multi-replica-drill` 的 wall clock 是 **3.6s**。历史序列：2026-09-20 三次落在
234.5–244.1s（记为「已复现但根因未查清」，预算放宽到 900s）、2026-10-10 复跑 8.2s、
本机本轮 28.4s、CI 本轮 3.6s。平台期已连续三次没有出现，**「240s 是常态」这个印象可以
彻底否掉**；但这仍不足以说根因已消失，仍按间歇现象对待。

### 未覆盖

- **外部 S3 路径仍未演练**：CI 上跑的仍是 Compose 里的 VersityGW，
  `docs/object-storage-rehearsal.md` 状态保持「尚未执行」。在 VersityGW 上成立不等于在
  AWS S3 上成立。
- **60 分钟的挂起保护仍是未实测代码**：本次作业正常运行并在 3 分钟内结束，没有进入挂起
  路径，因此它只是被确认「不会误触」，仍未验证过它真能中止一次挂起。
- **`-race` 之外**：本节的结论只覆盖上面四个作业；CI 的其它能力（覆盖率下限、lint）不在
  本轮结论范围，它们此前已有独立记录。

---

## 对象存储镜像下架与替换（MinIO → VersityGW + rclone）— 2026-10-12

- **脚本**：`scripts/compose-smoke.ps1`（`-KeepRunning`）、`scripts/compose-backup.ps1`、
  `scripts/compose-restore.ps1 -Execute -ConfirmText RESTORE`、`scripts/multi-replica-drill.ps1`；
  另有一组**一次性容器探针**直接驱动 `internal/evidence`（跑完即删，未入库）。CI 结论来自
  GitHub check-runs API（只读凭据）。
- **关键断言**：(1) 换存储必须用**仓库自己的代码路径**证明，「新服务能起来」不算；
  (2) 「同名子命令」不等于「同语义」——每条 `mc` → `rclone` 的等价关系都要有**对照实验**；
  (3) 判据「无缓存机器」不能用「本机跑通」代替，而这恰好是这条缺陷藏了 13 个月的原因。
- **专项档案**：[adr/0007-object-store-replacement.md](adr/0007-object-store-replacement.md)、
  `docs/runbooks/object-storage.md`（整篇重写）、`docs/operations.md` 的「镜像准备」、
  `docs/roadmap.md` 的 2.8。

### 1. 起点：main 上的合并提交红在同一处

上一轮（`codex/ci-gate-repair`）合并为 `3f4e09b` 之后，main 上这次运行的四个作业：

| 作业 | 结论 | 起止（UTC） | 时长 |
| --- | --- | --- | --- |
| `backend` | success | 03:31:17 → 03:33:46 | 149s |
| `node` | success | 03:31:17 → 03:32:31 | 74s |
| `compose-smoke` | **failure** | 03:33:49 → 03:34:37 | 48s |
| `multi-replica-drill` | **failure** | 03:33:50 → 03:33:58 | 8s |

对照上一轮分支 SHA `ffbd2e1` 的 run `38198384564`（backend 03:05:32→03:07:58、
node 03:05:32→03:06:36、compose-smoke 03:08:04→03:08:36、multi-replica-drill
03:08:05→03:08:16）：`node` 的修复在 main 上保持有效，而两个演练作业**从 `skipped`
变成了真实的 `failure`**——这既是那轮修复的成果（它们终于被执行了），也正是本轮要清掉的
阻塞：它们失败的原因不是脚本，是**镜像已经不存在**。

### 2. 探针直接驱动 `internal/evidence`，而不是「新服务能起」

在真实容器上跑仓库自己的代码路径，`Region` 留空（生产实际设置，走 `GetBucketLocation`）
与显式 `us-east-1` **两种都过**：

| 代码路径 | 结果 |
| --- | --- |
| `NewS3Store`（含在空存储上 `MakeBucket`） | 通过 |
| `Ready`（`HeadBucket`，`/readyz` 用它） | 通过 |
| `Put`（`PutObject` + `Content-MD5`） | 通过 |
| `Open`（`GetObject` + `Stat`） | 通过 |
| `Delete`（`RemoveObject`） | 通过 |
| `PresignedGetURL` | **真的把对象字节取回来了**，不是「URL 长得像」 |

**分片上传单独验过**：40 MiB 载荷经 `GetObject` 与预签名 URL 两条路取回，逐字节一致；
服务端访问日志确认走的是真分片路径（`POST ?uploads=` → 三次 `PUT ?partNumber=N` →
`POST ?uploadId=`），不是单片 PUT——否则这条对分片什么也没证明。

**healthcheck 红绿两侧都在真实容器上验过**：健康时探针 exit 0，把服务停掉后 exit 1。
（探针是 `printf "GET / HTTP/1.0\r\n\r\n" | nc -w 2 127.0.0.1 9000 | head -n1 | grep -qE
"HTTP/1\.[01] [234]"`：VersityGW 没有专用健康端点，未认证的 `GET /` 返回 403，所以判据是
「有完整的 HTTP 状态行且不是 5xx」，不弱于它替换掉的 `curl -f`。）

### 3. 四条 `mc` → `rclone` 语义等价值，每条都带对照实验

不看文档宣称，做对照：

| 原写法 | 新写法 | 对照实验 |
| --- | --- | --- |
| `mc mirror --overwrite` | `rclone copy --ignore-times` | 造一个**同大小、同 mtime、内容不同**的对象：默认 `copy` 跳过了它（桶里仍是旧内容），加 `--ignore-times` 才覆盖——所以这个标志不是风格选择 |
| `mc mirror --overwrite --remove` | `rclone sync --ignore-times` | `sync` 删掉了桶里多出来的对象，同时保留了其余对象 |
| `mc mb --ignore-existing` | `rclone mkdir`（**不设** `--s3-no-check-bucket`） | 设上该标志后 `mkdir` 退化成**静默空操作**，「目标桶不存在时先建桶」这条保证会无声消失 |
| （无对应） | — | 备份指向不存在的桶时 rclone **exit 3 报错退出**，而不是静默产出空备份；这比 `mc` 时期更严 |

**凭据不出现在 argv**：传给容器的是**变量名**（`-e RCLONE_CONFIG_BACKUP_...`）而不是值，
用正则把容器 argv 与密钥本身比对过，确认查不到。

### 4. 端到端：四条演练全部退出码 0

| 演练 | 结论（脚本实际输出） |
| --- | --- |
| `compose-smoke.ps1 -KeepRunning` | `Compose smoke test passed`，退出码 0 |
| `compose-backup.ps1` | `Backup created at .data-backups\20261012-121012`，退出码 0 |
| `compose-restore.ps1 -Execute -ConfirmText RESTORE` | `Restore verification passed: ready, evidence SHA-256, job states, detection pending=0, alert pending=0`，退出码 0 |
| `multi-replica-drill.ps1` | `Multi-replica drill passed`，退出码 0 |

`multi-replica-drill` 的完整数字（本次新跑，用于取得未被 `tail` 截断的输出）：

```
replicas: api=2 http-worker=3
verified+enabled: 40
  assets            : 40
  detection jobs    : 40 (expected 40)
  duplicate groups  : 0
  succeeded         : 40
  wall clock        : 28.4s
  worker consumers  : 8bfd02ae0f72-
0fbd9ae909e3-
972e79a5d967-
```

备份产物的 manifest 经逐条核对：`files` 数组 **19 条**——17 个证据对象（`evidence/` 前缀）
+ `webmonitoring.dump` + `redis/dump.rdb`，每条带 SHA-256；对象树里另有 `manifest.json`
本身，目录下共 20 个文件。

### 5. 选型记录（为什么不是另外两个）

| 选项 | 结论 | 依据 |
| --- | --- | --- |
| 自建冻结的 MinIO 二进制 | 否决 | 对安全产品是净降级：上游已停发版，等于钉进一个**不会再修的安全组件** |
| Garage v2.4.1 | 否决 | 运行时镜像 `FROM scratch`（无 shell / 无 curl / 无 nc），且实例可用前必须手工 `garage layout assign` + `layout apply` |
| **VersityGW v1.8.0** | **采纳** | Apache-2.0；运行时 `alpine`（busybox 的 `sh`/`nc`/`head`/`grep`）；`posix` 后端把证据存成卷里的普通文件；启动无需引导步骤 |

### 6. 运行手册与运维文档里的命令原样跑过一遍

文档里的命令必须实跑，不能只验证「改动生效」。为此用**基础 compose 文件**另起一套栈
（`docker compose --env-file deployments/compose/.env -f
deployments/compose/docker-compose.yml up -d --build postgres redis objectstore migrate api
http-worker`，项目名 `compose`，即手册假定的那套拓扑），把
`docs/runbooks/object-storage.md` 与 `docs/operations.md` 新增或改写的命令块**逐条原样执行**：

| 命令 | 结果 |
| --- | --- |
| `curl -s http://localhost:8080/readyz` | `{"status":"ready"}` |
| `docker compose … exec -T objectstore sh -c 'printf "GET / HTTP/1.0\r\n\r\n" \| nc -w 2 127.0.0.1 9000 \| head -n1'` | `HTTP/1.1 403 Forbidden`（与手册所述一致） |
| `docker inspect --format '{{.State.Health.Status}} restarts={{.RestartCount}}' …` | `healthy restarts=0` |
| `docker compose … config \| grep -E 'S3_ENDPOINT\|S3_BUCKET'` | `S3_ENDPOINT: http://objectstore:9000`、`S3_BUCKET: evidence` |
| `rclone_in_compose lsd backup:` | 列出桶 `evidence` |
| `rclone_in_compose size backup:evidence` | `Total objects: 1` |
| `rclone_in_compose lsjson backup:evidence` | 逐对象 JSON |
| `rclone_in_compose cat "backup:evidence/<key>"` | 取回对象内容 |
| `docker run --rm -v compose_objectstore-data:/data alpine du -sh /data/evidence` | 返回目录体积（Git Bash 下需 `MSYS_NO_PATHCONV=1`） |
| `docker system df -v \| grep -iE 'objectstore-data\|postgres-data\|redis-data'` | 列出卷 |
| `docker compose … ps --format 'table {{.Service}}\t{{.Status}}'` | 六个服务全部 `healthy` |
| `operations.md` 里 `rclone copy --ignore-times backup:evidence <目录>` 的完整展开式 | 对象被复制到本地目录，退出码 0 |
| 两条 `psql` 计数查询（`evidence` 行数/逻辑体积、`retain_until IS NULL` 分组） | 均返回结果 |

**由此改掉的一处手册错误**：`rclone` 把「对象不存在」当作**空结果**而不是错误——实测
`lsjson`、`lsf`、`cat`、`size` 四条命令在对象不存在时**全部退出 0**，与对象存在时一模一样。
原处置 C 让操作员「用 `lsjson` 判断对象是否存在」，若按退出码判断会一律得出「存在」。
手册已改为**看输出**判别（`lsjson` 返回 `[]`、`cat` 无输出即不存在），并写明这与「桶不存在」
（exit 3）的区别。

### 未覆盖

- **判据 1 与 5 未做** —— 写本节时这两个演练作业**一次都没有成功执行过**（当时该分支尚未
  推送）。CI 的 runner 正是那台「没有镜像缓存」的机器，所以判据 1 与 5 是同一个实验。
  → **同日稍后已结清**：分支推送后 run `38206727165` 四门全绿，两个演练作业首次执行到底，
  日志里有 `objectstore Pulling` → `objectstore Pulled`。见本文最前「CI 首次全绿」。
- **本机不是干净机器**：本机 daemon 里仍有 13 个月前那两份 MinIO 缓存，所以**本机**的通关
  记录不能单独充当判据 5。→ 干净机器上的那一半**已由 CI 完成**（同上）；本节能说的是
  「栈已经不再依赖本机缓存」（compose 文件里的镜像全部来自公开 registry 且按 digest 钉死），
  这句话在 CI 上得到了验证。
- **外部 S3 路径仍未演练**：`docs/object-storage-rehearsal.md` 状态保持「尚未执行」。
  在 VersityGW 上成立不等于在 AWS S3 上成立。
- **备份/恢复脚本只吃基础 compose 文件**（既有现象，本轮未修）：两者都只加
  `-f deployments/compose/docker-compose.yml`，没有传 overlay 的入口。用
  `-ProjectName webmonitoring-smoke` 对着 `compose-smoke -KeepRunning` 留下的栈跑，
  重建出来的 `api` 落在 **8080**（基础文件的端口）而不是 smoke overlay 的 18080，
  本节第 4 节的恢复演练因此是用 `API_BASE_URL=http://localhost:8080` 跑完的；断言本身
  照常执行并通过。同时会在项目里多出一个空的 `webmonitoring-smoke_objectstore-data` 卷。
  **这条与 `mc` → `rclone` 的替换无关，是既有的脚本/overlay 缺口**，修它需要给两个脚本
  加一个 overlay 参数，属于另一项改动。
- **轮换密钥的停机时间未在真实数据上验**：单账户模式没有 IAM，轮换必然是「改 `.env` +
  重启 objectstore/api/http-worker」的一次短停机，路径只在一次性容器上验过。

---

## CI 门禁首次诚实运行与对象存储镜像下架 — 2026-10-12

- **脚本**：`.github/workflows/ci.yml`（run `38198384564`，SHA
  `ffbd2e192b630deabf8420a0d7613e994f0f79ba`，分支 `codex/ci-gate-repair`，attempt 1）；
  用只读凭据拉作业结论与逐 job 日志；`docker pull`；quay.io 与 Docker Hub 的注册表 API 直查。
- **关键断言**：(1) 门禁「红了」与门禁「根本没跑」是两种状态，后者在 UI 上不比成功刺眼；
  (2) 本机演练通过 ≠ 依赖仍可获取；(3) 数字以 CI 的实际输出为准，不按机制推断。
- **专项档案**：`docs/operations.md`「镜像准备」一节、`docs/roadmap.md` 的 2.8。

### 1. 推送前：32 次运行、32 次 `failure`，而两个作业一次都没执行过

逐次拉取全部 32 次运行的作业结论（**不是在 `needs` 机制上推断**）：

| 作业 | 结论分布 |
| --- | --- |
| `backend` | `success` **30** / `failure` **2** |
| `node` | `failure` **31** / `success` **1**（即本次） |
| `compose-smoke` | `skipped` **30** / `failure` **1**（即本次） |
| `multi-replica-drill` | `skipped` **28** / `failure` **1**（即本次） |

（两个作业的作业记录条数少于运行数 32——`compose-smoke` 31 条、`multi-replica-drill` 29 条，
说明更早的运行里它们还不在这份工作流里；**没有逐次定位是哪些运行**。）

**结论**：本次之前，`compose-smoke` 与 `multi-replica-drill` **从未执行过一次**——它们的状态是
`skipped`，而不是红。这两个作业里的检查代码从未在 CI 上运行过，尽管路线图一直把它们当作
「推送后由 GitHub 验证」的待办。

### 2. 这更正了 2026-10-10 那节写下的推论

「SSE 事件流租户隔离与跨进程投递」一节（写于 2026-10-10）的 `### 未覆盖` 里有一条明标为
**推论（非实测）**的判断，原文两句话都不成立（按小节名定位，不引行号——追加会让行号漂移）：

| 推论原文 | 实测 |
| --- | --- |
| 「CI 的 `node` job 跑的是带 `test/` 的完整检出，所以它是绿的」 | `node` 31 次里红了 31 次（见第 3 节根因） |
| 「`compose-smoke` job 会构建镜像，因此它在 main 上**大概率一直是红的**」 | 它不是红的，是 `skipped`：30 次跳过、0 次执行 |

这正是 append-only 的用处：错误的推论与它的更正在同一份文件里并排存着。历史小节不改写。

### 3. `node` 的根因与修复（红 → 绿，同日证据）

**根因**（既有缺陷，与阶段一无关）：`apps/browser-worker/test/` 有 **4 个测试真的启动
Chromium**（`login.test.ts:73/95/118` 三个 + `security.test.ts:68` 一个），而 `node` 作业只装
`playwright` 这个 **npm 包**、从不下载浏览器二进制——全仓只有 `compose-smoke` 作业里有
`playwright install`。失败签名完全一致，3 秒内结束（而修好之后 `pnpm test` 这一步实测跑了
15 秒，所以那个时长本身就在说明它没跑到断言）：

```
browserType.launch: Executable doesn't exist at
/home/runner/.cache/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell
```

**修复**：在 `node` 作业里加一步（置于 `pnpm lint`/`typecheck` 之后，那两个便宜门禁不该排在
一次浏览器下载后面；用包内版本装，保证与 lockfile 解析出的 playwright 一致）：

```
pnpm --filter @webmonitoring/browser-worker exec playwright install --with-deps chromium
```

**绿了的证据**（同一次运行的日志，不是「这次碰巧绿了」）：

- 新步骤的输出出现了此前缺失的那个目录本身：
  `Chrome for Testing 153.0.8010.12 (playwright chromium v1243)` →
  `downloaded to /home/runner/.cache/ms-playwright/chromium-1243`；
- `apps/browser-worker`：**2 文件 / 14 用例全过**（5.80s），其中 `login.test.ts` 3 个
  （`submits the form so the shared context keeps the session` 1680ms 等，4878ms 总量）与
  `security.test.ts` 11 个（含此前失败的第 4 个 `removes ignored nodes before calculating
  snapshot fields` 737ms）——**真浏览器，不是 mock**；
- `apps/console`：**15 文件 / 85 用例全过**（13.37s）——**CI 上第一次跑完**（此前每次被 pnpm
  遇错即停杀掉）；
- `pnpm build` **CI 上第一次执行**：`vite v8.3.4` 转译 2396 个模块、`✓ built in 1.65s`，
  以及 `tsc -p tsconfig.build.json`（1.0 那个 `tsconfig.build.json` 修复此前只在本机验过）。

**同一次运行顺带关掉的两处验证空白**（阶段一遗留）：

- `-race` 此前从未在 CI 上跑过：`backend` 作业 146s（03:05:32→03:07:58）通过，含
  `go test -race -count=1 -covermode=atomic`、golangci-lint v2.14.0、覆盖率下限 55%、
  govulncheck v1.8.0；
- 「集成用例真的在容器里跑」而不是被跳过：`Configure integration test services` 步骤成功，
  postgres:17-alpine 与 redis:8-alpine 服务容器在场。

### 4. 但两个作业首次执行即失败：对象存储的镜像已经不存在

两个作业这次真的被创建并执行了（作业时长 32s / 11s），失败点是同一处、也是瞬间：

```
minio Error unauthorized: access to the requested resource is not authorized
Error response from daemon: unauthorized: access to the requested resource is not authorized
docker compose failed: up -d --build postgres redis minio migrate ...
```

`minio` 不是从 Docker Hub 拉的，而是 **quay.io 上按 digest 钉死**的
（`deployments/compose/docker-compose.yml:139` 与 `:171`）。五条独立探测结论一致：

| 路径 | 结果 |
| --- | --- |
| CI 干净机器（`docker compose up`） | `unauthorized`，且该机器**没有任何本地缓存** |
| 本机 daemon（`docker pull quay.io/minio/minio@sha256:14cea493…`） | `401 UNAUTHORIZED` |
| quay.io 注册表 API（匿名 token 已签发的情况下查 manifest） | 两个 digest 一律 `401`；tag 列表 API 返回 `{"detail": "Requires authentication"}` |
| Docker Hub `minio/minio`、`minio/mc` | 仓库 API `{"message": "object not found"}`；manifest `401` |
| `dl.min.io` 的二进制 | **HTTP 410 Gone** |

上游 README 原文（`raw.githubusercontent.com/minio/minio/master/README.md`）：

> **Important:** The MinIO community edition is now distributed as source code only. We will
> no longer provide pre-compiled binary releases for the community version.

它自带的 `Dockerfile` 第一行却是 `FROM minio/minio:latest`，且 `COPY ./minio-${TARGETARCH}.${RELEASE}`
——所谓「照上游指引自己 build」是循环依赖，走不通。GitHub Releases 里最新的
`RELEASE.2025-10-15T17-29-55Z`（标题即 `Security/CVE RELEASE`）**assets 数为 0**：旧二进制还能
下到（2025-09-07 那次有 48 个 asset），**那个安全补丁下不到**。

**这不是 CI 基础设施问题，是投产阻塞**，而且本机一直看不见它：`docker images` 里那两份
`quay.io/minio/{minio,mc}` 的 `created` 都是 **13 个月前**，所有「compose-smoke 通过」「备份/
恢复通过」的记录都建立在**镜像缓存**之上——**从来没有验证过一次拉取**。今天任何干净机器克隆
这个仓库都无法 `docker compose up`。同一个来源的 `mc` 客户端也拿不到，因此
`scripts/compose-backup.ps1` 与 `scripts/compose-restore.ps1` 一并受影响。

### 未覆盖

- **两个演练作业仍未成功执行过一次**：本次是首次执行，11s / 32s 即因拉镜像失败退出。作业内
  的断言**一条都没有被验证**。→ **2026-10-12 稍后已结清**：镜像替换后推送 `ab18f43`
  （run `38206727165`）两个作业首次执行到底并全绿，断言被真实执行过，见本文最前
  「CI 首次全绿」。
- **60 分钟的挂起保护仍是未实测代码**：它是为本轮首次执行加的，而两次失败都在 32s 内结束，
  触发不到。→ 本轮成功的那次同样没有进入挂起路径，**这一条依然未实测**。
- **`multi-replica-drill` 的 wall-clock 现象**没有获得新的观察：本次没有进入轮询阶段就退出了。
  → 稍后获得两次新观察：本机 28.4s、CI 上 3.6s，见「CI 首次全绿」。
- **本地演练的可复现性**：本机所有通关记录都依赖 13 个月前的镜像缓存；在换掉对象存储之前，
  「本机跑通」不再是「干净机器上跑通」的证据。→ **2026-10-12 稍后解除**：镜像已换为公开
  可拉取的 VersityGW + rclone，且在无缓存的 CI runner 上实测拉到。

---

## 阶段一缺陷清零（1.2 SSRF 判定 / 1.3 会话吊销 / 1.4 截图大小）— 2026-10-10

- **脚本**：`go test ./...`、`scripts/integration-test.ps1`、`scripts/compose-smoke.ps1`（两次）、
  `pnpm typecheck|lint|test|build`、六个 Dockerfile 各一次 `docker build`，以及四组 mutation 注入。
- **专项档案**：`docs/security-model.md`（地址判定对齐与 DNS Rebinding 的状态标注）、
  `docs/operations.md`（哪些升级会登出在线用户）、`docs/runbooks/browser-worker.md`（截图的
  大小约束）。

### 1. 1.2 浏览器 Worker 的 SSRF 地址判定与 Go 侧对齐

**断言**：IPv6 判定不能靠字符串前缀；IPv4-mapped 必须折叠成它承载的 IPv4 再判。

**结果**：

- 改前 `apps/browser-worker/src/security.ts` 的 IPv6 分支只判 `::`、`::1`、`fc*`、`fd*`、
  `fe8*`–`feb*`，于是 `::ffff:169.254.169.254`、`::ffff:127.0.0.1`、十六进制的 `::ffff:7f00:1`
  与 IPv4-compatible 的 `::127.0.0.1` **一律放行**；`ff00::/8` 完全没有规则。Go 侧同一策略
  是安全的（`netip.Addr.Unmap()`），**两侧不对称**。
- 改后展开成 8 个 16 位组，mapped / compatible 折叠后交给**同一套** IPv4 规则；逐条对齐 Go
  的谓词（unspecified、loopback、`fc00::/7`、`fe80::/10`、`ff00::/8`）；`net.isIPv6` 认了却
  解析不出来的拒绝。**刻意比 Go 更严一处**：IPv4-compatible 在 Go 侧不被拦，这里判为不可信
  （失败关闭，代码注释写明这处差异）。
- **mutation 三处**：去掉折叠 → mapped 那组测试失败（原始缺陷复现）；去掉 `ff00::/8` → IPv6
  那组失败；**去掉 loopback 规则 → 测试仍全绿**。第三处说明 unspecified 与 loopback 两条规则
  **当前是冗余的**——`::1` 与展开写法都会落进 IPv4-compatible 折叠、被 `a === 0` 拦下。没有
  删，也没有假装测试守住了它们：代码注释写明这是与折叠重叠的纵深防御，而重叠本身依赖
  「`0.0.0.0/8` 被拦」这个巧合，IPv4 表一旦收窄，这两条就是兜底。

### 2. 1.3 改密码与重置密码吊销该用户的其余会话

**断言**：改密码之后，已经拿到的 Cookie 必须立刻失效，而不是等 12 小时 TTL。

**结果**：

- 改前会话记录没有凭据版本，`Get` 每次重水合用户却只检查 `Disabled`：停用用户/租户会立刻
  踢掉，只有「改密码」这条路没覆盖——而它恰恰是凭据泄露后最常见的处置。
- 改后记录里存 `sha256(密码哈希)` 作为指纹（哈希串自带随机盐，任何一次重设都变），`Get` 在
  重水合之后比对，不一致即删会话并 401，与既有 disabled 分支同构。**零数据库迁移**，内存与
  Redis 两套实现行为一致。
- **只有执行改密的那个会话被保留**（`Service.Reissue`）。这条规则不是选的，是**既有测试逼出来
  的**：`TestAdminCanResetOwnSuperAdminPasswordButNotAnotherAccount` 会在重置自己密码之后继续
  用同一个 cookie 发请求并期望 403；mutation 去掉自重置重发后，该测试报
  `401 SESSION_EXPIRED`，证明那段是承重的。
- **运维后果**：该版本之前签发的会话没有这个字段，读出来是空串、与任何指纹都不匹配，因此
  升级后第一次请求即失效——一次性的全体重新登录。已写进 `operations.md`，并补了
  `TestSessionWrittenBeforeTheFingerprintExistsIsRejected` 钉住这个声明。
- **mutation 两处**：去掉指纹比对 → 三条新测试全红，其中 API 那条的失败信息直接显示泄露的
  会话返回 **200 并带完整会话体（含 csrfToken）**；去掉自重置重发 → 上条既有测试变红。

### 3. 1.4 截图证据的读回上限与视口降级

**断言**：任何被写进存储的证据对象都必须能被读回来。

**结果**：

- 改前读侧门禁 10 MiB（`internal/evidence/verify.go`），写侧（Browser Worker 的截图）**没有
  门禁**，Go 读 Worker 响应的上限 15 MiB。落在约 10–11.25 MiB 窗口的截图**写得进、读不出**：
  下载 404、报告 507。S3 部署下预签名直链绕过 `ReadVerified`，同一个对象在 S3 上能下、在本地
  存储上下不了。
- 改后 Worker 用 `screenshotBudget` 把**两道门**一起算（15 MiB 是对整个 JSON 响应的约束，而
  HTML 与快照同在一个响应里、base64 膨胀 4/3）；整页超限退化为视口截图，视口也放不下则整张
  不发，三种结果由 `screenshotMode` 显式标注。Go 侧加兜底：超限不存并记 `oversized`。
- **快照记录覆盖范围**：`domain.Snapshot` 增加 `screenshotMode`（契约同步 + 重生成类型，实测
  幂等），控制台的基线快照抽屉在非整页时显示一行。
- **两个由实现逼出来的发现**：① 最初只断言「有没有存截图」，那样只有超限那条在测新逻辑
  （VIEWPORT / OMITTED 的存储结果与 FULL 相同），补上指标断言后四个分支才都承重；② 「存不存」
  原来在写截图那一刻决定，而快照证据在它**之前**就写完了——超限被拒时快照里会留下「有截图」
  的描述和一个哈希，而磁盘上什么都没有。现在判定在任何写入之前定下，快照标注与指标从同一个
  结论派生，OMITTED / oversized 时同时清掉 `screenshotHash`。
- **mutation 四处**：去掉超限兜底、VIEWPORT 状态写死、OMITTED 状态写死、
  `screenshotModeForStatus` 恒返回空——四处都被对应子测试抓到。

### 4. 镜像输入集合审计（1.0 那条的同类风险）

1.0 的机制是「本地构建与镜像构建编译的**文件集合**不同」（本地有 `test/`、镜像只 `COPY src`），
TS 6 把这个差异从静默容忍变成硬错误。用同一只眼睛过一遍其余镜像：

- **Go 的五个镜像**不会静默漂移：缺一个包就是编译失败，不是产物不同。逐个 `docker build`
  全部 **exit 0**。
- 比「缺文件」更隐蔽的一类是**平台后缀**（`x_windows.go` / `x_linux.go`）与 build tag：本地按
  windows 编译、镜像按 linux 编译，会是**两套源码**。实测本仓库**没有**任何平台后缀文件、
  也没有任何 `//go:build` 标签，这条路暂时不通。
- **console** 用 `vite build`，Dockerfile 把整个 `apps/console` 都 COPY，不存在集合差异。
  一处观察（未改）：`apps/console/vite.config.ts` 的 `cacheDir` 指向
  `../../.cache/vite-console`，即**逃出应用目录**；镜像里以 root 构建所以能写，但换成非 root
  的构建阶段就会失败。这不是现在的缺陷，是换基础镜像或加 `USER` 时会踩的点。

### 5. 门禁结果

| 检查 | 结果 |
| --- | --- |
| `go build` / `go vet` / `go test ./...` | 全绿（21 个包） |
| `integration-test.ps1` | **通过**（退出码 0，**110.3s**） |
| `compose-smoke.ps1` | **通过**（两次，退出码 0；含打开快照抽屉的控制台 E2E） |
| `pnpm typecheck` / `lint` / `test` / `build` | 全绿（console 85 + browser-worker 14） |
| `docker build`（六个 Dockerfile） | 全部退出码 0 |

### 未覆盖

- **`-race` 仍未跑**（本机无 C 工具链）。1.1 的 relay 与 1.3 的会话比对都在并发路径上。
- **浏览器路径的 TOCTOU / DNS Rebinding 未修**：地址判定对齐了，但「先解析校验、再让 Chromium
  自己解析」之间的窗口还在。已在 `security-model.md` 就地标注
  `状态：部分实现 —— 未把连接钉到已校验 IP`。
- **截图窗口没有端到端复现**：没有构造出 PNG 恰好落在 10–11.25 MiB 的真实页面（尺寸不可稳定
  复现），窗口行为由预算算术的单测与 Go 侧兜底子测试覆盖。
- **控制台「截图范围」那一行从未在浏览器里显示过**：只有截图非整页时才渲染，而 smoke 栈的页面
  很小、走 FULL 分支。抽屉也没有组件级测试，`pnpm test` 不覆盖它。
- **`internal/auth` 没有真实 Redis 的集成用例**（全部走 miniredis），密码指纹在真 Redis 上的
  行为由 compose-smoke 间接覆盖。
- **升级效应没有被真实升级观察到**：`operations.md` 说「现存会话一次性失效」，那条行为由单测
  （写一条无指纹的记录再读）钉住，但没有在一次真实升级里观察过。

---

## SSE 事件流租户隔离与跨进程投递 — 2026-10-10

- **脚本**：`scripts/compose-smoke.ps1`（三次：一次红、一次通过、一次 `-KeepRunning` 供手工
  观测）、`scripts/multi-replica-drill.ps1`（两次）、`scripts/integration-test.ps1`、
  `docker build -f apps/browser-worker/Dockerfile`，以及一次手工的 SSE 观测。
- **专项档案**：[ADR-0006](adr/0006-cross-process-event-delivery.md)。

### 1. 修的是什么

两条独立缺陷叠在同一个 broker 上：

- **跨租户泄露**：订阅者是扁平的 `map[chan []byte]struct{}`，`Publish` 无差别广播，而
  `streamEvents` 订阅时不带租户——任意已登录用户（含只读的 AUDITOR）都能收到其他租户的事件
  转移与备注，载荷是完整 `domain.Incident`（含 `tenantId`/`title`/`signals`/`evidenceIds`）。
- **跨进程到不了**：broker 是纯进程内的。Compose 恒设 `REDIS_ADDR`，检测跑在 http-worker
  进程，它的 broker **一个订阅者都没有**，所以 worker 发布的
  `detection.job.running/succeeded/…`、`baseline.candidate.created`，以及最要紧的检测产生的
  新事件 `incident.created/updated`，全部投进虚空。能到达的只有 API 进程自己发布的两类
  （调度器排队的 `detection.job.queued`、事件转移/备注的 `incident.updated`）。

四个提交：`fd63d3b`（租户作用域）、`946c4b9`（relay 接缝）、`1796300`（Redis relay）、
`89d52f2`（装配到两个 main）；合并 `0163313`、`fabe14e`、`e324699`、`ab79b5c`。

### 2. 跨进程投递：真实栈上的手工观测

`compose-smoke.ps1 -KeepRunning` 起栈后，用 admin 会话把一条 SSE 挂在 **API**（宿主 18080），
再对 `fc19c1de-…`（Compose smoke target，VERIFIED）各排一个 HTTP 与一个 BROWSER 任务。
流上收到：

| 事件 | 次数 | 发布进程 |
| --- | --- | --- |
| `detection.job.queued` | 2 | API 的调度器（**修复前也能到**） |
| `detection.job.running` | 2 | **http-worker** |
| `detection.job.succeeded` | 2 | **http-worker** |
| `incident.updated` | 2 | **http-worker** |

后三行是判据：它们只在 `internal/detection/service.go` 的 `Process` 里发布，而 Compose 拓扑下
`Process` 只跑在 worker。拓扑证据也补齐了：`docker inspect` 显示 **api 与 http-worker 两个容器
都设了 `REDIS_ADDR`**（所以检测是投队列、不是 API 内联执行），worker 日志有
`detection worker started` 与消费者身份。载荷抽查完整（含 `signals`、`evidenceIds`、
`ruleVersion`、`severity`）。

**修复前三类都到不了任何浏览器**——这正是「检测发现了新事件，控制台不会自己冒出来」那条。

### 3. 租户隔离

- 端到端负向（HTTP 层、两条真实 SSE 连接）由 `TestStreamEventsAreScopedToTenant` 覆盖：
  A 的事件 B 收不到，反向再来一次。
- **红→绿已验证**：把 broker 临时改回广播语义后，`TestPublishIsScopedToTenant`、
  `TestPublishWithEmptyTenantDeliversToNobody` 与上面那条端到端测试全部失败，端到端那条报
  `another tenant's stream received the event: received "update"`。
- 跨进程侧同样做了 mutation 检验，三处逐个注入、对应测试都失败：去掉 origin 回显抑制、
  去掉 channel/tenant 一致性检查、把投递租户写死。

### 4. 顺带修掉的既有缺陷：browser-worker 镜像自 TS6 升级起就构建不了

第一次 compose-smoke 在 `browser-worker stage-0 7/7` 失败：

```
tsconfig.json(9,5): error TS5011: The common source directory of 'tsconfig.json' is
'./src'. The 'rootDir' setting must be explicitly set to this or another path to
adjust your output's file layout.
```

`tsconfig.json` 的 `include` 含 `test/**/*.ts` 而**没有 `rootDir`**，Dockerfile 只 `COPY src`：
本地（`test/` 在）公共源目录是包根、不报错；镜像里变成 `./src`，而 TypeScript 6 把它从
「静默调整输出布局」升级成硬错误。**决定性验证**：本地把 `test/` 挪开再 `pnpm build`，复现出
同一条错误。

**它为什么没被 2.6 抓到**：2.6 的五个批次里，**只有 TypeScript 6 那一批（`f798def`）的门禁
没有列 `compose-smoke`**（前三批都列了），而它正是弄坏镜像的那一批。2.6 第 2(a) 节甚至已经
记下了同一机制的另一半——「`tsc` 构建会把测试源码也编译进 `dist/test/`」——只是当时修的是
vitest 侧。**这是门禁覆盖面问题，不是门禁强度问题。**

修法 `04d040f`（合并 `ec13fd1`）：build 专用 `tsconfig.build.json`（显式 `rootDir: "src"`、
只 include `src`）+ Dockerfile 的 COPY 带上它。**顺带关掉了盲区**：本地与镜像现在编译同一
文件集合，本地 `pnpm build` 就等于跑镜像里那一步。

### 5. 门禁结果

| 检查 | 结果 |
| --- | --- |
| `compose-smoke.ps1` | **通过**（退出码 0）。1.0 修复前同一脚本是红的 |
| `multi-replica-drill.ps1` | **通过**（退出码 0），数字见下 |
| `integration-test.ps1` | **通过**（退出码 0，**159.0s**）；`internal/events` 13.454s，含两条真实 Redis 用例 |
| `go build` / `go vet` / `go test ./...` | 全绿（21 个包） |
| `pnpm typecheck` / `test` / `build` | 全绿（console 85 例、browser-worker 8 例） |
| `docker build`（browser-worker） | 退出码 0 |

多副本演练（`--scale api=2 --scale http-worker=3`）：

```
  assets            : 40
  detection jobs    : 40 (expected 40)
  duplicate groups  : 0
  succeeded         : 40
  wall clock        : 8.2s
  worker consumers  : 00f206cd5757-
a1e85b10e6e2-
4a8dfb05089b-
```

**一条与历史不符、值得记下的观察**：wall clock **8.2s**。此前三次落在 234–244s，roadmap 据此
记为「已复现但根因未查清」并把预算放宽到 900s。这次那个平台期没有出现——说明它是**间歇的，
不是恒定的**。单次 8.2s 不足以说根因消失，但足以否掉「240s 是常态」这个印象。

### 未覆盖

- **`-race` 没跑**：本机无 C 工具链（`gcc not found`）。本改动最需要它的地方恰是 sender 协程、
  退避重建与并发 `Publish` 的交错，以 CI 为准。
- **CI 上的 compose-smoke / drill job**：仍未在 GitHub 上验证（本机无 `gh`）。**推论**（非实测）：
  CI 的 `node` job 跑的是带 `test/` 的完整检出，所以它是绿的；而 `compose-smoke` job 会构建
  镜像，因此它在 main 上**大概率一直是红的**。→ **2026-10-12 已确认，推论只对了一半**：
  `node` 为绿，与推论一致；但 `compose-smoke` / `multi-replica-drill` 不是「一直是红的」，
  而是**从未真正执行过**（唯一的执行尝试因对象存储镜像下架在 32s 内退出）。根因也不是
  「会构建镜像」。实测输出见本文最前「CI 首次全绿」，逐条定位见「CI 门禁首次诚实运行与
  对象存储镜像下架」。
- **drill 无法确定性覆盖跨进程 SSE**：drill 只发布一个入口端口，无法把 SSE 连接与产出事件的
  副本确定性地分到不同实例上。跨进程那一条由第 2 节的手工观测，以及「两个 relay + 真实
  Redis」的集成用例覆盖；drill 侧只作不回归的回归。
- 真实 Redis 上的**断线重连**只在一次性容器里跑过，没做「停掉 Redis 再恢复」的时序演练。

---

## 阶段二 2.6 依赖升级与供应链实测 — 2026-10-10

- **脚本**：`go get` + `go mod tidy`（`GOPROXY=https://goproxy.cn,direct`）、
  `go vet`/`go test ./...`、`scripts/integration-test.ps1`、`corepack pnpm typecheck|lint|test|build`、
  `compose-smoke.ps1`（两次）、以及在**仓库外的隔离目录**里对工具链做的最小复现实验。
- **关键断言**：(1) driver/redis 类升级必须过**真实栈**，不只看单测；(2) 每个大版本单独提交、
  单独过完整门禁；(3) **升不动的要取证**——不许写「大概不行」，要给出可复现的失败。

### 1. 五个批次

| 批次 | 提交 | 门禁 |
| --- | --- | --- |
| Go：redis v9.12.1→**v9.23.0**、minio-go v7.0.95→**v7.3.0**、cascadia v1.3.3→**v1.3.5**、OTel 全家 v1.46.0→**v1.47.0** | `d54e9d1` | vet + `go test ./...` **21 包** + 集成测试（真实 PG/Redis，**107.4s**，覆盖率与 2.1 记录一致）+ **compose-smoke 通过** |
| Vite 7.1.3→**8.3.4**（Rolldown） | `0a68297` | typecheck/lint/test(85)/build + **compose-smoke 通过** |
| Vitest 3.2.4→**5.0.3**（两个 workspace） | `80c25dc` | typecheck/lint/test/build |
| vue-router 4.5.1→**5.4.0** | `d5308bf` | typecheck/lint/test(85)/build + **compose-smoke 通过** |
| TypeScript 5.9.2→**6.0.3** + vue-tsc 3.0.6→**3.3.12** | `f798def` | typecheck（两个 workspace）/lint/test/build |

pgx v5.11.0、x/crypto v0.57.0、x/net v0.60.0 已由 0.8 与 2.0 升到最新，本次无变化；
升级后 `go list -m -u all` 的**直接**依赖已无待升项。

### 2. 三个实测发现（都是这次升级才暴露的）

**(a) Vitest 5 的默认 exclude 不再覆盖 `dist/`。** `apps/browser-worker` 没有 vitest 配置、
一直吃默认值，而它的 `tsc` 构建会把测试源码也编译进 `dist/test/`。升级后实测
**4 个文件 / 16 个测试**——每条跑两遍：一遍对源码、一遍对**编译后的陈旧副本**。两个后果
都很安静：本地与 CI 会跑不同集合（CI 在 build 前跑 test，那时 `dist/` 不存在），且陈旧副本
能掩盖源码里已修好的失败。修法是显式写 `include: ["test/**/*.test.ts"]`（`80c25dc`），
修好后回到 **2 个文件 / 8 个测试**。console 侧无此问题（vite 构建不产出测试文件，已核实）。

**(b) Vite 8 是硬失败，不是警告。** 构建直接停在
`manualChunks is not a function`——对象形式被移除、`rollupOptions` 更名为 `rolldownOptions`。
迁移到 `rolldownOptions.output.codeSplitting.groups`（写法取自**本地安装的 rolldown 类型
定义**：`codeSplitting` 是现行 API，`advancedChunks` 在 1.2.13 里也已标 deprecated；
文档站在本机网络下抓不到，类型定义才是可核实的一手来源）。正则锚定包目录，所以
`vue-router` 不会被扫进 vendor chunk（旧配置也没算它）。

首屏字节（同 2.3 口径：浏览器实下载、无压缩）**全面略降**——压缩器换成 Oxc、CSS 换成
Lightning CSS：

| 入口 | vite 7 | vite 8 | |
| --- | --- | --- | --- |
| `#/login` | 355,844 B | 346,345 B | −2.7% |
| `#/assets` | 804,562 B | 794,750 B | −1.2% |
| `#/incidents` | 929,180 B | 918,979 B | −1.1% |
| `#/overview` | 1,937,336 B | 1,917,659 B | −1.0% |

产物里出现的 `css-*.js` 是 Element Plus 按需样式入口（模块本身就名为 `css`）被 Rolldown
归成的 chunk，不是把 CSS 打成了 JS——`css-*.css` 仍在。

**(c) TypeScript 7 被工具链双重阻塞，落点是 6.0.3。** 两步取证：

1. TS 7 是 Go 原生编译器，**不再提供程序化 Compiler API**；本仓库的 `typecheck` 就是
   `vue-tsc --noEmit`，而最新的 vue-tsc（3.3.12，无 4.x）依赖 `@volar/typescript`，即那套
   API。隔离目录实测（`typescript@7.0.2` + `vue-tsc@3.3.12` + 一个最小 `.vue`）：
   `Error [ERR_PACKAGE_PATH_NOT_EXPORTED]: Package subpath './lib/tsc' is not defined by
   "exports"`（`vue-tsc/index.js:73`）——**连启动都不行**。同目录 `typescript@6.0.3` 下
   `vue-tsc --noEmit` 退出码 0。
2. `typescript-eslint@8.71.1` 的 peer 是 `>=4.8.4 <6.1.0`，**TS 7 也会被 lint 拒**。

6.0.3 是官方指定的过渡版（报出 7 将移除的弃用项），两条工具链都接受。本仓库 tsconfig 早已
6/7 就绪（显式 `types`、`NodeNext`/`Bundler`、`ES2022`、`strict`、无 import assertions），
因此**零代码改动**。

**一条要写下来的非问题**：`openapi-typescript@7.13.0` 的 peer 是 `typescript ^5.x`，pnpm 报
unmet peer。实测不受影响：TS 6 下 `generate:api` 产出的 `api.d.ts` 与提交版本**逐字节一致**
（`git cat-file blob HEAD:… | diff` 无差异），CI 门禁 `git diff --exit-code` 退出码 0。
不必为此把 TS 降回去。

### 3. 刻意没做的

- **playwright 1.63.0 → 1.64.0**：它与 browser-worker 的镜像标签
  `mcr.microsoft.com/playwright:v1.63.0-noble` 耦合，升级要连 Dockerfile 一起改，属于独立
  一项（否则会出现「包是 1.64、浏览器是 1.63」的静默错配）。
- **其余 patch/minor**（element-plus 2.14.7、vue 3.5.43、eslint 10.12、@tanstack/vue-query
  5.104 等）：交给既有的 dependabot 月度批次——`.github/dependabot.yml` 的注释就写着这是
  既定节奏，手工再升一遍是重复劳动。

### 未覆盖

- CI 里的这些 job（backend/node/compose-smoke/drill）仍未在 GitHub 上验证，本节的结论全部
  来自本机（Windows + Docker Desktop）。
- 安装过程中本机直连 registry 不稳（`@vue-macros/common` 元数据取不到，`ERR_PNPM_META_FETCH_FAIL`），
  改走本机代理后成功；这不是仓库问题，但下一个遇到同类失败的人应先怀疑网络再怀疑版本。
- TS 7 何时可升取决于 vue-tsc 与 typescript-eslint 何时支持原生编译器，见 `docs/roadmap.md`。

---

## 2.3 与 2.4 合并进 main 及合并后整体回归 — 2026-10-09

- **脚本**：`git merge --no-ff`（两次）、合并后门禁（gofmt/vet/`go test ./...`、
  `pnpm typecheck|lint|test|build`）、`.\scripts\compose-smoke.ps1`、
  `.\scripts\multi-replica-drill.ps1`（本机需先设 `GOPROXY=https://goproxy.cn,direct`
  才能建 Go 镜像）。
- **关键断言**：(1) 合并只在总账顶部冲突，且两节都要保留；(2) 合并后**适用门禁必须
  重跑**；(3) 整体回归（smoke + drill）通过之后，才把这次合并称为已验证。

### 1. 合并

- `codex/frontend-quality`（2.3，8 提交）→ merge `27fe872`；`codex/openapi-drift-gate`
  （2.4，3 提交）→ merge `c59e0bf`。两次都用 `--no-ff` 保留分支边界。
- 唯一冲突：`docs/rehearsals.md` 顶部——总账是 append-only、最新在最前的追加式记录，
  两条分支都把各自小节插在「阶段二 2.2」之前。解法：两节都保留，**2.4 放在 2.3 上面**
  （同日，2.4 后跑）。`docs/engineering.md` 自动合并成功。
- 合并后 `git merge-base --is-ancestor <branch> main` 两条都为真。

### 2. 合并后的门禁（全部在合并后的 main 上）

- `gofmt`：对**索引/提交里的 LF 副本**校验通过（工作区是 CRLF，见第 4 节）。
- `go vet ./...` 无输出；`go test ./...` **21 个包全部 ok**。
- 前端：`typecheck`、`lint` 通过；`pnpm test` 为 **console 85 + browser-worker 8**
  （2.3 把 console 从 47 抬到 85，合并后生效）；`pnpm build` 通过。

### 3. 整体回归：smoke 先红、定位、修好、再绿

- **第一次 `compose-smoke.ps1` 失败**（退出码 1）：`console-smoke.mjs:39` 的侧边栏视图
  巡检，点「资产中心」后断言 `main` 含 `REGISTERED TARGETS` 失败。
- **根因（实测，不是推断）**：`assertPageHasContent` 是「等到 `main` 出现 → **读一次**
  → 断言包含」，而 2.3 把视图改成按需加载后，外壳先出现、视图 chunk 后到，这一次读取
  就落在上一个视图上。真实栈 + Playwright 直连五个视图实测：**三个视图「立刻读」读不到，
  但随后都渲染出来了**（`settledHit` 全为 true）——所以不是视图坏了，是断言与 chunk 赛跑。
  这也解释了为什么它在 2.3 分支上两次通过：当时 chunk 已热。
- **修法**：`assertPageHasContent` 改为等待文本出现（`locator("main", { hasText })` +
  `waitFor` 15 秒），失败时打印当时屏幕上真实的文本片段。12 处调用共用它，一处修复覆盖
  全部（含深链巡检 `#/jobs`、`#/evidence` 那一组）。改动只在 E2E 脚本，无生产代码变化。
  分支 `codex/e2e-view-race`（`1d611bb`）→ merge `dcdf8e7`。
- **重跑 compose-smoke：通过**（退出码 0，末行「Compose smoke test passed」，含控制台
  Playwright E2E 全部步骤与安全边界断言）。
- **`multi-replica-drill.ps1`：通过**——40 资产 / 40 任务 / **0 重复分组** / 40 成功 /
  3 个 worker 消费者。
  - wall clock **234.5s**。这是同一脚本**第三次**落在 240s 附近（2.2 记过 243.2s 与
    244.1s），说明「延迟出现在任务创建前的调度器排队阶段、根因未查清」那条是可复现的，
    不是一次性抖动；当前 900s 预算内仍然通过。

### 4. 一条查证过程中的坑（值得记住）

核对「提交进去的 Go 文件是 LF 还是 CRLF」时，`git show HEAD:<path> | grep -c $'\r'`
数出了大量 CR（看起来像**整个仓库以 CRLF 入库、CI 的 gofmt 门禁一直在红**）。这是**错的**：
Git for Windows 的 `git show <rev>:<path>` 会按 `core.autocrlf` 做工作区式转换。
权威口径是 `git ls-files --eol`（本例四类文件全是 **`i/lf` `w/crlf`**）与
`git cat-file blob`（原始字节是 `\n`）。**仓库里存的是 LF，CI 在 Linux 上看到的是 LF，
gofmt 门禁正常。** 本地 `gofmt -l` 报 CRLF 纯属工作区状况——要贴 CI 口径，就对
`git cat-file blob :<path>` 出来的 LF 副本跑 gofmt。

### 未覆盖

- 两个演练都是**本机**跑（Windows + Docker Desktop）；CI 里的 `compose-smoke` 与
  `multi-replica-drill` 两个 job 仍未在 GitHub 上验证过（既有的缺口，非本次引入）。
- drill 超过 900s 预算的排空行为没有构造过（只有「接近 240s」这一档被反复观察到）。

---

## 阶段二 2.4 openapi ↔ 路由漂移门禁实测 — 2026-10-09

- **脚本**：`go test ./internal/api/ -run TestRoutesMatchOpenAPIContract`（新增门禁）；
  四组注入故障（临时改 `server.go` / `api/openapi.yaml` / 门禁自身的正则，`git checkout`
  还原）；`openapi-typescript` 重新生成 client 类型后 `git diff`。
- **关键断言**：(1) 门禁必须**真的会红**——四种破坏各自都要被指名，其中「解析器弄坏」
  那组必须因下界而红，而不是两边空集相比变绿；(2) 契约改动后门禁仍绿（新增的 4 空格
  `servers:` 行不得被误收成 operation）；(3) 生成的 client 类型必须与契约一致。

### 1. 门禁本身

- 枚举路由只能**读源码**：`routes()` 建两个 `ServeMux` 并把受保护的那个挂在鉴权中间件
  后面（`internal/api/server.go:291`），那是局部变量，运行时无从遍历——对返回的 handler
  调 `(*http.ServeMux).Patterns()` 只能看到根上的 5 条与那个挂载。
- 用 `go/parser` + `go/ast` 而非正则：`routes()` 的函数体里**本来就写着路由字样的注释**
  （`server.go:214-252`），文本扫描会把注释当注册收进来；被注释掉的注册也会被误收。
- 契约按行解析（`go.mod` 里没有任何 YAML 库，本机 `proxy.golang.org` 不可达，故不新增
  依赖）：path 键 2 空格、method 键 4 空格且**锚定方法名**——path 级 `parameters:` 也在
  4 空格（共 9 处），「4 空格即方法」会把它收成 operation。方法名后不加 `$`，否则
  `get: { … }` 这种 flow 写法会被静默丢掉。
- 归一化：server 侧路径去掉契约 `servers:` 声明的 `/api/v1` 前缀再与裸路径比。根上的
  三条因此自然匹配，**不需要白名单**；前缀按整段锚定，`/api/v10/foo` 不会被误剥。
- 前缀挂载**记下来并断言恰为 `{"/api/v1/"}`**，而不是字符串匹配后跳过：将来多一个挂载
  会被抓到，而不是被悄悄丢掉。
- **六条防假绿**：两侧集合大小下界（≥60 operations / ≥50 paths）、key 形态校验、双向
  差集都空且数量相等、`routes()` 里必须真扫到调用、两侧各自查重、挂载集合与 `servers`
  取值守卫（后者会指名「归一化常量该更新了」）。

### 2. 敏感度：四组注入故障实测

| 注入 | 结果 |
| --- | --- |
| `routes()` 加一条 `GET /api/v1/__drift__` | 红，69 vs 68，指名 `GET /__drift__` 归入「missing from api/openapi.yaml」 |
| 契约加一条 `/__drift__` operation | 红，68 vs 69，指名同一条，归入反方向 |
| 把 path / method 正则改坏 | **因下界而红**（`parsed 0 operations across 54 paths …`），不是变绿 |
| 挂载点改成 `/api/v2/` | 红，`prefix mounts are [/api/v2/], expected exactly the "/api/v1/" mount` |

基线日志常驻一行 `compared 68 registered operations against 68 declared across 54 paths`，
一眼能看出比的不是 0 vs 0。

### 3. operation 集合现状：**已经同步，首日即绿**

68 条注册（5 条在根、63 条在受保护 mux）= 契约的 68 个 operation（54 个 path，全部有
operationId）。**roadmap 说「第一次跑就会把这些缺口暴露为红」不成立于 operation 这一类**
——它指的其实是响应声明那一类（见第 4 节），而这一类本次只做了手工核对，没有门禁。

### 4. 契约修正（客户端的可判断性）

- **根端点位置**：全局 `servers: /api/v1` 使 `/healthz`、`/readyz`、`/metrics` 的生效
  地址变成 `/api/v1/*`，而它们在 `server.go:205-207` 挂在根上（三条自身都写了
  `security: []`）。按 OpenAPI 的做法加 path 级 `servers: [{ url: / }]`，而不是在门禁里
  加白名单。
- **四处响应声明缺口**（逐条对照 handler 的 `writeError`/`notFound` 核实）：
  `POST /alert-channels` +400/+409（`CHANNEL_EXISTS`，`server.go:2477`）、
  `PUT /alert-channels/{id}` +400/+404（`2517`/`2544`）、
  `DELETE /alert-channels/{id}` +404（`2561`）、
  `GET /evidence/{id}/download` +404（`2065`/`2070`；同族 signed 端点早有 404，一写一漏）。
- **与 roadmap 原文的两处出入**：operation 集合早已同步（上节）；**409 在 POST 上**，
  不在 PUT/DELETE——PUT/DELETE 缺的是 400/404。另外给 POST 也补了 400（与 PUT 同一个
  decodeJSON/校验路径），只补 PUT 会让「为什么这条声明那条不声明」变成新的任意性。
- **生成的 client 类型一并重生成**：实测推翻了「响应声明不进类型」这个先验假设——
  `api.d.ts` 里 `operations[...].responses` 多了 4 条 400/404/409（6 行）。不重生成的话
  CI 的 `git diff --exit-code api.d.ts` 会红。`servers` 覆盖确实不进类型。

### 未覆盖

- **响应状态码的系统性缺口未做**：`500` 全文件一个都没有，而 `server.go` 有 14 处直接
  500 + 41 处 `repositoryFailure`（共享 500）；全局中间件产生的 401/403/429 也没有任何
  operation 声明。要做就得连 AST 级「声明必须覆盖 handler 能写的状态码」门禁一起做，
  否则只是逐个补声明。
- 门禁只管 **operation 集合**，不管响应/参数 schema 是否与实现一致。
- **没有跑 `compose-smoke.ps1`**：本项只有测试与契约改动，无运行时行为变化。写明以免
  读者以为跑过。
- 顺带观察（未改）：`updateAlertChannel` 把仓储失败也映射成 404——数据库故障会表现成
  「渠道不存在」，与 0.4 修过的那类静默失败同源，不在本项范围。

---

## 阶段二 2.3 前端工程质量实测 — 2026-10-09

- **脚本**：`apps/console/e2e/` 下的一次性 Playwright 探针（`vite preview` 提供
  构建产物 + 路由拦截 mock 后端；探针未入库，方法与数字都在本节写明）；
  `corepack pnpm typecheck|lint|test|build`；`.\scripts\compose-smoke.ps1`。
- **关键断言**：(1) 首屏体积要用**浏览器实测下载字节**，基线也必须同法实测，不能
  用文件求和凑一个更好看的对比；(2) 错误页必须显示**真实原因**且重试能恢复；
  (3) 深链必须落到同一个视图；(4) 骨架屏只能出现在首次加载，且不能把「还没到」
  说成「没有」。

### 1. 体积：基线也用同一探针实测

口径：同一探针、同一天、无压缩（`vite preview` 不 gzip；compose 里的 nginx 也未
开 gzip）。基线在 `HEAD`（`274462a`）另建 worktree 构建后实测，不是文件求和。

| 入口 | 基线 | 改造后 | |
| --- | --- | --- | --- |
| `#/login` | 2,676,778 B | 355,844 B | −86.7% |
| `#/assets` | 2,676,778 B | 804,562 B | −69.9% |
| `#/incidents` | 2,676,778 B | 929,180 B | −65.3% |
| `#/overview`（默认视图） | 2,676,778 B | 1,937,336 B | −27.6% |

- 基线的四个入口字节数相同：静态 import 让每个入口都下载全部产物。
- 现在 `#/overview` 比其他视图多约 1.14 MB，那是 ECharts，只在打开概览时加载。
- **sourcemap**：基线 12.9 MB 的 `.map` 随 dist 一起发布（浏览器不开 devtools 不
  会下载，但任何能打开控制台的人都能按 URL 取到）；现在默认不生成，
  `CONSOLE_SOURCEMAP=true` 才生成。
- 构建不再出现 921 kB 的 element-plus 单块（基线它与 1,271 kB 的 index 并列）。

### 2. 错误页、深链与边界范围（真实浏览器）

- 注入渲染期异常（临时改 `LoginPage.vue`，验证后还原）→ 显示「这个页面没能渲染」，
  正文是**注入的那条错误原文**；点「重试渲染」后登录页正常渲染回来。
- API 不可达（dev server 无后端，代理回 500）→ 登录页仍渲染，顶栏横幅显示
  「服务端错误（500），控制台无法完成本次请求」；基线文案是 `500 Internal Server Error`。
- `#/incidents` 深链 → 渲染「事件中心」，且**不加载** ECharts chunk。改前实测：最终
  停在 `#/` 并加载了概览的 ECharts。
- **边界范围是实测后收窄的**：最初只要子组件抛错就整页替换。这次在真实栈里撞到
  「对话框的 watcher 抛错 → 整个控制台被错误页替换」，才改成按 Vue 给的 `info`
  分流：只有 `render`/`setup` 失败才拿走视图，watcher/异步处理器/生命周期钩子失败
  走横幅（第 5 节是它的完整来源）。

### 3. 骨架屏、空态与 aria（真实浏览器）

- `#/assets` 首次加载：显示骨架，且**不出现**「尚未登记监测资产」。
- `#/overview` 首次加载：10 个骨架，页面**不含**「该区间内没有事件」。
- 响应落地后：骨架 0、空状态 3，真实数字与空状态正常渲染（截图核对）。
- aria：`[role="img"][aria-label]` 命中「检测任务成功率仪表盘」；
  侧边导航 `button[aria-current="page"]` = 总览。
- dev 模式另跑一次（`pnpm dev`，与构建路径的转换方式不同）：登录页与控制台外壳
  均正常、零 console 错误。**注意**：改动依赖后首次加载会撞上 Vite 的
  「504 Outdated Optimize Dep」，重载一次即好——这是重新预构建依赖，不是缺陷。

### 4. 与计划不同的地方（都是实测后的判断）

1. **计划称「`App.vue` 的 `onMounted` 里 `restore()` 无 try/catch」——实际有**
   （`stores/session.ts:19`）。
   真实缺口是失败后没有任何提示：401（未登录，正常）与「API 不可达」被压成同一个
   `false`。修法是让 `request()` 抛带 status 的 `HttpError`，只对 401 保持静默。
2. **`manualChunks` 里的 `element-plus` 条目会抵消按需引入**：它把整包强制塞进一个
   chunk，第一次改造后构建仍是 921 kB；删掉条目后才真正按需。
3. **`App.vue` 恢复会话后无条件 `router.replace("/")` 会取消深链**。这本是
   `router.ts` 注释与 `console-smoke.mjs:43-54` 深链断言声明过的契约，此前只能靠
   竞态通过（断言要在跳转生效前读到 DOM 才算过）。现在只在登录页跳转。
4. **`activeView` 默认 `"overview"` 会让深链也加载 ECharts**（默认值先渲染、再纠正）。
   改为从 hash 播种。
5. **`unplugin-vue-components` 的声明文件保持关闭**：打开会让 `vue-tsc` 按
   `el-table` 槽位的 `DefaultRow` 校验全站 emit（多处报错）。那是独立的一件事，
   不该由体积改动携带；相对改前也没有减少（`app.use(ElementPlus)` 本来就没给编辑器
   提供组件类型）。
6. **`BaselineSnapshotDrawer` 的两张表没有 `empty-text`，但不是缺陷**：它们在
   `v-if="rows.length"` 里，永远不会渲染空表。核对后不改。

### 5. 错误边界顺带挖出的服务端缺陷（本次最值得看的一条）

错误边界上线后，整栈 E2E 在「编辑资产策略」这一步超时。排查链（全部是实测）：

1. 点「策略」后对话框不出现，整个视图被兜底页替换，显示
   `Cannot read properties of null (reading 'join')`。
2. 定位到 `PolicyDialog.vue` 的 `watch(..., { immediate: true })` 里对
   `policy.ignoreSelectors/trustedDomains` 直接 `.join()`。
3. 真实栈取证：PUT 时给 `[]`，`GET /api/v1/policies` 回的是
   `"ignoreSelectors":null,"trustedDomains":null`，而 `api/openapi.yaml:1666` 把这两项
   声明为 **required array**——**服务端违反自己的契约**。
4. 服务端根因：`internal/api/server.go:1461-1462` 用
   `append([]string(nil), input...)` 构造字段，空输入得到 `nil` → JSON `null`。
   全文件该模式共 **2 处**，都在策略处理器里（已定点排查）。
5. **这个缺陷此前是隐形的**：改前没有错误边界，watcher 抛错只进 console，对话框照常
   打开、表单静默保留默认值；而默认 `timeoutSeconds` 恰好等于 compose smoke 种子的
   30，E2E 因此一直绿。改后边界把同一个异常放大成整页错误，才暴露出来。

处置（两侧都修）：

- **前端**：把 null 当空数组读（并在注释里写明服务端行为），边界按 `info` 分流
  （见第 2 节）。前端这一侧保留——接口现在按契约说话了，但前端不该依赖「另一个
  服务永远不会犯错」。
- **服务端**：`updatePolicy` 改 `append([]string{}, …)`，缺陷不再被制造；
  `redactPolicy` 把 nil 规整成空数组。后者是**所有**策略响应（列表与更新）的唯一
  出口，因此历史行——存进 jsonb 的 JSON 里就写着 null 的那些——一并修正，**不需要
  数据迁移**。

实测确认：

- 真实栈 + 重建镜像后对话框正常出现，且表单读到 `maxResponseBytes = 1048576`
  （种子值；组件默认是 5242880），证明策略确实加载，而不是回落到默认值。
- Go 测试：两个字段按 `json.RawMessage` 断言（把 null 与 `[]` 都解成 `[]string`
  得到同一个空切片，差别正是击穿 console 的那一点）。把修复整体还原验证过：两个
  用例都红，输出是 `ignoreSelectors is null, want []`；恢复后通过。
- **历史行的真机取证**（真实 Postgres 栈，非构造数据）：修复前写入的那一行
  （`15c705ea-…`，`updated_at` 09:01）库里仍是
  `jsonb_typeof(config->'ignoreSelectors') = null`，而修复后的 API 对**同一行**返回
  `"ignoreSelectors":[],"trustedDomains":[]`——响应侧归一化对存量数据同样成立。

### 6. jsdom 的边界（测试覆盖到哪、没到哪）

- Element Plus 的表格**表体**由列宽测量驱动，jsdom 没有布局引擎：所有测量为 0 时
  只渲染表头，断言会在空表上通过——那是绿的假象。页面测试因此用桩
  （`src/test/elTableStub.ts`，保留 `data`/列 `#default`/`empty-text` 契约），
  **表格自身的渲染由 compose-smoke 在真实浏览器覆盖**。给 jsdom 灌固定宽度的尝试
  也验证过：无效。
- ECharts 需要真实 canvas 尺寸，概览测试里桩掉 `EChart`。
- 单测 52 → 85。四批改动与本次修复都做过注入故障验证（去掉守卫/条件后测试确实变红）。

### 7. 整栈回归

- `.\scripts\compose-smoke.ps1`：**两次通过**（退出码 0，末行「Compose smoke test
  passed」，含控制台 Playwright E2E）——一次是本项前端改动，一次是加上服务端
  契约修复之后。第一次运行失败于 **Go 镜像构建**：`proxy.golang.org` 在本机网络下
  不可达（`connection refused`），与本项改动无关；按 `operations.md` 既有做法设
  `GOPROXY=https://goproxy.cn,direct` 后通过。控制台镜像的
  `pnpm install --frozen-lockfile` 每次都正常（313 个包）。

### 未覆盖

- **无障碍只做了基线三项**（图表标签、当前导航项、既有的图标按钮），没有全面审计，
  也没有跑 axe 之类的扫描。
- 视图级骨架屏的**首屏感知**没有量化：只有「骨架 → 空态」的状态断言与截图，没有
  真实网络下的时间线测量。
- 同类缺陷只做了**定点排查**（`append([]T(nil), …)` 全文件 2 处；控制台侧读取这两
  个字段的只有 `PolicyDialog`），没有全量审计「契约说非空、服务端可能给 null」的
  其他字段。

---

## 阶段二 2.2 测试与 lint 门禁实测 — 2026-10-09

- **脚本**：golangci-lint v2.14.0 本地实跑、`govulncheck`、全量测试（含真实
  Postgres/Redis）、`compose-smoke.ps1`、`multi-replica-drill.ps1`。
- **关键断言**：(1) lint 门禁必须**真的在检查**；(2) 补了断言的并发测试在注入故障
  时必须红；(3) 门禁不得有看不见的部分。

### 1. lint 门禁此前是坏的，不是弱的

CI 钉的 golangci-lint **v1.64.8 读不懂 go1.27 的导出数据**
（`cannot decode "cmp", export data version 5 is greater than maximum supported
version 2`），以 **Running error 退出码 3** 收场——**它什么都没检查过**。换成
v2.14.0（`go install` 而非 action，版本显式）后一次报出 **71 条真实问题**，清理后
**0 issues、退出码 0**。`.golangci.yml` 已迁移到 v2 格式。

**第二个陷阱**：golangci-lint **默认每个 linter 只报 50 条**。先看到的「50 条
errcheck」是上限而非全量，按它清理只放出了被截断的下一批——真实的 `_ =` 约 **108**
处。已在配置里关掉 `max-issues-per-linter` 与 `max-same-issues`。

### 2. check-blank 关闭（**与计划不同，实测后的判断**）

计划原本要开。开启后逐处审查约 108 个 `_ =`，**没有一处是真缺陷**：全是惯例写法
（关流、向已断开的客户端写、编码已知结构）与测试夹具。全注解是近百条噪音，按正则
整类排除等于门禁重新变成摆设。唯一值得警惕的两处（`strconv.ParseBool` 解析失败
静默回落，会让 `SESSION_SECURE` 之类悄悄变 false）在源码处标了 nolint 并写明它在
收尾项里。理由与测量结论写进了 `.golangci.yml`。

### 3. 修掉的 16 条

两处 negated 复合条件改成肯定谓词 `isHexDigit`/`isSlugChar`（过 QF1001 且更好读）、
四条以省略号结尾的错误信息改写（**同步了两处断言旧文案的测试**）、删除死代码
`appendUnique`；8 条 gosec 经核对为误报（会话 Cookie 的 Secure 取自配置且生产门禁
强制、证据路径由服务端 UUID 拼成、`template.CSS` 包的是编译期常量样式表）就地标注。

### 4. -race、覆盖率与那条零断言的测试

- CI 的 `go test ./...` 改为 `-race -count=1 -covermode=atomic -coverprofile`；
  新增覆盖率步骤打印真实数字、低于 55% 失败（**实测带服务容器 60.3%**，留了余量）。
- **本机跑不了 `-race`**（无 gcc、CGO_ENABLED=0），以 CI 为准，未写成已本地验证。
- `TestConcurrentPublishAndSubscribe` 此前是 4+4 goroutine 加一个 `time.Sleep`、
  **零断言**。补上三项：订阅者收到帧的计数（阻塞式读取，不赌运气）、风暴后订阅表
  必须为空、风暴后再发一条必须仍能送达。**注入故障验证过它会红**（把泄漏断言反转
  → FAIL；还原 → PASS）。

### 5. drill 门禁本身的缺陷（顺带修掉）

drill 五次里两次随机报 **0/40**，两次 wall clock 都恰好落在它自己的 240 秒上限上
（243.2s / 244.1s）。诊断显示它要验的东西两次都成立（40 个任务、0 重复），单个任务
只要毫秒级（排队 7.2s、执行 0.1s、零重试）；同一份代码的 wall clock 在 25.6s 到
244.1s 之间摆动。循环一满足条件即退出，所以超时是**上限而非时长**——放宽到 900 秒
不会拖慢通过路径，也不削弱它验的不变量。**根因未查清**：延迟出现在任务被创建之前
（调度器排队阶段），而调度器启动即 `scheduleAll`、无每轮上限，因此「为什么偶尔要
三分钟」仍无定论；若再观察到接近 900s 的排空，应按真故障处理。

- **合并前整体回归**（`codex/test-gates` @ `4b9d886`）：`compose-smoke.ps1` **通过**
  （退出码 0）；`multi-replica-drill.ps1` **通过**（40/40、0 重复、105.3s）——修复前
  同一命令报 0/40。
- **未覆盖**：`-race` 与覆盖率步骤、golangci-lint v2 的 CI 步骤本身（本地无 C 工具链、
  CI 未跑）；`check-blank` 决定与计划不同，已在配置与本节写明依据。

---

## 阶段二 2.1 本地集成测试入口实测 — 2026-10-09

- **脚本**：`scripts/integration-test.ps1`（`make test-integration`）。
- **关键断言**：(1) 一条命令能在本地跑通此前默认跳过的集成用例；(2) **缺 DSN 时
  必须失败而不是静默全绿**；(3) Docker 不可达时要快速给出可操作的错误。
- **结果**（2026-10-09）：
  - `make test-integration` **130.0 秒全绿**。覆盖率（对照无 DSN 时）：
    `internal/store` **32.3% → 56.4%**、`internal/jobqueue` **0.0% → 30.9%**、
    `internal/rewrap` 80.8%（此前本地为 0，其集成用例全部跳过）、
    `internal/migrate` 19.9%。**预测值是 store 约 70%，实测 56.4%**——差在
    Postgres 实现里并非每个方法都被集成用例走到。
  - **防假绿负向验证**：设 `WEBMONITORING_TEST_REQUIRE_SERVICES=1` 但不给 DSN，
    `internal/store` 的 13 个集成用例**全部失败**（不是跳过），退出码 1。
  - **Docker 不可达**（`DOCKER_HOST` 指向无效地址模拟）：**1.9 秒**退出，输出
    「Docker 守护进程不可达……请先启动 Docker Desktop 再重跑」，并说明没有 Docker
    时 `make test` 仍会跑内存实现那部分。
  - 预检 `go run ./apps/migrate up` 在空库上应用全部迁移成功（第一次跑失败于
    `migrate` 读的是 `POSTGRES_DSN` 而非 `POSTGRES_TEST_DSN`，已修）。
- **修法摘要**：新增运行器（一次性 `postgres:17-alpine` / `redis:8-alpine`、临时
  回环端口、`try/finally` 清理、`-KeepRunning` 与 `-Race` 开关）；七处跳过点在
  `WEBMONITORING_TEST_REQUIRE_SERVICES` 存在时改为 `t.Fatal`，其中 store 的
  `testRepositories` 是最危险的一处（它不跳过、只是少跑一半）。
- **合并前整体回归**：**未跑 compose-smoke / multi-replica-drill**。理由是本次改动
  逐文件核对后为零生产代码——只有 5 个 `_test.go`、新脚本、Makefile 与两份文档，
  部署物逐字节不变，两个脚本不会执行到任何被改动的内容。适用门禁（gofmt / vet /
  全量单测 / 运行器自身三条验证）已全部通过。
- **未覆盖**：`-Race` 开关未在本机验证（无 C 工具链，见 2.2a）；运行器未在 Linux
  上跑过（CI 已有服务容器，不打算加作业）。

---

## 阶段二 2.0 工具链与可达依赖安全修复实测 — 2026-10-09

- **脚本**：`govulncheck` 本地实跑、`docker build` 探针、`compose-smoke.ps1`、
  `multi-replica-drill.ps1`；一次性 Postgres/Redis 容器跑集成测试。
- **关键断言**：(1) 扫描必须归零；(2) 修复必须落到**部署物**上，而不只是本机；
  (3) 构建要能在取不到 `proxy.golang.org` 的网络里跑通。
- **结果**（2026-10-09）：
  - 修复前：`govulncheck ./...` 退出码 **3**，11 个可达漏洞全在标准库（修复于
    go1.27.2，本机 1.27.1），其中两个是 `html/template` 的上下文转义错判
    （GO-2026-6599、GO-2026-6600），而打印就绪报告正是靠它转义攻击者可控文本；
    另有 5 个 http2 漏洞**同时经 `golang.org/x/net@v0.58.0` 可达**。
  - 修复后：退出码 **0**（可达 0、imported 包 0；仅剩 `x/crypto/openpgp` 一条
    模块级告警，该包未被导入）。x/net v0.60.0、x/crypto v0.57.0、grpc v1.83.2、
    compress v1.18.7。
  - **容器侧缺口（本次实测发现）**：官方 `golang` 镜像设了 `GOTOOLCHAIN=local`，
    会让 `go.mod` 的 `toolchain` 指令在镜像构建里**静默失效**——探针实测容器内
    `go version` 为 **1.27.1**；而 `govulncheck` 扫的是源码与模块图，不会发现
    部署物仍带病。四个 Go 镜像加 `ENV GOTOOLCHAIN=auto` 后，容器内实测 **1.27.2**。
  - 构建网络：容器直连 `proxy.golang.org` 报 EOF（`goproxy.cn` 正常），故新增
    默认不生效的 `GOPROXY` 构建参数（四个镜像 + compose）。用
    `--build-arg GOPROXY=https://goproxy.cn,direct` 实测构建通过。
  - 全量单测 + 真实 Postgres/Redis 集成测试：全绿。
- **合并前整体回归**（`codex/security-toolchain` @ `fd5aa83`）：
  - `compose-smoke.ps1`：**通过**（退出码 0）。第一次失败在控制台镜像的
    `corepack` 下载 pnpm（`SocketError: other side closed`，等了 155 秒），重跑即过
    ——网络抖动，与本改动无关。
  - `multi-replica-drill.ps1`：**三次两绿**。第一次 **0/40 且 wall clock 243.2s**，
    恰为其 `-TimeoutSeconds` 默认值 240；第二、三次 40/40（221.9s / 25.6s）。
    数据库实测显示任务本身毫秒级完成（排队 7.2s、执行 0.1s、零重试），
    **耗时波动在调度器排队阶段**；同一现象在改动之前已出现（1.6 那次 191.4s）。
- **未覆盖**：`APP_VERSION` 等构建参数仍未实测注入真实版本号（沿用默认 `dev`）；
  首次构建需下载工具链，受限网络下必须先配好 `GOPROXY`。
  ⚠️ **2026-10-12 已修正前半句**：真实版本号已实测注入并读出（四个 Go 镜像）；后半句
  仍然成立。见总账「发布链路接通」。

---

## 阶段一 1.6 审计与事件清单导出实测 — 2026-10-03

- **脚本**：`compose-smoke.ps1`（新增断言段「exports」）+
  `multi-replica-drill.ps1`；`internal/api/export_test.go`。
- **关键断言**：(1) 导出的过滤条件必须与列表**完全一致**——文件是要交到别人手上的，
  调用方无法像在屏幕上那样一眼看出多给了或少给了；(2) 未知格式 400 而不是按默认
  格式给一份。
- **结果**（2026-10-03，全部符合）：
  - 单元：事件导出按 `status=FALSE_POSITIVE` 只导出该行（表头 + 1 行），Content-Type
    与 Content-Disposition 正确；审计导出按 `action=asset.create` 过滤且 `details`
    以 JSON 原样进入单元格（`"rows":3`）；`format=xlsx` / `format=pdf` 各 400；
    不带 format 时返回 JSON 且条数与列表一致。
  - smoke：事件导出 CSV 含表头与该事件、且**不含**被过滤掉的 FALSE_POSITIVE；审计
    导出可下；`format=xlsx` 400 `INVALID_FORMAT`。
- **修法摘要**：`GET /incidents/export` 与 `GET /audit-events/export`（`format=json|csv`，
  默认 JSON），复用列表的过滤解析器，绕过 keyset 分页，各记一条审计；控制台在事件页
  与审计页各加「导出 CSV」，导出的是当前过滤选中的全部。
- **合并前整体回归**（2026-10-03，`codex/data-export` @ `6ad836b`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **未覆盖**：导出在**大数据量**下的表现未实测（当前按过滤条件全量读入内存再写出，
  与既有资产导出一致；十万级记录时的内存占用未知）。

---

## 阶段一 1.5 版本与构建标识实测 — 2026-10-03

- **脚本**：`compose-smoke.ps1`（新增断言段「build identity」）+
  `multi-replica-drill.ps1`；`internal/api/version_test.go`、
  `internal/version/version_test.go`。
- **关键断言**：(1) 未认证不能拿到部署的确切版本——那是让攻击者替你挑已知问题的
  做法；(2) 没有 ldflags 注入的二进制仍要给出**真话**（dev/unknown），而不是空串
  （读起来像接口坏了）；(3) 该端点不读仓储，数据库坏掉时也要答得上来。
- **结果**（2026-10-03，全部符合）：
  - 单元：`version.Current()` 四个字段均非空；未认证 401、已登录 200 且字段完整。
  - smoke：匿名 `GET /api/v1/version` 返回 401；带会话返回的 version 与 goVersion
    非空。
- **修法摘要**：`internal/version`（链接期注入的 Version/Commit/BuildTime + Go 版本）；
  `GET /api/v1/version` 挂在受保护 mux；四个 Go 镜像的 Dockerfile 接
  VERSION/COMMIT/BUILD_TIME 构建参数，compose 从 `APP_VERSION`/`APP_COMMIT`/
  `APP_BUILD_TIME` 转发（不设置时如实自称 dev）；控制台总览页显示版本与短 commit。
- **合并前整体回归**（2026-10-03，`codex/version` @ `4a54159`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **未覆盖**：**没有实测过带真实版本号的镜像构建**——smoke 用的是默认值，因此它
  验证的是「接口可用且字段非空」，不是「`APP_VERSION=v1.2.3` 真的进了二进制」。
  release 流程（`.github/workflows/release.yml`）也未接上这些构建参数。
  ⚠️ **2026-10-12 已修正**：前半句已实测——用真实值构建四个 Go 镜像并逐个读二进制，
  `VERSION`/`COMMIT`/`BUILD_TIME` 三个串都在；后半句已修（构建参数接进 `release.yml`
  与本地 compose 的三个服务）。**仍未实测的是「按 tag 发布的产物」**。见总账
  「发布链路接通」。

---

## 阶段一 1.4 维护窗口周期化实测 — 2026-10-03

- **脚本**：`compose-smoke.ps1`（新增周期窗口往返断言）+ `multi-replica-drill.ps1`；
  `internal/detection/maintenance_window_test.go`、`internal/api/policy_schedule_test.go`。
- **关键断言**：(1) 窗口必须**原样往返**——偏移丢了就是几个小时的错位，星期丢了
  每周冻结会变成每天冻结；(2) 不匹配的窗口会让资产看起来安静而健康、实际什么都没
  查，因此非法取值必须拒绝而不是钳制；(3) 缺键不动、显式 null 清空，否则窗口能设
  不能删。
- **结果**（2026-10-03，全部符合）：
  - 单元（14 例）：窗口内/外、起含止不含、星期不匹配、不选星期表示每天、跨午夜
    两侧都命中、跨午夜归属**开始**的那一天（周三 23:00-01:00 覆盖周四 00:30，但
    不覆盖周三 00:30）、UTC+8 的周三 02:00 落在 **UTC 周二 18:30**、以及设置了偏移
    后不再匹配裸 UTC 小时。
  - API（12 例）：起止相同 / 分钟越界 / 缺 start 或 end / 星期越界 / 偏移越界
    各 400 `INVALID_POLICY`；往返保留 start/end/offset，星期**排序去重**（多选控件
    的提交顺序不该影响存储值）；显式 null 清除成功；缺键的无关更新不丢窗口。
  - smoke：PUT 一个周三 02:00-04:00（UTC+8）窗口后回读，minute 与偏移一致、星期
    为 [3]；零长度窗口 400 `INVALID_POLICY`。
- **修法摘要**：`domain.MaintenanceSchedule`（星期/起止分钟/UTC 偏移）+
  `Contains`；`inMaintenanceWindow` 改为「绝对区间或周期窗口」；策略存 jsonb，
  **本项无需迁移**。
- **合并前整体回归**（2026-10-03，`codex/maintenance-window` @ `2c08586`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **未覆盖**：调度器在真实周期窗口内的**行为**未单独演练（判定函数由单元测试覆盖，
  调度器只需调用它）；控制台的周期窗口编辑器只有 E2E 的间接覆盖。

---

## 阶段一 1.3 指派通知与超时升级实测 — 2026-10-03

- **脚本**：`compose-smoke.ps1`（新增断言段「assignment contact and the escalation
  trigger」）+ `multi-replica-drill.ps1`；`internal/api/escalation_test.go`、
  `internal/store/escalation_test.go`。
- **关键断言**：(1) 已指派必须真的通知到人，而不是只改一行数据；(2) 一条高危事件
  只上报**一次**，多副本各有定时器也不能重复；(3) 邮件地址非法要在入口被拒，而不是
  存下来看起来配好了。
- **结果**（2026-10-03，全部符合）：
  - 单元：无主 HIGH 事件在阈值后上报 1 条，第二次扫描报 0（`escalated_at` 已打标），
    审计 `incident.escalated` 恰好一行且带 `adminsNotified`；已被认领 / MEDIUM /
    已关闭 / 刚创建的四种情况下扫描均报 0；跨存储 `MarkIncidentEscalated` 两次调用
    只有第一次返回 true（memory 与 postgres 一致）。
  - 地址校验：合法与留空各 201；无 @、@ 后为空、@ 前为空、域名无点、含空格、
    **含换行（邮件头注入的形状）** 各 400 `INVALID_EMAIL`。
  - smoke：带邮箱创建用户后响应与列表都回读得到该地址；三种非法地址均
    400 `INVALID_EMAIL`；手工触发 `POST /maintenance/escalation` 返回
    `reported` 与 `unclaimedMinutes`。
- **修法摘要**：迁移 000018（`users.email`）与 000019（`incidents.escalated_at` +
  部分索引）；`Dispatcher.SendUserNotice` 走与渠道投递共用的 `sendEmailTo`；指派
  通知仅在负责人**确实变化**时发出；扫描先原子打标再发送。
- **合并前整体回归**（2026-10-03，`codex/assignment-notify` @ `db9d057`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **未覆盖**：**邮件实际投递未在实机验证**——栈里没有 SMTP 中继，测试断言的是
  「调用与记录」（审计行、打标、接口形状），不是「信件确实到了收件人」。真实 SMTP
  下的收信需人工确认。

---

## 阶段一 1.2 打印就绪报告实测 — 2026-10-03

- **脚本**：`compose-smoke.ps1`（新增断言段「print-ready reports」）+
  `multi-replica-drill.ps1`；`internal/api/report_html_test.go`；另在实机栈上取回
  生成物逐项核对渲染结果。
- **关键断言**：(1) 响应是 inline 的 HTML 且带按响应生成 nonce 的 style-src
  （脚本仍然禁止）；(2) 攻击者可控的标题必须**转义而不是丢弃**；(3) 证据只列出、
  不内嵌；(4) 汇总与 JSON 形态共用同一段聚合，对同一区间不可能给出不同数字。
- **结果**（2026-10-03）：
  - 单测：`<script>alert(1)</script>` 作标题 → 报告中不含未转义片段、含
    `&lt;script&gt;`；CSP 含 `style-src 'nonce-`、含 `default-src 'none'` 且无
    `unsafe-inline`；`Content-Disposition: inline`；未知事件 404；汇总的反向区间
    仍是 400（与 JSON 形态同源）。
  - smoke：两个报告端点 200/`text/html`，正文含标题、事件 ID、证据清单与处置时间线，
    **样式块内含 `@media print`**（见下）；汇总渲染出标题。
  - 实机核对（人工步骤，本次确有收获）：取回报告后逐项检查渲染结果，发现两个缺陷，
    均已修复并补上能拦住它们的断言：
    1. **html/template 把样式表重写掉了**：`<style>` 是 CSS 上下文，`{{ .Styles }}`
       经上下文转义后由 1.3 KB 变成 8 字节，报告完全没有样式。改用 `template.CSS`
       标注。此前测试只断言响应头里的 nonce、没断言样式内容非空，所以全绿——smoke
       现已补上内容检查。
    2. **汇总的每日趋势表每格都是 `<nil>`**：我把 buildTrend 的键猜成
       `day`/`total`/`CRITICAL`，实际是 `date`/`total` 与小写等级。已按实际键渲染，
       并加了「正文不得出现 `<nil>`」与「趋势表必须带计数」的断言。
  - 修复后实机复验：样式 1304 字节且含 `@media print`、四个板块齐全、`<nil>` 计数 0、
    趋势表 30 行且末行计数正确。
- **修法摘要**：新增 `GET /reports/{incidentId}/html` 与 `GET /reports/summary/html`；
  汇总聚合抽成 `summarizeReport` 供两个入口共用，请求级失败改为返回值
  （`reportRequestFailure`）；报告用 `html/template` 渲染，全部动态字段转义。
- **合并前整体回归**（2026-10-03，`codex/report-html` @ `a9eb67c`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **顺带修复**：`compose-smoke.ps1` 在这次改动中因缺少 UTF-8 BOM 而解析失败（中文
  进入语法位置后 PowerShell 5.1 按 ANSI 读会拆错位），同隐患存在于另外 5 个 `.ps1`。
  已为全部脚本补 BOM 并逐个做 PSParser 解析校验。
- **未覆盖**：**没有人在真实浏览器里把它打印成 PDF 看过**——本次核对是取回 HTML 后
  检查结构与文本（样式字节数、`@media print`、四个板块、无 `<nil>`）。打印分页效果
  与中文字体表现仍需人工确认。

---

## 阶段一 1.1 事件筛选与批量处置实测 — 2026-10-03

- **脚本**：`scripts/compose-smoke.ps1`（新增断言段「incident filtering and batch
  transition」）+ `multi-replica-drill.ps1`；另加两层自动化测试。
- **关键断言**：(1) 每个过滤维度都要有正例与反例——被静默忽略的过滤条件会通过正例
  而通不过反例，但要两者都在才成立；(2) 批量流转中「该行不能流转」是结果而不是
  请求错误，调用必须成功而该行返回 REJECTED；(3) 畸形过滤参数是 400 而不是静默
  返回子集。
- **结果**（2026-10-03，全部符合）：
  - 跨存储测试 `TestIncidentFilterAgreesAcrossStores`：15 组过滤（状态/风险/资产/
    负责人/关键字/半开与闭合时间区间/组合/`%` 按字面处理）在 memory 与 postgres
    两个实现上返回**同一集合**；`TestIncidentFilterKeepsTenantBoundary` 证明新增
    谓词没有挤掉租户谓词。
  - API 测试：9 种畸形过滤（未知状态、小写状态、未知风险、非 uuid 的 assetId/
    ownerId、非 RFC3339 的时间、倒置区间）全部 400 `INVALID_FILTER`；筛选在
    **翻页后依然生效**（第二页不会漏出被排除的行）。
  - 批量端点测试：混合状态一次提交 4 条（2 条可流转、1 条已误报、1 条不存在）
    → `requested=4 updated=2`，逐行给出 `UPDATED` / `REJECTED+INVALID_INCIDENT_TRANSITION`
    / `REJECTED+INCIDENT_NOT_FOUND`；空列表、未知状态、跨租户 owner 各自 400；
    超过上限 400 `BATCH_TOO_LARGE`（不截断执行）。
  - smoke：以事件**自身**的状态/风险/资产过滤都能取回它，另一个状态取不回；
    关键字无匹配返回空；3 种畸形过滤返回 400 `INVALID_FILTER`；批量重复施加当前
    状态 → `requested=1 updated=0` 且该行 `REJECTED/INVALID_INCIDENT_TRANSITION`；
    批量 ACKNOWLEDGED→INVESTIGATING → `updated=1` 且事件状态确实变为 INVESTIGATING。
  - 控制台 E2E 通过：新增的选择列会改变列索引，E2E 的标题单元格定位已从按索引改为
    按结构（`td:has(strong) strong`），否则该列只在有操作权限时渲染，按索引读会随
    角色错位。
- **合并前整体回归**（2026-10-03，`codex/incident-filtering` @ `3a4ac0b`）：
  `compose-smoke.ps1` 通过（退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（退出码 0）。
- **未覆盖**：前端筛选栏与批量按钮只有 E2E 中的间接覆盖（E2E 走的是既有路径），
  没有专门针对筛选栏交互的用例。

---

## 0.9 监测自身失效告警实测 — 2026-10-03

- **脚本**：手工实测，运行中的主 Compose 栈（分支 `codex/monitoring-health` 代码）
  + 两个一次性容器：`ops-target`（node:24-alpine，提供验证文件与页面）与
  `ops-capture`（node:24-alpine，作为 webhook 接收端，打印收到的请求体）。
  用后删除；演练资产用 API **暂停**（不删除），演练渠道用 API 删除。
- **关键断言**（路线图的验收条目是「停掉目标，连跑 N 次检测失败后确认收到通知
  渠道的告警，而不只看控制台事件」）：
  1. 目标可用时先跑一次检测建立基线并 SUCCEEDED；
  2. `docker stop ops-target` 后连跑 3 次检测，全部终态 FAILED；
  3. 第 3 次失败后 webhook **真的收到了通知**，且 run 长度正确；
  4. 第 4 次失败**不重复通知**（下一个倍数是 6）；
  5. 通知**不写 `alert_deliveries` 行**（该表是事件绑定的）；
  6. 审计留下 `monitoring.detection_failed`。
- **结果**（2026-10-03，全部符合）：
  - capture 容器收到：
    `{"event":"monitoring.detection_failed","data":{"headline":"监测失效：连续检测失败",
    "detail":"资产：ops rehearsal target\n模式：HTTP\n连续失败：3 次\n最近错误码：CHECK_FAILED\n…"}}`
  - 第 4 次失败（run=4）后 capture 的通知条数**未增加**。
  - 期间另有一条**独立**通知来自 0.7 演练遗留的资产（`sse rehearsal target`，其
    目标早已移除，调度器按 300s 间隔持续失败它），内容为「连续失败：6 次」——
    恰好验证了「每满一个阈值再提醒一次」的另一半，两条通知按资产各 1 条。
  - `select count(*) from alert_deliveries` = **0**（运维通知不建投递行）。
  - `audit_events`：`monitoring.detection_failed | FAILED |
    {"mode":"HTTP","errorCode":"CHECK_FAILED","consecutiveFailures":3}`。
  - 新指标在 `/metrics` 可见：
    `webmonitoring_last_successful_detection_timestamp_seconds 1790961589`、
    `webmonitoring_detection_checks_last_hour{result="failed"} 14`、
    `{result="succeeded"} 1`。
  - 单元测试另覆盖：run 规则（阈值 / 阈值间静默 / 下一个倍数）、取消的检测不参与
    计数、运维通知排除被熔断的那条渠道且不写投递行、熔断↔恢复两个方向的翻转
    （含冷却期探针窗口——`Open()` 在冷却结束后即返回 false，用它前后对比检测不到
    恢复，这是实现中实测踩到并修掉的坑）。
- **修法摘要**：`Repository.ConsecutiveFailedChecks` 从任务行推导尾部失败 run；
  检测服务在终态失败后按无状态规则上报；`Dispatcher.SendOperationalNotice` 承担
  无事件来源的运维消息；`Gate.RecordOutcome` 改为返回 `(open, changed)` 以报告
  翻转；迁移 000017 为自检读建两个部分索引。
- **未覆盖**：**熔断/恢复通知只做了单元验证，未在实机栈上跑通**（需要事件告警投递
  失败到阈值，本次未构造）；`/metrics` 的新指标未在真实 Prometheus 上抓取。
- **合并前整体回归**（2026-10-03，`codex/monitoring-health` @ `7fe7586`）：
  `compose-smoke.ps1` 通过（`Compose smoke test passed`，退出码 0，含控制台
  Playwright E2E）；`multi-replica-drill.ps1` 通过（`Multi-replica drill
  passed`，退出码 0）。合并后 main 快进到 `7fe7586` 所在提交。

---

## 密钥轮换渠道重封装缺陷与 pgx 安全升级实测 — 2026-10-03

- **脚本**：一次性 postgres:17-alpine 与 redis:8-alpine 容器（宿主端口 55432 /
  56379，用后删除），`POSTGRES_TEST_DSN` / `REDIS_TEST_ADDR` 指向它们跑集成测试；
  govulncheck@v1.8.0 本地实跑。
- **关键断言**：(1) pgx 升级后 `govulncheck` 退出码为 0；(2) 真实 Postgres 下
  集成测试全绿且**可重复**；(3) 密钥轮换的重封装必须真正改写告警渠道，而不只是
  政策。

### 1. 既存缺陷：Postgres 集成测试从未跑通

真实 Postgres 下 5 处失败，全部为既存（用 pgx v5.7.6 对照复现一致），修 4 处
测试侧缺陷后 `internal/store` 全绿：

| 缺陷 | 根因 |
| --- | --- |
| 指纹非法 UTF-8 | `'a'+byte(64)=0xa1`，写进 `char(64)` 列 |
| 时间断言恒不等 | fixture 用纳秒精度，`timestamptz` 只存微秒，`.Equal()` 必然不等 |
| 资产缺 owner | `assets.owner_id` 是 NOT NULL，测试没建 owner 用户 |
| 无效断言 | 断言 `GetPolicy` 在 v2-only keyring 下报错——但 `GetPolicy` 只做 JSON 反序列化、从不解密，该断言恒不成立 |

### 2. 真实缺陷：密钥轮换静默跳过所有告警渠道

- **现象**（修复前实测）：rewrap 报
  `{"channelsScanned":3,"channelsRewrapped":0,"policiesScanned":3,"policiesRewrapped":3,"alreadyCurrent":3}`
  ——政策全改写，渠道一条未动却计入 `alreadyCurrent`。
- **根因（两层）**：
  1. `ListAlertChannelsWithSecrets` 被语义相反的两个调用方共用：派发器要明文、
     轮换要密文；Postgres 实现无条件解密（内存实现返回原样存储值，两者已分叉），
     于是 `reseal` 看到无版本前缀的值、当作旧明文原样放行。
  2. `RewrapAlertChannel` 又加了一次密（`RewrapPolicy` 是原样存储），修好第 1 层
     后行内变成「密文的密文」，读回时把内层密文当成了 URL。
- **修法**：新增 `ListAlertChannelsEncrypted`（返回存储密文）供轮换读取，接口 +
  两个实现 + 运行器同步；`RewrapAlertChannel` 改为原样存储（与 `RewrapPolicy`
  一致，runner 交来的已是封装好的密文）。operations.md 新增「密钥轮换」小节，
  写明四计数判据与读不出来的回退步骤。
- **实测**：修复后同一集成测试通过——渠道被改写为 `v2:` 且 v2-only keyring 读回
  原文；`channelsRewrapped ≥ 1` 与 `policiesRewrapped ≥ 1` 同时成立。
- **连带修复**：该测试原先在共享库里运行，而重封装按设计遍历所有租户，会用本
  测试的密钥材料重封装其他包的渠道（`internal/store` 的密文测试因此报
  「key version v2 is not configured」）。改为每次创建专属数据库并在清理时删除；
  连续两次全量跑均全绿。

### 3. pgx 安全升级

- `govulncheck` 报出可达漏洞 **GO-2026-5004**（pgx/v5 占位符混淆 SQL 注入，调用链
  `TryAcquire → pgxpool.Conn.QueryRow → sanitize.SanitizeSQL`）。
- 升级 `jackc/pgx/v5` v5.7.6 → v5.11.0（修复版本 v5.9.2）后扫描归零，退出码 0；
  CI 的 govulncheck 步骤由 `continue-on-error` 改为阻断。
- 全量单测 + 真实 Postgres/Redis 集成测试通过。

- **未覆盖**：真实部署上跑一遍完整轮换（本项在一次性容器与专属测试库上验证）；
  CI 的 govulncheck/dependabot 步骤本身（推送后由 GitHub Actions 验证）。
- **合并前整体回归**（2026-10-03，`codex/rewrap-rotation` @ `7c006be`，含
  `codex/pgx-security` 与 `codex/observability`）：`compose-smoke.ps1` 通过
  （`Compose smoke test passed`，退出码 0，含控制台 Playwright E2E）；
  `multi-replica-drill.ps1` 通过（`Multi-replica drill passed`，退出码 0）。
  **构建提示**：pgx 升级使 `go mod download` 层缓存失效，而本机 Docker 构建网桥
  直连 proxy.golang.org 被拒（`connection refused`）。用 BuildKit 预定义的代理
  参数即可：

  ```powershell
  docker compose -f deployments/compose/docker-compose.yml build `
    --build-arg HTTPS_PROXY=http://host.docker.internal:7890 `
    --build-arg HTTP_PROXY=http://host.docker.internal:7890
  ```

  这只影响构建取包，不影响运行时；也无需改动 Dockerfile。

---

## 0.8 可观测性与 /metrics 鉴权实测 — 2026-10-03

- **脚本**：手工实测，运行中的主 Compose 栈（分支 `codex/observability` 代码，
  api 与 http-worker 已重建）；60 条事件由 SQL 直接灌入（55 OPEN + 5 CLOSED，
  用后删除）。
- **关键断言**：(1) 事件 gauge 此前统计「上次列表请求的那一页」，默认页大小 50
  → 超过 50 个未结事件后 gauge 饱和在 50；修复后必须等于 SQL 聚合真值。(2)
  `/metrics` 挂在无认证 root mux 且 compose 把 api 端口发布到主机，必须可用
  `METRICS_TOKEN`（Bearer）关闭，生产校验强制设置。(3) 检测任务此前只有计数器
  没有耗时直方图；且 worker 侧按 Go 错误打标签会把「检测失败但正常返回」的任务
  误标 succeeded——必须按终态（succeeded/failed/timed_out）打标签。
- **结果**（2026-10-03，全部符合）：
  - 灌入 55 OPEN（30 LOW/15 MEDIUM/7 HIGH/3 CRITICAL）+ 5 CLOSED HIGH 后，
    DB 聚合真值 55/10/3；`/metrics`（带令牌）读得
    `open_incidents 55`、`high_risk_incidents 10`、`critical_incidents 3`——
    旧实现在此场景下必然饱和在 50。
  - `/metrics` 无令牌 401、错令牌 401、正确 Bearer 200；令牌从 `.env` 移除重建
    后恢复开放（开发默认）。http-worker 的 `/metrics`（仅内部网络）同样
    401/200。
  - 任务耗时直方图 `webmonitoring_detection_job_duration_seconds` 按
    `mode`/`result` 出序列：对不可达目标触发任务，终态 FAILED 记为
    `result="failed"`（修复标签语义前同场景会被误记为 succeeded，已在代码
    审查中纠正：直方图改在 `detection.Service` 终态分支打点，用
    `job.StartedAt` 起算，重试只贡献终态一次）。
  - 连接池 `OnCollect` 指标回归无恙（`postgres_pool_max_connections 10`）。
  - govulncheck@v1.8.0 本地实跑：报 **1 个可达漏洞 GO-2026-5004**（pgx/v5
    占位符混淆 SQL 注入，修复于 v5.9.2；调用链
    `store.PostgresRepository.TryAcquire → sanitize.SanitizeSQL`）。CI 步骤以
    continue-on-error 落地（结果可见、不把已知项变全员红色），pgx 升级随紧随
    的安全提交完成并改回阻断。
- **修法摘要**：`Repository` 增 `CountOpenIncidents`（平台级、无租户参数），
  api main.go 在 `OnCollect` 里现读（3s 超时，失败保留旧值）；`listIncidents`
  不再喂 gauge，原 `updateIncidentMetrics` 删除。`METRICS_TOKEN` 经
  `Server.SetMetricsToken` + `metrics.BearerAuthorized`（constant-time 比较）
  同时罩住 api 与 worker 两个 `/metrics`；生产门禁 `validateMetricsToken`
  要求设置且 ≥16 字节。直方图在 `detection.Service` 成功/`failJob` 终态分支
  打点。dependabot.yml（gomod/npm/github-actions，月度）与 CI govulncheck
  （锁 v1.8.0）新增。
- **未覆盖**：Prometheus 真实抓取（文本格式输出已人工核对）；CI 的
  govulncheck/dependabot 步骤本身（推送后由 GitHub Actions 验证）。

---

## 0.7 SSE 流越过服务端 WriteTimeout 实测 — 2026-10-02

- **脚本**：手工实测，运行中的主 Compose 栈（分支 `codex/sse-deadline` 代码，
  api 已重建）+ 一次性 `sse-target` 容器（node:24-alpine，仅用于 FILE 验证资产，
  用后删除）。
- **关键断言**：`http.Server.WriteTimeout`（2 分钟）是**绝对写截止**，旧实现
  下 SSE 流会在该点静默失效（写错误被 `_, _ =` 吞掉，循环空转）；修复后每次
  写前续期写截止，流必须无限存活，且第 2 分钟后事件仍能送达。
- **结果**（2026-10-02，全部符合）：
  - 单测 `TestStreamEventsSurvivesPastServerWriteTimeout`：真实
    `http.Server`（WriteTimeout 压到 400ms）+ 登录 Cookie 挂
    `/api/v1/events/stream`，收到 ready 后越过 400ms 死线发布事件，**如期收到
    update**；同一测试在修复前的代码上失败（红→绿已验证，修复前报
    `no update event received after the server's WriteTimeout had passed`）。
  - 单测 `TestStreamEventsStopsOnWriteFailure`：写入即失败的 ResponseWriter
    让 handler 立即返回（订阅释放），不再在断链上空转。
  - 真实栈：`curl -N` 挂流 **约 6 分钟**（23:08:56–23:14:45），心跳每 20s
    到达且 **23:10:56（2 分钟死线）之后全部继续**（23:11:16、23:11:36…
    直至 23:14:36）；T+150s（23:11:45）与 T+330s（23:14:45）各触发一次检测
    任务，`detection.job.queued` 更新事件**均即时送达**。
- **修法摘要**：`streamEvents` 每次写前
  `http.NewResponseController(writer).SetWriteDeadline(now+30s)` 续期
  （单次写仍 bounded 30s，连接不再被绝对截止杀死）；写失败即 return
  （原 `_, _ =` 吞错循环空转）；`metricsResponseWriter` 补 `Unwrap()` 使
  ResponseController 穿透中间件包装到达连接级控制。
- **未覆盖**：浏览器 EventSource 断线重连路径（控制台侧
  `services/events/stream.ts` 已有指数退避重连，本次未模拟网络中断）。
- **合并前整体回归**（2026-10-02，`codex/sse-deadline` @ `119b17b`）：
  `compose-smoke.ps1` 通过（`Compose smoke test passed`，退出码 0，含控制台
  Playwright E2E）；`multi-replica-drill.ps1` 通过（`Multi-replica drill
  passed`，退出码 0）。合并后 main 快进到 `119b17b` 所在提交。

---

## 0.6 迁移回滚路径与破坏性标记实测 — 2026-10-02

- **脚本**：手工实测，一次性 postgres 容器（`postgres:17-alpine`，宿主端口
  55432，用后删除），本地 `go build ./apps/migrate` 出的二进制连 DSN 执行。
- **关键断言**：(1) 空库引导不需要确认（安装/CI 不受阻）；(2) 有数据的库遇
  未确认破坏性迁移必须**拒绝并点名迁移与环境变量**；(3) `MIGRATE_CONFIRM_DESTRUCTIVE=1`
  后同一库应用成功；(4) 二进制遇到库里的未知版本拒绝启动并**点名版本**，
  按 runbook 删除事故行后恢复。
- **结果**（2026-10-02，全部符合）：
  - 空库 `migrate up` 无确认变量：直接成功（16 条全应用）。
  - 把库 rewind 到 v11（删除 12–16 的迁移记录与其创建的对象）后 `up`：
    拒绝，`migration 000012_active_job_uniqueness.up.sql is marked destructive
    (-- migrate:destructive); refusing ... set MIGRATE_CONFIRM_DESTRUCTIVE=1`，
    退出码 1。
  - 设 `MIGRATE_CONFIRM_DESTRUCTIVE=1` 后同一 `up`：12–16 应用成功，
    `status` 全 applied。
  - 手工插入 `version=999` 假行后 `up`：`database contains unknown migration
    version 999`，退出码 1；按 runbook 删行后 `up` 干净退出。
  - 单测：`hasDestructiveDirective`（头部指令识别、正文提及不误判、首个
    语句后不再扫描）与已知破坏性迁移标记（000012/000015 旁车文件生效）
    通过。
- **修法摘要**：`migrationFile` 增 `destructive`；新迁移用文件头指令
  `-- migrate:destructive`，历史迁移（字节被 checksum 冻结）用旁车文件
  `db/migrations/<名>.destructive`（embed 图案同步扩为 `*.up.sql
  *.destructive`）；`Up` 语义=空库引导放行、有数据且未确认则失败关闭；
  `MIGRATE_CONFIRM_DESTRUCTIVE` 经 compose migrate 服务转发。runbook
  （operations.md「升级失败怎么办」）补「只前进不回滚」、确认流程、
  unknown version 救援三步。
- **未覆盖**：跨真实「旧镜像 vs 新库」组合（用假版本行等效验证了拒绝与点名
  路径）；down 文件的处置（维持死重，runbook 明确无人执行）。
- **合并前整体回归**（2026-10-02，`codex/migration-safety` @ `13fc47c`）：
  `compose-smoke.ps1` 通过（`Compose smoke test passed`，退出码 0，含控制台
  Playwright E2E）；`multi-replica-drill.ps1` 通过（`Multi-replica drill
  passed`，退出码 0）。合并后 main 快进到 `13fc47c` 所在提交。

---

## 0.5 备份保留、Redis 快照与调度模板实测 — 2026-10-02

- **脚本**：`scripts/backup-rehearsal.ps1`（新增），对运行中的主栈连跑两个
  备份周期（`-KeepBackups 1`，输出到一次性目录 `.data-backup-rehearsal`，
  用后删除），随后用 `compose-restore.ps1` dry-run 校验幸存备份。
- **关键断言**：(1) 第二个周期成功后旧备份目录被轮转出、且只留最新一份；
  (2) manifest 带 `redisSnapshot` 字段且 `redis/dump.rdb` 真实存在并参与
  SHA-256 校验；(3) 恢复脚本 dry-run 对幸存备份校验通过（不执行恢复）。
- **结果**（2026-10-02，退出码 0）：
  - 合并前整体回归（2026-10-02）：`compose-smoke.ps1` 通过（退出码 0）；
    `multi-replica-drill.ps1` 通过（duplicate groups 0、退出码 0）。
  - 周期 1 产出 `20261002-001630`，周期 2 产出 `20261002-001646`，轮转日志
    `Retention: kept 1 of 2 backups (KeepBackups=1)`，断言确认旧目录被删、
    新目录幸存。
  - `Redis snapshot present: redis/dump.rdb`（`redis-cli --rdb`，经
    `REDISCLI_AUTH` 传参，密码不进进程列表）。
  - `Backup verification passed`（manifest + 全文件 SHA-256），
    `Backup rehearsal passed`。
  - 四个 PowerShell 脚本 parse 检查通过（含新模板
    `register-windows-task.ps1`；实际注册计划任务未做，模板未经 schtasks
    真实注册验证）。
- **修法摘要**：`compose-backup.ps1` 增加 `-KeepBackups`（默认 14，
  `BACKUP_KEEP_COUNT` 可覆盖）按时间戳目录名轮转、`-EnvFile` 从
  `deployments/compose/.env` 静默补 S3 凭据（显式环境变量优先）、Redis RDB
  快照（拿不到不否决整份备份）；`compose-restore.ps1` 同步增加
  `-EnvFile`。新增 `deployments/scheduling/`（Windows 计划任务注册脚本 +
  systemd service/timer 模板 + README）。
- **未覆盖**：计划任务/systemd 的真实调度触发（只做了脚本内两周期连跑）；
  `-Execute` 真实恢复未在本节演练（此前验收已覆盖恢复链路）；备份期间
  高写入下的快照一致性未压测（operations.md 已要求备份前暂停大量写入）。

---

## 0.4 会话路径静默失败实测 — 2026-10-01

- **脚本**：手工实测，真实 Compose 栈（分支 `codex/auth-failures` 构建的 api
  镜像），临时账号（argon2id，用后连同审计行删除）；`docker compose stop redis`
  制造中断，`start redis` 恢复。
- **关键断言**：(1) Redis 中断期间，带有效会话 Cookie 的请求必须得到 **5xx**
  而非 401（区分「不存在」与「后端故障」）；(2) 正确密码登录在会话写不进去时
  必须 **500** 而非 401，也不返回会被下一条请求拒绝的 Cookie；(3) Redis 恢复后
  原会话直接 200——抖动不得把全员登出。
- **结果**：
  - 合并前整体回归（2026-10-02，`codex/auth-failures` 镜像）：`compose-smoke.ps1`
    通过（退出码 0）；`multi-replica-drill.ps1` 通过（duplicate groups 0、
    退出码 0）。
  - 中断前 `GET /api/v1/dashboard`：200。
  - `stop redis` 后同一 Cookie：500 `STORAGE_UNAVAILABLE`（非 401
    `SESSION_EXPIRED`）；正确密码登录：500 `SESSION_STORE_UNAVAILABLE`
    （非 401 `INVALID_CREDENTIALS`）。
  - `start redis` 后同一 Cookie 再请求：**200**（会话存活，无人被登出）；
    新登录 201。
  - 单元/接口回归：`internal/auth` 新增「Put 失败则登录失败」「Get 区分
    ErrSessionNotFound 与后端错误」（miniredis 模拟 Redis 宕机），
    `internal/api` 新增「会话后端故障 → 500」「登录写会话失败 → 500」；
    全部通过，19 包全绿。
- **修法摘要**：`SessionStore.Get` 签名从 `(record, bool)` 改为
  `(record, error)`，新增 `ErrSessionNotFound` 哨兵；Redis 实现把 `redis.Nil`
  映射为 NotFound、其余错误原样包装；`Service.Login` 的 `store.Put` 错误
  从忽略改为失败关闭；`authenticate` 中间件按错误类型分 401/500，登录
  handler 对后端故障返回 500 且不计入登录失败锁定计数。
- **未覆盖**：会话存储故障期间登出（`DELETE` 同样 best-effort）无单独断言；
    http-worker 消费端重试行为在 0.3 已实测，本次未重复。

---

## 0.3 worker 优雅停机与消费重试实测 — 2026-10-01

- **脚本**：手工实测，在真实 Compose 栈（分支 `codex/worker-shutdown` 构建的镜像）
  上加一次性 overlay（compose 内网慢目标：node HTTP 服务固定 20 秒才回响应；
  `ALLOW_PRIVATE_TARGETS=true` 仅供本网段目标可达，用完即删），检测任务经
  `POST /api/v1/detection-jobs` 用临时账号（argon2id，用后连同资产/策略/任务/
  证据/审计行一并删除）触发。
- **关键断言**：(1) http-worker 在检测在途时收到 SIGTERM，须等该任务跑完再退出，
  任务不丢、状态落库为 SUCCEEDED；(2) Redis 中断 ~10 秒，worker 打退避重试日志
  且进程不退出，恢复后仍能消费；(3) browser-worker 在快照在途时收到 SIGTERM，
  打排水日志、在途检查完成、退出码 0、不触发 60 秒强制退出；(4) 所有服务
  PID 1 为 init。
- **结果**：
  - 合并前整体回归（2026-10-01，`codex/worker-shutdown` 镜像）：
    `compose-smoke.ps1` 通过（`Compose smoke test passed`，退出码 0，含控制台
    Playwright E2E）；`multi-replica-drill.ps1` 通过（`Multi-replica drill passed`，
    40 任务、duplicate groups 0、退出码 0）。 drill 首跑失败系其复用的
    postgres 卷存着 CRLF 时期的 checksum 记录，属同一行尾问题的既有库症状，
    `down -v` 清掉一次性演练卷后通过。
  - http-worker 排水：任务 14:56:26 进入 RUNNING（慢目标 20 秒），`stop` 发送
    SIGTERM，日志 `shutdown signal received; draining in-flight work` → 约 19 秒后
    `in-flight work drained`，容器退出码 0；任务 `finished_at` 14:56:46，
    状态 SUCCEEDED。
  - Redis 中断：`stop redis` 后两条消费线各打一条
    `consumer stopped; retrying ... backoff=500ms`，进程存活；`start redis` 后
    再触发任务，正常消费至 SUCCEEDED（期间无进程重启）。
  - browser-worker 排水：BROWSER 任务 RUNNING 后 `stop`，日志
    `received SIGTERM; draining in-flight checks`，容器约 33 秒后退出码 0
    （未触发 `drain timeout exceeded; forcing exit`），任务 SUCCEEDED。
  - `docker top compose-browser-worker-1`：PID 1 为 `/sbin/docker-init`，
    node 为其子进程；api `stop` 退出码 0。
- **顺带修复（独立提交 `641ed60`）**：migrate 按原始字节校验迁移文件，
  autocrlf 曾把 worktree 副本翻成 CRLF 导致重建镜像校验和不符、migrate 拒绝
  启动。已加 `.gitattributes`（`db/migrations/*.sql text eol=lf`），并把
  schema_migrations 里 11 条按 CRLF 记录的 checksum 修回 LF  canonical 值
  （逐文件核对 worktree 字节与 git blob 一致后才更新）。
- **未覆盖**：浏览器 worker 排水期间 chromium 子进程的逐一存活清点（以容器
  整体退出、退出码 0 与 init PID 1 为证据）；api 在途请求排水的精确时序。

---

## 0.2 有界查询与任务保留实测 — 2026-10-01

- **脚本**：手工实测，在真实 Compose 栈（`deployments/compose/docker-compose.yml`，
  分支 `codex/bounded-queries` 构建的镜像）上执行：psql 灌入 100,000 行
  `detection_jobs`（`generate_series`，40 天均匀散布，初始全部 SUCCEEDED，
  其中 25,069 行早于 30 天窗口）+ 临时测量账号（argon2id，用后删除）。
- **关键断言**：(1) dashboard 在 10 万行任务下仍为聚合查询延迟；(2) 保留
  dry-run 报出的 `expiredJobs` 与窗口内行数一致且 execute 恰好删除这些行；
  (3) 调度器跨副本不重复分组；(4) 主链路 smoke 回归通过。
- **结果**：
  - dashboard（登录会话实测，20 次采样）：p50 = 1ms、p95 = 2ms，
    返回体正确；把 100 行改为 FAILED 后 p50 = 2ms、p95 = 3ms 且
    `failedJobs = 100`，证明数字来自 `FailedJobCount` 聚合而非分页内容。
  - 调度聚合查询 `EXPLAIN (ANALYZE)`（74,916 行上）：Seq Scan +
    HashAggregate，执行 13.6ms，每组一行输出；单租户场景下计划器选择
    顺序扫描是正确选择，成本由任务保留（30 天窗口）封顶，不随明细增长。
  - 保留清理：dry-run `expiredJobs=25084 deletedJobs=0`；execute
    `deletedJobs=25084` 且 `failureReasons` 为空；二次执行又清掉恰好
    3 行（种子 `created_at` 随机散布在窗口边界上的行），无多删。
  - 迁移 `000016`（`detection_jobs_reap_idx ON (tenant_id, status, started_at)`）
    已应用，`pg_indexes` 可见。
  - `multi-replica-drill.ps1`：40 资产 40 任务、0 重复分组、40 成功
    （调度器改写后跨副本语义不变）；`compose-smoke.ps1`：
    `Compose smoke test passed`，退出码 0。
- **未覆盖**：`reports/summary` 仍是全表搬运（本次未改，其成本现由任务保留
  封顶）；10 万行是在单租户单资产下灌入的，未测多租户大表下计划器是否仍选
  顺序扫描（语义不受计划影响）。
- **专项档案**：无。

## 合并后主链路回归（smoke）— 2026-09-26

- **脚本**：`scripts/compose-smoke.ps1`（`-ExecutionPolicy Bypass`；策略探测本身
  在本机不可用，故直接绕过）
- **关键断言**：脚本 `$ErrorActionPreference = "Stop"` 且全程 `throw` 即中止，
  因此「通过」等于全部断言为真：登录锁定（`ACCOUNT_LOCKED`）、keyset 分页、
  资产暂停后的 `JOB_REJECTED`、任务取消、浏览器登录态的正负控
  （无 `AuthConfig` 见登录页 / 有则见受保护页）、控制台 Playwright E2E。
- **结果**：`Compose smoke test passed`，退出码 0，容器与网络全部正常回收。
  本次是 `codex/fix-error-leak` 与 `codex/pool-timeouts` 合并进 `main`
  （`c136530`、`7f0d7df`）后的回归，覆盖前面那条「未跑 `compose-smoke`」的缺口。
- **未覆盖**：仍未做计划里 `pg_sleep(120)` 的慢查询用例（池的建连/查询边界）；
  S3 超时仍未对真实 MinIO 造过中途停住的对象。
- **专项档案**：无。

## 连接池与配置转发实测 — 2026-09-26

- **脚本**：无（手工实测；`docker compose config --format json` + 真实 Compose 栈
  上的一次性 `webmonitoring-verify`，用完即 `down -v` 拆除）
- **关键断言**：
  - 池上限对真实连接生效：给 40 路并发 `/readyz` 打压力时，用
    `pg_stat_activity` 采样应用侧连接数，上限 2 的那次峰值恰好为 2，上限 10 的
    对照那次峰值为 3（本轮压力只需 3 条，上限 10 不构成约束）。
  - 连接池是逐副本的：两个副本各自 `POSTGRES_MAX_CONNS=2`，`pg_stat_activity`
    里出现 4 条应用连接，即 2×2 —— 与「扩容副本要同步下调单副本上限」这条
    运维要求一致。
  - 生产门禁在容器内生效（`APP_ENV=production`）：`POSTGRES_MAX_CONNS=0`
    与 `ALERT_FAILURE_THRESHOLD=0` 都让进程以退出码 1 失败，且分别点名该变量。
  - `.env` 里声明的键真的进到服务：改动前三个服务的 environment 里这些键是
    `<absent>`，改动后 `rewrap` 拿到池上限，`api`/`http-worker` 拿到池上限与
    全部 8 个队列/告警键。
- **结果**：
  - 池上限：`POSTGRES_MAX_CONNS=2` → `/metrics`
    `webmonitoring_postgres_pool_max_connections 2`，采样峰值 2；对照 10 → 峰值 3。
  - 逐副本：2 副本 × 2 = 4 条应用连接。
  - 门禁 A：`invalid production configuration: … POSTGRES_MAX_CONNS must be
    positive in production: 0 leaves the pool on its own default instead of the
    limit you asked for`（退出码 1；同一次还报了 S3_ACCESS_KEY 与 sslmode，
    说明门禁把本条与既有的几条一起判了）。
  - 门禁 B：`ALERT_FAILURE_THRESHOLD must be positive in production: 0 disables
    that notification control`（退出码 1）——这正是 `.env.example` 上写的那句
    承诺，改动前那个 0 根本进不了容器（`validateAlertGate` 对空值 `continue`，
    而容器的 `os.Getenv` 只会返回空）。
  - 转发前后（`docker compose config`，shell 里给 `POSTGRES_MAX_CONNS=42`、
    `ALERT_FAILURE_THRESHOLD=9`）：改动前 `rewrap`/`api`/`http-worker` 三项
    都是 `<absent>`；改动后 `rewrap` 得 `42`，`api`/`http-worker` 得 `42` 与
    `9`，`migrate` 两项都不出现（它用自建的一次性池，有意不转发）。
- **未覆盖（不得当作已验）**：`compose-smoke.ps1` 本轮未跑；计划里那条
  `pg_sleep(120)` 慢查询用例未执行（本轮只证了池上限与逐副本，未证
  `POSTGRES_CONNECT_TIMEOUT_SECONDS` 对卡死查询的边界）；S3 超时只在单测里用
  「接受连接但永不回响应头」的 httptest 服务证过，未对真实 MinIO 造过中途停住的
  对象。
- **专项档案**：无（本条为手工实测记录）。

## 多副本（HA）演练 — 2026-09-20

- **脚本**：`scripts/multi-replica-drill.ps1`（`--scale api=2 --scale http-worker=3`）
- **关键断言**：调度器自行排队的任务数恰好等于资产数；无重复的
  `(tenant, asset, mode)` 分组；每个 worker 副本有唯一消费者身份。
- **结果**：40 资产 40 任务，0 重复分组，40 全部成功，wall clock 12.2 秒。
  同次运行的两个变异检查如期失败（`-DropActiveJobIndex` 使任务数变 66、
  重复组 26；固定 `WORKER_ID` 使三副本注册成同一名字），证明断言非空转。
- **专项档案**：设计论证见 [architecture.md](architecture.md) 多副本一节。

## 100 资产浏览器压测 — 2026-09-20

- **脚本**：`scripts/load-test.ps1 -Mode BROWSER -AssetCount 100 -Tenants 2 -HttpWorkers 3`
- **关键断言**：100 个 BROWSER 任务全部 SUCCEEDED；基线审批前 0 事件
  （无假阳性）。
- **结果**：100/100 成功，500 条证据，wall clock 85.2 秒，吞吐 1.17 任务/秒
  （下限 1.0）。
- **专项档案**：吞吐模型分析见 [operations.md](operations.md) 浏览器模式压测一节。

## 备份恢复演练 — 2026-09-15

- **脚本**：`scripts/compose-backup.ps1` + `scripts/compose-restore.ps1`
- **关键断言**：manifest 中每个文件 SHA-256 校验一致；恢复后 `/readyz` 200、
  证据 SHA-256 逐对象一致、任务数一致、Redis pending 归零。
- **结果**：1198 个受校验文件全部通过；冷启动恢复约 7 分钟，重复恢复 29 秒。
  首次恢复暴露「目标 bucket 不存在时 `mc mirror` 拒绝写入」，已修复并有
  回归保护。
- **专项档案**：[backup-restore-rehearsal.md](backup-restore-rehearsal.md)
  （完整字段表与暴露问题清单）。

## 100 资产 HTTP 压测 — 2026-09-15

- **脚本**：`scripts/load-test.ps1`（HTTP 模式，100 资产 / 2 租户）
- **关键断言**：100 任务全部 SUCCEEDED；吞吐不低于 1.0 任务/秒；基线审批前
  0 事件（无假阳性）。
- **结果**：吞吐 32.8 任务/秒，0 假阳性事件。

## 单机 Compose 验收（smoke）— 2026-09-15

- **脚本**：`scripts/compose-smoke.ps1`（含控制台 Playwright E2E）
- **关键断言**：登录锁定、keyset 分页、暂停/恢复、任务取消、控制台核心流程
  全通过。
- **结果**：通过。

---

## 尚未执行的演练

- **外部对象存储（S3）演练**：状态：部分实现 —— MinIO 已实测，外部 S3
  演练尚未执行。方案与回填步骤见
  [object-storage-rehearsal.md](object-storage-rehearsal.md)。
