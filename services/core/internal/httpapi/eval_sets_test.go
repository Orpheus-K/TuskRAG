package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type setTestService struct {
	t     *testing.T
	err   error
	calls int
}

func (s *setTestService) List(_ context.Context, a retrieval.Actor, kb, cursor int64, limit int32) (eval.SetPage, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || kb != 7 || cursor != 9 || limit != 2 {
		s.t.Fatal("incorrect list scope")
	}
	return eval.SetPage{Items: []eval.SetListItem{}}, s.err
}

func TestEvalSetListHTTP(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
		domain error
	}{
		{"cursor=9&limit=2", 200, nil},
		{"cursor=-1&limit=2", 422, nil},
		{"cursor=9&limit=11", 422, nil},
		{"cursor=9&limit=0", 422, nil},
		{"cursor=not-an-id&limit=2", 422, nil},
		{"cursor=9&limit=2", 404, eval.ErrSetNotFound},
		{"cursor=9&limit=2", 403, eval.ErrSetForbidden},
	} {
		service := &setTestService{t: t, err: tc.domain}
		app := New(Dependencies{Identity: uploadTestIdentity{}, EvalSets: service, InsecureLocalCookies: true})
		req := authenticatedUploadRequest(nil)
		req.Method = "GET"
		req.URL.Path = "/api/v1/kbs/7/eval-sets"
		req.URL.RawQuery = tc.query
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s status=%d body=%s", tc.query, response.Code, response.Body.String())
		}
		if tc.status == 422 && service.calls != 0 {
			t.Fatal("invalid pagination reached service")
		}
		if tc.status == 200 && !strings.Contains(response.Body.String(), `"items":[],"next_cursor":null`) {
			t.Fatal("empty page lost null cursor")
		}
	}
}

func (s *setTestService) Create(ctx context.Context, a retrieval.Actor, kb int64, key string, d eval.SetDefinition) (eval.StoredSet, error) {
	if key != "request-1" {
		s.t.Fatal("incorrect idempotency key")
	}
	return s.save(ctx, a, kb, 0, 0, d)
}
func (s *setTestService) Update(ctx context.Context, a retrieval.Actor, kb, id int64, revision int32, d eval.SetDefinition) (eval.StoredSet, error) {
	return s.save(ctx, a, kb, id, revision, d)
}
func (s *setTestService) save(_ context.Context, a retrieval.Actor, kb, id int64, revision int32, d eval.SetDefinition) (eval.StoredSet, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || kb != 7 || d.Name != "test" || (id != 0 && id != 9) || (id == 0 && revision != 0) || (id == 9 && revision != 1) {
		s.t.Fatal("incorrect save scope")
	}
	return eval.StoredSet{ID: 9, Revision: revision + 1, Definition: d}, s.err
}
func (s *setTestService) Get(_ context.Context, a retrieval.Actor, kb, id int64, revision int32) (eval.StoredSet, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || kb != 7 || id != 9 || revision != 1 {
		s.t.Fatal("incorrect get scope")
	}
	return eval.StoredSet{ID: 9, Revision: 1}, s.err
}

func TestEvalSetHTTP(t *testing.T) {
	valid := `{"name":"test","cases":[{"id":"q1","question":"未知？","answerable":false,"expected":[],"tags":[]}]}`
	for _, tc := range []struct {
		name, method, etag, body string
		status                   int
		domain                   error
	}{
		{"create", "POST", "", valid, 200, nil},
		{"read revision", "GET", "", "", 200, nil},
		{"update", "PUT", `"1"`, valid, 200, nil},
		{"missing precondition", "PUT", "", valid, 422, nil},
		{"weak precondition", "PUT", `W/"1"`, valid, 400, nil},
		{"wildcard", "PUT", "*", valid, 400, nil},
		{"noncanonical", "PUT", `"01"`, valid, 400, nil},
		{"unlabeled", "POST", "", strings.Replace(valid, `"answerable":false,`, "", 1), 422, nil},
		{"null label", "POST", "", strings.Replace(valid, `"answerable":false`, `"answerable":null`, 1), 422, nil},
		{"unknown field", "POST", "", strings.Replace(valid, `"question":`, `"extra":1,"question":`, 1), 422, nil},
		{"csrf", "POST", "", valid, 403, nil},
		{"session", "POST", "", valid, 401, nil},
		{"conflict", "PUT", `"1"`, valid, 409, eval.ErrSetConflict},
		{"idempotency conflict", "POST", "", valid, 409, eval.ErrSetIdempotencyConflict},
		{"hidden", "GET", "", "", 404, eval.ErrSetNotFound},
		{"forbidden", "GET", "", "", 403, eval.ErrSetForbidden},
		{"archived", "POST", "", valid, 409, eval.ErrSetArchived},
		{"invalid", "POST", "", valid, 400, eval.ErrInvalidCase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &setTestService{t: t, err: tc.domain}
			app := New(Dependencies{Identity: uploadTestIdentity{}, EvalSets: service, InsecureLocalCookies: true})
			req := authenticatedUploadRequest(strings.NewReader(tc.body))
			req.Method = tc.method
			req.URL.Path = "/api/v1/kbs/7/eval-sets"
			if tc.method != "POST" {
				req.URL.Path += "/9"
			}
			if tc.method == "GET" {
				req.URL.RawQuery = "revision=1"
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "request-1")
			if tc.etag != "" {
				req.Header.Set("If-Match", tc.etag)
			}
			if tc.name == "csrf" {
				req.Header.Del("X-CSRF-Token")
			}
			if tc.name == "session" {
				req.Header.Del("Cookie")
			}
			response := httptest.NewRecorder()
			app.Handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != 200 && tc.domain == nil && service.calls != 0 {
				t.Fatal("invalid request reached service")
			}
			if tc.status == 200 {
				want := `"1"`
				if tc.method == "PUT" {
					want = `"2"`
				}
				if response.Header().Get("ETag") != want {
					t.Fatalf("ETag=%s", response.Header().Get("ETag"))
				}
			}
		})
	}
}
