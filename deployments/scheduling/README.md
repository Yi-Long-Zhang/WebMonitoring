# 备份调度模板

`compose-backup.ps1` 本身只负责「做一次备份 + 按保留策略轮转」。定时化由宿主机
的计划任务完成，本目录提供两份模板。两者都以仓库根为工作目录调用备份脚本；
S3 凭据由脚本从 `deployments/compose/.env` 读取，不出现在任务定义里。

## Windows：计划任务

```powershell
powershell -ExecutionPolicy Bypass -File deployments/scheduling/register-windows-task.ps1
```

可选参数：`-TaskName`（默认 `WebMonitoring-Backup`）、`-StartTime`（默认
`02:30`）、`-KeepBackups`（默认 14）、`-OutputRoot`（默认 `.data-backups`）。
重复执行同名注册即更新。手动触发与删除：

```powershell
schtasks /Run /TN WebMonitoring-Backup
schtasks /Delete /TN WebMonitoring-Backup /F
```

任务以 SYSTEM 运行，要求 Docker Desktop 对 SYSTEM 可见（单用户桌面安装可能
不满足；看不到 Docker 时改用登录用户注册，或改用 WSL 内的 systemd 方案）。

## Linux：systemd timer

```bash
cp deployments/scheduling/webmonitoring-backup.service.example \
   /etc/systemd/system/webmonitoring-backup.service
cp deployments/scheduling/webmonitoring-backup.timer.example \
   /etc/systemd/system/webmonitoring-backup.timer
# 把两个文件里的 <REPO> 替换为仓库的绝对路径
sed -i 's|<REPO>|/opt/webmonitoring|' /etc/systemd/system/webmonitoring-backup.service
systemctl daemon-reload
systemctl enable --now webmonitoring-backup.timer
systemctl list-timers webmonitoring-backup.timer   # 确认下次触发时间
```

注意 `.service` 里的 `ExecStart` 假定 `powershell`（PowerShell 7）在
`/usr/bin/powershell`；发行版包名是 `powershell`，安装后路径即此。

## 轮换与演练

- 保留策略：备份脚本每次成功后按时间戳目录名轮转出最旧的，保留最新
  `KeepBackups` 个（可用环境变量 `BACKUP_KEEP_COUNT` 覆盖），与调度器无关。
- 恢复演练：随时手工执行 `scripts/backup-rehearsal.ps1`（跑两个备份周期 +
  restore 脚本 dry-run 校验，不动数据），结果记入 `docs/rehearsals.md`。
