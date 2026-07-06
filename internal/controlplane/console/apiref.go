package console

import (
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
)

// APIRefEntry 是渲染用的一条管理面 API 端点：授权要素（来自 ruleTable）+ REST 路由（若有）。
type APIRefEntry struct {
	FullMethod string
	RESTMethod string // 空 = 仅 gRPC
	RESTPath   string
	Resource   string
	Action     string
	Scope      string
	IsWrite    bool
}

// buildAPIReference 以 ruleTable（授权唯一真相）为锚，join REST route 注册表。
// 锚定 ruleTable 保证每条 admin RPC 都出现（DP-2），REST 列对 gRPC-only RPC 为空。
func buildAPIReference() []APIRefEntry {
	restByFM := map[string]restgw.RouteDoc{}
	for _, rt := range restgw.Routes() {
		if _, exists := restByFM[rt.FullMethod]; !exists { // 同一 RPC 多路由取首条（稳定排序后确定）
			restByFM[rt.FullMethod] = rt
		}
	}
	rules := mgmt.RuleEntries() // 已按 FullMethod 稳定排序
	out := make([]APIRefEntry, 0, len(rules))
	for _, r := range rules {
		e := APIRefEntry{FullMethod: r.FullMethod, Resource: r.Resource, Action: r.Action, Scope: r.Scope, IsWrite: r.IsWrite}
		if rt, ok := restByFM[r.FullMethod]; ok {
			e.RESTMethod, e.RESTPath = rt.Method, rt.Pattern
		}
		out = append(out, e)
	}
	return out
}
