# M5.3-migrate 迁移自动化 + 零停机（expand/contract）— 设计规格

> M5.3「部署硬化」剩余切片（〔TODO-M5.3-migrate〕）。BASE=main `2ce70d6`（M5.3-k8s）。补上 M5.3-k8s 在 NOTES 显式排除的「chart 迁移 Job」缺口，并确立零停机迁移纪律。

## 1. 背景与目标

当前迁移是手动的：`make migrate-up DSN=...` 用 `go run golang-migrate`（`internal/db.RunMigrations(dsn, "file://db/migrations")`），迁移文件在 `db/migrations/`（20 个迁移、40 文件、164K）。compose 用第三方 `migrate/migrate` 镜像跑一次性 Job。M5.3-k8s 的 Helm chart **刻意不含迁移**、在 NOTES 提示「rollout 前先手动迁移」——这是留给本切片补的洞。

**目标**：
1. **迁移自动化**：控制面部署时自动、幂等地把数据库迁到目标版本，无需人工 `make migrate-up`。
2. **零停机**：迁移在滚动更新**之前**完成，配合 M5.3-k8s 的 `RollingUpdate maxUnavailable:0` + expand/contract 纪律，升级期不中断服务。
3. **单一真相源 + 硬化**：迁移文件嵌入控制面二进制（`//go:embed`），迁移 Job 复用同一 distroless 硬化镜像，不引第三方镜像、不复制迁移文件。

**非目标（明确排除）**：
- 生产自动回滚/`down`：迁移 Job 只 `up`（前滚）。`MigrateDown` 保持 dev/手动（回滚靠恢复备份 = M5.3-backup 或前滚修复迁移）——生产 `down` 常破坏性、危险。
- 在线 schema 变更工具（gh-ost/pt-osc）：当前规模 PG 原生 DDL 足够；expand/contract 纪律即可零停机。
- 迁移文件本身的 expand/contract 重写：既有 20 个迁移不动；本切片确立**今后**的纪律 + 自动化机制。

## 2. 现状（实查）

- `internal/db/migrate.go`：`RunMigrations(dsn, sourceURL)`（golang-migrate `source/file` + `database/postgres`，幂等——`migrate.ErrNoChange` 吞掉）；`MigrateDown(dsn, sourceURL)`。
- `db/migrations/*.sql`：`NNNNNN_name.{up,down}.sql`，golang-migrate v4.17.1。
- `internal/controlplane/app/config.go`：`LoadConfig(path, getenv)` 读 YAML+env；**`SYDOM_DATABASE_DSN` env 已覆盖 `database_dsn`**（line 75）；`fileConfig` 承载 `database_dsn`。生产模式 `LoadConfig` 要求 master key/root secret/TLS——**迁移不需要这些**，故须轻量取 DSN。
- `cmd/sydom-controlplane/main.go`：`os.Exit(app.Main())`；flag 解析在 `app.Main()`（读 `-config`）。
- M5.3-k8s chart：Deployment `RollingUpdate maxUnavailable:0`、config Secret 含 `config.yaml`（有 `database_dsn`）。

## 3. 方案选择

**A（选定）嵌入迁移 + 控制面 `-migrate` 模式 + Helm hook Job 复用控制面镜像。**
- `db/migrations/embed.go` `//go:embed *.sql` → `var FS embed.FS`（迁移文件单一真相源，构建期嵌入二进制）。
- `internal/db.RunMigrationsFS(dsn, fs.FS)`（golang-migrate `source/iofs`）。
- 控制面加 `-migrate` flag：轻量 `LoadDSN` 取 DSN → `RunMigrationsFS(dsn, migrations.FS)` → 退出（0 成功/非 0 失败）。
- Helm `pre-install,pre-upgrade` hook Job 用**同一控制面镜像** `args:[-config,/etc/sydom/config.yaml,-migrate]`，挂 config Secret（只需 config.yaml 取 DSN），同款硬化 securityContext。
- **优点**：单一真相源（embed）、迁移与代码同版本同镜像（content-addressed）、复用配置装载 + Secret 挂载、无第三方镜像、无 ConfigMap 复制迁移文件（Helm `.Files` 够不到 chart 外的 `db/migrations`，复制必致漂移）。

