# 备份与恢复运维手册

司域唯一真相源是 **PostgreSQL**（租户/应用/角色/策略/审计/casbin_rule/policy_outbox）。**Redis 是瞬态 pub/sub 广播总线，非真相源**——全丢后 sidecar 重连从控制面全量快照（读 PG）重建，`policy_outbox`（PG）保证 delta 可重放，**无数据丢失，故不做关键备份**（可选 RDB 快照仅为加速 warm restart）。

## 两层备份策略

| 层 | 手段 | 覆盖 | 归属 |
|---|---|---|---|
| **逻辑备份** | `pg_dump -Fc`（`deploy/scripts/pg-backup.sh` / Helm CronJob） | 可移植 DR、跨 provider 迁移、选择性恢复单表 | 司域交付 |
| **PITR（时间点恢复）** | 托管 PG 原生自动备份 + WAL（RDS/CloudSQL/Aurora 开箱） | 细粒度（秒级）恢复到故障前 | **启用托管 PG PITR + 设保留期**（不自建 archive_command） |

RPO：托管 PITR ≈ 秒级；逻辑备份 = 备份间隔（CronJob 默认每日 → RPO 24h，可调 schedule）。RTO：逻辑恢复 = `pg_restore` 时长（当前规模分钟级）。

## 定时备份（k8s）

```
helm upgrade cp deploy/helm/sydom-controlplane \
  --set backup.enabled=true \
  --set backup.pvcClaim=<backups-pvc> \
  --set backup.dsnSecret.name=<secret-with-DATABASE_URL>
```
→ CronJob 每日 `pg_dump -Fc` 到 PVC、按 `retentionDays` prune。**产物含全部策略数据（敏感）**：部署侧须 ①静态加密 PVC/对象存储 ②访问控制 ③异地副本。手动脚本的 `POST_HOOK` 可挂「加密 + 上传对象存储」。

## 手动备份

在有 PG 客户端处：
```
DATABASE_URL=postgres://... BACKUP_DIR=/backups deploy/scripts/pg-backup.sh
```

## 灾难恢复（DR）步骤

1. 置备新 PG（同大版本 17），拿到新库 DSN。
2. 取最近 dump（PVC / 对象存储）。
3. 恢复（**破坏性，须 --yes**）：
   ```
   DATABASE_URL=<新库DSN> deploy/scripts/pg-restore.sh <dump> --yes
   ```
   （`pg_restore --clean --if-exists`，幂等可重放。）
4. 校验：`psql -tAc "SELECT count(*) FROM application"` 等关键表计数与预期一致。
5. 控制面指向新库（改 `database_dsn` 的 Secret + 滚动）；sidecar 自动重连、从控制面全量快照重建。
6. 若用托管 PITR：优先按 provider 控制台恢复到故障前时间点（比逻辑恢复 RPO 更优），逻辑备份作跨 provider / 离线兜底。

## 回滚（呼应零停机迁移手册）

迁移只前滚（见 `zero-downtime-migrations.md`）。误迁移 / 坏发布的回滚 = ①前滚一个修复迁移，或 ②本手册的 DR 恢复（托管 PITR 到故障前 / 逻辑 dump 恢复）。生产**不**跑 `migrate down`。

## 不做什么

- 不备份 Redis（可从 PG 重建）。
- 不自建 WAL 归档 / PITR（用托管 PG 原生）。
