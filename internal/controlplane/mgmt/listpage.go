package mgmt

import (
	"strconv"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

// resolveOrder 把外部 sort 列名经 allowed 白名单映射为受控 SQL 列名，order→ASC|DESC。
// 非法 sort/order 一律回退 defaultCol/ASC。返回值永远是受控标识符，绝不含用户原始输入（防注入）。
func resolveOrder(sort, order string, allowed map[string]string, defaultCol string) string {
	col, ok := allowed[sort]
	if !ok {
		col = defaultCol
	}
	dir := "ASC"
	if strings.EqualFold(order, "desc") {
		dir = "DESC"
	}
	return col + " " + dir
}

// pageOf 取 ListPage（nil 安全）→ clamped limit（复用 clampLimit：0→50/上限200）+ 非负 offset。
func pageOf(p *adminv1.ListPage) (int, int) {
	if p == nil {
		return clampLimit(0), 0
	}
	off := int(p.Offset) // Offset 为 uint32，int(...) 恒非负
	if off < 0 {         // 防御性下限保护（当前类型下不会触发）
		off = 0
	}
	return clampLimit(p.Limit), off
}

// searchClause 为白名单列生成 "(c1 ILIKE $n OR ...)" + 参数 "%q%"（所有列共享一个占位 $argPos）。
// q 空或无列 → ("", nil)。列名来自服务端白名单（非用户输入），无注入风险。
func searchClause(q string, cols []string, argPos int) (string, any) {
	if q == "" || len(cols) == 0 {
		return "", nil
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + " ILIKE $" + strconv.Itoa(argPos)
	}
	return "(" + strings.Join(parts, " OR ") + ")", "%" + q + "%"
}
