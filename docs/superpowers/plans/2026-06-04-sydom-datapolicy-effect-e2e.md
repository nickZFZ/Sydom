# DataPolicy effect 端到端贯通 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把数据策略 `effect`（allow/deny）字段从管理 API 创建端到端穿过 DB → 控制面写/读 → translate → 同步 wire proto，使 deny 数据策略可被创建并下发（Sidecar 消费归 ④-3）。

**架构：** 单一关注点的跨层穿字段改造。新增 DB 列 `data_policy.effect`（默认 allow + CHECK 约束）；`cp.DataPolicy`/store 读写/两个 proto（sync + admin，均 `string effect`）/translate/mgmt 各透传一个字段。取值约束由 DB CHECK（硬）+ 管理层校验（软，返 InvalidArgument）双兜底；空串语义 = allow。

**技术栈：** Go 1.26 + PostgreSQL（golang-migrate）+ buf/protobuf + gRPC。测试用 testify + `internal/dbtest`（testcontainers PG）+ bufconn。

**规格：** `docs/superpowers/specs/2026-06-04-sydom-datapolicy-effect-e2e-design.md`。

---

## 已核实的现状（实现者据此写，勿臆测）

- `data_policy` 表（migration `000008`）**无 effect 列**；最大 migration 号为 `000014`，故新增 `000015`。
- `cp.DataPolicy{ID int64; SubjectType, SubjectID, Resource, Condition string}`（`internal/controlplane/types.go:25`，本计划加 `Effect string`）。
- `store.UpsertDataPolicy(ctx, ex cp.DBTX, appID int64, p cp.DataPolicy, version int64) (id int64, created bool, err error)` 在 `internal/controlplane/store/store.go`：ID==0 走 INSERT（RETURNING id），否则 UPDATE（带 RowsAffected==0 报错，**勿回退此校验**）。
- `store.ReadAppDataPolicies(ctx, q cp.DBTX, appID int64) ([]cp.DataPolicy, error)` 在 `internal/controlplane/store/read.go`，`package store`；其测试 `TestReadAppDataPolicies` 在 `package store_test`，断言完整 `cp.DataPolicy{...}` 相等（本计划需同步加 `Effect`）。
- `translate.dataPolicyToProto(p cp.DataPolicy) *syncv1.DataPolicy`（`internal/controlplane/translate/translate.go:64`）；`DataPoliciesToProto`（快照）与 `DeltaToProto(d cp.Delta) *syncv1.Delta`（增量）**都复用** `dataPolicyToProto`——一处加字段两条出口同时生效。translate 测试为 `package translate`（白盒）。
- 同步 proto `sync.v1.DataPolicy` 字段号到 5（id/subject_type/subject_id/resource/condition），加 `string effect = 6`。
- 管理 proto `admin.v1.UpsertDataPolicyRequest` 字段号到 6，加 `string effect = 7`。
- `mgmt/server.go UpsertDataPolicy`（`internal/controlplane/mgmt/server.go:95`）已 import `codes`/`status`；mgmt 测试用 `dialMgmt(t, db, principal, secret)`（bufconn + `dbtest.SetupSchema` + root operator）。
- proto 重新生成：`make proto-gen`（buf 自带 protocompile；若工具缺失先 `make proto-tools`），输出到 `gen/`。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `db/migrations/000015_data_policy_effect.{up,down}.sql` | 加 effect 列 + CHECK 约束 | 1 |
| `docs/.../2026-05-31-sydom-database-schema-design.md` | DB spec 同步 effect 列 | 1 |
| `internal/controlplane/store/data_policy_effect_test.go` | 列默认/CHECK/deny 的 schema 级测试 | 1 |
| `internal/controlplane/types.go` | `cp.DataPolicy` +Effect | 2 |
| `internal/controlplane/store/store.go` | UpsertDataPolicy 写 effect（空串归一 allow） | 2 |
| `internal/controlplane/store/read.go` | ReadAppDataPolicies 读 effect | 2 |
| `api/proto/sydom/sync/v1/policy_sync.proto` + `gen/` | sync DataPolicy +effect | 3 |
| `internal/controlplane/translate/translate.go` | dataPolicyToProto +Effect | 3 |
| `api/proto/sydom/admin/v1/admin.proto` + `gen/` | admin UpsertDataPolicyRequest +effect | 4 |
| `internal/controlplane/mgmt/server.go` | UpsertDataPolicy 透传 + 归一/校验 | 4 |

