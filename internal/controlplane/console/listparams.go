package console

import (
	"html/template"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

const consolePageSize = 50

// currentPage 取 ?page= 页码（缺省/非法/<1 → 1），是分页页码的唯一真相源。
func currentPage(r *http.Request) int {
	if v, err := formUint64(r, "page"); err == nil && v >= 1 {
		return int(v)
	}
	return 1
}

// listPageFromReq 从 query 解析 ListPage（Console 固定 limit=consolePageSize，offset 由 ?page= 页码算）。
func listPageFromReq(r *http.Request) *adminv1.ListPage {
	page := currentPage(r)
	q := r.URL.Query()
	return &adminv1.ListPage{
		Limit: consolePageSize, Offset: uint32((page - 1) * consolePageSize),
		Sort: q.Get("sort"), Order: q.Get("order"), Q: q.Get("q"),
	}
}

// pagerData 构造分页条模板数据（当前页、是否有上下页、total、显示区间、保留的 query 串）。
func pagerData(r *http.Request, total uint32) map[string]any {
	page := currentPage(r)
	from := (page-1)*consolePageSize + 1
	to := page * consolePageSize
	if uint32(to) > total {
		to = int(total)
	}
	// total==0 或页码越界（offset 超出数据）→ 显示区间归零，避免 "4951-5 / 共 5" 这种荒谬区间。
	if total == 0 || from > int(total) {
		from, to = 0, 0
	}
	q := r.URL.Query()
	q.Del("page")
	return map[string]any{
		"Page": page, "Total": total, "From": from, "To": to,
		"HasPrev": page > 1, "HasNext": uint32(page*consolePageSize) < total,
		"PrevPage": page - 1, "NextPage": page + 1,
		"Query": template.URL(q.Encode()), // 保留 sort/order/q/过滤；template.URL 避免 html/template 对 =& 二次转义
		"Sort":  q.Get("sort"), "Order": q.Get("order"), "Q": q.Get("q"),
	}
}
