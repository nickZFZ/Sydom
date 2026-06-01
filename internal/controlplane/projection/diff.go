// Package projection 把业务表投影为 casbin_rule，并计算变更 diff 与角色继承环检测。
package projection

import (
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ruleKey 把一条规则编码为可比较的字符串键（ptype + v0..v5，分隔符不可能出现在值中）。
func ruleKey(r cp.Rule) string {
	var b strings.Builder
	b.WriteString(r.Ptype)
	for i := range r.V {
		b.WriteByte('\x1f')
		b.WriteString(r.V[i])
	}
	return b.String()
}

// Diff 计算集合差：desired - current = adds，current - desired = removes。
func Diff(current, desired []cp.Rule) (adds, removes []cp.Rule) {
	cur := make(map[string]cp.Rule, len(current))
	for _, r := range current {
		cur[ruleKey(r)] = r
	}
	des := make(map[string]cp.Rule, len(desired))
	for _, r := range desired {
		des[ruleKey(r)] = r
	}
	for k, r := range des {
		if _, ok := cur[k]; !ok {
			adds = append(adds, r)
		}
	}
	for k, r := range cur {
		if _, ok := des[k]; !ok {
			removes = append(removes, r)
		}
	}
	return adds, removes
}