---

## 任务 1：DB migration 000015（effect 列 + CHECK）

**文件：**
- 创建：`db/migrations/000015_data_policy_effect.up.sql`、`db/migrations/000015_data_policy_effect.down.sql`
- 创建测试：`internal/controlplane/store/data_policy_effect_test.go`
- 修改文档：`docs/superpowers/specs/2026-05-31-sydom-database-schema-design.md`（data_policy 表加 effect 列说明，无测试）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/store/data_policy_effect_test.go`：
```go
package store_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestDataPolicyEffectColumn 验证 000015 迁移：effect 列默认 allow、CHECK 拒非法值、接受 deny。
func TestDataPolicyEffectColumn(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var eff string
	// 不指定 effect → 默认 allow
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','m','order','{}'::jsonb,1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "allow", eff)

	// 显式 deny 接受
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'user','a','order','{}'::jsonb,'deny',1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "deny", eff)

	// 非法值被 CHECK 拒
	_, err := db.Exec(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'role','m','order','{}'::jsonb,'bogus',1)`, appID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run TestDataPolicyEffectColumn -count=1`
预期：FAIL（effect 列不存在，INSERT/RETURNING 报 column does not exist）。

- [ ] **步骤 3：编写实现**

`db/migrations/000015_data_policy_effect.up.sql`：
```sql
ALTER TABLE data_policy
    ADD COLUMN effect VARCHAR(8) NOT NULL DEFAULT 'allow'
    CONSTRAINT data_policy_effect_chk CHECK (effect IN ('allow', 'deny'));
```

`db/migrations/000015_data_policy_effect.down.sql`：
```sql
ALTER TABLE data_policy DROP COLUMN effect;
```

并在 DB schema spec 的 data_policy 表定义处补一行 effect 列说明（`effect VARCHAR(8) NOT NULL DEFAULT 'allow'`，取值 allow|deny，具名 CHECK `data_policy_effect_chk`）。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -run TestDataPolicyEffectColumn -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000015_data_policy_effect.up.sql db/migrations/000015_data_policy_effect.down.sql internal/controlplane/store/data_policy_effect_test.go docs/superpowers/specs/2026-05-31-sydom-database-schema-design.md
git commit -m "feat(db): data_policy 加 effect 列（默认 allow + CHECK allow|deny）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：cp.DataPolicy +Effect + store 读写穿 effect

**文件：**
- 修改：`internal/controlplane/types.go`（DataPolicy 加 Effect）
- 修改：`internal/controlplane/store/store.go`（UpsertDataPolicy 写 effect）
- 修改：`internal/controlplane/store/read.go`（ReadAppDataPolicies 读 effect）
- 修改测试：`internal/controlplane/store/read_test.go`（既有 TestReadAppDataPolicies 加 Effect）
- 新增测试：在 `internal/controlplane/store/data_policy_effect_test.go` 追加 store 往返用例

- [ ] **步骤 1：编写失败的测试**

在 `internal/controlplane/store/data_policy_effect_test.go` 追加（注意补 import `context`、`store`、`cp`）：
```go
func TestUpsertDataPolicy_EffectRoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 新增 deny
	denyID, created, err := store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "deny",
	}, 1)
	require.NoError(t, err)
	require.True(t, created)

	// 空串归一为 allow
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "user", SubjectID: "alice", Resource: "invoice", Condition: "{}", Effect: "",
	}, 2)
	require.NoError(t, err)

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	byID := map[int64]string{}
	for _, p := range got {
		byID[p.ID] = p.Effect
	}
	require.Equal(t, "deny", byID[denyID])
	require.Len(t, got, 2)
	for _, p := range got {
		require.Contains(t, []string{"allow", "deny"}, p.Effect)
	}

	// UPDATE 改 effect deny→allow
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		ID: denyID, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "allow",
	}, 3)
	require.NoError(t, err)
	got2, _ := store.ReadAppDataPolicies(ctx, db, appID)
	for _, p := range got2 {
		if p.ID == denyID {
			require.Equal(t, "allow", p.Effect)
		}
	}
}
```
导入块改为：
```go
import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)
```