**B ConfigMap 挂迁移 + `migrate/migrate` 镜像 Job。** 须把 `db/migrations` 复制进 chart（Helm `.Files` 只能读 chart 内）→ 双份漂移；用非我方硬化的第三方镜像。否决。

**C initContainer 在控制面 Pod 内跑迁移。** 每个副本启动都跑迁移→N 副本并发迁移竞态（虽 golang-migrate 有迁移锁，但 N 副本同时抢锁、失败副本 CrashLoop）；且违背「迁移先于 rollout」的零停机时序。否决（用 pre-upgrade hook Job 单次迁移更干净）。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `db/migrations/embed.go`（新） | `package migrations` + `//go:embed *.sql` → `var FS embed.FS` |
| `internal/db/migrate.go`（改） | 加 `RunMigrationsFS(dsn string, fsys fs.FS) error`（`source/iofs`）；`RunMigrations`/`MigrateDown` 不动 |
| `internal/db/migrate_iofs_test.go`（新） | `RunMigrationsFS(embed.FS)` 迁移全新库 + 幂等（二次调用 ErrNoChange）|
| `internal/controlplane/app/config.go`（改） | 加 `LoadDSN(path, getenv)(string,error)`（轻量：file `database_dsn` + `SYDOM_DATABASE_DSN` override；空→错）|
| `internal/controlplane/app/config_test.go`（改/新） | `LoadDSN` file/env override/空 fail |
| `internal/controlplane/app/migrate.go`（新） | `runMigrate(path, getenv)(error)`：LoadDSN → `db.RunMigrationsFS(dsn, migrations.FS)` |
| `internal/controlplane/app/main.go`（改，即 `app.Main()` 所在） | 加 `-migrate` bool flag：置位则 `runMigrate` 后 return（不进 Run）|
| `deploy/helm/sydom-controlplane/templates/migration-job.yaml`（新） | pre-install/pre-upgrade hook Job，复用控制面镜像 `-migrate` |
| `deploy/helm/sydom-controlplane/values.yaml`（改） | 加 `migrations.enabled`(默认 true)/`hookWeight` |
| `deploy/helm/sydom-controlplane/templates/NOTES.txt`（改） | 迁移已自动化（去「先手动迁移」告警，改述 hook 时序）|
| `deploy/helm/sydom-controlplane/README.md`（改） | 迁移自动化说明 + expand/contract 引用 |
| `docs/runbooks/zero-downtime-migrations.md`（新） | expand/contract 纪律运维手册 |

### 4.2 `-migrate` 模式（控制面二进制）

`app.Main()` 解析 flag 时新增 `-migrate`。置位时：`runMigrate(configPath, os.Getenv)` → 成功 return 0、失败打错误 return 1，**不进入 `Run`**（不起监听器/不连 Redis/不校验密钥/TLS）。`runMigrate`：
```
dsn, err := LoadDSN(path, getenv)       // 轻量，只要 DSN
if err != nil { return err }
return db.RunMigrationsFS(dsn, migrations.FS)   // 幂等 up
```
`LoadDSN` 复用 `fileConfig` 解析，返回 `firstNonEmpty(getenv("SYDOM_DATABASE_DSN"), fc.DatabaseDSN)`，空则错。**不触发** master key/root secret/TLS 的生产 fail-close（迁移无关）。

### 4.3 Helm hook Job

```yaml
{{- if .Values.migrations.enabled }}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ fullname }}-migrate
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-5"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: Never
      securityContext: {runAsNonRoot, 65532, seccomp RuntimeDefault}
      containers:
        - name: migrate
          image: {{ image }}
          args: ["-config", "/etc/sydom/config.yaml", "-migrate"]
          securityContext: {readOnlyRootFilesystem, drop ALL, allowPrivEsc false}
          volumeMounts: [config.yaml subPath from Secret]
      volumes: [config Secret]
{{- end }}
```
hook 在 install/upgrade **应用其它资源之前**跑（weight -5 早于默认 0），Job 成功才继续 rollout；失败则 helm release 失败、**不滚动**（fail-close：迁移没成就不上新代码）。挂 config Secret 只取 DSN（迁移不需密钥/TLS mount）。

