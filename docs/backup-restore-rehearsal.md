# 备份恢复演练记录

本记录用于单机 Docker Compose 首期交付验收。正式环境由宿主机反向代理负责
HTTPS，恢复操作必须先取得审批并保留审计记录。

## 目标

| 项目 | 目标 |
| --- | --- |
| RPO | 不超过 24 小时 |
| RTO | 不超过 4 小时 |
| 数据范围 | PostgreSQL 元数据、MinIO `evidence` bucket、Redis 持久化状态 |

## 演练步骤

1. 记录版本、配置校验结果、当前 `/readyz`、任务数量、Redis detection/alert
   pending 数量和 MinIO evidence 对象数量。
2. 执行 `scripts/compose-backup.ps1`，保存输出目录和 `manifest.json`。
3. 验证 manifest 中每个文件的 SHA-256，并确认备份目录不含密钥或客户数据之外
   的临时文件。
4. 在隔离的 Compose 数据卷上执行：

   ```powershell
   .\scripts\compose-restore.ps1 -BackupRoot .data-backups\YYYYMMDD-HHMMSS `
     -Execute -ConfirmText RESTORE
   ```

5. 记录恢复脚本自动检查的 `/readyz`、证据 SHA-256、任务状态和 Redis pending
   结果，再下载一个事件报告验证证据完整性。
6. 记录实际 RPO、RTO、失败项、人工操作和回滚结论。

## 验收记录

单机 Compose 实机演练，2026-09-15，源数据由 `scripts/load-test.ps1 -KeepRunning`
（100 资产 / 2 租户）和此前的 smoke 运行累积而成。

| 字段 | 结果 |
| --- | --- |
| 演练时间（UTC） | 2026-09-15 08:31 备份；08:33–08:40 首次恢复；重复恢复 29 秒 |
| 版本/镜像摘要 | 本地构建的 `compose-*` 镜像（api、http-worker、migrate、console、browser-worker）；数据库迁移 11 个版本全部已应用 |
| 备份目录 | `.data-backups/20260915-163112`（dump 386 KiB，evidence 2.00 MiB，1198 个受校验文件） |
| 备份 SHA-256 manifest | 通过（1198/1198 校验一致，manifest 不含口令/密钥） |
| `/readyz` | 通过（恢复后 `/readyz` 返回 200） |
| 证据 SHA-256 | 通过（恢复后的 MinIO 对象逐个比对 manifest，1197/1197 一致；库内 evidence 行数 1197 与对象数一致） |
| 任务状态 | 通过（截至备份时刻 `created_at <= 2026-09-15T08:31:20Z` 的任务数为 500，源端与恢复端一致；任务状态全部落在合法终态） |
| Redis pending | 通过（detection 0、alert 0） |
| 实际 RPO | 备份为在线导出，备份时刻之后写入不包含在内；恢复端在备份时间戳上的资产数（200）与任务数（500）与源端逐项一致 |
| 实际 RTO | 冷启动恢复约 7 分钟（含镜像启动、库恢复、对象回灌、API 就绪与验证）；重复执行 29 秒 |
| 审批/审计记录 | 本机演练，无生产审批；生产执行前须另行记录审批、RPO/RTO 与回滚方案 |
| 失败与回滚结论 | 首次恢复失败：目标对象存储为空白时 `evidence` bucket 不存在，`mc mirror` 拒绝写入。已在恢复脚本中加入 `mc mb --ignore-existing`，修复后连续两次恢复通过。回滚方案：停止 API/Worker，用上一次成功备份覆盖恢复 |

演练暴露并已修复的问题（均已有回归保护）：

- `internal/store/postgres.go` 的分页 SQL 把 `LIMIT` 拼在 `ORDER BY` 之前，PostgreSQL
  直接报语法错误，`GET /api/v1/sites` 等列表接口返回 500 `STORAGE_UNAVAILABLE`。
  已由 `paginateQuery` 统一拼接排序与 LIMIT，并新增
  `TestPostgresKeysetPaginationWalksEveryRowOnce` 覆盖真实 SQL。
- `scripts/load-test.ps1` 复用内置 `default` 租户，在保留数据的具名卷上第二次运行
  会因 `UNIQUE (tenant_id, url)` 冲突而创建 0 个资产；现改为每次运行新建租户。
- `scripts/compose-backup.ps1` / `compose-restore.ps1` 引用了未定义的 `$composeFile`，
  `docker compose -f "" cp ...` 必然失败。
- 恢复脚本假定目标 `evidence` bucket 已存在。
- `apps/console/e2e/console-smoke.mjs` 按输入框序号填写登录表单，而登录面板有
  租户/账号/密码三个字段，凭据被填进了错误的输入框；现按标签定位。