同时更新既有 `TestReadAppDataPolicies`（在 `store/read_test.go` 或 store 测试文件中）的期望值，给 `cp.DataPolicy{...}` 加 `Effect: "allow"`（读路径加列后会回默认值）。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestUpsertDataPolicy_EffectRoundTrip|TestReadAppDataPolicies' -count=1`
预期：FAIL（`cp.DataPolicy` 无 Effect 字段，编译错误）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/types.go` 的 `DataPolicy` 末尾加字段：
```go
type DataPolicy struct {
	ID          int64
	SubjectType string // "role" / "user"
	SubjectID   string
	Resource    string
	Condition   string // 条件树 JSON
	Effect      string // "allow" | "deny"；空串按 "allow"（对齐 DB 默认）
}
```

`internal/controlplane/store/store.go` 的 `UpsertDataPolicy` 改为（加 effect 列；空串归一为 allow 以避开 CHECK；其余逻辑含 RowsAffected 校验保持不变）：
```go
func UpsertDataPolicy(ctx context.Context, ex cp.DBTX, appID int64, p cp.DataPolicy, version int64) (id int64, created bool, err error) {
	effect := p.Effect
	if effect == "" {
		effect = "allow"
	}
	if p.ID == 0 {
		err = ex.QueryRowContext(ctx, `
			INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7) RETURNING id`,
			appID, p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, version).Scan(&id)
		return id, true, err
	}
	res, err := ex.ExecContext(ctx, `
		UPDATE data_policy SET subject_type=$1, subject_id=$2, resource=$3, condition=$4::jsonb,
		       effect=$5, version=$6, updated_at=now()
		WHERE app_id=$7 AND id=$8`,
		p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, version, appID, p.ID)
	if err != nil {
		return p.ID, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p.ID, false, err
	}
	if n == 0 {
		return 0, false, fmt.Errorf("data_policy id=%d not found for app %d", p.ID, appID)
	}
	return p.ID, false, nil
}
```

`internal/controlplane/store/read.go` 的 `ReadAppDataPolicies`：SELECT 加 effect、Scan 多收一个字段：
```go
	rows, err := q.QueryContext(ctx,
		`SELECT id, subject_type, subject_id, resource, condition, effect FROM data_policy WHERE app_id=$1`, appID)
