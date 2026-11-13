package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type reportTestService struct {
	calls int
	err   error
	t     *testing.T
}

func (s *reportTestService) Compare(_ context.Context, a retrieval.Actor, id int64, baseline, current string, offset, limit int32) (eval.ReportComparison, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || baseline != strings.Repeat("a", 64) || current != strings.Repeat("b", 64) || offset != 0 || limit != 20 {
		s.t.Fatal("incorrect comparison scope")
	}
	return eval.ReportComparison{BaselineDigest: baseline, CurrentDigest: current, Gate: "regressed", Rows: []eval.RowComparison{}}, s.err
}

func (s *reportTestService) List(_ context.Context, a retrieval.Actor, id int64, cursor string, limit int32) (eval.ReportPage, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || cursor != "" || limit != 10 {
		s.t.Fatal("incorrect list scope")
	}
	return eval.ReportPage{Items: []eval.ReportListItem{}}, s.err
}

func (s *reportTestService) Import(_ context.Context, a retrieval.Actor, id int64, r eval.RunReport) (eval.StoredReport, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || r.DatasetVersion != "fixture" {
		s.t.Fatal("incorrect report import scope")
	}
	return eval.StoredReport{Digest: strings.Repeat("a", 64), Source: "imported", Report: r}, s.err
}
func (s *reportTestService) Get(_ context.Context, a retrieval.Actor, id int64, digest string) (eval.StoredReport, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || digest != strings.Repeat("a", 64) {
		s.t.Fatal("incorrect report read scope")
	}
	return eval.StoredReport{Digest: digest, Source: "imported"}, s.err
}

func TestEvalReportHTTP(t *testing.T) {
	data, err := json.Marshal(eval.RunReport{DatasetVersion: "fixture", DatasetSHA256: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("b", 64), ModelLabel: "test", ModelKind: "stub", Mode: "rrf", Status: "passed", Summary: eval.Summary{Cases: 1, UnanswerableCases: 1}, Rows: []eval.RunRow{{ID: "q1", TimingsMS: map[string]float64{"total": 1}}}, LatencyMS: map[string][2]float64{"total": {1, 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body string
		csrf, session            bool
		status                   int
		domain                   error
	}{
		{"import", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, true, 200, nil},
		{"read", http.MethodGet, "/api/v1/kbs/7/eval-reports/" + strings.Repeat("a", 64), "", false, true, 200, nil},
		{"list", http.MethodGet, "/api/v1/kbs/7/eval-reports", "", false, true, 200, nil},
		{"csrf", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), false, true, 403, nil},
		{"session", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, false, 401, nil},
		{"invalid digest", http.MethodGet, "/api/v1/kbs/7/eval-reports/bad", "", false, true, 422, nil},
		{"unknown field", http.MethodPost, "/api/v1/kbs/7/eval-reports", `{"unexpected":true}`, true, true, 422, nil},
		{"null percentile", http.MethodPost, "/api/v1/kbs/7/eval-reports", strings.Replace(string(data), `"total":[1,1]`, `"total":null`, 1), true, true, 422, nil},
		{"invalid", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, true, 400, eval.ErrReportInvalid},
		{"hidden", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, true, 404, eval.ErrReportNotFound},
		{"forbidden", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, true, 403, eval.ErrReportForbidden},
		{"archived", http.MethodPost, "/api/v1/kbs/7/eval-reports", string(data), true, true, 409, eval.ErrReportArchived},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &reportTestService{t: t, err: tc.domain}
			app := New(Dependencies{Identity: uploadTestIdentity{}, Reports: service, InsecureLocalCookies: true})
			request := authenticatedUploadRequest(strings.NewReader(tc.body))
			request.Method = tc.method
			request.URL.Path = tc.path
			request.Header.Set("Content-Type", "application/json")
			if !tc.csrf {
				request.Header.Del("X-CSRF-Token")
			}
			if !tc.session {
				request.Header.Del("Cookie")
			}
			response := httptest.NewRecorder()
			app.Handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != 200 && tc.domain == nil && service.calls != 0 {
				t.Fatal("invalid request reached domain")
			}
			if tc.name == "list" && !strings.Contains(response.Body.String(), `"items":[],"next_cursor":null`) {
				t.Fatal("invalid empty page")
			}
			if tc.status == 200 && tc.name != "list" && !strings.Contains(response.Body.String(), `"source":"imported"`) {
				t.Fatal("source not disclosed")
			}
		})
	}
}

func TestEvalReportComparisonHTTP(t *testing.T) {
	for _, tc := range []struct {
		query  string
		domain error
		status int
	}{
		{"baseline=" + strings.Repeat("a", 64) + "&current=" + strings.Repeat("b", 64), nil, 200},
		{"baseline=" + strings.Repeat("a", 64) + "&current=" + strings.Repeat("b", 64), eval.ErrReportsIncomparable, 409},
		{"baseline=" + strings.Repeat("a", 64) + "&current=" + strings.Repeat("b", 64), eval.ErrReportNotFound, 404},
		{"baseline=" + strings.Repeat("a", 64) + "&current=" + strings.Repeat("b", 64) + "&limit=51", nil, 422},
		{"baseline=" + strings.Repeat("a", 64), nil, 422},
	} {
		service := &reportTestService{t: t, err: tc.domain}
		app := New(Dependencies{Identity: uploadTestIdentity{}, Reports: service, InsecureLocalCookies: true})
		request := authenticatedUploadRequest(nil)
		request.Method = http.MethodGet
		request.URL.Path = "/api/v1/kbs/7/eval-report-comparison"
		request.URL.RawQuery = tc.query
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if tc.status == 200 && !strings.Contains(response.Body.String(), `"gate":"regressed"`) {
			t.Fatal("regression is not a transport failure")
		}
		if tc.status == 422 && service.calls != 0 {
			t.Fatal("invalid query reached domain")
		}
	}
}
