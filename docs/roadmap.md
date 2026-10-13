# 硬化路线图与当前状态

**这一页存在的理由**：路线图原本只写在仓库外的计划文件里。后果在 2026-10-09 显形过
一次——接手的人（或 agent）得先把「还剩什么」从历史记录里重建一遍，才能知道下一步做
什么。条目清单落进仓库，人和 agent 才都能看见。

- **条目**：26 项，分三个阶段。阶段的定义与判据见
  [adr/0005-post-mvp-stage.md](adr/0005-post-mvp-stage.md)；产品边界见
  [product-scope.md](product-scope.md)。**注**：本行此前写作「22 项」，与下表行数始终对
  不上（该页落进仓库那天写的是 21 项，而表里已有 24 行）。2026-10-12 按下表实际行数重数
  ——当时 25 行（阶段 0 十项、阶段 1 六项、阶段 2 九项），同日新增 2.9 后为 26 行。旧数字
  来源不可考，**以可复现的点数为准**。
- **状态的唯一事实来源仍是 [rehearsals.md](rehearsals.md)**（append-only 的验收总账）：
  本页只做索引与状态。两者冲突时以总账为准，并修本页。
- **状态词**用 [README.md 的状态用语](README.md)词表（已实现 / 部分实现 / 规划中），
  另外区分「已记账」（总账里有实测小节）与「已合并」（在 `main` 上）。

---

## 状态总览

| 阶段 | 条目 | 主题 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| 0 | 0.1 | 连接池与查询/对象存储超时 | 已实现 · 已记账 · 已在 main | `64b01a6`；总账「连接池与配置转发实测」 |
| 0 | 0.2 | 有界查询与任务保留 | 同上 | `bee37de`；总账 0.2 |
| 0 | 0.3 | worker 优雅停机与消费重试 | 同上 | `02f79a6`；总账 0.3 |
| 0 | 0.4 | 会话路径静默失败 | 同上 | `0333e10`；总账 0.4 |
| 0 | 0.5 | 备份保留、Redis 快照与调度模板 | 同上 | `89db711`；总账 0.5 |
| 0 | 0.6 | 迁移回滚路径与破坏性标记 | 同上 | `13fc47c`；总账 0.6 |
| 0 | 0.7 | SSE 越过服务端 WriteTimeout | 同上 | `119b17b`；总账 0.7 |
| 0 | 0.8 | 可观测性与 `/metrics` 鉴权 | 同上 | `3f71ae3`；总账 0.8 |
| 0 | 0.9 | 监测自身失效告警 | 同上 | `171e964`、`12c2f7f`、`7fa4372`；总账 0.9 |
| 0 | 0.10 | 密钥轮换重封装与 pgx 安全升级 | 同上 | `0befa58`、`c711147`、`7c006be`；总账「密钥轮换渠道重封装缺陷与 pgx 安全升级实测」 |
| 1 | 1.1 | 事件筛选与批量处置 | 同上 | `3a4ac0b`；总账 1.1 |
| 1 | 1.2 | 打印就绪报告 | 同上 | `5e80cd0`、`9fb56c7`、`a9eb67c`；总账 1.2 |
| 1 | 1.3 | 指派通知与超时升级 | 同上 | `db9d057`；总账 1.3 |
| 1 | 1.4 | 维护窗口周期化 | 同上 | `2c08586`；总账 1.4 |
| 1 | 1.5 | 版本与构建标识 | 同上 | `4a54159`；总账 1.5 |
| 1 | 1.6 | 审计与事件清单导出 | 同上 | `6ad836b`；总账 1.6 |
| 2 | 2.0 | 工具链与可达依赖安全修复 | 同上 | `fd5aa83`；总账 2.0 |
| 2 | 2.1 | 本地集成测试入口 | 同上 | `f91a968`；总账 2.1 |
| 2 | 2.2 | 测试与 lint 门禁 | 同上 | `e8bec50`、`4b9d886`、`5f01afe`；总账 2.2 |
| 2 | 2.3 | 前端工程质量 | 已实现 · 已记账 · 已在 main | `274462a`…`673c2a7`，merge `27fe872`；总账 2.3 |
| 2 | 2.4 | openapi ↔ 路由漂移门禁 | 已实现 · 已记账 · 已在 main | `1981df1`、`d0fe847`、`7a7e929`，merge `c59e0bf`；总账 2.4 |
| 2 | 2.5 | 内存态有界化 | **规划中**（路线图已判定低优先，可不排期） | 见「阶段 2 剩余量」 |
| 2 | 2.6 | 依赖升级与供应链 | 已实现 · 已记账 · 已在 main（**有一处补记**） | `d54e9d1`…`f798def`，merge `c290c4b`；总账 2.6。**补记**：TS6 那一批（`f798def`）打断了 browser-worker 镜像构建，2026-10-10 才修（`04d040f`，merge `ec13fd1`）——见总账「SSE 事件流租户隔离与跨进程投递」第 4 节 |
| 2 | 2.7 | 结构治理（拆 `server.go`、拆 `Process`） | **规划中**（可选，收益是维护速度而非正确性） | 见「阶段 2 剩余量」 |
| 2 | 2.8 | 对象存储镜像的供应链 | 已实现 · 已记账 · 已在 main | `c2f1be1`、`ab18f43`，merge `d63a1be`；五个判据逐条见 2.8「结果」，总账「对象存储镜像下架与替换」「CI 首次全绿」 |
| 2 | 2.9 | 发布链路（`release.yml`）与首次运行 | 已实现 · 已记账 · 已在 main（**工作流本身尚未真实运行过**） | `7ba1839`、`3c3f44b`、`824c3b2`；见 2.9 与总账「发布链路接通」 |

