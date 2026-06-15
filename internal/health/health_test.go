package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/health"
)

func do(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestHealthzAlwaysOK(t *testing.T) {
	// 即使 readiness checker 返错，liveness /healthz 也恒 200——两者语义隔离。
	h := health.Handler(func(context.Context) error { return errors.New("not ready") })
	code, body := do(t, h, "/healthz")
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("healthz want 200 ok, got %d %q", code, body)
	}
}

func TestReadyzOKWhenCheckerNil(t *testing.T) {
	code, body := do(t, health.Handler(nil), "/readyz")
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("readyz nil checker want 200 ok, got %d %q", code, body)
	}
}

func TestReadyzOKWhenCheckerPasses(t *testing.T) {
	code, body := do(t, health.Handler(func(context.Context) error { return nil }), "/readyz")
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("readyz pass want 200 ok, got %d %q", code, body)
	}
}

func TestReadyzServiceUnavailableWhenCheckerFails(t *testing.T) {
	secret := "super-secret-token"
	h := health.Handler(func(context.Context) error { return errors.New(secret) })
	code, body := do(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("readyz fail want 503, got %d", code)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("readyz body must not leak checker error detail, got %q", body)
	}
}
