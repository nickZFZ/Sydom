// Package policy 编排控制面的版本号写事务，对外暴露策略写方法，返回领域 Delta。
package policy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// DeltaSink 在写事务内（提交前）持久化产出的 Delta；返回 error 触发整笔写回滚。
type DeltaSink interface {
	Persist(ctx context.Context, tx cp.DBTX, appID int64, delta *cp.Delta) error
}

// PolicyManager 是控制面真相源写入引擎。
type PolicyManager struct {
	db   *sql.DB
	sink DeltaSink // 可为 nil（退化为不落 outbox 的纯写）
}

// NewPolicyManager 构造 PolicyManager。sink 可为 nil。
func NewPolicyManager(db *sql.DB, sink DeltaSink) *PolicyManager {
	return &PolicyManager{db: db, sink: sink}
}

// writeOp 描述一次版本化写：审计元信息 + 业务表变更闭包 + 可选的 data 变更产出。
type writeOp struct {
	action     string
	entityType string
	entityID   string
	// mutate 在事务内执行业务表 CUD；返回本次的 data_policy 变更（功能权限类返回 nil）。
	mutate func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error)
	// preCheck 在加锁后、mutate 前执行（如环检测）；可为 nil。
	preCheck func(ctx context.Context, tx *sql.Tx) error
}

// auditDiff 把一次写的策略变更序列化为审计 diff JSON（绝不含 secret——casbin 规则/数据策略本无凭据）。
func auditDiff(adds, removes []cp.Rule, changes []cp.DataPolicyChange) []byte {
	payload := map[string]any{}
	if len(adds) > 0 {
		payload["adds"] = adds
	}
	if len(removes) > 0 {
		payload["removes"] = removes
	}
	if len(changes) > 0 {
		payload["data_changes"] = changes
	}
	if len(payload) == 0 {
		return nil
	}
	b, _ := json.Marshal(payload)
	return b
}

// runVersionedWrite 是 spec §6 统一写事务模板。
func (m *PolicyManager) runVersionedWrite(ctx context.Context, appID int64, op writeOp) (*cp.Delta, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("policy: begin tx: %w", err)
	}
	defer tx.Rollback() // COMMIT 成功后再次 Rollback 是 no-op；失败路径确保回滚

	// 1. 行锁串行化本 app
	cur, err := store.LockAppVersion(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: lock app %d version: %w", appID, err)
	}
	// 2. 前置校验（环检测等）
	if op.preCheck != nil {
		if err := op.preCheck(ctx, tx); err != nil {
			return nil, fmt.Errorf("policy: precheck %s: %w", op.action, err)
		}
	}
	// 3. 业务表 CUD
	dataChanges, err := op.mutate(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("policy: mutate %s: %w", op.action, err)
	}
	// 4. 重投影 + diff
	desired, err := projection.ProjectApp(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: reproject app %d: %w", appID, err)
	}
	current, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: read current rules app %d: %w", appID, err)
	}
	adds, removes := projection.Diff(current, desired)

	// 5. 无策略影响 → COMMIT 业务态，不 bump、不 audit、返回 nil
	if len(adds) == 0 && len(removes) == 0 && len(dataChanges) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("policy: commit no-op %s: %w", op.action, err)
		}
		return nil, nil
	}

	// 6. bump 版本、写 casbin_rule、写 audit
	vNew := cur + 1
	if err := store.ApplyDiff(ctx, tx, appID, adds, removes, vNew); err != nil {
		return nil, fmt.Errorf("policy: apply diff app %d v%d: %w", appID, vNew, err)
	}
	if err := store.BumpAppVersion(ctx, tx, appID, vNew); err != nil {
		return nil, fmt.Errorf("policy: bump app %d to v%d: %w", appID, vNew, err)
	}
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID,
		auditDiff(adds, removes, dataChanges), vNew); err != nil {
		return nil, fmt.Errorf("policy: audit %s v%d: %w", op.action, vNew, err)
	}
	delta := &cp.Delta{
		AppID: appID, Version: vNew,
		RuleAdds: adds, RuleRemoves: removes, DataChanges: dataChanges,
	}
	if m.sink != nil {
		if err := m.sink.Persist(ctx, tx, appID, delta); err != nil {
			return nil, fmt.Errorf("policy: sink %s v%d: %w", op.action, vNew, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("policy: commit %s v%d: %w", op.action, vNew, err)
	}
	return delta, nil
}

// ReportPermissions 批量上报权限点（app 凭据来源，全标 source=auto）。
// 单批走 runVersionedWrite：纯目录上报无投影 diff → 不 bump、不广播；若上报改了
// "已被授权权限点"的 resource/action 致投影变化 → 照常 bump+广播（一致性要求）。
// 命中 manual 行的条目被跳过、计入 Skipped，绝不覆盖人工配置。
func (m *PolicyManager) ReportPermissions(ctx context.Context, appID int64, points []cp.PermissionPoint) (cp.ReportResult, error) {
	var res cp.ReportResult
	ctx = cp.WithOperator(ctx, "auto-report") // bump 路径的 audit actor
	_, err := m.runVersionedWrite(ctx, appID, writeOp{
		action:     "report_permissions",
		entityType: "permission",
		entityID:   "",
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			for _, p := range points {
				applied, e := store.UpsertAutoPermission(ctx, tx, appID,
					p.Code, p.Resource, p.Action, p.Type, p.Name, p.Description)
				if e != nil {
					return nil, e
				}
				if applied {
					res.Upserted++
				} else {
					res.Skipped++
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return cp.ReportResult{}, err
	}
	return res, nil
}

// GrantPermission 给角色授予权限点（幂等）。
// 契约：幂等命中或无策略影响时返回 (nil, nil)——业务态已持久化但无需下发；
// 出错时返回 (nil, err)。调用方据 (err==nil && delta==nil) 判定"无变更、不下发"。
func (m *PolicyManager) GrantPermission(ctx context.Context, appID, roleID, permID int64, eft string) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "grant", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRolePermission(ctx, tx, appID, roleID, permID, eft)
		},
	})
}

