# M5.3-backup 备份与恢复 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给司域 PG（唯一真相源）交付逻辑备份/恢复脚本 + 可选 Helm 定时备份 CronJob + DR runbook，闭合 M5.3 部署硬化。

**架构：** `deploy/scripts/pg-backup.sh`（`pg_dump -Fc` 时间戳+保留期）与 `pg-restore.sh`（`pg_restore --clean --if-exists`+`--yes` 确认）为手动运维路径；Helm CronJob（`backup.enabled` 门控、默认 off）用 `postgres` 镜像非 root 内联跑 pg_dump 到 PVC、DSN 走 secretKeyRef；runbook 定策略/DR/RPO-RTO、指导启用托管 PG PITR、说明 Redis 可从 PG 重建不备份。

**技术栈：** POSIX `sh` + `pg_dump`/`pg_restore`（`postgres:17-alpine` 客户端）、Helm v4.2.0 CronJob、testcontainers-free 的真实 `docker run postgres` 往返验证。

**BASE：** `feat/m5-3-backup-restore` @ 含设计规格提交；规格 `docs/superpowers/specs/2026-07-12-sydom-m5-3-backup-restore-design.md`。

**零触碰铁律：** `git diff 95b8dd8..HEAD -- '*.go' casbin/ adminauthz/ internal/` 必须为空（纯 `deploy/*`+`docs/*`）。

---

## 任务 1：备份/恢复脚本 + 真实往返验证

**文件：**
- 创建：`deploy/scripts/pg-backup.sh`
- 创建：`deploy/scripts/pg-restore.sh`

- [ ] **步骤 1：写 pg-backup.sh**

`deploy/scripts/pg-backup.sh`：
```sh
#!/usr/bin/env sh
# 司域 PG 逻辑备份：pg_dump 自定义格式(-Fc,压缩,可选择性恢复)到 BACKUP_DIR，时间戳命名。
# 用法：DATABASE_URL=postgres://... BACKUP_DIR=/backups [RETENTION_DAYS=7] [POST_HOOK=cmd] pg-backup.sh
set -eu
: "${DATABASE_URL:?DATABASE_URL required}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
mkdir -p "$BACKUP_DIR"
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$BACKUP_DIR/sydom-$TS.dump"
pg_dump "$DATABASE_URL" -Fc --no-owner --no-privileges -f "$OUT"
echo "backup written: $OUT ($(wc -c < "$OUT") bytes)"
if [ -n "${RETENTION_DAYS:-}" ]; then
  find "$BACKUP_DIR" -name 'sydom-*.dump' -type f -mtime "+$RETENTION_DAYS" -delete
fi
[ -n "${POST_HOOK:-}" ] && sh -c "$POST_HOOK" "$OUT" || true
```

- [ ] **步骤 2：写 pg-restore.sh**

`deploy/scripts/pg-restore.sh`：
```sh
#!/usr/bin/env sh
# 司域 PG 恢复：pg_restore --clean --if-exists（破坏性，先删既有对象）。须 --yes 确认。
# 用法：DATABASE_URL=postgres://... pg-restore.sh <dumpfile> --yes
set -eu
: "${DATABASE_URL:?DATABASE_URL required}"
DUMP="${1:?usage: pg-restore.sh <dumpfile> --yes}"
[ -f "$DUMP" ] || { echo "dump not found: $DUMP" >&2; exit 1; }
case "${2:-}" in
  --yes) ;;
  *) echo "危险操作：--clean 会先删除既有对象。确认请加 --yes" >&2; exit 2 ;;
esac
pg_restore --clean --if-exists --no-owner --no-privileges -d "$DATABASE_URL" "$DUMP"
echo "restore complete from $DUMP"
```

- [ ] **步骤 3：可执行位 + 静态检查**

运行：
```bash
chmod +x deploy/scripts/pg-backup.sh deploy/scripts/pg-restore.sh
sh -n deploy/scripts/pg-backup.sh && sh -n deploy/scripts/pg-restore.sh && echo "SYNTAX-OK"
# 缺 DATABASE_URL → 非 0
( unset DATABASE_URL; sh deploy/scripts/pg-backup.sh; echo "rc=$?" ) 2>&1 | grep -E "DATABASE_URL required|rc=[^0]" && echo "NO-DSN-FAIL-OK"
# pg-restore 无 --yes → 退出 2（先造个假 dump 文件绕过前置检查）
touch /tmp/x.dump
( DATABASE_URL=x sh deploy/scripts/pg-restore.sh /tmp/x.dump; echo "rc=$?" ) 2>&1 | grep -E "确认请加 --yes|rc=2" && echo "NO-YES-FAIL-OK"
```
预期：`SYNTAX-OK`、`NO-DSN-FAIL-OK`、`NO-YES-FAIL-OK`。

