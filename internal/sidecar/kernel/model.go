package kernel

import "github.com/casbin/casbin/v3/model"

// modelText 是锁定的 RBAC-with-domain model（架构 §6.2；与控制面 projection 落的行结构对齐）。
const modelText = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act, eft

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && r.obj == p.obj && r.act == p.act
`

// buildModel 装配内嵌 model。
func buildModel() (model.Model, error) {
	return model.NewModelFromString(modelText)
}
