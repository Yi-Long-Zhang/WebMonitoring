# 外部对象存储演练

## 状态：**尚未执行**

本演练需要一组真实的外部 S3 兼容凭据和一个已存在的 bucket。**2026-09-20 未执行** ——
编写它的环境既没有外部对象存储的凭据，也无法访问外部网络。因此
`docs/README.md` 中「外部对象存储尚未验证」的说法**保持有效**，直到有人按下面的步骤
实际跑通并回填结果。

已在本机验证的部分：`NewS3Store` 对畸形 endpoint 的拒绝
（`internal/evidence/s3_test.go`）。创建 bucket、预签名 URL 和真实读写**未**验证。

## 为什么单独演练

Compose 里的 MinIO 与 AWS S3 在大面上等价，但在三条路径上行为可能不同，而这三条
都在关键路径上：

1. **创建 bucket**。`NewS3Store` 会尝试 `MakeBucket`；最小权限策略通常没有
   `s3:CreateBucket`。此时走的是「竞态回退」分支 —— 重新检查 bucket 是否存在，
   存在就继续。**这里的失败是启动失败，不是检测失败**，所以必须在部署前确认。
2. **区域解析**。不设 `S3_REGION` 时 minio-go 用 `GetBucketLocation` 发现区域，
   代价是多一次往返，且策略必须允许该操作。为此本仓库已支持 `S3_REGION`
   （`internal/evidence/s3.go` 的 `S3Options.Region`），演练应在固定区域上跑。
3. **预签名 URL**。`PresignedGetURL` 是本地证据路径下 `signedEvidenceURL` 用的那条
   （控制面 `/api/v1/evidence/{id}/signed-url`）。MinIO 与 AWS 在签名规范上最可能
   表现不同的就是这条，所以断言必须包含**一次真实的签名下载**，而不只是元数据列表。

## 前置条件

- 一个 S3 兼容 endpoint，其中一个**已存在**的 bucket（或允许 `s3:CreateBucket` 的凭据）。
- 凭据具备：`s3:PutObject`、`s3:GetObject`、`s3:ListBucket`、`s3:DeleteObject`，
  以及不设 `S3_REGION` 时的 `s3:GetBucketLocation`。
- Docker 可运行栈；本演练不需要 MinIO 服务。

## 步骤

用真实凭据覆盖对象存储相关变量，并**省略 minio 服务**（它仍留在 compose 文件里，
但因为 `S3_ENDPOINT` 指向外部而不会被使用；若要彻底不启动，用
`docker compose up` 时逐个列服务而不是 `up` 全部）：

```bash
export S3_ENDPOINT=https://s3.<region>.amazonaws.com
export S3_REGION=<region>
export S3_ACCESS_KEY=...
export S3_SECRET_KEY=...
export S3_BUCKET=<pre-created bucket>
export ALERT_SECRET_KEY=$(openssl rand -base64 32)
```

然后：

```powershell
# 1. 栈起来，确认 /readyz 通过（对象存储不可用会在启动阶段暴露）
docker compose -f deployments/compose/docker-compose.yml up -d

# 2. 登记并验证一个资产，跑一次检测，确认证据写入外部 bucket
#    可直接复用 smoke 脚本的前半段，或手工走资产/验证/检测三步。

# 3. 关键断言：对生成的证据签发短时 URL 并真实下载
```

第 3 步是这条演练最不可替代的一步：

```powershell
$signed = Invoke-RestMethod -Method Post `
    -Uri "$apiBase/api/v1/evidence/$evidenceId/signed-url" `
    -WebSession $session -Headers $csrf
Invoke-WebRequest -UseBasicParsing -Uri $signed.url -OutFile "$env:TEMP\evidence.bin"
# 下载到的字节的 SHA-256 必须等于证据元数据里的 sha256
```

hash 必须匹配。只比对「URL 返回 200」是不够的：预签名错误的请求同样可能返回 200
而内容是错误对象或错误分片。

## 记录模板

跑通后把下面这段填好放进 `docs/README.md`，并把「外部对象存储尚未验证」收窄：

```
外部对象存储演练已于 YYYY-MM-DD 在 <endpoint 类型> 上执行：
- bucket <name> 预先存在，未触发 MakeBucket
- S3_REGION=<region> 已设置
- 证据写入/读取通过，sha256 校验一致
- 预签名 URL 下载成功且 sha256 一致
- 未覆盖：bucket 策略只允许 PutObject 时的降级路径
```