另外两件不在路线图条目里、但属于「已交付」的事实：0.1 与 0.10 的总账小节是**主题式
标题**（不带编号），所以按编号检索总账找不到这两节；表里的对应关系是核出来的。

**另有一次不在上表条目内的工作**（2026-10-10）：一次全面审计核出的 4 条缺陷与 1 处文档
overclaim 已全部修复并合并——1.0 browser-worker 镜像构建被 TS6 打断、1.1 SSE 事件流的租户
隔离与跨进程投递、1.2 浏览器 Worker 的 SSRF 地址判定、1.3 改密码吊销其余会话、1.4 截图证据
的读回上限。对应总账两节：「SSE 事件流租户隔离与跨进程投递」与「阶段一缺陷清零」。

**又一次不在上表条目内的工作**（2026-10-13）：租户 bootstrap 的一处缺陷——`createTenant`
把初始管理员密码的下限写成了 8，而 `HashPassword` 要求 12，于是 8–11 位的密码会**在租户行
已经写库之后**才失败，留下一个没有管理员、**且无法从 API 修复**的租户（租户管理员只有
bootstrap 这一条来路）。已修：下限收进 `security.MinPasswordLength` 一处，两行改为一次写
（Postgres 走事务）。对应总账「租户 bootstrap 缺陷」。

---

## 阶段 2 的剩余量（精确到可执行）

### 2.5 内存态有界化 — 未开始，路线图判定可不排期

三处内存实现都只有**惰性过期**（读到已过期的键才顺带删除），没有任何周期清理：
`internal/ratelimit/ratelimit.go`、`internal/auth/limiter.go`、`internal/notifygate/store.go`。
判据是**单机 Compose 下这三条路径不可达**（`REDIS_ADDR` 非空时一律走 Redis 实现，
`deployments/compose/docker-compose.yml` 恒设它）。

### 2.6 依赖升级与供应链 — 已完成（TypeScript 停在 6.0.3，7 被工具链阻塞）

升级后的状态：`go list -m -u all` 的**直接**依赖已无待升项；前端四个大版本已升到能升的
最新。详见总账「阶段二 2.6」。

- **已就位**（属于 0.8 与 2.0 的产物）：`.github/dependabot.yml`（gomod / npm /
  github-actions，月度）；CI 里**阻断式**的 `govulncheck`（锁 `v1.8.0`）。
- **本轮升的**：Go 侧 `redis v9.23.0`、`minio-go v7.3.0`、`cascadia v1.3.5`、OTel 全家
  `v1.47.0`；前端 `vite 8.3.4`（Rolldown）、`vitest 5.0.3`、`vue-router 5.4.0`、
  `typescript 6.0.3`（+ `vue-tsc 3.3.12`）。
