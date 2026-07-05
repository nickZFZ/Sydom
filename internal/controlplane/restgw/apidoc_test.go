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
	// 每条都有 method/pattern/fullMethod。
	for _, d := range docs {
		require.NotEmpty(t, d.Method)
		require.NotEmpty(t, d.Pattern)
		require.NotEmpty(t, d.FullMethod)
	}
	// 稳定排序（FullMethod 升序，同 method 再按 pattern）。
	require.True(t, sort.SliceIsSorted(docs, func(i, j int) bool {
		if docs[i].FullMethod != docs[j].FullMethod {
			return docs[i].FullMethod < docs[j].FullMethod
		}
		return docs[i].Pattern < docs[j].Pattern
	}))
}
