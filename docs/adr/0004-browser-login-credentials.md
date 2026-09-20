# ADR-0004：浏览器模式登录态检测的凭据传递

状态：Accepted
日期：2026-09-20

## 背景

登录态检测目前只在 HTTP 路径可用：控制面用 `security.SecretCipher` 加密客户凭据
（AES-256-GCM，关联数据 `AuthAssociatedData(tenantID, assetID)`，即
`tenantID:assetID:auth`，密文以无填充的 base64 RawURL 编码），检测时由
`internal/detection/checker.go` 解密，在写入和检测两个阶段各做一次 SSRF 校验。

浏览器路径在 `internal/browser/client.go` 明确失败关闭：遇到 `AuthConfig` 直接返回
「login-state detection is only supported in HTTP mode」，任务归为可重试的
`CHECK_FAILED`，重试耗尽后失败。因此「后台登录页监测」这个核心场景在浏览器渲染模式下
不可用。

开启它必须先回答一个问题：**明文凭据如何到达 Node 侧？** 控制面目前从不把明文凭据
发给任何进程，`SecretCipher` 只在 api 和 http-worker 内实例化；浏览器 Worker 走同步
HTTP 调用，compose 只给它注入了 `WORKER_TOKEN`。

## 决策

控制面解密后，通过内部 HTTP 请求体把明文凭据传给浏览器 Worker。

配套约束：

- 生产配置门禁要求 `BROWSER_WORKER_URL` 为 `https://`，或解析到私网地址。凭据只允许
  在受控的容器网络或 TLS 通道上传输，禁止明文跨公网。
- 凭据只出现在请求体中，不得出现在 URL、查询参数、Header 或任何日志字段。
- Worker 的响应体、错误消息、`blockedRequests` 和截图元数据都不得包含凭据；Go 侧复用
  `internal/security/redact.go` 的 `RedactString` / `RedactAny` 做兜底脱敏。
- 登录地址在 Worker 侧独立跑一次 `assertSafeTarget`，`allowPrivateTargets` 语义必须与
  Go 侧 `URLGuard` 一致，不能因为控制面已校验过就跳过。

## 原因

- 密钥材料只留在控制面。主密钥仍只有一个持有者，轮换时不需要协调第三个服务。
- Node 侧不需要实现任何密码学。跨语言复现 AES-GCM 是本方案最大的替代风险（见下）。
- 传输面是受控的：Worker 与 api 同在 Compose 的私有网络内，且 Worker 已经容器隔离
  （只读根文件系统、`cap_drop: ALL`、`no-new-privileges`、内存和 CPU 限制），
  生产门禁再收紧传输协议即可覆盖剩余风险。

## 未采用的方案

### 把 `ALERT_SECRET_KEY` 注入浏览器 Worker，由 Node 侧自行解密

不采用。明文不上网线，但代价更大：

- 主密钥从两个服务扩散到三个，任何一处泄露的影响面都扩大。
- 需要在 TypeScript 里精确复现 Go 的 AES-256-GCM：同样的关联数据拼接、同样的
  无填充 base64 RawURL、同样的 nonce 前置布局。任何一处不一致都不会报错，而是表现为
  解密失败或错误解密，且只在生产凭据上暴露。
- 同一把 `ALERT_SECRET_KEY` 还被 `security.NewSignedURLSigner` 用作 HMAC 密钥，
  密钥轮换要同时协调三个服务和两种用途。

### 由浏览器 Worker 直连数据库或对象存储取凭据

不采用。这会把租户隔离的校验点搬到 Worker 侧，而 Worker 是处理不可信网页内容的进程，
不应持有数据访问凭据。

## 后果

- 浏览器 Worker 的 `/check` 请求体新增 `auth` 字段，Go 侧 `internal/browser` 的请求
  结构与之同步；`api/openapi.yaml` 中 `AuthConfig` 的形状不变，因此前端生成类型不需要
  因本决策改动。
- 「使用客户凭据进行登录态检测」属于 AGENTS.md 列明的高风险操作，必须保持可审计：
  策略写入阶段的审计记录已经覆盖，浏览器路径不得新增绕过它的写入点。
- 生产门禁新增一条约束，`BROWSER_WORKER_URL` 不满足时启动即失败（失败关闭）。这是对
  既有部署配置的收紧，升级时需要先确认该变量。
- 浏览器 Worker 的测试目前只有 `security.test.ts`，需要补登录流程用例；Go 侧
  `internal/browser/client_test.go` 需要补凭据透传与失败关闭的负向用例。
