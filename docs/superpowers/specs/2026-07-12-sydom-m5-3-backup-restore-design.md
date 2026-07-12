# M5.3-backup 备份与恢复 — 设计规格

> M5.3「部署硬化」收官切片（〔TODO-M5.3-backup〕）。BASE=main `95b8dd8`（M5.3-migrate）。补全 zero-downtime runbook 里「回滚靠恢复备份」的引用，闭合 M5.3。**纯 `deploy/scripts` + `deploy/helm` + `docs/runbooks`，零 Go 触碰。**

## 1. 背景与目标

司域是全托管多租户 SaaS，**PostgreSQL 是唯一真相源**（租户/应用/角色/策略/审计/casbin_rule/policy_outbox 全在 PG）。Redis 仅作策略变更的**瞬态 pub/sub 广播总线**（`internal/controlplane/broadcast/redis.go` 的 `RedisPublisher` 向 channel 发 delta），**非真相源**——Redis 全丢，sidecar 重连后从控制面全量快照（读 PG）重建，`policy_outbox`（PG）保证 delta 可重放，无数据丢失。

当前仓库**无任何备份工具**。目标：给运维备份/恢复 PG 的能力与纪律。

**目标**：
1. **PG 逻辑备份**：`pg_dump` 自定义格式（`-Fc`，压缩、可选择性恢复）脚本 + 可选 Helm CronJob 定时执行。
2. **恢复/灾难恢复**：`pg_restore` 脚本（带安全确认）+ DR runbook（RPO/RTO、步骤）。
3. **纪律与边界**：runbook 明确逻辑备份 vs 托管 PG PITR 的分工、Redis 无需关键备份。

**非目标（明确排除）**：
- **自建 WAL 归档 / PITR 基础设施**：物理 PITR 强依赖存储/provider，托管 SaaS 用**托管 PG 的原生自动备份 + PITR**（RDS/CloudSQL 开箱）。SYDOM 交付的是**可移植的逻辑备份**（跨 provider DR/迁移）+ runbook 指导「启用托管 PITR + 保留期」。不手搓 `archive_command`。
- **Redis 关键备份**：Redis 可从 PG 重建，不做 PITR 级备份；runbook 给可选 RDB 快照建议（加速 warm restart）。
- **备份加密/对象存储上传的具体实现**：object storage/KMS 因 provider 而异；脚本产出到目标目录/PVC，runbook 说明「产物加密 + 上传对象存储 + 异地」是部署侧集成点（脚本留 `POST_HOOK` 扩展位）。

## 2. 现状（实查）

- PG：`postgres:17-alpine`，DSN `postgres://sydom:sydom@postgres:5432/sydom`；无备份脚本/CronJob。
- 宿主无 `pg_dump`/`pg_restore`/`psql` → 备份须在带 PG 客户端的镜像里跑（CronJob 用 `postgres` 镜像；验证用 `postgres` 容器）。
- M5.3-migrate runbook「回滚」段引用「恢复备份（见 M5.3-backup）」——本切片兑现。
- M5.3-k8s chart：config Secret 含 DSN（`config.yaml`）；CronJob 取 DSN 用独立 secretKeyRef（备份是独立运维关注点，不复用控制面 config 挂载）。

## 3. 方案选择

**A（选定）逻辑备份脚本 + 可选 Helm CronJob + DR runbook；PITR 委托托管 PG。**
- `deploy/scripts/pg-backup.sh`（`pg_dump -Fc` 时间戳命名 + 可选保留期 prune）、`deploy/scripts/pg-restore.sh`（`pg_restore --clean --if-exists` + 安全确认）。
- `deploy/helm/sydom-controlplane/templates/backup-cronjob.yaml`（values 门控 `backup.enabled`，默认 **false**；`postgres` 镜像跑 pg_dump 到 PVC，DSN 走 secretKeyRef，schedule/保留期可配）。
- `docs/runbooks/backup-restore.md`（策略/RPO/RTO/DR 步骤/托管 PITR 指导/Redis 说明）。
- **优点**：逻辑备份可移植（跨 provider、验证性强、可选择性恢复单表）；PITR 交给托管 PG 最省最稳；脚本纯运维、零 Go；CronJob 可选不强加。

**B 自建 WAL 归档 PITR。** 强依赖持久卷/对象存储 + `archive_command` + base backup 编排，运维重、provider 耦合，且托管 PG 已原生提供。否决（runbook 指导启用托管 PITR 代替）。