- **TypeScript 7 升不了，有两道独立阻塞**（都已取证，见总账 2.6 第 2(c) 节）：
  vue-tsc（最新 3.3.12）在 TS 7 下**连启动都不行**（TS 7 移除了程序化 Compiler API，
  `./lib/tsc` 不再导出），而 `typescript-eslint` 的 peer 上限是 `<6.1.0`。
  等这两条工具链支持原生编译器后再升。
- **刻意留给 dependabot 的**：其余 patch/minor（element-plus、vue、eslint 等）——月度批次
  就是既有节奏。
- **需要单独一项**：`playwright` 1.63 → 1.64 与 browser-worker 的镜像标签
  `mcr.microsoft.com/playwright:v1.63.0-noble` 耦合，要连 Dockerfile 一起升，否则会出现
  「包是 1.64、浏览器是 1.63」的静默错配。
- **补记（2026-10-10）**：本轮有一批**只过了前半段门禁**。五个批次里，只有 TypeScript 6 那一批
  （`f798def`）的门禁没列 `compose-smoke`，而它正是打断了 browser-worker 镜像构建的那一批
  ——`tsconfig.json` 的 `include` 含 `test/**/*.ts` 且无 `rootDir`，而 Dockerfile 只 `COPY src`；
  TS 6 把由此产生的公共源目录不一致从静默调整升级成硬错误 `TS5011`。本地因为有 `test/` 所以
  永远绿，**直到 2026-10-10 跑 compose-smoke 才暴露**（修：`04d040f`，merge `ec13fd1`）。
  本节上面的「升级后的状态」因此只在「不含镜像构建」的意义上成立。详见总账
  「SSE 事件流租户隔离与跨进程投递」第 4 节。
  **可复用的教训**：依赖升级里受影响的不只是运行时代码，还有**镜像构建的输入集合**；门禁要
  覆盖到「构建产物」，而不只是「类型与测试」。

### 2.7 结构治理 — 未做（可选）

`internal/api/server.go` 仍是单文件（3526 行，含 68 条路由与 38 个 handler）。
`internal/detection/service.go` 的 `Process` 仍是单函数。拆分的收益是后续维护速度。

### 2.8 对象存储镜像的供应链 — 2026-10-12 新立，阻塞投产

**现状：2026-10-12 当日完成替换并在本机端到端验收，随后推送（`ab18f43`）由 CI 的干净
runner 结清判据 1 与 5，四门全绿后合并回 `main`。五个判据全部已做，见本节末「结果」。**

**这是一条投产阻塞，不是测试基础设施问题。** `deployments/compose/docker-compose.yml:139`
与 `:171` 把对象存储与其客户端按 digest 钉死在 **quay.io**，而这两个仓库已不再对匿名读者开放：

| 探测路径 | 结果 |
| --- | --- |
| CI 干净机器 `docker compose up` | `minio Error unauthorized` |
| 本机 `docker pull quay.io/minio/minio@sha256:14cea493…` | `401 UNAUTHORIZED` |
| quay.io 注册表 API（匿名 token 已签发） | manifest `401`；tag 列表 `{"detail": "Requires authentication"}` |
| Docker Hub `minio/minio`、`minio/mc` | 仓库 API `{"message": "object not found"}`；manifest `401` |
| `dl.min.io` 的二进制 | **HTTP 410 Gone** |

上游 README 已声明 community edition **只发源码**、不再提供预编译二进制；而它自带的
`Dockerfile` 第一行是 `FROM minio/minio:latest`——「照指引自己 build」是循环依赖。GitHub
Releases 里最新的 `RELEASE.2025-10-15T17-29-55Z`（标题即 `Security/CVE RELEASE`）**assets=0**。

**为什么一直没被发现**：本机 `docker images` 里那两份是 **13 个月前**拉的缓存，所有本地演练
都跑在缓存上，**从未验证过一次拉取**。「本机跑通」与「干净机器跑通」在这里是两件事。

**下一步的判据**（选定服务后按此验收，不按「CI 变绿」验收）：

1. `compose-smoke` 与 `multi-replica-drill` 两个作业在 GitHub 上**真正执行完并全绿**——
   它们此前的全部记录是 `skipped`（30 次 / 28 次），本次才第一次被执行；
