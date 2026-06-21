// Package tenanttemplate 把一个 app 的整套授权模型捕获为可复用 bundle（租户自有模板内容）。
// 捕获=纯读：权限点(auto+manual) + 业务角色 + 角色授权 + 角色数据范围；排除 user 绑定/凭据。
package tenanttemplate

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// maxBundleRoleKey 限制 bundle role key 长度，使 apply 期确定性 code `tpl:tt-<id>:<key>` 不超 role.code 列宽(64)。
const maxBundleRoleKey = 40

// Bundle 是从一个 app 捕获的完整授权模型，可序列化存入 tenant_template.bundle。
type Bundle struct {
	Permissions []BundlePermission `json:"permissions"`
	Roles       []BundleRole       `json:"roles"`
}

// BundlePermission 是捕获的一条权限点。
type BundlePermission struct {
	Code        string `json:"code"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BundleRole 是捕获的一个业务角色，含已授权限码与数据范围。
type BundleRole struct {
	Key             string            `json:"key"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	PermissionCodes []string          `json:"permission_codes"`
	DataScopes      []BundleDataScope `json:"data_scopes"`
}

// BundleDataScope 是角色持有的一条数据范围策略（符号化条件）。
type BundleDataScope struct {
	Resource  string          `json:"resource"`
	Effect    string          `json:"effect"`
	Condition json.RawMessage `json:"condition"`
}

// SafeKey 把角色 code 派生为合法 bundle key：去 ':'（ApplyTemplate 拒含 ':'），限长，去重。
// seen 由调用方传入并在调用间共享，保证同一 bundle 内 key 唯一。
func SafeKey(code string, seen map[string]bool) string {
	k := strings.ReplaceAll(code, ":", "_")
	if k == "" {
		k = "role"
	}
	if len(k) > maxBundleRoleKey {
		k = k[:maxBundleRoleKey]
	}
	base := k
	for n := 2; seen[k]; n++ {
		k = base + "_" + strconv.Itoa(n)
	}
	seen[k] = true
	return k
}

// Capture 读取 appID 全部授权模型，组装为 Bundle（不改 app，不下发，不读 secret）。
func Capture(ctx context.Context, db cp.DBTX, appID int64) (Bundle, error) {
	var b Bundle

	// 1. 权限点（auto+manual），排序保证幂等输出。
	prows, err := db.QueryContext(ctx,
		`SELECT code, resource, action, type, name, COALESCE(description,'') FROM permission WHERE app_id=$1 ORDER BY code`, appID)
	if err != nil {
		return Bundle{}, err
	}
	for prows.Next() {
		var p BundlePermission
		if err := prows.Scan(&p.Code, &p.Resource, &p.Action, &p.Type, &p.Name, &p.Description); err != nil {
			prows.Close()
			return Bundle{}, err
		}
		b.Permissions = append(b.Permissions, p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return Bundle{}, err
	}

	// 2. 数据范围：读全部 data_policy，仅保留 subject_type='role' 的条目（TT-4 排除 user 主体）。
	dps, err := store.ReadAppDataPolicies(ctx, db, appID)
	if err != nil {
		return Bundle{}, err
	}
	scopesByRole := map[string][]BundleDataScope{}
	for _, dp := range dps {
		if dp.SubjectType != "role" {
			continue // 排除 user 主体数据策略（TT-4）
		}
		scopesByRole[dp.SubjectID] = append(scopesByRole[dp.SubjectID], BundleDataScope{
			Resource:  dp.Resource,
			Effect:    dp.Effect,
			Condition: json.RawMessage([]byte(dp.Condition)), // Condition 是 string，转 RawMessage
		})
	}

	// 3. 角色列表，按 id 排序保证幂等。
	rrows, err := db.QueryContext(ctx,
		`SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1 ORDER BY id`, appID)
	if err != nil {
		return Bundle{}, err
	}
	type roleRow struct {
		id          int64
		code, name  string
		description string
	}
	var roles []roleRow
	for rrows.Next() {
		var r roleRow
		if err := rrows.Scan(&r.id, &r.code, &r.name, &r.description); err != nil {
			rrows.Close()
			return Bundle{}, err
		}
		roles = append(roles, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return Bundle{}, err
	}

	// 4. 每角色拉取已授权限码（通过 role_permission join permission）。
	seen := map[string]bool{}
	for _, r := range roles {
		grows, err := db.QueryContext(ctx,
			`SELECT p.code FROM role_permission rp
			 JOIN permission p ON p.id = rp.permission_id
			 WHERE rp.app_id=$1 AND rp.role_id=$2
			 ORDER BY p.code`,
			appID, r.id)
		if err != nil {
			return Bundle{}, err
		}
		var codes []string
		for grows.Next() {
			var c string
			if err := grows.Scan(&c); err != nil {
				grows.Close()
				return Bundle{}, err
			}
			codes = append(codes, c)
		}
		grows.Close()
		if err := grows.Err(); err != nil {
			return Bundle{}, err
		}

		b.Roles = append(b.Roles, BundleRole{
			Key:             SafeKey(r.code, seen),
			Name:            r.name,
			Description:     r.description,
			PermissionCodes: codes,
			DataScopes:      scopesByRole[r.code],
		})
	}
	return b, nil
}
