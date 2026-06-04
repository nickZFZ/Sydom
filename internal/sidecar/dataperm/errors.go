package dataperm

import "errors"

var (
	// ErrInvalidPolicy 表示策略条件树非法（JSON/字段名/算子/元数/effect），命中时 fail-close 拒绝。
	ErrInvalidPolicy = errors.New("dataperm: invalid data policy condition")
	// ErrMissingVar 表示条件引用的 $user.xxx 在请求 userAttrs 中缺失，fail-close 拒绝。
	ErrMissingVar = errors.New("dataperm: missing user attribute for runtime variable")
)
