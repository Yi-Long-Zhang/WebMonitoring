# 验收演练总账

本文是全部实测结果的**唯一事实来源**（append-only）。README 和 docs/README 的
实测速览只保留一行摘要并指向这里；`architecture.md` 与 `operations.md` 内嵌的
实测输出是设计论证证据，原地保留。

**追加约定**：每次演练后在下方新增一节，按时间倒序（最新在最前），不修改、
不删除历史小节。每节固定字段：日期、脚本、关键断言、结果、专项档案（如有）。
数字以脚本实际输出为准，不四舍五入、不补估算值。

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
