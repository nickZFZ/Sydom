package dataperm

import (
	"fmt"
	"strings"
)

// RoleResolver 把用户展开为隐式角色集（含继承）。*kernel.Engine 满足之。
type RoleResolver interface {
	GetImplicitRolesForUser(user, dom string) ([]string, error)
}

// Filter 是无状态查询/渲染编排器。
type Filter struct {
	roles RoleResolver
	table *Table
}

func NewFilter(roles RoleResolver, table *Table) *Filter {
	return &Filter{roles: roles, table: table}
}

// planMode 区分三种渲染前结果。
type planMode int

const (
	modeNoFilter planMode = iota // resource 未配置 → 无行过滤
	modeDenyAll                  // 配了但无 allow 命中 → 1=0
	modeTree                     // 有合并树（变量已解析）
)

type plan struct {
	mode planMode
	tree *Condition // 仅 modeTree
}

// buildPlan 跑规格 §5 流水线（除最终方言渲染外）：
// tier-1 守卫 → 主体展开 → 遍历命中（中毒 fail-close + 变量解析）拆 allow/deny → 空 allow 守卫 → 合并。
func (f *Filter) buildPlan(user, dom, resource string, attrs map[string]any) (plan, error) {
	bucket, configured := f.table.Lookup(resource)
	if !configured {
		return plan{mode: modeNoFilter}, nil
	}
	roles, err := f.roles.GetImplicitRolesForUser(user, dom)
	if err != nil {
		return plan{}, err // fail-close 透传（含 ErrNotReady/ErrForeignDomain）
	}
	roleSet := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		roleSet[r] = struct{}{}
	}

	var allow, deny []*Condition
	for _, s := range bucket {
		if !subjectMatches(s, user, roleSet) {
			continue
		}
		if s.parseErr != nil {
			return plan{}, s.parseErr // 命中中毒策略 → fail-close（绝不静默丢，丢 deny 会扩权）
		}
		resolved, err := resolveVars(s.cond, attrs)
		if err != nil {
			return plan{}, err // ErrMissingVar
		}
		if s.effect == effectDeny {
			deny = append(deny, resolved)
		} else {
			allow = append(allow, resolved)
		}
	}
	if len(allow) == 0 {
		return plan{mode: modeDenyAll}, nil
	}
	merged := orAll(allow)
	if len(deny) > 0 {
		merged = &Condition{Op: OpAnd, Children: []*Condition{
			merged,
			{Op: OpNot, Children: []*Condition{orAll(deny)}},
		}}
	}
	return plan{mode: modeTree, tree: merged}, nil
}

// subjectMatches 判定一条策略的主体是否落在请求用户的主体集内。
func subjectMatches(s stored, user string, roleSet map[string]struct{}) bool {
	switch s.subjectType {
	case SubjectUser:
		return s.subjectID == user
	case SubjectRole:
		_, ok := roleSet[s.subjectID]
		return ok
	default:
		return false // 未知主体类型 inert（既不 allow 也不 deny）
	}
}

// orAll 把多个条件折叠为 OR（单个直接返回，避免冗余 OR 包裹）。
func orAll(cs []*Condition) *Condition {
	if len(cs) == 1 {
		return cs[0]
	}
	return &Condition{Op: OpOr, Children: cs}
}

// resolveVars 深拷贝条件树并把叶子里的 $user.xxx 解析为 attrs 的具体值（缺键→ErrMissingVar）。
func resolveVars(c *Condition, attrs map[string]any) (*Condition, error) {
	if isLogicalOp(c.Op) {
		children := make([]*Condition, len(c.Children))
		for i, ch := range c.Children {
			rc, err := resolveVars(ch, attrs)
			if err != nil {
				return nil, err
			}
			children[i] = rc
		}
		return &Condition{Op: c.Op, Children: children}, nil
	}
	val, err := resolveValue(c.Value, attrs)
	if err != nil {
		return nil, err
	}
	return &Condition{Op: c.Op, Field: c.Field, Value: val}, nil
}

// resolveValue 把 "$user.xxx" 解析为 attrs 值（数组逐元素解析；缺键→ErrMissingVar）。
func resolveValue(v any, attrs map[string]any) (any, error) {
	switch tv := v.(type) {
	case string:
		if name, ok := userVarName(tv); ok {
			val, present := attrs[name]
			if !present {
				return nil, fmt.Errorf("%w: $user.%s", ErrMissingVar, name)
			}
			return val, nil
		}
		return tv, nil
	case []any:
		out := make([]any, len(tv))
		for i, e := range tv {
			rv, err := resolveValue(e, attrs)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil // number/bool/nil
	}
}

// userVarName 识别 "$user.xxx" 并返回 xxx。
func userVarName(s string) (string, bool) {
	const prefix = "$user."
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// Match 取值：整体过滤语义。
const (
	MatchAll         = "all"         // resource 未配置，无行过滤
	MatchNone        = "none"        // 配了但无 allow 命中，全拒
	MatchConditional = "conditional" // 命中合并条件树
)

// RawResult 是 raw 方言结果：Match 表达整体语义，Tree 为变量已解析的合并树（仅 MatchConditional）。
type RawResult struct {
	Match string     // MatchAll | MatchNone | MatchConditional
	Tree  *Condition // 仅 Match==MatchConditional
}

// FilterRaw 返回合并后的条件树（变量已解析），交 SDK 自渲染参数化语句。
func (f *Filter) FilterRaw(user, dom, resource string, attrs map[string]any) (RawResult, error) {
	p, err := f.buildPlan(user, dom, resource, attrs)
	if err != nil {
		return RawResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return RawResult{Match: MatchAll}, nil
	case modeDenyAll:
		return RawResult{Match: MatchNone}, nil
	default:
		return RawResult{Match: MatchConditional, Tree: p.tree}, nil
	}
}
