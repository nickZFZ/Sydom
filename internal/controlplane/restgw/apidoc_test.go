package restgw

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutes_CoversAllRoutes(t *testing.T) {
	docs := Routes()
	all := allRoutes()
	require.Len(t, docs, len(all))
	// 逐条比对实际取值（有齿：抓 method/pattern 搬运错位，非仅判空）。
	idx := map[[2]string]route{}
	for _, rt := range all {
		idx[[2]string{rt.fullMethod, rt.pattern}] = rt
	}
	for _, d := range docs {
		require.NotEmpty(t, d.Method)
		require.NotEmpty(t, d.Pattern)
		require.NotEmpty(t, d.FullMethod)
		rt, ok := idx[[2]string{d.FullMethod, d.Pattern}]
		require.True(t, ok, "doc 未对应任何 route: %s %s", d.FullMethod, d.Pattern)
		require.Equal(t, rt.method, d.Method)
		require.Equal(t, rt.pattern, d.Pattern)
		require.Equal(t, rt.fullMethod, d.FullMethod)
	}
	// 稳定排序（FullMethod 升序，同 method 再按 pattern）。
	require.True(t, sort.SliceIsSorted(docs, func(i, j int) bool {
		if docs[i].FullMethod != docs[j].FullMethod {
			return docs[i].FullMethod < docs[j].FullMethod
		}
		return docs[i].Pattern < docs[j].Pattern
	}))
}