**C 只给脚本不给 CronJob。** 少了 k8s 定时自动化。折中：CronJob 作可选（默认 off）加法，兼顾手动脚本与自动化。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `deploy/scripts/pg-backup.sh`（新） | `pg_dump -Fc` 到目标目录、时间戳命名、可选 `RETENTION_DAYS` prune、可选 `POST_HOOK` |
| `deploy/scripts/pg-restore.sh`（新） | `pg_restore --clean --if-exists` 恢复、须 `--yes` 或交互确认、恢复后计数校验 |
| `deploy/helm/sydom-controlplane/templates/backup-cronjob.yaml`（新） | values 门控 CronJob，`postgres` 镜像跑 pg_dump 到 PVC，DSN secretKeyRef，硬化 securityContext |
| `deploy/helm/sydom-controlplane/values.yaml`（改） | 加 `backup` 块（enabled/schedule/image/pvcClaim/dsnSecret/retentionDays） |
| `docs/runbooks/backup-restore.md`（新） | 备份策略 + DR 恢复步骤 + RPO/RTO + 托管 PITR 指导 + Redis 说明 |

### 4.2 `pg-backup.sh`

```bash
#!/usr/bin/env sh
set -eu
# 用法：DATABASE_URL=postgres://... BACKUP_DIR=/backups [RETENTION_DAYS=7] pg-backup.sh
: "${DATABASE_URL:?DATABASE_URL required}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
mkdir -p "$BACKUP_DIR"
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$BACKUP_DIR/sydom-$TS.dump"
# -Fc 自定义格式（压缩、可 pg_restore 选择性恢复）；--no-owner/--no-privileges 便于跨环境恢复
pg_dump "$DATABASE_URL" -Fc --no-owner --no-privileges -f "$OUT"
echo "backup written: $OUT ($(wc -c < "$OUT") bytes)"
# 可选保留期：删早于 N 天的 .dump
if [ -n "${RETENTION_DAYS:-}" ]; then
  find "$BACKUP_DIR" -name 'sydom-*.dump' -type f -mtime "+$RETENTION_DAYS" -delete
fi
# 可选后置钩子（加密/上传对象存储，部署侧提供）
[ -n "${POST_HOOK:-}" ] && sh -c "$POST_HOOK" "$OUT" || true
```
sh（非 bash）以兼容 alpine/distroless-adjacent；`set -eu`；DSN 从 env（不落命令行明文历史，且 CronJob 走 secretKeyRef）。

### 4.3 `pg-restore.sh`

```bash
#!/usr/bin/env sh
set -eu
# 用法：DATABASE_URL=postgres://... pg-restore.sh <dumpfile> --yes
: "${DATABASE_URL:?DATABASE_URL required}"
DUMP="${1:?usage: pg-restore.sh <dumpfile> --yes}"
[ -f "$DUMP" ] || { echo "dump not found: $DUMP" >&2; exit 1; }
case "${2:-}" in --yes) ;; *) echo "危险操作：--clean 会先删除既有对象。确认请加 --yes" >&2; exit 2;; esac
# --clean --if-exists：恢复前 DROP 既有对象（幂等重放）；-Fc dump 由 pg_restore 读
pg_restore --clean --if-exists --no-owner --no-privileges -d "$DATABASE_URL" "$DUMP"
echo "restore complete from $DUMP"
```

### 4.4 Helm CronJob（values 门控，默认 off）

```yaml
{{- if .Values.backup.enabled }}
apiVersion: batch/v1
kind: CronJob
metadata: { name: {{ fullname }}-backup, ... }
spec:
  schedule: {{ .Values.backup.schedule | quote }}   # 默认 "0 3 * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          securityContext: {runAsNonRoot, 65532, seccomp}   # postgres 镜像非 root 跑 pg_dump
          containers:
            - name: backup
              image: {{ .Values.backup.image }}             # 例 postgres:17-alpine
              command: ["/bin/sh","/scripts/pg-backup.sh"]
              env:
                - name: DATABASE_URL
                  valueFrom: { secretKeyRef: {name: {{ .Values.backup.dsnSecret.name }}, key: {{ .Values.backup.dsnSecret.key }}} }
                - name: BACKUP_DIR
                  value: /backups
                - name: RETENTION_DAYS
                  value: {{ .Values.backup.retentionDays | quote }}
              volumeMounts:
                - { name: scripts, mountPath: /scripts, readOnly: true }
                - { name: backups, mountPath: /backups }
          volumes:
            - name: scripts
              configMap: { name: {{ fullname }}-backup-scripts, defaultMode: 0555 }
            - name: backups
              persistentVolumeClaim: { claimName: {{ .Values.backup.pvcClaim }} }
{{- end }}
```
脚本经 ConfigMap（由 `pg-backup.sh` 内容渲染）挂入——保持脚本单一真相源在 `deploy/scripts/`，chart 用 `.Files.Get` 读入 ConfigMap。备份落 PVC；「加密+上传对象存储」由 `POST_HOOK`/部署侧集成（runbook 说明）。`postgres` 镜像 runAsNonRoot（65532）跑 pg_dump（pg_dump 不需 root）。

