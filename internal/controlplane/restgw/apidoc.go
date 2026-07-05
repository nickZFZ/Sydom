package restgw

import "sort"

// RouteDoc 是一条 REST 路由的只读文档视图（从 allRoutes 派生，不暴露内部 route）。
type RouteDoc struct {
	Method     string // HTTP 动词
	Pattern    string // 路径模式
	FullMethod string // gRPC FullMethod（= ruleTable 键）
}

// Routes 返回全部 REST 网关路由的只读文档视图，稳定排序。纯只读派生自 allRoutes()。
func Routes() []RouteDoc {
	all := allRoutes()
	out := make([]RouteDoc, 0, len(all))
	for _, rt := range all {
		out = append(out, RouteDoc{Method: rt.method, Pattern: rt.pattern, FullMethod: rt.fullMethod})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FullMethod != out[j].FullMethod {
			return out[i].FullMethod < out[j].FullMethod
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}
