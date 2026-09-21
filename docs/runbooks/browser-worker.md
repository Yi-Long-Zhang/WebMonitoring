# 运行手册：浏览器 Worker 崩溃 / Chromium 需要更新

## 症状

`/readyz` 里 `browser_worker` 报 `unavailable`；只有 `BROWSER` 模式的任务失败
（`CHECK_FAILED` / `BROWSER_UNAVAILABLE` / `CHECK_TIMEOUT`），HTTP 模式正常；
或容器反复 OOM 重启（退出码 137）。

## 影响范围（先知道代价）

**每一次 BROWSER 模式检测都同步调用这个 worker。** worker 崩溃期间，浏览器
模式的检测会一直失败到有人手工恢复它（compose 文件里对此有明确注释）。
它不参与 HTTP 模式检测，所以「HTTP 任务照常、BROWSER 任务全挂」正是这个
组件的典型指纹。

吞吐提示：**扩容 `browser-worker` 不会提高吞吐**，因为 http-worker 每个副本
一次只消费一个任务（`XReadGroup Count: 1`，同步处理），吞吐 ≈ 副本数 ÷ 单次
检测耗时。浏览器模式单次实测约 25 秒（`docs/operations.md`，2026-09-20）。

## 判断

### 1. 进程活着吗

```bash
curl -s http://localhost:8080/readyz

docker compose -f deployments/compose/docker-compose.yml ps browser-worker
docker inspect --format 'health={{.State.Health.Status}} restarts={{.RestartCount}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' \
  $(docker compose -f deployments/compose/docker-compose.yml ps -q browser-worker)
```

compose 的 healthcheck 打的是**容器内** `http://localhost:8081/healthz`：

```bash
docker compose -f deployments/compose/docker-compose.yml exec -T browser-worker \
  node -e "fetch('http://localhost:8081/healthz').then(r=>r.text()).then(console.log)"
```

worker 有两个探针，含义不同（`apps/browser-worker/src/index.ts`）：

| 探针 | 检查内容 | 不通说明 |
| --- | --- | --- |
| `GET /healthz` | 进程在监听 | 进程死了 / 端口没起来 |
| `GET /readyz` | `access(chromium.executablePath())` 能读到浏览器可执行文件 | **Chromium 缺失或被删**，进程还活着但做不了检测 |

`/readyz` 503 而 `/healthz` 200 是「Chromium 问题」的特征信号，与本手册后半
部分对应。

### 2. 崩溃还是被 OOM

```bash
docker compose -f deployments/compose/docker-compose.yml logs --tail=400 browser-worker
```

关注点：

- `exit=137` / `oom=true`：cgroup 内存限制触发。compose 给的是
  `mem_limit: 2g`、`cpus: 2`。一次检测要启动一个完整的 headless Chromium，
  而每个 http-worker 副本都可能并发调用它。
- 启动类错误（`browserType.launch` 失败、缺少共享库）：Chromium 装不住，
  见「Chromium 更新」一节。
- 401：worker 与调用方 token 不一致（见「处置 C」）。

### 3. 是 worker 挂了，还是任务在超时

```bash
curl -s http://localhost:8080/metrics | grep 'webmonitoring_detection_jobs_total'
docker compose -f deployments/compose/docker-compose.yml exec -T postgres \
  psql -U webmonitoring -d webmonitoring -c \
  "SELECT mode, status, coalesce(error_code,'') AS code, count(*)
     FROM detection_jobs WHERE created_at > now() - interval '1 hour'
    GROUP BY 1,2,3 ORDER BY 4 DESC;"
```

- `error_code = BROWSER_UNAVAILABLE`：`BROWSER_WORKER_URL` 没配或 client 不可用
  （`internal/detection/service.go`：`browser worker is not configured`）。
- `error_code = CHECK_TIMEOUT`：不是 worker 挂了，是检查超时。任务最终状态会
  是 `TIMED_OUT`。判断标准是**任务本身**而不是 worker：策略里
  `timeoutSeconds` 上限 90 秒，浏览器检查会把这个预算用两次。
- `error_code = CHECK_FAILED`：worker 返回 422 `BROWSER_CHECK_FAILED`，
  `message` 是脱敏后的原因。

### 4. 目标页是否被判为不安全目标

worker 内部有 SSRF 防护（`assertSafeTarget`）。私有地址目标需要
`ALLOW_PRIVATE_TARGETS=true`；若为 `false`，内部/私有地址目标会直接被拒。
验收脚本用的覆盖文件就是通过这个开关让 smoke 能打内部容器
（`deployments/compose/docker-compose.smoke.yml`）。**不要**为了「让任务通过」
在生产打开这个开关——那等于放开 SSRF 边界。

## 处置

### 处置 A：进程不健康 / 崩溃循环

```bash
# 取证优先：read_only 容器没有可留在磁盘上的现场，日志是唯一的
docker compose -f deployments/compose/docker-compose.yml logs --tail=400 browser-worker
```

**【改变状态】** 重启：

```bash
docker compose -f deployments/compose/docker-compose.yml restart browser-worker
```

代价很低但要说清楚：重启期间**正在执行的浏览器检测会失败**（调用方拿到错误，
任务按重试码自动重排）。worker 是 `read_only: true` + `tmpfs /tmp:size=512m`
+ `cap_drop: [ALL]` + `no-new-privileges`，**它不保存任何状态**：所有证据都由
调用方（http-worker）写入对象存储。所以重启 worker 不会丢证据。

如果重启后立刻再崩，看是不是 OOM：`mem_limit: 2g` 对一个 headless Chromium
池来说会随**并发调用数**线性吃紧。降低并发（减少 http-worker 副本）比提高
限制更安全；提高 `mem_limit` 需要重新评估宿主容量。