> 注：`.Files.Get "scripts/pg-backup.sh"` 要求脚本在 chart 目录内可达。**方案**：脚本真身在 `deploy/scripts/`；chart 的 backup-scripts ConfigMap 用 `.Files.Get` 读——但 Helm `.Files` 只能读 chart 目录内。故 chart 内放一份 `files/pg-backup.sh`（`.helmignore` 不排除 `files/`），**由 `deploy/scripts/pg-backup.sh` 复制而来**，用一条 `make` 目标或 CI 校验两者一致防漂移。**权衡**：为免复制漂移，本切片 CronJob 的脚本**直接内联在模板的 ConfigMap `data` 里**（与 `deploy/scripts/` 手动脚本并存但内容一致，lint 测断言关键命令一致），避免 `.Files` 跨目录限制。→ 采用**内联 ConfigMap**。

### 4.5 数据流

**备份（定时）**：CronJob（`backup.enabled`）→ `postgres` 镜像非 root 跑内联脚本 `pg_dump -Fc` → 落 PVC `/backups/sydom-<ts>.dump` → prune 过期 → 可选 POST_HOOK 加密上传。**手动备份**：运维在有 pg 客户端处 `DATABASE_URL=... deploy/scripts/pg-backup.sh`。**恢复（DR）**：`DATABASE_URL=<新库> deploy/scripts/pg-restore.sh <dump> --yes` → `pg_restore --clean --if-exists` 重建 → 控制面连新库 → sidecar 重连全量快照。

### 4.6 一致性 / 安全

- 备份产物含全部策略数据（敏感）：runbook 要求**静态加密 + 访问控制 + 异地**；DSN 走 secretKeyRef 不落 values/命令行。
- 恢复是破坏性（`--clean`）：脚本强制 `--yes` 确认防误操作。
- CronJob 非 root 跑（postgres 镜像支持 `runAsNonRoot`：pg_dump 只需网络+写 PVC，不需 root）。
- Redis 不备份是**刻意**（可从 PG 重建）——runbook 写明，避免误以为遗漏。

## 5. 验证

- **真实往返（核心，用 `postgres:17-alpine` 容器）**：起 PG 容器 → 建表插一行 → 跑 `pg-backup.sh` 出 dump → `DROP TABLE` → 跑 `pg-restore.sh --yes` → 查行回来。证脚本 pg_dump/pg_restore 命令+flag 真可用、往返无损。
- **脚本静态**：`sh -n`（语法）、缺 `--yes` 时 pg-restore 退出码 2、缺 `DATABASE_URL` 退出非 0。
- **helm**：`helm lint` 0；`helm template --set backup.enabled=true` 渲染 CronJob（schedule/secretKeyRef/nonroot/内联脚本 ConfigMap）；`backup.enabled=false`（默认）→ 无 CronJob。
- **零触碰**：`git diff 95b8dd8..HEAD -- '*.go' casbin/ adminauthz/ internal/` = 空。

## 6. 验收标准（M53B2-1..7）

- **M53B2-1** 零触碰 Go/authz：上述 diff = 空（纯 `deploy/*`+`docs/*`）。
- **M53B2-2** `pg-backup.sh`：`pg_dump -Fc` 时间戳产物 + 可选保留期 prune；`sh -n` 通过；缺 `DATABASE_URL` fail。
- **M53B2-3** `pg-restore.sh`：`--clean --if-exists` 恢复、无 `--yes` 拒（退出 2）、缺 dump fail。
- **M53B2-4** 真实往返：seed→backup→drop→restore→行回来（`postgres` 容器实测）。
- **M53B2-5** Helm CronJob：`helm lint` 0、`backup.enabled` 门控、DSN secretKeyRef、nonroot、内联脚本与 `deploy/scripts/` 关键命令一致。
- **M53B2-6** runbook：备份策略 + DR 步骤 + RPO/RTO + 托管 PITR 指导 + Redis rebuildable 说明。
- **M53B2-7** `go test ./...` 不受影响（零 Go 改，EXIT 0）；M5.3 收官（k8s/migrate/backup 三片全完）。

## 7. 风险

- **备份一致性**：`pg_dump` 默认单事务快照（一致）；无需停机。大库时长/锁——当前规模无虞，runbook 提长期可迁移到托管 PITR。
- **内联脚本 vs `deploy/scripts/` 漂移**：CronJob 用内联 ConfigMap（避 Helm `.Files` 跨目录限制），与手动脚本并存；lint 测断言关键 `pg_dump -Fc`/`pg_restore --clean` 一致；长期可用 `.Files`+chart 内 `files/` 副本 + CI 校验。本切片取内联 + 测试守恒。
- **postgres 镜像非 root**：`postgres:17-alpine` 默认 entrypoint 复杂，但直接 `sh pg-backup.sh` 只调 `pg_dump` 客户端，`runAsNonRoot` 可行（pg_dump 不碰数据目录）。验证覆盖。
