# 零停机迁移（expand / contract）运维手册

司域控制面用 Helm pre-upgrade hook Job 自动迁移（`migrations.enabled`），配 Deployment `RollingUpdate maxUnavailable:0`。**零停机不是单靠自动化——迁移的形态与时序共同保证**：滚动更新期新旧版本 Pod 短暂共存，二者都必须能在当时的 schema 上工作。

## 铁律：一次发布只做 expand（向后兼容）

| 可以（expand，向后兼容） | 不可以（contract/破坏，须延后） |
|---|---|
| 加列（nullable 或有 DEFAULT） | 删列 / 改列名 |
| 加表、加视图 | 改列类型（不兼容） |
| 加索引（`CREATE INDEX CONCURRENTLY`） | 对既有列加 `NOT NULL`（除非先回填+两步） |
| 加约束 `NOT VALID` 后单独 `VALIDATE CONSTRAINT` | 直接加会全表校验锁写的约束 |
| 加枚举值（Postgres `ADD VALUE`） | 删/改枚举值 |

## 时序（一次 `helm upgrade`）

1. **pre-upgrade hook Job 先迁移**（只含 expand 变更）→ 成功才继续；失败则 release 失败、旧副本原样跑（fail-close，不上新代码）。
2. **`maxUnavailable:0` 滚动上新代码**：新旧 Pod 共存期，DB 已 expand，旧代码不碰新列、新代码可用新列，双向兼容。
3. **contract 延到下一次发布**：确认旧版本 Pod 全下线后，下个 release 的 expand-only 迁移里再删旧列/加 NOT NULL。

## 破坏性变更的两段式范例（删列 `old_col`）

- 发布 N（expand）：停止写 `old_col`（新代码不再写），迁移不动 schema。
- 发布 N+1（contract）：确认 N 的 Pod 全退，迁移 `ALTER TABLE ... DROP COLUMN old_col`。

## 违反后果

在滚动窗口内做 contract（如删列）→ 旧 Pod 查询撞缺列 → 500，破坏「权限一致性优先」。故 contract 必须与「旧代码已全退」解耦到下一发布。

## 回滚

迁移 Job 只前滚（`up`）。生产回滚**不**用 `down`（常破坏性）——靠：①前滚一个修复迁移，或 ②恢复备份（见 M5.3-backup）。`down`/`MigrateDown` 仅供开发/测试往返。