- [ ] **步骤 4：真实往返验证（postgres 容器）**

运行（seed→backup→drop→restore→行回来）：
```bash
docker rm -f pgbk >/dev/null 2>&1 || true
docker run -d --name pgbk -e POSTGRES_USER=sydom -e POSTGRES_PASSWORD=sydom -e POSTGRES_DB=sydom postgres:17-alpine >/dev/null
for i in $(seq 1 30); do docker exec pgbk pg_isready -U sydom >/dev/null 2>&1 && break; sleep 1; done
DSN='postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable'
docker exec pgbk psql -U sydom -d sydom -c "CREATE TABLE bkcheck(id int); INSERT INTO bkcheck VALUES (42);"
docker cp deploy/scripts/pg-backup.sh pgbk:/tmp/pg-backup.sh
docker cp deploy/scripts/pg-restore.sh pgbk:/tmp/pg-restore.sh
docker exec -e DATABASE_URL="$DSN" -e BACKUP_DIR=/tmp/bk pgbk sh /tmp/pg-backup.sh
DUMP=$(docker exec pgbk sh -c 'ls /tmp/bk/sydom-*.dump')
docker exec pgbk psql -U sydom -d sydom -c "DROP TABLE bkcheck;"
docker exec -e DATABASE_URL="$DSN" pgbk sh /tmp/pg-restore.sh "$DUMP" --yes
VAL=$(docker exec pgbk psql -U sydom -d sydom -tAc "SELECT id FROM bkcheck;")
echo "restored value=$VAL"; [ "$VAL" = "42" ] && echo "ROUNDTRIP-OK" || echo "ROUNDTRIP-FAIL"
docker rm -f pgbk >/dev/null
```
预期：`backup written: ...`、`restore complete`、`restored value=42`、`ROUNDTRIP-OK`。

- [ ] **步骤 5：Commit**

```bash
git add deploy/scripts/pg-backup.sh deploy/scripts/pg-restore.sh
git commit -m "feat(deploy): M5.3-backup PG 逻辑备份/恢复脚本(pg_dump -Fc 时间戳+保留期 prune+POST_HOOK;pg_restore --clean --if-exists+--yes 安全确认;真实容器往返 seed→backup→drop→restore→行回来验证)"
```

---

## 任务 2：Helm 定时备份 CronJob（门控/nonroot/DSN secretKeyRef）

**文件：**
- 修改：`deploy/helm/sydom-controlplane/values.yaml`
- 创建：`deploy/helm/sydom-controlplane/templates/backup-cronjob.yaml`

- [ ] **步骤 1：values 加 backup 块**

在 `deploy/helm/sydom-controlplane/values.yaml` 的 `migrations:` 块后加：
```yaml
# 定时逻辑备份（M5.3-backup）：CronJob 用 postgres 镜像 pg_dump 到 PVC。默认关（目的地/存储部署侧定）。
backup:
  enabled: false
  schedule: "0 3 * * *"          # 每日 03:00 UTC
  image: "postgres:17-alpine"
  pvcClaim: ""                    # 备份落此 PVC；enabled 时必填
  retentionDays: "7"
  dsnSecret:                      # 含 DATABASE_URL 的 Secret 引用
    name: ""
    key: "DATABASE_URL"
```

- [ ] **步骤 2：写 backup-cronjob.yaml**

`deploy/helm/sydom-controlplane/templates/backup-cronjob.yaml`（CronJob 内联 pg_dump，避 Helm `.Files` 跨目录限制；与 `deploy/scripts/pg-backup.sh` 共用 `pg_dump -Fc` 契约，步骤 3 测断言一致）：
```
{{- if .Values.backup.enabled }}
{{- if not .Values.backup.pvcClaim }}{{ fail "backup.enabled=true 需设 backup.pvcClaim" }}{{- end }}
{{- if not .Values.backup.dsnSecret.name }}{{ fail "backup.enabled=true 需设 backup.dsnSecret.name" }}{{- end }}
apiVersion: batch/v1
kind: CronJob
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}-backup
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
spec:
  schedule: {{ .Values.backup.schedule | quote }}
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 2
      template:
        metadata:
          labels:
            {{- include "sydom-controlplane.selectorLabels" . | nindent 12 }}
        spec:
          restartPolicy: Never
          serviceAccountName: {{ include "sydom-controlplane.serviceAccountName" . }}
          automountServiceAccountToken: false
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            runAsGroup: 65532
            fsGroup: 65532
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: backup
              image: {{ .Values.backup.image | quote }}
              securityContext:
                allowPrivilegeEscalation: false
                readOnlyRootFilesystem: true
                capabilities:
                  drop: ["ALL"]
              command: ["/bin/sh", "-c"]
              args:
                - |
                  set -eu
                  TS=$(date -u +%Y%m%dT%H%M%SZ)
                  OUT="/backups/sydom-$TS.dump"
                  pg_dump "$DATABASE_URL" -Fc --no-owner --no-privileges -f "$OUT"
                  echo "backup written: $OUT ($(wc -c < "$OUT") bytes)"
                  find /backups -name 'sydom-*.dump' -type f -mtime "+${RETENTION_DAYS}" -delete
              env:
                - name: DATABASE_URL
                  valueFrom:
                    secretKeyRef:
                      name: {{ .Values.backup.dsnSecret.name | quote }}
                      key: {{ .Values.backup.dsnSecret.key | quote }}
                - name: RETENTION_DAYS
                  value: {{ .Values.backup.retentionDays | quote }}
              volumeMounts:
                - name: backups
                  mountPath: /backups
          volumes:
            - name: backups
              persistentVolumeClaim:
                claimName: {{ .Values.backup.pvcClaim | quote }}
{{- end }}
```