// RevokePermission 撤销角色的权限点。
// 契约同 GrantPermission：无策略影响（如该授权本不存在）返回 (nil, nil)。
func (m *PolicyManager) RevokePermission(ctx context.Context, appID, roleID, permID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "revoke", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRolePermission(ctx, tx, appID, roleID, permID)
		},
	})
}

// CreateRole 建角色。建角色本身不产生 casbin_rule（无绑定/授权），故通常返回 nil Delta。
func (m *PolicyManager) CreateRole(ctx context.Context, appID int64, code, name string) (roleID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "create_role", entityType: "role", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.InsertRole(ctx, tx, appID, code, name)
			roleID = id
			return nil, e
		},
	})
	return roleID, d, err
}

// generateRoleCode 生成系统内部唯一角色 code（业务管理员永不见/不填）。
// 纯随机避免中文 name slug 复杂度；唯一性由 uq_role_app_code 兜底。
func generateRoleCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "br-" + hex.EncodeToString(b), nil
}

// CreateBusinessRole 业务语言建角色：单事务内建角色 + 批量授权（原子，杜绝半授权空角色）。
func (m *PolicyManager) CreateBusinessRole(ctx context.Context, appID int64, name string, permIDs []int64) (roleID int64, d *cp.Delta, err error) {
	code, err := generateRoleCode()
	if err != nil {
		return 0, nil, fmt.Errorf("policy: gen role code: %w", err)
	}
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "create_business_role", entityType: "role", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.InsertRole(ctx, tx, appID, code, name)
			if e != nil {
				return nil, e
			}
			roleID = id
			for _, pid := range permIDs {
				if e := store.InsertRolePermission(ctx, tx, appID, id, pid, cp.EffectAllow); e != nil {
					return nil, e
				}
			}
			return nil, nil
		},
	})
	return roleID, d, err
}

// TemplateRole 是 ApplyTemplate 的角色输入（与 presets 解耦，由 mgmt 转换填入）。
type TemplateRole struct {
	Key             string
	Name            string
	PermissionCodes []string
}