### 4.4 零停机（expand/contract）纪律

零停机不是单靠自动化，而是**迁移形态 + 时序**共同保证。运维手册确立纪律：
- **一次发布只做 expand（向后兼容）**：加列（nullable/有默认）、加表、加索引（CONCURRENTLY）、加约束（NOT VALID 后 VALIDATE）。旧代码 + 新代码在滚动期都能工作。
- **contract（破坏性：删列/改类型/加 NOT NULL）延到下一次发布**，且须确认旧版本 Pod 全下线。
- **时序**：pre-upgrade hook Job 先迁移（expand）→ `maxUnavailable:0` 滚动上新代码（新旧共存期 DB 已 expand，双向兼容）→ 下次发布再 contract。
- 违反（在滚动期做 contract）→ 滚动窗口内旧 Pod 撞缺列/类型不符 → 500。手册以「可/不可」清单 + 示例固化。

### 4.5 数据流

`helm upgrade` → pre-upgrade hook：起 `{{fullname}}-migrate` Job（控制面镜像 `-migrate`）→ `LoadDSN` 读挂载 config.yaml 的 DSN → `RunMigrationsFS(embed)` 幂等 `up`（golang-migrate 迁移锁防并发）→ Job 成功 → helm 继续 → `maxUnavailable:0` 滚动新副本（DB 已迁）→ 旧副本渐退。Job 失败 → helm release 失败、rollout 不发生（fail-close）。

## 5. 验证

- **TDD Go**：`RunMigrationsFS(embed.FS)` 对 testcontainers 全新库迁移成功 + 二次幂等（`schema_migrations` 版本达最新、无错）；变异实验证有齿（临时给错 FS/空 FS → FAIL）。`LoadDSN` file/env-override/空-fail 三态。`-migrate` flag 路径：`go build` + 单测 `runMigrate` 对 dbtest DSN 迁移成功。
- **helm**：`helm lint` 0 failed；`helm template`（默认 migrations.enabled）渲染 Job 带 `helm.sh/hook: pre-install,pre-upgrade`、`-migrate` arg、复用 image、硬化 securityContext；`migrations.enabled=false` → 无 Job。
- **零触碰授权核心**：`git diff 2ce70d6..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/ internal/auth/ internal/obs/` = 空。
- 全量 `go test ./...` EXIT 0。

## 6. 验收标准（M53M-1..7）

- **M53M-1** 零触碰授权核心：上述 diff = 空。
- **M53M-2** 迁移嵌入：`db/migrations/embed.go` `//go:embed *.sql`；`go build ./...` 成功，二进制含迁移。
- **M53M-3** `RunMigrationsFS(embed)` 迁移全新库成功 + 幂等（TDD，变异证有齿）。
- **M53M-4** `-migrate` 模式：控制面二进制 `-migrate` 只迁移不起服务、DSN 走轻量 `LoadDSN`（不要求密钥/TLS）；`LoadDSN` 三态测试。
- **M53M-5** Helm Job：`helm lint` 0、渲染 pre-install/pre-upgrade hook Job 复用控制面镜像 `-migrate` + 硬化 + `migrations.enabled` 门控。
- **M53M-6** 零停机纪律手册：`docs/runbooks/zero-downtime-migrations.md` 含 expand/contract 可/不可清单 + 时序 + 示例。
- **M53M-7** `go test ./...` EXIT 0；NOTES/README 去「先手动迁移」告警改述自动化。

## 7. 风险

- **golang-migrate `source/iofs`**：v4.17.1 已含；`iofs.New(fsys, ".")` 以 embed 根为迁移目录。风险低，TDD 覆盖。
- **迁移锁并发**：pre-upgrade hook 是单个 Job（非 N 副本），且 golang-migrate 持 PG 迁移锁，天然串行。
- **embed 与 file:// 双源**：dbtest/既有测试仍用 `file://`（不改，减小爆炸半径）；生产走 embed。两者读同一批 `db/migrations/*.sql`，非真复制。
- **hook 失败阻断 rollout**：刻意 fail-close（迁移不成不上新代码），运维须监控 hook Job。
