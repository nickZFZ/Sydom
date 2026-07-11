package obs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/obs"
)

func scrapeRelay(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rr.Body.String()
}

func TestSetRelayLeader_Gauge(t *testing.T) {
	m := obs.New()

	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 0") {
		t.Fatalf("默认应为 sydom_relay_leader 0，实测:\n%s", body)
	}

	m.SetRelayLeader(true)
	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 1") {
		t.Fatalf("SetRelayLeader(true) 后应为 sydom_relay_leader 1，实测:\n%s", body)
	}

	m.SetRelayLeader(false)
	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 0") {
		t.Fatalf("SetRelayLeader(false) 后应回 sydom_relay_leader 0，实测:\n%s", body)
	}

	var nilM *obs.Metrics
	nilM.SetRelayLeader(true) // nil 接收者不得 panic
}
