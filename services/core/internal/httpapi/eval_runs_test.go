package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type runTestService struct {
	t     *testing.T
	err   error
	calls int
}

func (s *runTestService) Start(_ context.Context, a retrieval.Actor, kb int64, key string, input eval.StartRequest) (eval.StartedRun, error) {
	s.scope(a, kb, 9)
	if key != "create-1" || input.SetID != 3 || input.Revision != 2 || input.Mode != "rrf" {
		s.t.Fatal("incorrect start input")
	}
	return eval.StartedRun{ID: 9, JobID: 10}, s.err
}

func TestEvalRunStartHTTP(t *testing.T) {
	valid := `{"set_id":3,"revision":2,"mode":"rrf"}`
	for _, tc := range []struct {
		name, body string
		status     int
		domain     error
	}{
		{"start", valid, 200, nil}, {"missing key", valid, 422, nil}, {"csrf", valid, 403, nil}, {"session", valid, 401, nil},
		{"missing revision", `{"set_id":3,"mode":"rrf"}`, 422, nil}, {"latest revision", `{"set_id":3,"revision":0,"mode":"rrf"}`, 422, nil},
		{"forged config", `{"set_id":3,"revision":2,"mode":"rrf","config":{"model_kind":"stub"}}`, 422, nil},
		{"forged scope", `{"set_id":3,"revision":2,"mode":"rrf","allowed_document_ids":[99]}`, 422, nil},
		{"conflict", valid, 409, eval.ErrRunIdempotencyConflict}, {"archive", valid, 409, eval.ErrSetArchived},
		{"missing reranker", valid, 409, retrieval.ErrRerankUnavailable}, {"large scope", valid, 422, eval.ErrRunScopeTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &runTestService{t: t, err: tc.domain}
			app := New(Dependencies{Identity: uploadTestIdentity{}, EvalRuns: service, InsecureLocalCookies: true})
			req := authenticatedUploadRequest(strings.NewReader(tc.body))
			req.URL.Path = "/api/v1/kbs/7/eval-runs"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "create-1")
			if tc.name == "missing key" {
				req.Header.Del("Idempotency-Key")
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
				t.Fatal("unsafe input reached start service")
			}
		})
	}
}

func (s *runTestService) scope(a retrieval.Actor, kb, id int64) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || kb != 7 || id != 9 {
		s.t.Fatal("incorrect run scope")
	}
}
func (s *runTestService) Status(_ context.Context, a retrieval.Actor, kb, id int64) (eval.RunStatus, error) {
	s.scope(a, kb, id)
	return eval.RunStatus{ID: 9, JobID: 10, Set: eval.SetBinding{SetID: 3, Revision: 1}, Mode: "rrf", Status: "queued", MaxAttempts: 3}, s.err
}
func (s *runTestService) GetResult(_ context.Context, a retrieval.Actor, kb, id int64) (eval.StoredReport, error) {
	s.scope(a, kb, id)
	return eval.StoredReport{Source: "executed", Report: eval.RunReport{Rows: []eval.RunRow{}, LatencyMS: map[string][2]float64{}}}, s.err
}

func TestEvalRunReadHTTP(t *testing.T) {
	for _, suffix := range []string{"", "/result"} {
		for _, tc := range []struct {
			name   string
			domain error
			status int
			code   string
		}{
			{"success", nil, 200, ""}, {"missing", eval.ErrRunNotFound, 404, "not_found"}, {"scope", eval.ErrSetNotFound, 404, "not_found"},
			{"evidence", eval.ErrReportNotFound, 404, "not_found"}, {"role", eval.ErrSetForbidden, 403, "forbidden"}, {"report role", eval.ErrReportForbidden, 403, "forbidden"},
			{"pending", eval.ErrRunResultUnavailable, 409, "result_unavailable"}, {"corrupt", eval.ErrReportInvalid, 500, "internal"},
			{"database", errors.New("secret database cause"), 500, "internal"},
		} {
			t.Run(suffix+tc.name, func(t *testing.T) {
				service := &runTestService{t: t, err: tc.domain}
				app := New(Dependencies{Identity: uploadTestIdentity{}, EvalRuns: service, InsecureLocalCookies: true})
				req := authenticatedUploadRequest(nil)
				req.Method = "GET"
				req.URL.Path = "/api/v1/kbs/7/eval-runs/9" + suffix
				req.Header.Del("X-CSRF-Token")
				response := httptest.NewRecorder()
				app.Handler.ServeHTTP(response, req)
				body := response.Body.String()
				if response.Code != tc.status || service.calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, body)
				}
				if tc.code != "" && !strings.Contains(body, `"code":"`+tc.code+`"`) {
					t.Fatalf("wrong error: %s", body)
				}
				if strings.Contains(body, "secret database cause") || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("unsafe response: %s", body)
				}
				if tc.status == 200 {
					if suffix == "" && (!strings.Contains(body, `"failure_code":null`) || !strings.Contains(body, `"finished_at":null`) || strings.Contains(body, "configuration")) {
						t.Fatalf("status shape: %s", body)
					}
					if suffix != "" && !strings.Contains(body, `"source":"executed"`) {
						t.Fatalf("result source: %s", body)
					}
				}
			})
		}
	}
}

func TestEvalRunReadBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"session", "/api/v1/kbs/7/eval-runs/9", 401},
		{"invalid id", "/api/v1/kbs/7/eval-runs/0", 422},
		{"invalid kb", "/api/v1/kbs/0/eval-runs/9/result", 422},
		{"missing dependency", "/api/v1/kbs/7/eval-runs/9/result", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &runTestService{t: t}
			deps := Dependencies{Identity: uploadTestIdentity{}, EvalRuns: service, InsecureLocalCookies: true}
			if tc.name == "missing dependency" {
				deps.EvalRuns = nil
			}
			app := New(deps)
			req := authenticatedUploadRequest(nil)
			req.Method = "GET"
			req.URL.Path = tc.path
			if tc.name == "session" {
				req.Header.Del("Cookie")
			}
			response := httptest.NewRecorder()
			app.Handler.ServeHTTP(response, req)
			if response.Code != tc.status || service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
			}
		})
	}
}

func (s *runTestService) List(_ context.Context, a retrieval.Actor, kb, cursor int64, limit int32) (eval.RunPage, error) {
	s.scope(a, kb, 9)
	if cursor != 0 || limit != 10 {
		s.t.Fatal("incorrect list pagination")
	}
	return eval.RunPage{Items: []eval.RunStatus{}}, s.err
}
func TestEvalRunListHTTP(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
	}{{"", 200}, {"?cursor=-1", 422}, {"?limit=0", 422}, {"?limit=11", 422}} {
		service := &runTestService{t: t}
		app := New(Dependencies{Identity: uploadTestIdentity{}, EvalRuns: service, InsecureLocalCookies: true})
		req := authenticatedUploadRequest(nil)
		req.Method = "GET"
		req.URL.Path = "/api/v1/kbs/7/eval-runs"
		req.URL.RawQuery = strings.TrimPrefix(tc.query, "?")
		req.Header.Del("X-CSRF-Token")
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, response.Code, response.Body.String())
		}
		if tc.status == 200 && (!strings.Contains(response.Body.String(), `"items":[]`) || !strings.Contains(response.Body.String(), `"next_cursor":null`)) {
			t.Fatal(response.Body.String())
		}
	}
}

func (s *runTestService) Compare(_ context.Context, a retrieval.Actor, kb, baseline, current int64, offset, limit int32) (eval.RunComparison, error) {
	s.scope(a, kb, 9)
	if baseline != 9 || current != 10 || offset != 0 || limit != 20 {
		s.t.Fatal("incorrect comparison arguments")
	}
	return eval.RunComparison{BaselineRunID: baseline, CurrentRunID: current, Source: "executed"}, s.err
}
func TestEvalRunComparisonHTTP(t *testing.T) {
	for _, tc := range []struct {
		query  string
		domain error
		status int
	}{
		{"baseline=9&current=10", nil, 200}, {"baseline=0&current=10", nil, 422}, {"baseline=9", nil, 422}, {"baseline=9&current=10&limit=51", nil, 422},
		{"baseline=9&current=10", eval.ErrReportsIncomparable, 409}, {"baseline=9&current=10", eval.ErrRunResultUnavailable, 409}, {"baseline=9&current=10", eval.ErrReportNotFound, 404}, {"baseline=9&current=10", eval.ErrReportForbidden, 403}, {"baseline=9&current=10", eval.ErrReportInvalid, 500},
	} {
		service := &runTestService{t: t, err: tc.domain}
		app := New(Dependencies{Identity: uploadTestIdentity{}, EvalRuns: service, InsecureLocalCookies: true})
		req := authenticatedUploadRequest(nil)
		req.Method = "GET"
		req.URL.Path = "/api/v1/kbs/7/eval-run-comparison"
		req.URL.RawQuery = tc.query
		req.Header.Del("X-CSRF-Token")
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s %v: %d %s", tc.query, tc.domain, response.Code, response.Body.String())
		}
		if tc.status == 200 && (!strings.Contains(response.Body.String(), `"source":"executed"`) || !strings.Contains(response.Header().Get("Cache-Control"), "no-store")) {
			t.Fatal(response.Body.String(), response.Header())
		}
	}
}
