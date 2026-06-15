// Package health 提供两二进制共享的明文健康探针 handler。
// /healthz 恒活（进程在即 200，不连依赖，避免抖动误重启）；
// /readyz 跑就绪 checker，fail-close：checker 返错即 503。
// 响应体仅 "ok"/"not ready"——零业务、零 secret、零内部错误细节。
package health

import (
	"context"
	"net/http"
)

// Checker 返回 nil 表示就绪；返回非 nil 表示未就绪（fail-close）。
type Checker func(ctx context.Context) error

// Handler 构造健康 mux。ready 为 nil 时 /readyz 恒就绪。
// 返回的 Handler 并发安全，可直接挂载到 http.Server。
func Handler(ready Checker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready == nil || ready(r.Context()) == nil {
			writePlain(w, http.StatusOK, "ok")
			return
		}
		writePlain(w, http.StatusServiceUnavailable, "not ready")
	})
	return mux
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
