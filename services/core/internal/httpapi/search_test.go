package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type searchServiceFunc func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)

func (f searchServiceFunc) Search(ctx context.Context, actor retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
	return f(ctx, actor, request)
}

func TestSearchMapsConfigurationFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{indexing.ErrUnsupportedDistance, http.StatusUnprocessableEntity, "unsupported_embedding"},
		{models.ErrRuntimeChanged, http.StatusConflict, "configuration_changed"},
		{models.ErrRuntimeUnavailable, http.StatusServiceUnavailable, "model_unavailable"},
	} {
		var result *ErrorModel
		if err := searchError(context.Background(), tc.err); !errors.As(err, &result) || result.GetStatus() != tc.status || result.Body.Code != tc.code {
			t.Fatalf("error %v mapped to %v", tc.err, err)
		}
	}
}

func TestSearchRerankAndDebugContract(t *testing.T) {
	score := -0.5
	service := searchServiceFunc(func(_ context.Context, _ retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
		if request.Rerank == nil || request.Rerank.ProviderID != 3 || request.Rerank.Model != "rank" || !request.Debug {
			t.Fatalf("rerank options lost: %+v", request)
		}
		candidate := retrieval.Candidate{ChunkID: 1, DocumentID: 2, VersionID: 3, SectionID: 4, Title: "test", Content: "evidence", Score: score, RerankScore: &score}
		return retrieval.Result{Candidates: []retrieval.Candidate{candidate}, Trace: &retrieval.Trace{Candidates: []retrieval.Candidate{candidate}}}, nil
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Search: service, InsecureLocalCookies: true})
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, searchRequest(`{"query":"住宿","kb_ids":[7],"options":{"rerank":true,"debug":true,"reranker":{"provider_id":3,"model":"rank","top_n":30}}}`))
	var output struct {
		Evidence []SearchEvidence `json:"evidence"`
		Trace    *retrieval.Trace `json:"trace"`
		Degraded []string         `json:"degraded"`
	}
	if json.Unmarshal(w.Body.Bytes(), &output) != nil || w.Code != 200 || len(output.Evidence) != 1 || output.Evidence[0].ScoreKind != "rerank" || output.Evidence[0].Score != score || output.Trace == nil || len(output.Degraded) != 0 {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func searchRequest(body string) *http.Request {
	req := authenticatedUploadRequest(strings.NewReader(body))
	req.URL.Path = "/api/v1/search"
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestSearchReturnsRRFScoreAndExplicitDegradation(t *testing.T) {
	start, end := int32(2), int32(3)
	service := searchServiceFunc(func(_ context.Context, actor retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
		if actor.TenantID != 1 || actor.UserID != 2 || request.KnowledgeBaseID != 7 || request.Query != "申请" || request.Limit != 8 ||
			request.Filters.UpdatedAfter == nil || len(request.Filters.DocumentTags) != 1 {
			t.Fatalf("unexpected search request: actor=%+v request=%+v", actor, request)
		}
		return retrieval.Result{Degraded: []string{"rerank"}, Candidates: []retrieval.Candidate{{
			ChunkID: 11, DocumentID: 12, VersionID: 13, SectionID: 14, Title: "制度", Content: "申请。",
			HeadingPath: []string{"制度"}, PageStart: &start, PageEnd: &end, Score: 0.03,
		}}}, nil
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Search: service, InsecureLocalCookies: true})
	req := searchRequest(`{"query":"申请","kb_ids":[7],"filters":{"doc_tags":["制度"],"updated_after":"2026-01-01"},"options":{"rerank":false}}`)
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	var body struct {
		Status   string           `json:"status"`
		Degraded []string         `json:"degraded"`
		Evidence []SearchEvidence `json:"evidence"`
		TraceID  string           `json:"trace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body.Status != "ok" ||
		len(body.Degraded) != 1 || body.Degraded[0] != "rerank" || len(body.Evidence) != 1 || body.Evidence[0].ScoreKind != "rrf" ||
		body.Evidence[0].ID != "c_11" || len(body.Evidence[0].Pages) != 2 || body.TraceID != w.Header().Get("X-Trace-ID") {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
}

func TestSearchRejectsUnsupportedOptionsAndPropagatesFailures(t *testing.T) {
	var calls int
	service := searchServiceFunc(func(_ context.Context, _ retrieval.Actor, _ retrieval.Request) (retrieval.Result, error) {
		calls++
		return retrieval.Result{}, &models.UpstreamError{Retryable: true}
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Search: service, InsecureLocalCookies: true})
	for _, tc := range []struct {
		body   string
		status int
		code   string
		called bool
	}{
		{`{"query":"申请","kb_ids":[7]}`, http.StatusServiceUnavailable, "rerank_unavailable", false},
		{`{"query":"申请","kb_ids":[7],"options":{"rerank":false,"expand_parent":true}}`, http.StatusNotImplemented, "unsupported", false},
		{`{"query":"申请","kb_ids":[7],"filters":{"updated_after":"invalid"},"options":{"rerank":false}}`, http.StatusBadRequest, "invalid_request", false},
		{`{"query":"申请","kb_ids":[7],"options":{"rerank":false}}`, http.StatusServiceUnavailable, "model_unavailable", true},
	} {
		before := calls
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, searchRequest(tc.body))
		var body struct {
			Error ErrorBody `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != tc.status || body.Error.Code != tc.code || (calls > before) != tc.called {
			t.Fatalf("status=%d body=%s calls=%d err=%v", w.Code, w.Body.String(), calls-before, err)
		}
	}
	req := searchRequest(`{"query":"申请","kb_ids":[7],"options":{"rerank":false}}`)
	req.Header.Del("X-CSRF-Token")
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("search accepted without CSRF: status=%d calls=%d", w.Code, calls)
	}
	var missing *ErrorModel
	if err := searchError(context.Background(), retrieval.ErrNotFound); !errors.As(err, &missing) || missing.GetStatus() != http.StatusNotFound {
		t.Fatalf("invisible knowledge base: %v", err)
	}
}
