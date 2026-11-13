package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpointLoopbackAndTokenAuth(t *testing.T) {
	app := New(Dependencies{
		Check: func(context.Context) error { return nil },
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health/live", nil)
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("live status = %d", w.Code)
	}

	// Non-loopback without token is forbidden.
	mReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mReq.RemoteAddr = "10.0.0.5:12345"
	mW := httptest.NewRecorder()
	app.Handler.ServeHTTP(mW, mReq)
	if mW.Code != http.StatusForbidden {
		t.Fatalf("remote metrics without token = %d, want 403", mW.Code)
	}

	// Loopback without token succeeds.
	mReq2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mReq2.RemoteAddr = "127.0.0.1:12345"
	mW2 := httptest.NewRecorder()
	app.Handler.ServeHTTP(mW2, mReq2)
	if mW2.Code != http.StatusOK {
		t.Fatalf("loopback metrics status = %d, want 200", mW2.Code)
	}
	body := mW2.Body.String()
	for _, metric := range []string{"tuskrag_up 1", "tuskrag_ready 1", "tuskrag_http_requests_total{status_class=\"2xx\"} 1", "tuskrag_go_goroutines"} {
		if !strings.Contains(body, metric) {
			t.Fatalf("metrics body missing %q:\n%s", metric, body)
		}
	}

	// Token mode requires matching bearer token.
	t.Setenv("TUSKRAG_METRICS_TOKEN", "secret-metrics-token")
	mReq3 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mReq3.RemoteAddr = "127.0.0.1:12345"
	mW3 := httptest.NewRecorder()
	app.Handler.ServeHTTP(mW3, mReq3)
	if mW3.Code != http.StatusForbidden {
		t.Fatalf("token mode without header = %d, want 403", mW3.Code)
	}

	mReq4 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mReq4.RemoteAddr = "10.0.0.5:12345"
	mReq4.Header.Set("Authorization", "Bearer secret-metrics-token")
	mW4 := httptest.NewRecorder()
	app.Handler.ServeHTTP(mW4, mReq4)
	if mW4.Code != http.StatusOK {
		t.Fatalf("token mode with valid bearer = %d, want 200", mW4.Code)
	}
}
