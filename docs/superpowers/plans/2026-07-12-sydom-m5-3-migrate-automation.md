# M5.3-migrate 迁移自动化 + 零停机 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 DB 迁移编入控制面二进制并加 `-migrate` 模式，配 Helm pre-upgrade hook Job 在滚动更新前自动幂等迁移，并确立 expand/contract 零停机纪律。

**架构：** `db/migrations/embed.go` 用 `//go:embed *.sql` 嵌入迁移（单一真相源）；`internal/db.RunMigrationsFS`（golang-migrate iofs）应用嵌入迁移；控制面 `-migrate` flag 走轻量 `LoadDSN`（只要 DSN、不要密钥/TLS）跑迁移后退出；Helm `pre-install,pre-upgrade` hook Job 复用控制面硬化镜像 `-migrate`，在其它资源前跑、成功才滚动（fail-close）。

**技术栈：** Go `//go:embed` + golang-migrate v4.17.1 `source/iofs`、testcontainers PG（内部 `startPostgres` helper）、Helm v4.2.0 hook、`docs/runbooks`。

**BASE：** `feat/m5-3-migrate-automation` @ 含设计规格提交；规格 `docs/superpowers/specs/2026-07-12-sydom-m5-3-migrate-automation-design.md`。

**零触碰铁律：** `git diff 2ce70d6..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs` 必须为空。

---

## 任务 1：嵌入迁移 + RunMigrationsFS（TDD）

**文件：**
- 创建：`db/migrations/embed.go`
- 修改：`internal/db/migrate.go`
- 测试：`internal/db/migrate_iofs_test.go`（新）

- [ ] **步骤 1：写嵌入包**

`db/migrations/embed.go`：
```go
// Package migrations 嵌入 db/migrations 下的全部 SQL 迁移文件，供控制面 -migrate 模式用
// golang-migrate 的 iofs source 应用（单一真相源，构建期编入二进制、与代码同版本）。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **步骤 2：写失败的测试**

`internal/db/migrate_iofs_test.go`：
```go
package db

import (
	"database/sql"
	"testing"

	migrations "github.com/nickZFZ/Sydom/db/migrations"
	"github.com/stretchr/testify/require"
)

// 从嵌入的迁移把全新库迁到最新，关键表存在，且二次调用幂等（无错）。
func TestRunMigrationsFS_EmbedFreshDBIdempotent(t *testing.T) {
	dsn := startPostgres(t)

	require.NoError(t, RunMigrationsFS(dsn, migrations.FS))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, tbl := range []string{"tenant", "application", "casbin_rule", "policy_outbox"} {
		require.Truef(t, tableExists(t, db, tbl), "表 %s 应在嵌入迁移 up 后存在", tbl)
	}

	// 幂等：再次调用应无错（golang-migrate ErrNoChange 被吞）。
	require.NoError(t, RunMigrationsFS(dsn, migrations.FS))
}
```
（`startPostgres`/`tableExists` 为 `internal/db` 既有测试 helper，见 `helpers_test.go`/`schema_test.go`，同 `package db` 可直接复用。）

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/db/ -run TestRunMigrationsFS_EmbedFreshDBIdempotent -v`
预期：编译失败 `undefined: RunMigrationsFS`。

- [ ] **步骤 4：实现 RunMigrationsFS**

