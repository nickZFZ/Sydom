package kernel

import "errors"

var (
	// ErrNotReady 表示尚未成功应用过快照，鉴权 fail-close 拒绝。
	ErrNotReady = errors.New("kernel: enforcer not ready (no snapshot applied)")
	// ErrForeignDomain 表示规则/请求的 domain 不属于本 app 的固定域。
	ErrForeignDomain = errors.New("kernel: rule/request domain does not match pinned app domain")
	// ErrStaleVersion 表示 delta 版本未严格大于当前已应用版本（重放/乱序）。
	ErrStaleVersion = errors.New("kernel: delta version not greater than current applied version")
)
