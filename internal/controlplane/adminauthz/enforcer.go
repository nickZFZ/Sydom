package adminauthz

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
)

// modelText 是 admin 鉴权的 RBAC-with-domain 模型。
// 租户域设计：tdom 是 app 所属租户的域（"t:<tenant_id>"），作为 app 域之上的包含层。
//   - g(r.sub,p.sub,r.dom) —— app 直绑（既有路径，向后兼容）；
//   - g(r.sub,p.sub,r.tdom) —— 租户管理员经租户域命中其名下所有 app（新增包含层）；
//   - g(r.sub,p.sub,"*") —— super-admin 在 * 域的兜底；
//   - p.dom/p.res/p.act == "*" —— 通配 grant（super-admin 的 * 行、租户管理员的 t:<id> 行）。
const modelText = `
[request_definition]
r = sub, dom, tdom, res, act
[policy_definition]
p = sub, dom, res, act
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = (g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, r.tdom) || g(r.sub, p.sub, "*")) && (p.dom == r.dom || p.dom == r.tdom || p.dom == "*") && (p.res == r.res || p.res == "*") && (p.act == r.act || p.act == "*")
`

// dbAdapter 是只读 casbin Adapter：仅实现 LoadPolicy，从 admin 表装配 p/g 行。
// 写入路径（Save/Add/Remove/RemoveFiltered）刻意全部 no-op —— admin 策略只能经
// store.go 的结构化方法（store DAO + 版本 bump）改动，绝不经 casbin 落库。
// 为防止 casbin 默认 auto-save 把误调的写操作“静默成功”但不落库（破坏一致性），
// NewEnforcer 构造后立即 EnableAutoSave(false)，把只读契约 fail-loud 地固化下来。
type dbAdapter struct{ db *sql.DB }

func (a *dbAdapter) LoadPolicy(m model.Model) error {
	ctx := context.Background()
	pRows, err := LoadPolicyRows(ctx, a.db)
	if err != nil {
		return err
	}
	for _, r := range pRows {
		if err := persist.LoadPolicyArray(append([]string{"p"}, r...), m); err != nil {
			return err
		}
	}
	gRows, err := LoadGroupingRows(ctx, a.db)
	if err != nil {
		return err
	}
	for _, r := range gRows {
		if err := persist.LoadPolicyArray(append([]string{"g"}, r...), m); err != nil {
			return err
		}
	}
	return nil
}

func (a *dbAdapter) SavePolicy(model.Model) error                              { return nil }
func (a *dbAdapter) AddPolicy(string, string, []string) error                  { return nil }
func (a *dbAdapter) RemovePolicy(string, string, []string) error               { return nil }
func (a *dbAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }

// Enforcer 是控制面 admin 鉴权 enforcer：包一个 casbin enforcer，
// 用 admin_policy_version 做版本化重载，保证多副本一致。
type Enforcer struct {
	db      *sql.DB
	mu      sync.Mutex
	e       *casbin.Enforcer
	loadedV int64
}

// NewEnforcer 装配 admin enforcer：建模型 + 只读 Adapter，并记录当前已加载版本。
func NewEnforcer(db *sql.DB) (*Enforcer, error) {
	m, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: parse model: %w", err)
	}
	ce, err := casbin.NewEnforcer(m, &dbAdapter{db: db})
	if err != nil {
		return nil, fmt.Errorf("adminauthz: new enforcer: %w", err)
	}
	// 只读契约：关掉 casbin 默认 auto-save，避免误调写方法时“静默成功”却不落库。
	ce.EnableAutoSave(false)
	v, err := ReadPolicyVersion(context.Background(), db)
	if err != nil {
		return nil, err
	}
	return &Enforcer{db: db, e: ce, loadedV: v}, nil
}

// Enforce 鉴权：先比对 admin_policy_version，若变化则重载策略，再求值。
// 必须传 5 个值（sub, dom, tdom, res, act），与 5-token 请求定义匹配，
// 否则 casbin 报 "invalid request size"。fail-close 由调用方据 err 拒绝。
func (en *Enforcer) Enforce(ctx context.Context, sub, dom, tdom, res, act string) (bool, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	cur, err := ReadPolicyVersion(ctx, en.db)
	if err != nil {
		return false, err
	}
	if cur != en.loadedV {
		if err := en.e.LoadPolicy(); err != nil {
			return false, fmt.Errorf("adminauthz: reload policy: %w", err)
		}
		en.loadedV = cur
	}
	return en.e.Enforce(sub, dom, tdom, res, act)
}

// TenantDomain 把 tenant_id 转成租户域字符串。"t:" 前缀与纯数字 app 域天然不冲突。
func TenantDomain(tenantID int64) string { return "t:" + strconv.FormatInt(tenantID, 10) }

// TenantDomainOf 查 app 所属租户并返回其租户域。app 不存在/查询失败 → error
// （fail-close：调用方据此拒绝；绝不放行，也绝不借差异泄露 app 存在性）。
func (en *Enforcer) TenantDomainOf(ctx context.Context, appID int64) (string, error) {
	var tenantID int64
	if err := en.db.QueryRowContext(ctx,
		`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID); err != nil {
		return "", fmt.Errorf("adminauthz: tenant of app %d: %w", appID, err)
	}
	return TenantDomain(tenantID), nil
}
