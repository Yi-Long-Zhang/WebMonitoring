# 快速部署：一页搭到能干活

单机 Compose，从零到「跑完一次检测、看到证据」。**只到能干活为止**：TLS、生产
门禁、备份恢复、升级回滚、交付前清单都不在这一页，见 [operations.md](operations.md)
（部署升级 / 备份恢复 / 密钥轮换 / TLS 终止）与 [runbooks/](runbooks/README.md)
（出故障时）。

> 2026-10-13 之前的完整部署手册（主机规格、生产加固、TLS、备份与恢复、交付前清单）
> 已移出本页，全文在 git 历史里：`git show 599b04f:docs/deployment.md`。

## 前置

- **Docker + Compose v2**：`docker compose version` 要答得上来。
- **4 核 / 8 GiB 起**：设了内存上限的七个常驻服务相加是 8.25 GiB（是上限，不是
  实测峰值）。
- **主机上空出 8080（API）与 8088（控制台）**。数据库、Redis、对象存储有意不发布
  端口。
- **首次构建要能拉基础镜像**（golang / node / nginx / postgres / redis / versitygw /
  distroless / Playwright）。拉不到 `proxy.golang.org` 时，在 `.env` 里加一行
  `GOPROXY=https://goproxy.cn,direct`——它只影响构建。

## 1. 取代码，写 `.env`

```powershell
git clone <仓库地址> WebMonitoring
cd WebMonitoring
Copy-Item .env.example deployments\compose\.env
```

`.env` 必须落在 `deployments/compose/`：`-f` 指向那个目录，Compose 就去那里找
`.env`；留在仓库根它看不见，栈会因为变量缺失起不来。

**然后改一行。** 模板里六个被强制要求的变量都带值，只有 `ALERT_SECRET_KEY` 是
「照抄就跑不起来」的那一个——它必须是 base64 的 **32 字节**，占位值会被拒绝：
`http-worker` 拿到它直接退出并反复重启（API 只打一行警告，然后不带登录态检测继续
跑）。生成一个，填进 `deployments/compose/.env` 的 `ALERT_SECRET_KEY=`：

```powershell
$b = New-Object byte[] 32
[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
[Convert]::ToBase64String($b)
```

（Linux/macOS 用 `openssl rand -base64 32`。）

其余值先照用模板：`ADMIN_PASSWORD=change-me-before-production` 是你一会儿的登录
口令，`APP_ENV=development` 就是这个形态。

## 2. 起栈

```powershell
docker compose -f deployments/compose/docker-compose.yml up -d --build
docker compose -f deployments/compose/docker-compose.yml ps    # 除 migrate 外都在跑
curl.exe http://localhost:8080/readyz                          # {"status":"ready"}
```

第一次要几分钟（四个 Go 镜像 + 控制台 + Playwright 底座）。`migrate` 跑完就退出，
显示 `Exited (0)` 是正常的；非 0 才是问题。

## 3. 登录

打开 `http://localhost:8088`：账号 `admin`，口令 = `.env` 里的 `ADMIN_PASSWORD`
（模板值 `change-me-before-production`），「租户」留空。进去先做一件事：右上角
用户名 →「修改密码」（至少 12 位）。

## 4. 让它干活

**只检测你已获授权的站点。** 没完成所有权验证的资产不会进入检测——这是门槛。

1. **资产中心 →「登记资产」**：名称、监测地址（端口只能是 80/443）、类型、站点。
2. **行内「授权验证」**：弹窗给出 30 分钟有效的令牌。控制台建出来的资产一律是
   `FILE` 方式——在目标站点**根**下建 `/.well-known/webmonitoring-verification.txt`，
   内容就是令牌值；放好后点「已完成，开始验证」。
3. **行内「HTTP 检测」**：到「检测任务」页看它跑完。
4. **基线审批 →「批准」**：首次检测只产生**候选**基线，不批准就永远不参与篡改
   判断，后面也永远看不到差异事件。
5. **改一下目标页面**（比如改一行标题），再点一次「HTTP 检测」→ **事件中心**出现
   差异事件，**证据中心**有那一轮的快照。恢复/关闭/误报都要填处置说明。

想先拿本机自测：把 `.env` 的 `ALLOW_PRIVATE_TARGETS` 改成 `true`（验收脚本就是这么
做的），就能检测私网地址。**生产上必须是 `false`。**

## 卡住了

| 现象 | 先看这里 |
| --- | --- |
| `http-worker` 反复重启 | 第 1 步：`ALERT_SECRET_KEY` 不是 base64 的 32 字节 |
| `docker compose config` 报某变量没设 | `.env` 不在 `deployments/compose/` |
| `migrate` 非 0 退出 | `docker compose -f deployments/compose/docker-compose.yml logs migrate` |
| 检测报目标地址不允许 | 只接受 http/https 的 80/443；私网/回环默认拒绝 |
| 授权验证 422 失败 | 令牌 30 分钟过期，重新点「授权验证」；或文件不在站点根 |
| 有依赖不 ready | `curl.exe http://localhost:8080/readyz` 会点名是哪一个 |

上生产之前：`APP_ENV=production` 会启用一整套启动门禁（默认口令、`SESSION_SECURE`、
`POSTGRES_DSN` 的 `sslmode`、凭据强度……），再加上 TLS、备份、升级与回滚——按
[operations.md](operations.md) 做，故障处置进 [runbooks/](runbooks/README.md)。

**这一页的实测边界**：命令与界面文案逐条核对过（`compose-smoke` 与多副本演练在无
镜像缓存的 runner 上跑通、本机演练、文档里的命令原样执行），但「照这一页从零做
一遍」没有被单独演练过。第 1 步关于 `ALERT_SECRET_KEY` 的结论是 2026-10-13 用探针
实测的：占位值被 `security.NewKeyring` 拒绝，`http-worker` 因此退出。