```
```go
		if err := rows.Scan(&p.ID, &p.SubjectType, &p.SubjectID, &p.Resource, &p.Condition, &p.Effect); err != nil {
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -count=1`
预期：PASS（新往返用例 + 既有 store 用例全绿）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/types.go internal/controlplane/store/store.go internal/controlplane/store/read.go internal/controlplane/store/data_policy_effect_test.go internal/controlplane/store/read_test.go
git commit -m "feat(controlplane/store): data_policy 读写穿 effect（空串归一 allow，写校验 RowsAffected 不变）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：sync proto +effect + translate 穿 effect（下发出口）

**文件：**
- 修改：`api/proto/sydom/sync/v1/policy_sync.proto` + 重新生成 `gen/`
- 修改：`internal/controlplane/translate/translate.go`（dataPolicyToProto）
- 新增测试：`internal/controlplane/translate/translate_effect_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/translate/translate_effect_test.go`：
```go
package translate

import (
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
)

// TestDataPoliciesToProto_Effect 验证快照出口携带 effect。
func TestDataPoliciesToProto_Effect(t *testing.T) {
	out := DataPoliciesToProto([]cp.DataPolicy{
		{ID: 1, SubjectType: "role", SubjectID: "m", Resource: "order", Condition: "{}", Effect: "deny"},
	})
	require.Len(t, out, 1)
	require.Equal(t, "deny", out[0].Effect)
}

// TestDeltaToProto_DataEffect 验证增量出口携带 effect（复用 dataPolicyToProto）。
func TestDeltaToProto_DataEffect(t *testing.T) {
	out := DeltaToProto(cp.Delta{
		Version: 2,
		DataChanges: []cp.DataPolicyChange{
			{Op: cp.ChangeAdd, Policy: cp.DataPolicy{ID: 1, SubjectType: "user", SubjectID: "a", Resource: "order", Condition: "{}", Effect: "deny"}},
		},
	})
	require.Len(t, out.DataChanges, 1)
	require.Equal(t, "deny", out.DataChanges[0].Policy.Effect)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/translate/ -run Effect -count=1`
预期：FAIL（`syncv1.DataPolicy` 无 Effect 字段，编译错误）。

- [ ] **步骤 3：编写实现**

`api/proto/sydom/sync/v1/policy_sync.proto` 的 `DataPolicy` 消息加字段：
```proto
message DataPolicy {
  uint64 id = 1;
  string subject_type = 2;
  string subject_id = 3;
  string resource = 4;
  string condition = 5;
  string effect = 6; // "allow" / "deny"；空串按 "allow"（协议层不约束，DB/dataperm 兜底）
}
```

重新生成代码：
```bash
make proto-gen   # 若工具缺失先 make proto-tools
```

`internal/controlplane/translate/translate.go` 的 `dataPolicyToProto` 加 `Effect`：
```go
func dataPolicyToProto(p cp.DataPolicy) *syncv1.DataPolicy {
	return &syncv1.DataPolicy{
		Id:          uint64(p.ID),
		SubjectType: p.SubjectType,
		SubjectId:   p.SubjectID,
		Resource:    p.Resource,
		Condition:   p.Condition,
		Effect:      p.Effect,
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/translate/ -count=1`
预期：PASS（新用例 + 既有 translate 用例全绿）。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/sync/v1/policy_sync.proto gen/ internal/controlplane/translate/translate.go internal/controlplane/translate/translate_effect_test.go
git commit -m "feat(sync/proto+translate): sync DataPolicy 加 effect 字段并在快照/增量出口透传

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：admin proto +effect + mgmt UpsertDataPolicy 归一/校验（创建入口）

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto` + 重新生成 `gen/`
- 修改：`internal/controlplane/mgmt/server.go`（UpsertDataPolicy）
- 新增测试：`internal/controlplane/mgmt/data_policy_effect_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/data_policy_effect_test.go`（复用 `server_test.go` 的 `dialMgmt`/`mk`，同 `package mgmt_test`）：
```go
package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminService_UpsertDataPolicy_Effect(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx := context.Background()

	// effect="deny" 落库为 deny
	_, err := cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "role", SubjectId: "manager", Resource: "order", Condition: "{}", Effect: "deny",
	})
	require.NoError(t, err)

	// effect="" 归一为 allow
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "user", SubjectId: "alice", Resource: "invoice", Condition: "{}", Effect: "",
	})
	require.NoError(t, err)

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	effs := map[string]string{} // resource→effect
	for _, p := range got {
		effs[p.Resource] = p.Effect
	}
	require.Equal(t, "deny", effs["order"])
	require.Equal(t, "allow", effs["invoice"])

	// effect="bogus" 返 InvalidArgument 且不落库
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "role", SubjectId: "m", Resource: "audit", Condition: "{}", Effect: "bogus",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	after, _ := store.ReadAppDataPolicies(ctx, db, appID)
	require.Len(t, after, 2) // 仍只有 order + invoice
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminService_UpsertDataPolicy_Effect -count=1`
预期：FAIL（`UpsertDataPolicyRequest` 无 Effect 字段，编译错误）。

- [ ] **步骤 3：编写实现**

`api/proto/sydom/admin/v1/admin.proto` 的 `UpsertDataPolicyRequest` 加字段：
```proto
message UpsertDataPolicyRequest {
  uint64 app_id = 1;
  int64 id = 2; // 0=新增
  string subject_type = 3;
  string subject_id = 4;
  string resource = 5;
  string condition = 6; // 条件树 JSON
  string effect = 7;    // "allow" / "deny"；空串按 "allow"
}
```

重新生成代码：
```bash
make proto-gen
```

`internal/controlplane/mgmt/server.go` 的 `UpsertDataPolicy` 改为（归一空串、校验非法值返 InvalidArgument，再透传 Effect）：
```go
func (s *AdminServer) UpsertDataPolicy(ctx context.Context, r *adminv1.UpsertDataPolicyRequest) (*adminv1.UpsertDataPolicyResponse, error) {
	eff := r.Effect
	if eff == "" {
		eff = "allow"
	}
	if eff != "allow" && eff != "deny" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid effect %q (want allow|deny)", r.Effect)
	}
	d, err := s.mgr.UpsertDataPolicy(ctx, int64(r.AppId), cp.DataPolicy{
		ID: r.Id, SubjectType: r.SubjectType, SubjectID: r.SubjectId, Resource: r.Resource, Condition: r.Condition, Effect: eff,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert data policy: %v", err)
	}
	resp := &adminv1.UpsertDataPolicyResponse{}
	if d != nil && len(d.DataChanges) > 0 {
		resp.DataPolicyId = d.DataChanges[0].Policy.ID
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminService_UpsertDataPolicy_Effect -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ internal/controlplane/mgmt/server.go internal/controlplane/mgmt/data_policy_effect_test.go
git commit -m "feat(mgmt+admin/proto): UpsertDataPolicy 接收 effect（空串归 allow，非法返 InvalidArgument）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 收尾：全量验证

- [ ] 运行 proto 漂移检查 + 全量构建 + 全量测试 + 静态检查：

```bash
make proto-gen && git diff --exit-code gen/        # 无漂移（生成产物已提交）
go build ./...
go vet ./internal/controlplane/...
gofmt -l internal/controlplane/ db/                 # 应无输出
go test ./... -count=1                              # 全量回归（需 Docker 起 PG）
go test ./internal/controlplane/mgmt/ ./internal/controlplane/store/ -race -count=1
```
预期：proto 无漂移、构建通过、全测试绿、race 干净、gofmt 干净。

- [ ] 完成后用 `superpowers:finishing-a-development-branch` 收尾分支。

---

## 自检结果

- **规格覆盖**：spec §3 改动清单 9 处 → 任务 1（DB migration + schema spec）、任务 2（cp.DataPolicy + store 读写）、任务 3（sync proto + translate）、任务 4（admin proto + mgmt）全覆盖；§4.1 列定义→任务1；§4.2 写→任务2；§4.3 读/翻译→任务2/3；§4.4 管理 API→任务4；§6 fail-close 矩阵→任务1（CHECK）/任务4（InvalidArgument）/任务2（空串归一）；§7 测试分布任务1-4 + 收尾 proto-check。
- **占位符扫描**：无 TODO/待定；每步含完整可编译代码与精确命令。
- **类型一致性**：`cp.DataPolicy.Effect`（任务2 定义）被任务3 translate、任务4 mgmt 一致引用；`store.UpsertDataPolicy`/`ReadAppDataPolicies` 签名不变（仅内部 SQL 改）；`syncv1.DataPolicy.Effect`/`adminv1.UpsertDataPolicyRequest.Effect` 由各自 proto 加字段 + regen 产生，测试在对应任务的同一步验证；空串→allow 归一在 store（任务2）与 mgmt（任务4）两处一致，DB CHECK（任务1）兜底。
- **依赖顺序**：任务1（DB 列）→任务2（store 往返需列）→任务3（translate 需 cp.Effect）→任务4（mgmt 需 cp.Effect）。任务3/4 各自 regen 整个 gen/ 树，互不冲突。

相关：规格 `2026-06-04-sydom-datapolicy-effect-e2e-design.md`；④-2 `2026-06-04-sydom-sidecar-data-policy-engine-design.md`（effect 消费端）；[[feedback-consistency-over-simplicity]]。