### 处置 B：Chromium / Playwright 更新

**先明确边界：更新 Chromium 是镜像变更，不是运行时操作。** worker 的浏览器
来自基础镜像 `mcr.microsoft.com/playwright:v1.63.0-noble`
（`apps/browser-worker/Dockerfile`），而 `apps/browser-worker/package.json`
里 `playwright` 的版本区间是 `^1.55.0`。**两处版本必须匹配**：Playwright 与它
自带的浏览器构建是绑定的，只改一处会出现「库能装、浏览器起不来」。

更新流程：

```bash
# 1. 确认当前基础镜像与库版本
grep -n "FROM mcr" apps/browser-worker/Dockerfile
grep -n '"playwright"' apps/browser-worker/package.json

# 2. 同时提升两处到同一个 Playwright 版本后重建
docker compose -f deployments/compose/docker-compose.yml build browser-worker

# 3. 【改变状态】替换运行中的 worker
docker compose -f deployments/compose/docker-compose.yml up -d browser-worker
```

更新的代价与风险：

- **【改变状态：影响检测结论】** 浏览器引擎换了会改变渲染结果、DOM、截图和
  最终 URL，从而改变差异检测的输入。基线是在旧引擎下建立的，升级后**可能
  产生一批差异**，需要有人复核是不是误报，而不是直接认定被篡改。
- 受限网络中基础镜像可能拉不下来（IPv6 被重置，重试或预先导入，
  见 [operations.md](../operations.md) 的「镜像准备」）。这是构建环境问题，
  不是应用缺陷。
- 镜像变更是需要走发布流程的动作（版本、回滚方案、验收），不要在故障中
  顺手做。

**回滚**：把 Dockerfile 的 `FROM` 与 `package.json` 的版本改回原值并重建，
`up -d browser-worker` 覆盖。回滚同样会改变检测输入，同样需要复核。

### 处置 C：token 不一致（401）

控制面用 `BROWSER_WORKER_TOKEN` 以 `Authorization: Bearer <token>` 调用
`POST /check`，worker 侧用 `WORKER_TOKEN` 校验。两者必须相同（compose 里都从
`BROWSER_WORKER_TOKEN` 注入）。

```bash
# 两侧是否同源（只比较哈希，不打印 token）
docker compose -f deployments/compose/docker-compose.yml config \
  | grep -c BROWSER_WORKER_TOKEN
```

**【改变状态】** 轮换 token 必须**同时**重启 `browser-worker` 与所有调用方
（`api`、`http-worker`），否则会出现「一半能调、一半 401」，而失败的一半只
在重试列表里安静地重试。

### 处置 D：`tmpfs` 用完

worker 的 `/tmp` 是一个 **512MB 的 tmpfs**（`size=512m,mode=1777`），
Chromium 的临时文件与 profile 都落在这里。页面越大越吃紧。如果日志里出现
写 `/tmp` 失败：

- 单页响应有上限 `maxResponseBytes`（worker 自身硬上限 10MB，
  `apps/browser-worker/src/index.ts`；策略侧 `MaxResponseBytes` 的 API 校验
  范围是 1KB–10MB，默认策略 5MB，`internal/api/server.go` 的 `defaultPolicy`）。
  但 DOM、截图和 profile 不受这个上限约束。
- **【改变状态：改配置并重启】** 提高 `/tmp` 的 `size=`（例如 1g），或降低
  并发。提高 tmpfs 占用的是宿主内存/交换，不是磁盘。

## 恢复验证

```bash
# 1. 两个探针都通过（/readyz 才能证明 Chromium 真的可用）
docker compose -f deployments/compose/docker-compose.yml exec -T browser-worker \
  node -e "fetch('http://localhost:8081/healthz').then(r=>console.log('healthz',r.status))"
docker compose -f deployments/compose/docker-compose.yml exec -T browser-worker \
  node -e "fetch('http://localhost:8081/readyz').then(r=>console.log('readyz',r.status))"

# 2. 依赖分级恢复
curl -s http://localhost:8080/readyz

# 3. 一次真实的浏览器检测跑通（这是唯一能证明端到端可用的证据）
#    可以在 smoke 栈上验证，不要拿客户生产站点做实验：
#      docker compose ... -f deployments/compose/docker-compose.smoke.yml
#    smoke 脚本覆盖了 HTTP 与 Browser 两种检测（scripts/compose-smoke.ps1）

# 4. 失败率回落
curl -s http://localhost:8080/metrics | grep 'webmonitoring_detection_jobs_total'
```

判据是**一次 BROWSER 模式检测成功并产出证据**，而不是 `/healthz` 返回 200。

## 升级/上报

- 重建镜像 / 更新 Chromium：**镜像变更，需要走发布流程**（版本、回滚、
  验收），并预期需要复核一批新增差异。
- 崩溃原因是目标页面触发的（超大页面、渲染循环、Chromium 缺陷）：按
  `AGENTS.md`，不得因为单个页面而放宽沙箱、放开 `ALLOW_PRIVATE_TARGETS`
  或提高 `maxResponseBytes` 到无条件接受任意大小。需要评估该目标是否应在
  维护窗口内降低检测频率，或单独处理。
- worker OOM 与并发相关、需要长期扩容：宿主容量规划，需要人工决策。
- 需要调整 `BROWSER_WORKER_TOKEN`、`ALLOW_PRIVATE_TARGETS`、`cap_drop`
  或 `read_only` 等安全边界：属于安全配置变更，必须先授权。
