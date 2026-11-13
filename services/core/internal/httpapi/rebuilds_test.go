package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

type rebuildTestService struct {
	t      *testing.T
	calls  int
	err    error
	status string
}

func (s *rebuildTestService) scope(a kb.Actor, kbID, job int64) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || kbID != 7 || job != 9 {
		s.t.Fatal("incorrect rebuild scope")
	}
}
func (s *rebuildTestService) Create(_ context.Context, a kb.Actor, id int64, key string, input kb.RebuildRequest) (kb.Rebuild, error) {
	s.scope(a, id, 9)
	if key != "create-1" || input.TargetSpaceID != 3 || input.Revision != 2 {
		s.t.Fatal("incorrect rebuild request")
	}
	return kb.Rebuild{JobID: 9, TargetSpaceID: 3, Status: "queued", MaxAttempts: 3}, s.err
}
func (s *rebuildTestService) Get(_ context.Context, a kb.Actor, id, job int64) (kb.Rebuild, error) {
	s.scope(a, id, job)
	return kb.Rebuild{JobID: job, TargetSpaceID: 3, Status: s.status, MaxAttempts: 3}, s.err
}
func (s *rebuildTestService) Cancel(_ context.Context, a kb.Actor, id, job int64) error {
	s.scope(a, id, job)
	return s.err
}

func TestRebuildCreateHTTP(t *testing.T) {
	valid := `{"target_space_id":3,"revision":2}`
	for _, tc := range []struct {
		name, body string
		status     int
		domain     error
	}{
		{"ok", valid, 200, nil}, {"session", valid, 401, nil}, {"csrf", valid, 403, nil}, {"key", valid, 422, nil},
		{"revision", `{"target_space_id":3}`, 422, nil}, {"zero", `{"target_space_id":0,"revision":2}`, 422, nil},
		{"overflow", `{"target_space_id":3,"revision":2147483647}`, 422, nil}, {"forged", `{"target_space_id":3,"revision":2,"tenant_id":9}`, 422, nil},
		{"pending", valid, 409, kb.ErrRebuildInProgress}, {"stale", valid, 409, kb.ErrRevisionConflict}, {"idempotency", valid, 409, kb.ErrIdempotencyConflict},
		{"unsupported", valid, 422, kb.ErrRebuildTargetUnsupported}, {"egress", valid, 422, models.ErrEgressDenied}, {"archived", valid, 409, kb.ErrArchived},
		{"forbidden", valid, 403, kb.ErrForbidden}, {"missing", valid, 404, kb.ErrNotFound}, {"internal", valid, 500, errors.New("private database detail")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &rebuildTestService{t: t, err: tc.domain}
			app := New(Dependencies{Identity: uploadTestIdentity{}, Rebuilds: service, InsecureLocalCookies: true})
			req := authenticatedUploadRequest(strings.NewReader(tc.body))
			req.URL.Path = "/api/v1/kbs/7/rebuilds"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "create-1")
			switch tc.name {
			case "session":
				req.Header.Del("Cookie")
			case "csrf":
				req.Header.Del("X-CSRF-Token")
			case "key":
				req.Header.Del("Idempotency-Key")
			}
			response := httptest.NewRecorder()
			app.Handler.ServeHTTP(response, req)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "private database detail") {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			if tc.status != 200 && tc.domain == nil && service.calls != 0 {
				t.Fatal("invalid request reached domain")
			}
		})
	}
}

func TestRebuildReadAndCancelHTTP(t *testing.T) {
	for _, operation := range []string{"get", "cancel"} {
		for _, tc := range []struct {
			name   string
			code   int
			domain error
		}{{"ok", 200, nil}, {"session", 401, nil}, {"missing", 404, kb.ErrNotFound}, {"forbidden", 403, kb.ErrForbidden}, {"dependency", 503, nil}, {"path", 422, nil}} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				service := &rebuildTestService{t: t, err: tc.domain, status: "queued"}
				deps := Dependencies{Identity: uploadTestIdentity{}, Rebuilds: service, InsecureLocalCookies: true}
				if tc.name == "dependency" {
					deps.Rebuilds = nil
				}
				app := New(deps)
				req := authenticatedUploadRequest(nil)
				req.URL.Path = "/api/v1/kbs/7/rebuilds/9"
				if operation == "get" {
					req.Method = "GET"
					req.Header.Del("X-CSRF-Token")
				} else {
					req.URL.Path += "/cancel"
				}
				if tc.name == "session" {
					req.Header.Del("Cookie")
				}
				if tc.name == "path" {
					req.URL.Path = strings.Replace(req.URL.Path, "/9", "/0", 1)
				}
				response := httptest.NewRecorder()
				app.Handler.ServeHTTP(response, req)
				if response.Code != tc.code {
					t.Fatalf("%d %s", response.Code, response.Body.String())
				}
				if tc.name == "ok" && operation == "get" && (!strings.Contains(response.Body.String(), `"failure_code":null`) || !strings.Contains(response.Body.String(), `"finished_at":null`)) {
					t.Fatal("nullable fields missing")
				}
			})
		}
	}
	service := &rebuildTestService{t: t}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Rebuilds: service, InsecureLocalCookies: true})
	req := authenticatedUploadRequest(nil)
	req.URL.Path = "/api/v1/kbs/7/rebuilds/9/cancel"
	req.Header.Del("X-CSRF-Token")
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, req)
	if response.Code != 403 || service.calls != 0 {
		t.Fatal("cancel bypassed CSRF")
	}
}
