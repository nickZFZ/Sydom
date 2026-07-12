// Package migrations 嵌入 db/migrations 下的全部 SQL 迁移文件，供控制面 -migrate 模式用
// golang-migrate 的 iofs source 应用（单一真相源，构建期编入二进制、与代码同版本）。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
