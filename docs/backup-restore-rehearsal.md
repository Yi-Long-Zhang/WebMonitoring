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

| 字段 | 结果 |
| --- | --- |
| 演练时间（UTC） | 待填写 |
| 版本/镜像摘要 | 待填写 |
| 备份目录 | 待填写 |
| 备份 SHA-256 manifest | 通过 / 失败 |
| `/readyz` | 通过 / 失败 |
| 证据 SHA-256 | 通过 / 失败 |
| 任务状态 | 通过 / 失败 |
| Redis pending | 通过 / 失败 |
| 实际 RPO | 待填写 |
| 实际 RTO | 待填写 |
| 审批/审计记录 | 待填写 |
| 失败与回滚结论 | 待填写 |
