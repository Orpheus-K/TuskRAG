package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHealthAndUnknownAPIRoute(t *testing.T) {
	app := New(Dependencies{Check: func(context.Context) error { return errors.New("unavailable") }})
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/health/live", http.StatusOK},
		{"/api/health/ready", http.StatusServiceUnavailable},
		{"/api", http.StatusNotFound},
		{"/api/v1/missing", http.StatusNotFound},
		{"/mcp", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			app.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestHealthResponseHasNoUnavailableSchemaLink(t *testing.T) {
	app := New(Dependencies{Check: func(context.Context) error { return nil }})
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health/ready", nil))
	var body struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body.Status != "ok" {
		t.Fatalf("unexpected readiness response: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIErrorsIncludeTraceWithoutInternalDetails(t *testing.T) {
	app := New(Dependencies{Check: func(context.Context) error { return errors.New("database-secret") }})
	for _, tc := range []struct {
		path, code string
		status     int
	}{
		{"/api/health/ready", "dependency_unavailable", http.StatusServiceUnavailable},
		{"/api/v1/missing", "not_found", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var body struct {
			Error ErrorBody `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || body.Error.Code != tc.code || len(body.Error.TraceID) != 32 ||
			body.Error.TraceID != w.Header().Get("X-Trace-ID") || strings.Contains(w.Body.String(), "database-secret") {
			t.Fatalf("unexpected error response: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestProductionSessionCookie(t *testing.T) {
	cookie := sessionCookie(false, "token", false)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Name != "__Host-tuskrag_session" || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatal("production cookie does not enforce host and transport restrictions")
	}
}

func TestSPAFallbackDoesNotHideAPIErrors(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("console")},
		"assets/app.js": {Data: []byte("script")},
	}
	handler := serveSPA(http.NotFoundHandler(), assets)
	for _, tc := range []struct {
		path string
		want int
		body string
	}{
		{"/kb/42/documents", http.StatusOK, "console"},
		{"/assets/app.js", http.StatusOK, "script"},
		{"/assets/missing.js", http.StatusNotFound, ""},
		{"/api/v1/missing", http.StatusNotFound, ""},
		{"/mcp", http.StatusNotFound, ""},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want || (tc.body != "" && !strings.Contains(w.Body.String(), tc.body)) {
			t.Errorf("%s: status = %d, body = %q", tc.path, w.Code, w.Body.String())
		}
	}
}
