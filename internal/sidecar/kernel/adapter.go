package kernel

import (
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
)

// memoryAdapter 是只读内存 Adapter：持本地快照，仅启动/重建时被加载。
// 运行期策略经 Engine 高层 API 改内存、不回写 adapter（autoSave=false）。
// 实现 Adapter+BatchAdapter+FilteredAdapter（FilteredAdapter 为架构 §3.2 强制契约 + ④-5 冷启动预留）。
type memoryAdapter struct {
	rules    []Rule
	filtered bool
}

func newMemoryAdapter(rules []Rule) *memoryAdapter {
	cp := make([]Rule, len(rules))
	copy(cp, rules)
	return &memoryAdapter{rules: cp}
}

func (a *memoryAdapter) loadInto(m model.Model, domainFilter string) error {
	for _, r := range a.rules {
		if domainFilter != "" && r.domainValue() != domainFilter {
			continue
		}
		line := append([]string{r.Ptype}, r.values()...)
		if err := persist.LoadPolicyArray(line, m); err != nil {
			return err
		}
	}
	return nil
}

// LoadPolicy 全量加载持有的行。
func (a *memoryAdapter) LoadPolicy(m model.Model) error {
	a.filtered = false
	return a.loadInto(m, "")
}

// LoadFilteredPolicy 按 domain 过滤加载；filter 期望为本域 domain 字符串，空/非字符串视为不过滤。
func (a *memoryAdapter) LoadFilteredPolicy(m model.Model, filter interface{}) error {
	dom, _ := filter.(string)
	a.filtered = dom != ""
	return a.loadInto(m, dom)
}

func (a *memoryAdapter) IsFiltered() bool { return a.filtered }

// 写路径一律 no-op（只读契约；运行期改内存走 Engine 高层 API）。
func (a *memoryAdapter) SavePolicy(model.Model) error                              { return nil }
func (a *memoryAdapter) AddPolicy(string, string, []string) error                  { return nil }
func (a *memoryAdapter) RemovePolicy(string, string, []string) error               { return nil }
func (a *memoryAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }
func (a *memoryAdapter) AddPolicies(string, string, [][]string) error              { return nil }
func (a *memoryAdapter) RemovePolicies(string, string, [][]string) error           { return nil }

var (
	_ persist.Adapter         = (*memoryAdapter)(nil)
	_ persist.BatchAdapter    = (*memoryAdapter)(nil)
	_ persist.FilteredAdapter = (*memoryAdapter)(nil)
)
