package sydomsql

import (
	"context"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// Filterer 是 Apply 对核心客户端的窄依赖；*sydom.Client 自动满足。
type Filterer interface {
	FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error)
}

// Apply 调 f.FilterSQL 取数据权限片段，再 Build 成目标方言 Clause。
// FilterSQL 的错误原样透传：默认应 fail-close（拒绝请求）；仅在风险可接受的点，
// 才针对 sydom.ErrUnavailable 哨兵显式选择 fail-open（与 sydomhttp.WithFailOpen 同口径）。
func Apply(ctx context.Context, f Filterer, subject, resource string, attrs map[string]any, d Dialect, startIndex int) (Clause, error) {
	fr, err := f.FilterSQL(ctx, subject, resource, attrs)
	if err != nil {
		return Clause{}, err
	}
	return Build(fr, d, startIndex)
}