- [ ] **步骤 3：验证渲染 + 门控 + 一致性**

运行：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
helm lint $CP
# 默认 backup.enabled=false → 无 CronJob
test $(helm template t $CP $SET 2>&1 | grep -c "kind: CronJob") -eq 0 && echo "CRON-OFF-OK"
# 开启 → 渲染 CronJob（schedule/secretKeyRef/nonroot/pg_dump -Fc）
helm template t $CP $SET --set backup.enabled=true --set backup.pvcClaim=sydom-backups --set backup.dsnSecret.name=sydom-dsn 2>&1 | \
  grep -E "kind: CronJob|schedule: \"0 3|secretKeyRef|runAsNonRoot: true|pg_dump .* -Fc|readOnlyRootFilesystem: true"
# enabled 但缺 pvcClaim → fail
helm template t $CP $SET --set backup.enabled=true --set backup.dsnSecret.name=x 2>&1 | grep -E "需设 backup.pvcClaim" && echo "PVC-REQUIRED-OK"
# CronJob 内联与脚本共用 pg_dump -Fc 契约
grep -q 'pg_dump .* -Fc' deploy/scripts/pg-backup.sh && grep -q 'pg_dump .* -Fc' $CP/templates/backup-cronjob.yaml && echo "PGDUMP-CONTRACT-CONSISTENT"
```
预期：`helm lint` 0 failed；`CRON-OFF-OK`；开启渲染命中 CronJob/schedule/secretKeyRef/nonroot/pg_dump -Fc/readOnlyRoot；`PVC-REQUIRED-OK`；`PGDUMP-CONTRACT-CONSISTENT`。

- [ ] **步骤 4：Commit**

```bash
git add deploy/helm/sydom-controlplane/values.yaml deploy/helm/sydom-controlplane/templates/backup-cronjob.yaml
git commit -m "feat(deploy): M5.3-backup Helm 定时备份 CronJob(backup.enabled 门控默认关+postgres 镜像 nonroot/readOnlyRoot 内联 pg_dump -Fc 到 PVC+DSN secretKeyRef+保留期 prune+缺 pvcClaim/dsnSecret fail)"
```

---

## 任务 3：备份恢复 runbook + 最终验收

**文件：**
- 创建：`docs/runbooks/backup-restore.md`

- [ ] **步骤 1：写 runbook**

`docs/runbooks/backup-restore.md`：
````markdown
# 备份与恢复运维手册

司域唯一真相源是 **PostgreSQL**（租户/应用/角色/策略/审计/casbin_rule/policy_outbox）。**Redis 是瞬态 pub/sub 广播总线，非真相源**——全丢后 sidecar 重连从控制面全量快照（读 PG）重建，`policy_outbox` 保证 delta 可重放，**无数据丢失，故不做关键备份**（可选 RDB 快照仅为加速 warm restart）。

## 两层备份策略

| 层 | 手段 | 覆盖 | 归属 |
|---|---|---|---|
| **逻辑备份** | `pg_dump -Fc`（`deploy/scripts/pg-backup.sh` / Helm CronJob） | 可移植 DR、跨 provider 迁移、选择性恢复单表 | 司域交付 |
| **PITR（时间点恢复）** | 托管 PG 原生自动备份 + WAL（RDS/CloudSQL/Aurora 开箱） | 细粒度（秒级）恢复到故障前 | **启用托管 PG PITR + 设保留期**（不自建 archive_command） |

RPO：托管 PITR ≈ 秒级；逻辑备份 = 备份间隔（CronJob 默认每日→RPO 24h，可调）。RTO：逻辑恢复 = `pg_restore` 时长（当前规模分钟级）。

## 定时备份（k8s）

`helm upgrade ... --set backup.enabled=true --set backup.pvcClaim=<pvc> --set backup.dsnSecret.name=<secret>` → CronJob 每日 `pg_dump -Fc` 到 PVC、按 `retentionDays` prune。**产物含全部策略数据（敏感）**：部署侧须 ①静态加密 PVC/对象存储 ②访问控制 ③异地副本（脚本 `POST_HOOK` 可挂加密+上传对象存储）。

## 手动备份

在有 PG 客户端处：`DATABASE_URL=postgres://... BACKUP_DIR=/backups deploy/scripts/pg-backup.sh`。

