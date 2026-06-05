// Package sydom 是司域 Go SDK 的核心客户端：封装同机 Sidecar 的本地 AuthService
// （Check/BatchCheck/FilterSQL），不含任何鉴权逻辑。零 HTTP 依赖。
package sydom

import "errors"

// ErrUnavailable 表示「此刻拿不到可信鉴权决策」——Sidecar 自报未就绪/过陈旧，
// 或与 Sidecar 的传输不可达，二者统一为同一哨兵。调用方据风险自定 fail-open/close。
var ErrUnavailable = errors.New("sydom: authorization decision unavailable")
