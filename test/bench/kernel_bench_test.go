package bench

import (
	"fmt"
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

const benchDom = "dom"

// seedEngine 建 rules 条 p(role_i,dom,obj_i,read,allow) + 一条 g(user,role_0,dom)，就绪。
func seedEngine(tb testing.TB, rules int) *kernel.Engine {
	tb.Helper()
	e, err := kernel.New(benchDom, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	rs := make([]kernel.Rule, 0, rules+1)
	for i := 0; i < rules; i++ {
		rs = append(rs, kernel.Rule{Ptype: "p", V: [6]string{
			fmt.Sprintf("role_%d", i), benchDom, fmt.Sprintf("obj_%d", i), "read", "allow", "",
		}})
	}
	rs = append(rs, kernel.Rule{Ptype: "g", V: [6]string{"user", "role_0", benchDom, "", "", ""}})
	if err := e.ApplySnapshot(kernel.Snapshot{Version: 1, Rules: rs}); err != nil {
		tb.Fatal(err)
	}
	return e
}

// 缓存命中热路径：同元组重复 Enforce（生产主路径）。
func BenchmarkEnforce_CacheHit(b *testing.B) {
	e := seedEngine(b, 100)
	if allow, err := e.Enforce("user", benchDom, "obj_0", "read"); err != nil || !allow {
		b.Fatalf("预热应 allow：allow=%v err=%v", allow, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Enforce("user", benchDom, "obj_0", "read")
	}
}

// 冷 matcher 随策略规模伸缩：唯一 sub 每次未命中，对每条 p 评 g() → 全遍历。
func BenchmarkEnforce_MissScaling(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			e := seedEngine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = e.Enforce(fmt.Sprintf("u%d", i), benchDom, "obj_0", "read")
			}
		})
	}
}

// 批量吞吐：一次 BatchEnforce 50 条混合元组。
func BenchmarkBatchEnforce(b *testing.B) {
	e := seedEngine(b, 100)
	reqs := make([][]string, 0, 50)
	for i := 0; i < 50; i++ {
		reqs = append(reqs, []string{"user", benchDom, fmt.Sprintf("obj_%d", i%100), "read"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.BatchEnforce(reqs)
	}
}
