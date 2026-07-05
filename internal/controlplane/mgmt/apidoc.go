package mgmt

import "sort"

// RPCDoc 是一条 admin RPC 的只读文档视图（从权威 ruleTable 派生，不暴露内部 rpcRule）。
// 文档面与授权面同源——改一条 RPC 授权要素，文档自动跟随，不漂移。
type RPCDoc struct {
	FullMethod string
	Resource   string
	Action     string
	Scope      string // "system" | "app" | "tenant" | "self"
	IsWrite    bool
}

func scopeName(s ruleScope) string {
	switch s {
	case scopeSystem:
		return "system"
	case scopeApp:
		return "app"
	case scopeTenant:
		return "tenant"
	case scopeSelf:
		return "self"
	default:
		return "unknown"
	}
}

// RuleEntries 返回全部 admin RPC 的授权文档视图，按 FullMethod 升序稳定排序。
// 纯只读派生自 ruleTable（授权唯一真相），不修改任何授权状态。
func RuleEntries() []RPCDoc {
	out := make([]RPCDoc, 0, len(ruleTable))
	for fm, r := range ruleTable {
		out = append(out, RPCDoc{FullMethod: fm, Resource: r.resource, Action: r.action, Scope: scopeName(r.scope), IsWrite: r.isWrite})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullMethod < out[j].FullMethod })
	return out
}
