package mgmt

import (
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/stretchr/testify/require"
)

func TestResolveOrder_WhitelistAndInjection(t *testing.T) {
	allowed := map[string]string{"id": "id", "name": "name"}
	require.Equal(t, "name DESC", resolveOrder("name", "desc", allowed, "id"))
	require.Equal(t, "name ASC", resolveOrder("name", "", allowed, "id"))
	// 非法 sort（注入尝试）→ 回退默认列，绝不含用户串
	require.Equal(t, "id ASC", resolveOrder("id;DROP TABLE role", "asc", allowed, "id"))
	require.Equal(t, "id ASC", resolveOrder("unknown_col", "weird", allowed, "id"))
}

func TestPageOf_ClampAndNil(t *testing.T) {
	l, o := pageOf(nil)
	require.Equal(t, 50, l) // 默认
	require.Equal(t, 0, o)
	l, _ = pageOf(&adminv1.ListPage{Limit: 0})
	require.Equal(t, 50, l)
	l, _ = pageOf(&adminv1.ListPage{Limit: 1000})
	require.Equal(t, 200, l) // 上限
	_, o = pageOf(&adminv1.ListPage{Offset: 30})
	require.Equal(t, 30, o)
}

func TestSearchClause(t *testing.T) {
	cond, arg := searchClause("alice", []string{"code", "name"}, 3)
	require.Equal(t, "(code ILIKE $3 OR name ILIKE $3)", cond)
	require.Equal(t, "%alice%", arg)
	cond, arg = searchClause("", []string{"code"}, 3)
	require.Equal(t, "", cond)
	require.Nil(t, arg)
}