## 灾难恢复（DR）步骤

1. 置备新 PG（同大版本 17），拿到新库 DSN。
2. 取最近 dump（PVC/对象存储）。
3. 恢复（**破坏性，须 --yes**）：`DATABASE_URL=<新库DSN> deploy/scripts/pg-restore.sh <dump> --yes`（`pg_restore --clean --if-exists`，幂等可重放）。
4. 校验：`psql -tAc "SELECT count(*) FROM application"` 等关键表计数与预期一致。
5. 控制面指向新库（改 `database_dsn` 的 Secret + 滚动）；sidecar 自动重连、从控制面全量快照重建。
6. 若用托管 PITR：优先按 provider 控制台恢复到故障前时间点（比逻辑恢复 RPO 更优），逻辑备份作跨 provider/离线兜底。

## 回滚（呼应零停机迁移手册）

迁移只前滚。误迁移/坏发布的回滚 = ①前滚一个修复迁移，或 ②本手册的 DR 恢复（托管 PITR 到故障前 / 逻辑 dump 恢复）。生产**不**跑 `migrate down`。

## 不做什么

- 不备份 Redis（可从 PG 重建）。
- 不自建 WAL 归档/PITR（用托管 PG 原生）。
````

- [ ] **步骤 2：最终验收**

运行：
```bash
go build ./... && echo BUILD-OK    # 零 Go 改，仍应 OK
helm lint deploy/helm/sydom-controlplane
# M53B2-1 零触碰
git diff 95b8dd8..HEAD -- '*.go' casbin/ adminauthz/ internal/ | head; echo "ZERO-TOUCH-DONE(空)"
```
预期：`BUILD-OK`；`helm lint` 0 failed；零触碰 diff 为空。（`go test ./...` 不受零 Go 改动影响，与 BASE 一致 EXIT 0；如需可全量复跑。）

- [ ] **步骤 3：Commit**

```bash
git add docs/runbooks/backup-restore.md
git commit -m "docs(runbook): M5.3-backup 备份恢复手册(两层=逻辑 pg_dump+托管 PG PITR;DR 步骤;RPO/RTO;Redis 可从 PG 重建不备份;回滚呼应零停机迁移;M5.3 收官)"
```

---

## 自检

**1. 规格覆盖度：**
- §4.1 文件 → 任务 1（脚本）+任务 2（CronJob/values）+任务 3（runbook）。
- §4.2/4.3 脚本 → 任务 1。§4.4 CronJob → 任务 2（内联 pg_dump 落实 §4.4 note 的「避 .Files 跨目录」决策）。§4.5 数据流/§4.6 一致性 → runbook（任务 3）。
- §5 验证 → 任务 1 步 3/4（静态+真实往返）、任务 2 步 3（helm）、任务 3 步 2（零触碰）。
- §6 M53B2-1..7 → M53B2-1 任务3步2、M53B2-2 任务1步1+3、M53B2-3 任务1步2+3、M53B2-4 任务1步4、M53B2-5 任务2、M53B2-6 任务3步1、M53B2-7 任务3步2。全覆盖。

**2. 占位符扫描：** 各步含实际脚本/模板/命令+预期输出；无 TODO。runbook 里 `<pvc>`/`<dump>` 是给运维填的占位（文档本意）。

**3. 类型一致性：** 脚本环境变量 `DATABASE_URL`/`BACKUP_DIR`/`RETENTION_DAYS`/`POST_HOOK` 在 backup.sh 定义、往返验证与 CronJob（`DATABASE_URL`/`RETENTION_DAYS`）一致；values `backup.{enabled,schedule,image,pvcClaim,retentionDays,dsnSecret.{name,key}}` 任务1定义、任务2 模板引用一致；helm helper `sydom-controlplane.{fullname,labels,selectorLabels,serviceAccountName}` 沿用既有定义；`pg_dump -Fc`/`pg_restore --clean --if-exists` 脚本与 CronJob/runbook 一致。
