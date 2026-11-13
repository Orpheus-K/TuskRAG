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

type answerReportTestService struct {
	calls int
	err   error
	t     *testing.T
}

func (s *answerReportTestService) Compare(_ context.Context, a retrieval.Actor, id int64, baseline, current string, offset, limit int32) (eval.AnswerReportComparisonView, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || baseline != strings.Repeat("a", 64) || current != strings.Repeat("b", 64) || offset != 0 || limit != 20 {
		s.t.Fatal("incorrect answer comparison scope")
	}
	return eval.AnswerReportComparisonView{
		Gate:     "regressed",
		Baseline: eval.AnswerReportListItem{Digest: baseline, Source: "imported", DatasetVersion: "fixture", ModelLabel: "test", ModelKind: "stub", PromptVersion: "v1"},
		Current:  eval.AnswerReportListItem{Digest: current, Source: "imported", DatasetVersion: "fixture", ModelLabel: "test", ModelKind: "stub", PromptVersion: "v1"},
		Rows:     []eval.AnswerRowComparison{},
	}, s.err
}

func (s *answerReportTestService) List(_ context.Context, a retrieval.Actor, id int64, cursor string, limit int32) (eval.AnswerReportPage, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || cursor != "" || limit != 10 {
		s.t.Fatal("incorrect answer report list scope")
	}
	return eval.AnswerReportPage{Items: []eval.AnswerReportListItem{}}, s.err
}

func (s *answerReportTestService) Import(_ context.Context, a retrieval.Actor, id int64, r eval.AnswerReport) (eval.StoredAnswerReport, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || r.DatasetVersion != "fixture" {
		s.t.Fatal("incorrect answer report import scope")
	}
	return eval.StoredAnswerReport{Digest: strings.Repeat("a", 64), Source: "imported", Report: r}, s.err
}

func (s *answerReportTestService) Get(_ context.Context, a retrieval.Actor, id int64, digest string) (eval.StoredAnswerReport, error) {
	s.calls++
	if a.TenantID != 1 || a.UserID != 2 || id != 7 || digest != strings.Repeat("a", 64) {
		s.t.Fatal("incorrect answer report read scope")
	}
	return eval.StoredAnswerReport{Digest: digest, Source: "imported"}, s.err
}

func TestAnswerEvalReportHTTP(t *testing.T) {
	data, err := json.Marshal(eval.AnswerReport{
		DatasetVersion: "fixture",
		DatasetSHA256:  strings.Repeat("a", 64),
		ConfigSHA256:   strings.Repeat("b", 64),
		ModelLabel:     "test",
		ModelKind:      "stub",
		PromptVersion:  "v1",
		Status:         "passed",
		Summary:        eval.AnswerSummary{Cases: 1, UnanswerableCases: 1},
		Rows: []eval.AnswerRow{{
			ID:                  "q1",
			Outcome:             "refused",
			ValidCitation:       true,
			SourceIntegrity:     true,
			EvidenceIDs:         []string{},
			EvidenceChunkCounts: []int{},
			EvidenceDocuments:   []int64{},
			Degraded:            []string{},
			TimingsMS:           map[string]float64{"total": 1},
		}},
		LatencyMS: map[string][2]float64{"total": {1, 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	comparePath := "/api/v1/kbs/7/answer-eval-report-comparison?baseline=" + strings.Repeat("a", 64) + "&current=" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, method, path, body string
		csrf, session            bool
		status                   int
		domain                   error
	}{
		{"import", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, true, 200, nil},
		{"read", http.MethodGet, "/api/v1/kbs/7/answer-eval-reports/" + strings.Repeat("a", 64), "", false, true, 200, nil},
		{"list", http.MethodGet, "/api/v1/kbs/7/answer-eval-reports", "", false, true, 200, nil},
		{"compare", http.MethodGet, comparePath, "", false, true, 200, nil},
		{"csrf", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), false, true, 403, nil},
		{"session", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, false, 401, nil},
		{"invalid digest", http.MethodGet, "/api/v1/kbs/7/answer-eval-reports/bad", "", false, true, 422, nil},
		{"unknown field", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", `{"unexpected":true}`, true, true, 422, nil},
		{"null percentile", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", strings.Replace(string(data), `"total":[1,1]`, `"total":null`, 1), true, true, 422, nil},
		{"invalid", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, true, 400, eval.ErrReportInvalid},
		{"hidden", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, true, 404, eval.ErrReportNotFound},
		{"forbidden", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, true, 403, eval.ErrReportForbidden},
		{"archived", http.MethodPost, "/api/v1/kbs/7/answer-eval-reports", string(data), true, true, 409, eval.ErrReportArchived},
		{"incomparable", http.MethodGet, comparePath, "", false, true, 409, eval.ErrReportsIncomparable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &answerReportTestService{t: t, err: tc.domain}
			app := New(Dependencies{Identity: uploadTestIdentity{}, AnswerReports: service, InsecureLocalCookies: true})
			request := authenticatedUploadRequest(strings.NewReader(tc.body))
			request.Method = tc.method
			if i := strings.Index(tc.path, "?"); i >= 0 {
				request.URL.Path = tc.path[:i]
				request.URL.RawQuery = tc.path[i+1:]
			} else {
				request.URL.Path = tc.path
			}
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
			if tc.name == "compare" && !strings.Contains(response.Body.String(), `"gate":"regressed"`) {
				t.Fatalf("unexpected compare body=%s", response.Body.String())
			}
			if tc.status == 200 && (tc.name == "import" || tc.name == "read") && !strings.Contains(response.Body.String(), `"source":"imported"`) {
				t.Fatalf("unexpected body=%s", response.Body.String())
			}
		})
	}
}
