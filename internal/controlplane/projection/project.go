package projection

import (
	"context"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ProjectApp 按投影规则，从业务表算出该 app 的期望 casbin_rule 全集。
func ProjectApp(ctx context.Context, q cp.DBTX, appID int64) ([]cp.Rule, error) {
	var rules []cp.Rule

	// p 行：role_permission ⋈ role ⋈ permission ⋈ application
	pRows, err := q.QueryContext(ctx, `
		SELECT r.code, app.domain, p.resource, p.action, rp.eft
		FROM role_permission rp
		JOIN role r        ON rp.role_id = r.id
		JOIN permission p  ON rp.permission_id = p.id
		JOIN application app ON rp.app_id = app.id
		WHERE rp.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project p rows: %w", err)
	}
	defer pRows.Close()
	for pRows.Next() {
		var sub, dom, obj, act, eft string
		if err := pRows.Scan(&sub, &dom, &obj, &act, &eft); err != nil {
			return nil, fmt.Errorf("scan p row: %w", err)
		}
		rules = append(rules, cp.Rule{Ptype: "p", V: [6]string{sub, dom, obj, act, eft, ""}})
	}
	if err := pRows.Err(); err != nil {
		return nil, err
	}

	// g 行（用户→角色）：user_role_binding ⋈ role ⋈ application
	gURows, err := q.QueryContext(ctx, `
		SELECT urb.user_id, r.code, app.domain
		FROM user_role_binding urb
		JOIN role r          ON urb.role_id = r.id
		JOIN application app ON urb.app_id = app.id
		WHERE urb.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project g(user) rows: %w", err)
	}
	defer gURows.Close()
	for gURows.Next() {
		var user, role, dom string
		if err := gURows.Scan(&user, &role, &dom); err != nil {
			return nil, fmt.Errorf("scan g(user) row: %w", err)
		}
		rules = append(rules, cp.Rule{Ptype: "g", V: [6]string{user, role, dom, "", "", ""}})
	}
	if err := gURows.Err(); err != nil {
		return nil, err
	}

	// g 行（子→父）：role_inheritance ⋈ role(child) ⋈ role(parent) ⋈ application
	gIRows, err := q.QueryContext(ctx, `
		SELECT cr.code, pr.code, app.domain
		FROM role_inheritance ri
		JOIN role cr         ON ri.child_role_id = cr.id
		JOIN role pr         ON ri.parent_role_id = pr.id
		JOIN application app ON ri.app_id = app.id
		WHERE ri.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project g(inherit) rows: %w", err)
	}
	defer gIRows.Close()
	for gIRows.Next() {
		var child, parent, dom string
		if err := gIRows.Scan(&child, &parent, &dom); err != nil {
			return nil, fmt.Errorf("scan g(inherit) row: %w", err)
		}
		rules = append(rules, cp.Rule{Ptype: "g", V: [6]string{child, parent, dom, "", "", ""}})
	}
	if err := gIRows.Err(); err != nil {
		return nil, err
	}

	return rules, nil
}

// ErrCycle 表示加入该继承边会在角色继承图中形成环。
var ErrCycle = errors.New("projection: role inheritance cycle")

// CheckNoCycle 校验把边 (childID 继承 parentID) 加入该 app 的角色继承图后不成环。
// 继承语义：role_inheritance(child_role_id, parent_role_id) = child 继承 parent。
// 加边 child→parent 成环，当且仅当 child==parent，或 parent 已（传递）继承 child。
func CheckNoCycle(ctx context.Context, q cp.DBTX, appID, childID, parentID int64) error {
	if childID == parentID {
		return fmt.Errorf("%w: self loop on role %d", ErrCycle, childID)
	}
	// 构建邻接：child → []parent（"继承"方向）
	rows, err := q.QueryContext(ctx,
		`SELECT child_role_id, parent_role_id FROM role_inheritance WHERE app_id = $1`, appID)
	if err != nil {
		return err
	}
	defer rows.Close()
	adj := map[int64][]int64{}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return fmt.Errorf("scan inheritance edge: %w", err)
		}
		adj[c] = append(adj[c], p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// 从 parentID 出发沿"继承"边 DFS，若能到达 childID，则加 child→parent 成环。
	visited := map[int64]bool{}
	var dfs func(n int64) bool
	dfs = func(n int64) bool {
		if n == childID {
			return true
		}
		if visited[n] {
			return false
		}
		visited[n] = true
		for _, nxt := range adj[n] {
			if dfs(nxt) {
				return true
			}
		}
		return false
	}
	if dfs(parentID) {
		return fmt.Errorf("%w: adding %d->%d closes a loop", ErrCycle, childID, parentID)
	}
	return nil
}
