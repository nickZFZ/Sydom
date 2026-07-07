package obs

import (
	"log/slog"
	"net/http"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// statusRecorder 捕获写出的 status（默认 200）。
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// HTTPMiddleware 记 HTTP RED（handler=路由模板，非具体 path）+ 每请求一条访问日志。
// next 须是设置 r.Pattern 的路由器（Go 1.22 ServeMux）；未匹配路由 handler 标签退化为 "other"。
func (m *Metrics) HTTPMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-Id", rid)
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		r = r.WithContext(With(r.Context(), logger.With("request_id", rid)))
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		handlerLabel := r.Pattern // Go 1.22 ServeMux 匹配后填充
		if handlerLabel == "" {
			handlerLabel = "other"
		}
		m.ObserveHTTP(handlerLabel, r.Method, rec.code, dur)
		logger.Info("http_request",
			"request_id", rid, "handler", handlerLabel, "method", r.Method,
			"status", rec.code, "duration_ms", dur.Milliseconds(),
			"principal", cp.OperatorFromContext(r.Context()))
	})
}