2. `scripts/compose-backup.ps1` 与 `scripts/compose-restore.ps1` 依赖的 `mc` 客户端有替代
   （两者同源，一起拿不到）；
3. 健康检查、凭据变量名（`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`）、console 端口等
   MinIO 专有假设一并迁移或明确保留；
4. `docs/operations.md` 的「镜像准备」清单，与 `docs/runbooks/object-storage.md`（整篇都是
   MinIO 专有假设：`minio/health/live` 健康端点、`mc` 客户端、`MC_HOST_*` 写法）同步更新；
5. 在一台**没有镜像缓存**的机器上验证一次完整 `docker compose up`。

**已排除的一条**：把本地那份冻结的旧二进制自建镜像推到自己的注册表——它拿不到 2025-10-15
那个 CVE 补丁，对安全产品是净降级。**已验证公开可拉**的候选是 SeaweedFS
（`chrislusf/seaweedfs`）与 VersityGW（`versity/versitygw`）；Garage 的 Docker Hub 仓库只发
commit-hash 标签，作为候选需要额外确认发布节奏。

### 结果 — 2026-10-12

选型、实测与回滚见 [adr/0007-object-store-replacement.md](adr/0007-object-store-replacement.md)。
一句话：服务端换成 **VersityGW v1.8.0**（Apache-2.0，`posix` 后端，证据是卷里的普通文件），
备份客户端 `mc` 换成 **rclone 1.75.2**，服务名 `minio` → `objectstore`。逐条对账上面五条判据：

| 判据 | 状态 |
| --- | --- |
| 1. 两个作业在 GitHub 上真正执行完并全绿 | **已做**——推送 `ab18f43` 后 run `38206727165` 四门全绿，两个演练作业首次执行到底（`Compose smoke test passed` / `Multi-replica drill passed`） |
| 2. `mc` 有替代 | **已做**——rclone；四条语义等价值每条都带对照实验（见 ADR「与 MinIO 的语义差异」） |
| 3. MinIO 专有假设迁移或明确保留 | **已做**——healthcheck 改为读原始 HTTP 状态行（VersityGW 无专用健康端点，`/health`、`/healthz` 均 404）；凭据变量名**有意保留** `S3_ACCESS_KEY`/`S3_SECRET_KEY`（`.env` 契约不变，单账户模式下 VersityGW 从 `ROOT_ACCESS_KEY`/`ROOT_SECRET_KEY` 取）；**console 端口 9001 取消**而非迁移——VersityGW 不提供控制台 |
| 4. `operations.md` 与 `runbooks/object-storage.md` 同步更新 | **已做**——runbook 整篇重写 |
| 5. 在一台**没有镜像缓存**的机器上验证一次 | **已做**——CI 的 runner 就是那台无缓存的机器，两个演练作业的日志里都有 `objectstore Pulling` → `objectstore Pulled` |

判据 1 与 5 是同一个实验的两面，都只能由推送后的干净 runner 完成，本机无法替代——**2026-10-12
推送后两者同时结清**（run `38206727165`，见总账「CI 首次全绿」）。判据 5 恰恰是当初让这条
缺陷藏了 13 个月的那一条，「本机跑通」不能代替它，现在也不需要代替了。

本地端到端验收已过（数字以脚本实际输出为准，记在总账「对象存储镜像下架与替换」）：
`compose-smoke`、备份演练、恢复演练三条均退出码 0，且对象存储的读写路径走的是仓库自己的
代码（`internal/evidence`），不是旁路脚本。

### 2.9 发布链路（`release.yml`）— 2026-10-12 接通，首次运行待做

**代码已在 `main` 上，而工作流本身一次都没跑过。** 这一项含三件事：把构建标识接进
`release.yml`（此前它从不传 `build-args`，按 tag 发布的镜像会自称 `dev`）、把矩阵漏掉的
`rewrap` 补上（`deployments/compose/docker-compose.yml` 里它是正式服务，运维手册直接让
运维 `docker compose run --rm rewrap`）、以及修掉两处上路前才读出来的缺陷（`migrate` /
`rewrap` 的窄式 COPY 列表缺 `internal/version`；四处拼出的 GHCR 镜像名带大写，docker 连
解析都不接受）。经过、逐行证据与 §8 规则对照见总账「发布链路接通」。

