package obs

import (
	"net/http"

	"github.com/nickZFZ/Sydom/internal/health"
)

// OpsHandler 组合内部 ops 端口的路由：/metrics（Prometheus）+ /healthz + /readyz（复用 health）。
// 免鉴权（内部 ops 端口，沿用明文健康探针约定）；指标不含 secret/敏感值。
func OpsHandler(m *Metrics, ready health.Checker) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	h := health.Handler(ready)
	mux.Handle("/healthz", h)
	mux.Handle("/readyz", h)
	return mux
}
