package console

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

// httpStatusForCode 须把 ResourceExhausted 映射为 429（配额超限对外语义），
// 而非落 default 500 把配额文案吞成「internal error」。挡 M6.1a 遗漏的 latent bug 回归。
func TestHTTPStatusForCode_ResourceExhausted(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                http.StatusOK,
		codes.PermissionDenied:  http.StatusForbidden,
		codes.NotFound:          http.StatusNotFound,
		codes.ResourceExhausted: http.StatusTooManyRequests, // 429（M6.1d）
		codes.Internal:          http.StatusInternalServerError,
	}
	for c, want := range cases {
		if got := httpStatusForCode(c); got != want {
			t.Errorf("httpStatusForCode(%v)=%d，want %d", c, got, want)
		}
	}
}
