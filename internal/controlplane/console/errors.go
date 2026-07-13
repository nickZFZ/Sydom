package console

import (
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const codeInternal = codes.Internal

// httpStatusForCode：gRPC code → HTTP status（其余 500）。
// console 自有一份（渲染 HTML 而非 JSON，职责不同非重复）。
func httpStatusForCode(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.FailedPrecondition:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// renderGRPCError：gRPC status → 友好 HTML 错误页。
// Unauthenticated→302 登录；Internal/Unknown(→500) 脱敏（通用文案+细节仅日志）。
func (h *Handler) renderGRPCError(w http.ResponseWriter, r *http.Request, method string, err error) {
	st := status.Convert(err)
	hs := httpStatusForCode(st.Code())
	if hs == http.StatusUnauthorized {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	msg := st.Message()
	if hs == http.StatusInternalServerError {
		h.logger.Error("console internal error", "method", method, "code", st.Code().String(), "detail", st.Message())
		msg = "internal error"
	}
	h.renderError(w, r, st.Code(), msg, nil)
}

// renderError：渲染错误页（自定义 code/文案；errDetail 仅日志，不入页）。
func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, c codes.Code, msg string, errDetail error) {
	if errDetail != nil {
		h.logger.Error("console error", "code", c.String(), "detail", errDetail)
	}
	h.renderPage(w, r, "error.html", httpStatusForCode(c), map[string]any{"Message": msg, "Code": c.String()})
}