在 `internal/db/migrate.go`：import 追加 `"io/fs"` 与 `"github.com/golang-migrate/migrate/v4/source/iofs"`，并加：
```go
// RunMigrationsFS 对 dsn 指向的数据库应用 fsys 里的全部 up migration（幂等）。
// fsys 通常是 //go:embed 的 embed.FS（生产路径，迁移编入二进制、与代码同版本）。
func RunMigrationsFS(dsn string, fsys fs.FS) error {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
```
（`migrate`/`errors` 已 import；`source/file`/`database/postgres` 既有匿名 import 保留。）

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/db/ -run TestRunMigrationsFS_EmbedFreshDBIdempotent -v`
预期：PASS（需 Docker 起 PG）。

- [ ] **步骤 6：变异实验证有齿**

临时把测试里 `RunMigrationsFS(dsn, migrations.FS)` 的 `migrations.FS` 换成空 `embed.FS{}`（`var empty embed.FS`）。
运行同命令，预期 **FAIL**（`iofs.New` 或 `Up` 报无迁移/first .. file does not exist）。观察 FAIL 后**还原** `migrations.FS`，再跑确认 PASS。证测试对「迁移源丢失」有齿。

- [ ] **步骤 7：Commit**

```bash
git add db/migrations/embed.go internal/db/migrate.go internal/db/migrate_iofs_test.go
git commit -m "feat(db): M5.3-migrate 嵌入迁移(go:embed *.sql)+RunMigrationsFS(golang-migrate iofs source,幂等,与代码同版本单一真相源)"
```

---

## 任务 2：LoadDSN 轻量取 DSN（TDD）

**文件：**
- 修改：`internal/controlplane/app/config.go`
- 测试：`internal/controlplane/app/loaddsn_test.go`（新）

- [ ] **步骤 1：写失败的测试**

`internal/controlplane/app/loaddsn_test.go`：
```go
package app

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDSN(t *testing.T) {
	empty := func(string) string { return "" }

	// (a) 取 file database_dsn
	p := writeCfg(t, "database_dsn: \"postgres://f/db\"\n")
	if dsn, err := LoadDSN(p, empty); err != nil || dsn != "postgres://f/db" {
		t.Fatalf("file DSN: got %q err %v", dsn, err)
	}

	// (b) env SYDOM_DATABASE_DSN 覆盖 file
	getenv := func(k string) string {
		if k == "SYDOM_DATABASE_DSN" {
			return "postgres://e/db"
		}
		return ""
	}
	if dsn, err := LoadDSN(p, getenv); err != nil || dsn != "postgres://e/db" {
		t.Fatalf("env override: got %q err %v", dsn, err)
	}

	// (c) 空 DSN → 错（不静默放行）
	p2 := writeCfg(t, "redis_addr: \"r:6379\"\n")
	if _, err := LoadDSN(p2, empty); err == nil {
		t.Fatal("空 DSN 应返错")
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/app/ -run TestLoadDSN -v`
预期：编译失败 `undefined: LoadDSN`。

- [ ] **步骤 3：实现 LoadDSN**

在 `internal/controlplane/app/config.go` 末尾加：
```go
// LoadDSN 只解析出数据库 DSN（file database_dsn，SYDOM_DATABASE_DSN env 覆盖），供 -migrate
// 模式使用——迁移无关密钥/TLS，故不走 LoadConfig 的生产 fail-close。空 DSN 返错。
func LoadDSN(path string, getenv func(string) string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return "", fmt.Errorf("parse config: %w", err)
	}
	dsn := firstNonEmpty(getenv("SYDOM_DATABASE_DSN"), fc.DatabaseDSN)
	if dsn == "" {
		return "", errors.New("database_dsn required（config 或 SYDOM_DATABASE_DSN）")
	}
	return dsn, nil
}
```
（`os`/`fmt`/`errors`/`yaml`/`firstNonEmpty`/`fileConfig` 均已在本文件。）

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/app/ -run TestLoadDSN -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/config.go internal/controlplane/app/loaddsn_test.go
git commit -m "feat(cp): M5.3-migrate LoadDSN 轻量取 DSN(file database_dsn+SYDOM_DATABASE_DSN 覆盖,空返错;迁移无关密钥/TLS 故不走生产 fail-close)"
```

---

## 任务 3：`-migrate` 模式（runMigrate + Main flag）

**文件：**
- 创建：`internal/controlplane/app/migrate.go`
- 修改：`internal/controlplane/app/run.go`（`Main()`）
- 测试：`internal/controlplane/app/migrate_test.go`（新）

- [ ] **步骤 1：写 runMigrate**

`internal/controlplane/app/migrate.go`：
```go
package app

import (
	migrations "github.com/nickZFZ/Sydom/db/migrations"
	"github.com/nickZFZ/Sydom/internal/db"
)

// runMigrate 是 -migrate 模式主体：轻量取 DSN → 应用嵌入的迁移（幂等 up）→ 返回。
// 不装载密钥/TLS、不起监听器（迁移无关运行时凭据）。
func runMigrate(path string, getenv func(string) string) error {
	dsn, err := LoadDSN(path, getenv)
	if err != nil {
		return err
	}
	return db.RunMigrationsFS(dsn, migrations.FS)
}
```

- [ ] **步骤 2：写失败的测试**

`internal/controlplane/app/migrate_test.go`：
```go
package app

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// runMigrate 对全新库应用嵌入迁移：关键表建立、幂等。
func TestRunMigrate_FreshDB(t *testing.T) {
	dsn := dbtest.StartPostgres(t)
	cfg := writeCfg(t, "database_dsn: \""+dsn+"\"\n")
	empty := func(string) string { return "" }

	require.NoError(t, runMigrate(cfg, empty))

	conn, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	var reg sql.NullString
	require.NoError(t, conn.QueryRow(`SELECT to_regclass('application')`).Scan(&reg))
	require.True(t, reg.Valid, "application 表应在 runMigrate 后存在")

	// 幂等
	require.NoError(t, runMigrate(cfg, empty))
}
```
（`writeCfg` 为任务 2 测试文件里的 helper，同 `package app` 可复用；`dbtest.StartPostgres` 返回全新库 DSN。）

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/app/ -run TestRunMigrate_FreshDB -v`
预期：编译失败 `undefined: runMigrate`（步骤 1 若已写则应通过——先确保测试存在再跑）。若 runMigrate 已定义，预期直接尝试跑（可能因 Main 未接线仍 PASS，接线在步骤 4）。

- [ ] **步骤 4：Main() 接 -migrate flag**

在 `internal/controlplane/app/run.go` 的 `Main()` 里，把：
```go
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := LoadConfig(*configPath, os.Getenv)
```
改为：
```go
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	migrateOnly := flag.Bool("migrate", false, "apply embedded DB migrations then exit（迁移专用模式，不起服务）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *migrateOnly {
		if err := runMigrate(*configPath, os.Getenv); err != nil {
			logger.Error("migrate", "err", err)
			return 1
		}
		logger.Info("migrations applied")
		return 0
	}

	cfg, err := LoadConfig(*configPath, os.Getenv)
```

- [ ] **步骤 5：运行验证通过 + 编译**

运行：`go build ./... && go test ./internal/controlplane/app/ -run 'TestRunMigrate_FreshDB|TestLoadDSN' -v`
预期：build 无错；两测试 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/app/migrate.go internal/controlplane/app/migrate_test.go internal/controlplane/app/run.go
git commit -m "feat(cp): M5.3-migrate 控制面 -migrate 模式(runMigrate=LoadDSN+RunMigrationsFS 嵌入迁移;Main 加 -migrate flag 只迁移不起服务;幂等)"
```

---

## 任务 4：Helm 迁移 Job（pre-upgrade hook）+ values + NOTES/README

**文件：**
- 创建：`deploy/helm/sydom-controlplane/templates/migration-job.yaml`
- 修改：`deploy/helm/sydom-controlplane/values.yaml`
- 修改：`deploy/helm/sydom-controlplane/templates/NOTES.txt`
- 修改：`deploy/helm/sydom-controlplane/README.md`

- [ ] **步骤 1：values 加 migrations**

在 `deploy/helm/sydom-controlplane/values.yaml` 的 `pdb:` 块后加：
```yaml
# 迁移自动化（M5.3-migrate）：pre-install/pre-upgrade hook Job 在滚动前跑嵌入迁移。
migrations:
  enabled: true
  hookWeight: "-5"     # 早于默认 0，迁移先于其它资源
  backoffLimit: 3
```

- [ ] **步骤 2：写 migration-job.yaml**

`deploy/helm/sydom-controlplane/templates/migration-job.yaml`：
```
{{- if .Values.migrations.enabled }}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}-migrate
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": {{ .Values.migrations.hookWeight | quote }}
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  backoffLimit: {{ .Values.migrations.backoffLimit }}
  template:
    metadata:
      labels:
        {{- include "sydom-controlplane.selectorLabels" . | nindent 8 }}
    spec:
      restartPolicy: Never
      serviceAccountName: {{ include "sydom-controlplane.serviceAccountName" . }}
      automountServiceAccountToken: false
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: migrate
          image: {{ include "sydom-controlplane.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          args: ["-config", "/etc/sydom/config.yaml", "-migrate"]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          volumeMounts:
            - name: config
              mountPath: /etc/sydom/config.yaml
              subPath: config.yaml
              readOnly: true
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
      volumes:
        - name: config
          secret:
            secretName: {{ include "sydom-controlplane.secretName" . }}
{{- end }}
```

- [ ] **步骤 3：改 NOTES.txt（迁移已自动化）**

把 `deploy/helm/sydom-controlplane/templates/NOTES.txt` 里这段：
```
⚠️ 迁移时序：本 chart 不含数据库迁移（归 M5.3-migrate 零停机切片）。
   首次安装/升级前，请先对目标库应用 db/migrations（make migrate-up 或外部 Job）。
```
替换为：
```
✅ 迁移自动化（M5.3-migrate）：pre-install/pre-upgrade hook Job（{{ include "sydom-controlplane.fullname" . }}-migrate）
   在滚动更新之前跑嵌入迁移（幂等 up）；Job 失败则 release 失败、不滚动（fail-close）。
   零停机纪律（一次发布只做 expand 向后兼容变更）见 docs/runbooks/zero-downtime-migrations.md。
```

- [ ] **步骤 4：改 README（迁移前置说明）**

把 `deploy/helm/sydom-controlplane/README.md` 里 `## 前置` 下这条：
```
- **数据库迁移须先于安装完成**（本 chart 不含迁移，见 M5.3-migrate）：`make migrate-up` 或外部 Job 对目标库应用 `db/migrations`。
```
替换为：
```
- **数据库迁移已自动化**（M5.3-migrate）：`migrations.enabled=true`（默认）时，pre-install/pre-upgrade hook Job 在滚动前跑嵌入迁移；失败则不滚动（fail-close）。零停机纪律见 `docs/runbooks/zero-downtime-migrations.md`。
```

- [ ] **步骤 5：验证渲染**

运行：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
helm lint $CP
helm template t $CP $SET 2>&1 | grep -E "kind: Job|helm.sh/hook: pre-install,pre-upgrade|hook-weight|\"-migrate\"|-migrate|name: t-sydom-controlplane-migrate" | head
# migrations.enabled=false → 无 Job
test $(helm template t $CP $SET --set migrations.enabled=false 2>&1 | grep -c "kind: Job") -eq 0 && echo "JOB-OFF-OK"
```
预期：`helm lint` 0 failed；渲染含 `kind: Job`、`helm.sh/hook: pre-install,pre-upgrade`、`-migrate` arg；`JOB-OFF-OK`。

- [ ] **步骤 6：Commit**

```bash
git add deploy/helm/sydom-controlplane/
git commit -m "feat(deploy): M5.3-migrate Helm pre-install/pre-upgrade hook Job(复用控制面镜像 -migrate,硬化,只挂 config Secret 取 DSN,失败不滚动 fail-close)+migrations 开关+NOTES/README 去手动迁移告警"
```

---

## 任务 5：零停机 runbook + 最终验收

**文件：**
- 创建：`docs/runbooks/zero-downtime-migrations.md`

- [ ] **步骤 1：写 runbook**

`docs/runbooks/zero-downtime-migrations.md`：
````markdown
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
````

- [ ] **步骤 2：最终验收 —— 全量测试 + helm + 零触碰**

运行：
```bash
go build ./... && go test ./... 2>&1 | grep -E "FAIL|panic" | head; echo "GO-EXIT=${PIPESTATUS[1]}"
helm lint deploy/helm/sydom-controlplane
git diff 2ce70d6..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs | head; echo "ZERO-TOUCH-DONE(应空)"
```
预期：`go test ./...` 无 FAIL（GO-EXIT=0）；`helm lint` 0 failed；零触碰 diff 为空。

- [ ] **步骤 3：Commit**

```bash
git add docs/runbooks/zero-downtime-migrations.md
git commit -m "docs(runbook): M5.3-migrate 零停机迁移 expand/contract 纪律手册(可/不可清单+一次发布只 expand+两段式删列范例+回滚靠前滚/备份非 down)"
```

---

## 自检

**1. 规格覆盖度：**
- §4.1 文件 → 任务 1-5 全部。
- §4.2 `-migrate` 模式 → 任务 2（LoadDSN）+任务 3（runMigrate+Main flag）。
- §4.3 Helm hook Job → 任务 4。
- §4.4 expand/contract 纪律 → 任务 5 runbook。
- §5 验证 → 各任务 TDD + 任务 5 最终验收。
- §6 M53M-1..7 → M53M-1 任务5步2、M53M-2 任务1、M53M-3 任务1（含变异步6）、M53M-4 任务2+3、M53M-5 任务4、M53M-6 任务5步1、M53M-7 任务5步2+任务4步3/4。全覆盖。

**2. 占位符扫描：** 各步含实际代码/命令+预期输出；无 TODO/伪代码。变异实验（任务1步6）给确切改法与还原。

**3. 类型一致性：** `RunMigrationsFS(dsn string, fsys fs.FS) error`（任务1定义、任务3 `db.RunMigrationsFS(dsn, migrations.FS)` 调用一致）；`migrations.FS embed.FS`（任务1定义、任务3 import 复用）；`LoadDSN(path string, getenv func(string) string)(string,error)`（任务2定义、任务3 runMigrate 调用一致）；`runMigrate(path string, getenv func(string) string) error`（任务3定义、Main 调用一致）；`dbtest.StartPostgres(t)` 既有导出（返回全新库 DSN）；helm helper `sydom-controlplane.{fullname,labels,selectorLabels,serviceAccountName,image,secretName}` 沿用 M5.3-k8s 既有定义。
