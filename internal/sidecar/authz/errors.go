// Package authz 是 Sidecar 数据面对外的鉴权出口：组合内核功能鉴权 + 数据权限下推 + 陈旧守卫。
// 一切"无法判定"（未就绪/太陈旧）以独立错误上抛，绝不伪装成 allowed=false——让调用方自定 fail-open/close。
package authz

import "errors"

// ErrTooStale 表示快照陈旧度超过 Config.MaxStaleness，fail-close 拒绝判定。
var ErrTooStale = errors.New("authz: snapshot too stale (exceeds max staleness)")
