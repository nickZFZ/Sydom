package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

const consolePageSize = 50

// listPageFromReq 从 query 解析 ListPage（Console 固定 limit=consolePageSize，offset 由 ?page= 页码算）。
func listPageFromReq(r *http.Request) *adminv1.ListPage {
	page := 1
	if v, err := formUint64(r, "page"); err == nil && v >= 1 {
		page = int(v)
	}
	q := r.URL.Query()
	return &adminv1.ListPage{
		Limit: consolePageSize, Offset: uint32((page - 1) * consolePageSize),
		Sort: q.Get("sort"), Order: q.Get("order"), Q: q.Get("q"),
	}
}

// pagerData 构造分页条模板数据（当前页、是否有上下页、total、显示区间、保留的 query 串）。
func pagerData(r *http.Request, total uint32) map[string]any {
	page := 1
	if v, err := formUint64(r, "page"); err == nil && v >= 1 {
		page = int(v)
	}
	from := (page-1)*consolePageSize + 1
	to := page * consolePageSize
	if uint32(to) > total {
		to = int(total)
	}
	if total == 0 {
		from = 0
	}
	q := r.URL.Query()
	q.Del("page")
	return map[string]any{
		"Page": page, "Total": total, "From": from, "To": to,
		"HasPrev": page > 1, "HasNext": uint32(page*consolePageSize) < total,
		"PrevPage": page - 1, "NextPage": page + 1,
		"Query": q.Encode(), // 保留 sort/order/q/过滤
		"Sort":  r.URL.Query().Get("sort"), "Order": r.URL.Query().Get("order"), "Q": r.URL.Query().Get("q"),
	}
}