// ApplyTemplateResult 是一次模板应用的写入统计。
type ApplyTemplateResult struct {
	PermsUpserted int // 权限点新增/刷新（source=auto）
	PermsSkipped  int // 权限点命中 manual 被保留
	RolesCreated  int // 角色新建
	RolesSkipped  int // 角色已存在被跳过（确定性 code）
}

// ApplyTemplate 原子幂等应用一个模板到 app（单 runVersionedWrite）：
//  1. 逐 permission 复用 UpsertAutoPermission（auto 不覆盖 manual，TP-3）；
//  2. 解析 code→id；
//  3. 逐 role 用确定性 code `tpl:<templateID>:<key>` upsert，仅新建时按 permission_codes 授权。
//
// 任一步失败整事务回滚（TP-5）。投影变化照常 bump+广播。
func (m *PolicyManager) ApplyTemplate(ctx context.Context, appID int64, templateID string,
	perms []cp.PermissionPoint, roles []TemplateRole) (ApplyTemplateResult, *cp.Delta, error) {
	var res ApplyTemplateResult
	// 确定性角色 code 以 ':' 分隔（tpl:<templateID>:<key>）；templateID 或 key 含 ':' 会破坏
	// 该不变量并可能与另一组合冲突命中同一 uq_role_app_code 行——fail-close 直接拒绝。
	if strings.ContainsRune(templateID, ':') {
		return ApplyTemplateResult{}, nil, fmt.Errorf("policy: apply_template: template id %q must not contain ':'", templateID)
	}
	for _, r := range roles {
		if strings.ContainsRune(r.Key, ':') {
			return ApplyTemplateResult{}, nil, fmt.Errorf("policy: apply_template: role key %q must not contain ':'", r.Key)
		}
	}
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "apply_template", entityType: "template", entityID: templateID,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			// 1. 权限点。
			var codes []string
			for _, p := range perms {
				applied, e := store.UpsertAutoPermission(ctx, tx, appID,
					p.Code, p.Resource, p.Action, p.Type, p.Name, p.Description)
				if e != nil {
					return nil, e
				}
				if applied {
					res.PermsUpserted++
				} else {
					res.PermsSkipped++
				}
				codes = append(codes, p.Code)
			}
			// 2. code→id（含 manual 命中行，权限点不论 auto/manual 都参与授权）。
			idByCode, e := store.PermissionIDsByCode(ctx, tx, appID, codes)
			if e != nil {
				return nil, e
			}
			// 3. 角色（确定性 code 幂等）。
			for _, r := range roles {
				code := "tpl:" + templateID + ":" + r.Key
				roleID, created, e := store.UpsertTemplateRole(ctx, tx, appID, code, r.Name)
				if e != nil {
					return nil, e
				}
				if !created {
					res.RolesSkipped++
					continue // 已存在 → 不改授权（不动人工后续编辑）
				}
				res.RolesCreated++
				for _, pc := range r.PermissionCodes {
					pid, ok := idByCode[pc]
					if !ok {
						// fail-close：role 引用了本模板 perms 未声明的 code（loader 应已拦截）。
						// 真走到这里说明输入不一致，整笔回滚而非静默少授（一致性优先）。
						return nil, fmt.Errorf("policy: apply_template %s: role %q references undeclared permission code %q", templateID, r.Key, pc)
					}
					if e := store.InsertRolePermission(ctx, tx, appID, roleID, pid, cp.EffectAllow); e != nil {
						return nil, e
					}
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return ApplyTemplateResult{}, nil, err
	}
	return res, d, nil
}

// DeleteRole 删角色（级联删其全部引用），重投影会清掉相关 casbin_rule 行。
func (m *PolicyManager) DeleteRole(ctx context.Context, appID, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "delete_role", entityType: "role", entityID: fmt.Sprintf("%d", roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRole(ctx, tx, appID, roleID)
		},
	})
}

// UpsertPermission 幂等注册权限点。仅注册不授权时不产生 casbin_rule。
func (m *PolicyManager) UpsertPermission(ctx context.Context, appID int64, code, resource, action, ptype, name string) (permID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "upsert_permission", entityType: "permission", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.UpsertPermission(ctx, tx, appID, code, resource, action, ptype, name)
			permID = id
			return nil, e
		},
	})
	return permID, d, err
}