**待做的判据是产物，不是「workflow 绿了」**：

1. **推一个 `v*` tag，让工作流第一次真实运行**——这是唯一能验证它的方式，也是
   「产物与发布」那条未实测项的收口处。
2. 四个 Go 镜像的二进制里确实带着这次发布的版本 / commit / 提交时间，`api` 的
   `GET /api/v1/version` 报的与 tag 一致。
3. `cosign verify` 验得过（模板在 `operations.md`「发版」一节）。
4. SBOM 与 provenance 取得出来（`docker buildx imagetools inspect`）。
5. **把 GHCR 上的 package 改成 public**——首次创建默认私有，未登录的机器拉不到。
   网页操作，工作流代劳不了。
6. **可选、本轮未做**：给六个有 `build` 的服务写一份 `image: …@sha256:…` +
   `build: !reset null` 的 overlay，让整套栈跑在**发布产物**上。代价是要改
   `scripts/compose-smoke.ps1`（它把 `-f` 写死在开头，`up -d --build` 还会强制本地构建）。
   在那之前，「本地 `docker compose up` 跑通」不等于「发布产物跑通」——与 2.8 判据 5
   同类的区别。

**同批登记、本轮不做**：只出 amd64（无 `platforms:`、无 QEMU）；没有任何 GitHub Release
或可下载产物（`contents: read`，也没有 release 步骤），所以 §8 要求的「迁移顺序与回滚
策略」「配置与密钥变更清单」在 tag 上**没有载体**；action pin 是浮动 tag
（`actions/checkout@v4`、`aquasecurity/trivy-action@0.28.0` 等），与本仓库对对象存储镜像
按 digest 钉死的做法不一致；`release.yml` 没有 `concurrency` / `timeout-minutes`
（`ci.yml` 的长作业有 60 分钟上限）；`.dockerignore` 是黑名单式，漏了 `http-worker.exe`
一类的本机产物（进构建上下文与缓存，不进镜像）。

---

## 从未实测的场景

总账里累积的「未覆盖」条目，去重归并后是这些（详细断言与结论见各节）。**注**：本页此前
写作「36 条」；2026-10-10 按各节末尾的 `### 未覆盖` 逐条点数，共 **22 条**（当日新增 10 条）
——两个数字对不上，旧数字的来源已不可考，**以可复现的点数为准**。**22 这个数只对应
2026-10-10 那一刻**：总账是 append-only 的，此后每加一节都可能多一条，所以它只会往上走；
要当前数字就按同一算法重数，不要引用这里。

- **外部依赖实机**：外部 S3（bucket 创建、预签名 URL、真实读写）、真实 SMTP 收信、
  Prometheus 真实抓取。
- **产物与发布**：**2026-10-12 已实测一半**——真实版本号注入四个 Go 镜像并逐个读二进制
  确认三个串都在，构建参数也已接进 `release.yml` 与本地 compose（见总账「发布链路
  接通」）。**仍未实测的是「按 tag 发布的产物」**：tag 从未推过，推镜像、`cosign` 签名、
  Trivy 扫描、SARIF 上传全部是纸面。
- **报告**：在真实浏览器里把报告打印成 PDF（分页与中文字体）。
- **CI 门禁自身**：**2026-10-12 已实测**（SHA `ffbd2e1`，run `38198384564`）。`-race`、覆盖率
  下限 55% 与 golangci-lint v2.14.0 在 CI 上通过，且 `Configure integration test services`
  步骤成功、postgres:17/redis:8 容器确实在场——但两个演练作业**本次是史上第一次被执行**，
  且立即因对象存储镜像下架而失败：作业内的断言**一条都还没验证过**。见 2.8 与总账
  「CI 门禁首次诚实运行与对象存储镜像下架」。**同日稍后已整条收口**：下架的镜像替换后分支
  推送（`ab18f43`，run `38206727165`），四个作业全绿，**两个演练作业第一次真正执行到底**，
  作业内的断言被真实执行过（日志原文见总账「CI 首次全绿」）。该现象连同 2.8 判据 1／5
  一并结清。
- **数据库边界**：`pg_sleep` 慢查询与连接池边界、S3 中途停住的对象、迁移「旧镜像 vs
  新库」的真实组合、多租户大表下的查询计划。
