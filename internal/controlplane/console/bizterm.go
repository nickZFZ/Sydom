package console

// bizterm —— 业务概念翻译层：把技术原语（action/resource）渲染为一致的中文业务语言。
// 纯函数 + 系统动词词表，无 I/O、无新表。运营台所有面共用（TP-8 无原语）。

// actionVerb 是系统内置动作动词词表（原语 action → 中文动词）。
var actionVerb = map[string]string{
	"read": "查看", "list": "查看", "get": "查看", "view": "查看",
	"create": "新建", "add": "新建",
	"update": "编辑", "write": "编辑", "edit": "编辑",
	"delete": "删除", "remove": "删除",
	"export": "导出", "import": "导入",
	"approve": "审批", "reject": "驳回", "assign": "分配",
}

// actionLabel 返回 action 的中文动词；未在词表中则原样返回（不臆造）。
func actionLabel(action string) string {
	if v, ok := actionVerb[action]; ok {
		return v
	}
	return action
}

// capabilityName 解析一条能力的业务名：① 显式 name 非空→用之；② 否则合成「resource · 动词」。
// 绝不返回裸 "resource:action"（TP-8）。
func capabilityName(name, resource, action string) string {
	if name != "" {
		return name
	}
	return resource + " · " + actionLabel(action)
}

// roleName 从 code→name map 取业务名，缺省返回 code 自身（绝不回退到技术 role_id）。
func roleName(m map[string]string, code string) string {
	if n, ok := m[code]; ok {
		return n
	}
	return code
}

// planName 是套餐技术名 → 中文业务名词表。
var planName = map[string]string{
	"free": "免费版",
	"pro":  "专业版",
}

// planLabel 返回套餐的中文业务名；未在词表中则原样返回（不臆造，
// 与 actionLabel/roleName 的回退范式一致）。
func planLabel(name string) string {
	if v, ok := planName[name]; ok {
		return v
	}
	return name
}