// AddRoleInheritance 加角色继承边（child 继承 parent），加锁后先做环检测。
func (m *PolicyManager) AddRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "add_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		preCheck: func(ctx context.Context, tx *sql.Tx) error {
			return projection.CheckNoCycle(ctx, tx, appID, childID, parentID)
		},
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// RemoveRoleInheritance 删角色继承边。
func (m *PolicyManager) RemoveRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "remove_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// BindUserRole 绑定用户到角色（幂等）。
func (m *PolicyManager) BindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "bind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}

// UnbindUserRole 解绑用户角色。
func (m *PolicyManager) UnbindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "unbind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}

// UpsertDataPolicy 新增/更新一条数据策略（不参与投影，只 bump 版本 + 更新 data_policy.version）。
func (m *PolicyManager) UpsertDataPolicy(ctx context.Context, appID int64, p cp.DataPolicy) (*cp.Delta, error) {
	// 写路径锚点归一：空串 effect 统一为 "allow"，确保 Delta 回显与落库真相值严格一致。
	// store 层既有的归一保留不动（对其它直接调用方的防御深度）。
	if p.Effect == "" {
		p.Effect = cp.EffectAllow
	}
	return m.runVersionedWriteData(ctx, appID, writeOpData{
		action: "upsert_data_policy", entityType: "data_policy", entityID: p.SubjectID,
		apply: func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error) {
			id, created, err := store.UpsertDataPolicy(ctx, tx, appID, p, vNew)
			if err != nil {
				return nil, err
			}
			p.ID = id
			op := cp.ChangeUpdate
			if created {
				op = cp.ChangeAdd
			}
			return []cp.DataPolicyChange{{Op: op, Policy: p}}, nil
		},
	})
}

// DeleteDataPolicy 删一条数据策略。
func (m *PolicyManager) DeleteDataPolicy(ctx context.Context, appID, dataPolicyID int64) (*cp.Delta, error) {
	return m.runVersionedWriteData(ctx, appID, writeOpData{
		action: "delete_data_policy", entityType: "data_policy", entityID: fmt.Sprintf("%d", dataPolicyID),
		apply: func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error) {
			if err := store.DeleteDataPolicy(ctx, tx, appID, dataPolicyID); err != nil {
				return nil, err
			}
			return []cp.DataPolicyChange{{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: dataPolicyID}}}, nil
		},
	})
}

// writeOpData 是 data_policy 写变体：apply 接收回填的 v_new（data_policy 需写入 version）。
type writeOpData struct {
	action, entityType, entityID string
	apply                        func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error)
}

// runVersionedWriteData 是 data_policy 类的写事务：始终 bump 版本（data 变更即策略变更），
// 不动 casbin_rule，data_policy 写入本次 v_new。
func (m *PolicyManager) runVersionedWriteData(ctx context.Context, appID int64, op writeOpData) (*cp.Delta, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("policy: begin tx: %w", err)
	}
	defer tx.Rollback()

	cur, err := store.LockAppVersion(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: lock app %d version: %w", appID, err)
	}
	vNew := cur + 1
	changes, err := op.apply(ctx, tx, vNew)
	if err != nil {
		return nil, fmt.Errorf("policy: apply %s: %w", op.action, err)
	}
	if err := store.BumpAppVersion(ctx, tx, appID, vNew); err != nil {
		return nil, fmt.Errorf("policy: bump app %d to v%d: %w", appID, vNew, err)
	}
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID,
		auditDiff(nil, nil, changes), vNew); err != nil {
		return nil, fmt.Errorf("policy: audit %s v%d: %w", op.action, vNew, err)
	}
	delta := &cp.Delta{AppID: appID, Version: vNew, DataChanges: changes}
	if m.sink != nil {
		if err := m.sink.Persist(ctx, tx, appID, delta); err != nil {
			return nil, fmt.Errorf("policy: sink %s v%d: %w", op.action, vNew, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("policy: commit %s v%d: %w", op.action, vNew, err)
	}
	return delta, nil
}