- **导出与数据量**：导出在十万级记录下的内存占用。
- **调度与停机**：真实周期维护窗口下的调度行为、浏览器 worker 排水期 chromium 逐一
  清点、api 在途请求排水的精确时序。
- **备份与调度**：计划任务的真实触发（schtasks/systemd）、高写入下的快照一致性。
- **密钥轮换**：真实部署上跑一遍完整轮换（在一次性容器与测试库上验过）。
- **Redis 事件投递的断线恢复**：`internal/events` 的 relay 在真实 Redis 上只跑过「两个 relay
  互投 + 租户隔离 + 回显去重」的正向用例，**没做「停掉 Redis 再恢复」的时序演练**。
- **阶段一缺陷清零带出的**（2026-10-10）：截图的 10–11.25 MiB 窗口没有端到端复现（那张尺寸
  造不稳）；控制台新增的「截图范围」一行从未在浏览器里显示过（smoke 的页面太小、走 FULL 分支）；
  `internal/auth` 没有真实 Redis 的集成用例（全部走 miniredis）；密码指纹的升级效应（现存会话
  一次性失效）只有单测钉住，没有在一次真实升级里观察过。
- **浏览器路径的 DNS Rebinding**：地址判定已于 2026-10-10 与 Go 侧对齐，但「先解析校验、再让
  Chromium 自己解析」之间的 TOCTOU 窗口仍在（`security-model.md` 标为
  `状态：部分实现 —— 未把连接钉到已校验 IP`）。

一处**已复现但根因未查清**的现象，单列：`multi-replica-drill` 的 wall clock 曾三次落在
240s 附近（243.2s / 244.1s / 234.5s），而单个任务只需毫秒级；延迟出现在任务创建前的
调度器排队阶段。预算已放宽到 900s（不削弱它验的不变量），但若再观察到接近 900s 的排空，
应按真故障处理。**2026-10-10 复跑得 8.2s，平台期没有复现**——所以这个现象是**间歇的，不是
恒定的**，单次 8.2s 也不足以说根因消失（见总账「SSE 事件流租户隔离与跨进程投递」第 5 节）。
**2026-10-12 又得两次**：本机 28.4s、CI 上 3.6s，平台期连续三次缺席，「240s 是常态」可以
否掉；根因仍未查清，仍按间歇现象对待（见总账「CI 首次全绿」）。

---

## 已知文档漂移

**2026-10-10 已全部清理**（`codex/docs-refresh`），逐条如下——保留记录是为了说明
「查过什么」，不是还没做：

1. `docs/operations.md` 曾把 `golangci-lint` 的版本写死为 `v1.64.8`（实际 CI 用的是
   `v2.14.0`，且那个旧版本读不懂新版 Go 的导出数据、其实什么都没检查过）。现在文档
   **不再抄写版本号**，改为指向 `.github/workflows/ci.yml`。
2. `README.md` 与 `docs/README.md` 曾写「最近一次为 2026-10-03」且只提阶段 0/1。
   现已是 2026-10-09/10 与阶段二的 2.0–2.6，并指向本页。
3. `docs/adr/0005-post-mvp-stage.md` 的背景段引用了已经改掉的旧口径——ADR 是历史记录
   不追溯改写，但**已就地加了一句提示**：那三处已在同批改动中同步，现行口径以文件为准。
4. 外部 KMS 的「部分实现」三处措辞不一，`docs/product-scope.md` 更是在同一句里先称
   「再加密已实现」又称「尚未执行」。现在三处统一为一句话，并写清真实状态：扫描与执行
   路径都已实现并在一次性容器上验证，**尚未在真实部署的数据上执行过**。

顺带补上的（同属「文档落后于进度」）：`README.md` 的「验证」一节此前完全没提 Go 的
lint——它需要单独安装且必须与 CI 同版本，现在写明了，版本同样指向 `ci.yml`。

## 维护方式

一个条目完成后：**先在 [rehearsals.md](rehearsals.md) 追加实测小节**（数字以脚本输出
为准），再更新本页对应行的状态与证据。两者放在同一次提交或相邻提交里。状态从「已记账」
走到「已在 main」，以 `git merge-base --is-ancestor <分支> main` 为准，不凭印象。
