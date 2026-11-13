package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/kb"
)

type configTestService struct {
	get func(context.Context, kb.Actor, int64) (kb.KnowledgeConfig, error)
	put func(context.Context, kb.Actor, int64, int32, kb.RetrievalConfig) (kb.KnowledgeConfig, error)
}

func (s configTestService) GetConfig(ctx context.Context, a kb.Actor, id int64) (kb.KnowledgeConfig, error) {
	return s.get(ctx, a, id)
}
func (s configTestService) PutConfig(ctx context.Context, a kb.Actor, id int64, revision int32, config kb.RetrievalConfig) (kb.KnowledgeConfig, error) {
	return s.put(ctx, a, id, revision, config)
}

func TestConfigHTTPPreconditions(t *testing.T) {
	calls := 0
	service := configTestService{
		get: func(_ context.Context, a kb.Actor, id int64) (kb.KnowledgeConfig, error) {
			if a.TenantID != 1 || a.UserID != 2 || id != 7 {
				t.Fatal("incorrect config read scope")
			}
			return kb.KnowledgeConfig{}, nil
		},
		put: func(_ context.Context, a kb.Actor, id int64, revision int32, config kb.RetrievalConfig) (kb.KnowledgeConfig, error) {
			calls++
			if a.TenantID != 1 || a.UserID != 2 || id != 7 || revision != 0 || config.TopK != 8 || config.Reranker != nil {
				t.Fatal("incorrect configuration request")
			}
			return kb.KnowledgeConfig{Revision: 1, Retrieval: &config}, nil
		},
	}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Config: service, InsecureLocalCookies: true})
	request := authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodGet, "/api/v1/kbs/7/config"
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("ETag") != `"0"` || strings.Contains(response.Body.String(), `"retrieval"`) {
		t.Fatalf("initial config: %d %s", response.Code, response.Body.String())
	}
	for _, tc := range []struct {
		name, match, body string
		csrf              bool
		status            int
	}{
		{"success", `"0"`, `{"retrieval":{"top_k":8}}`, true, 200},
		{"missing CSRF", `"0"`, `{"retrieval":{"top_k":8}}`, false, 403},
		{"missing match", "", `{"retrieval":{"top_k":8}}`, true, 422},
		{"weak match", `W/"0"`, `{"retrieval":{"top_k":8}}`, true, 400},
		{"wildcard", "*", `{"retrieval":{"top_k":8}}`, true, 400},
		{"unquoted", "0", `{"retrieval":{"top_k":8}}`, true, 400},
		{"leading zero", `"00"`, `{"retrieval":{"top_k":8}}`, true, 400},
		{"negative", `"-1"`, `{"retrieval":{"top_k":8}}`, true, 400},
		{"overflow", `"2147483648"`, `{"retrieval":{"top_k":8}}`, true, 400},
		{"missing retrieval", `"0"`, `{}`, true, 422},
		{"out of range", `"0"`, `{"retrieval":{"top_k":31}}`, true, 422},
		{"unknown setting", `"0"`, `{"retrieval":{"top_k":8,"lexical_fallback":true}}`, true, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			request := authenticatedUploadRequest(strings.NewReader(tc.body))
			request.Method, request.URL.Path = http.MethodPut, "/api/v1/kbs/7/config"
			request.Header.Set("Content-Type", "application/json")
			if tc.match != "" {
				request.Header.Set("If-Match", tc.match)
			}
			if !tc.csrf {
				request.Header.Del("X-CSRF-Token")
			}
			response := httptest.NewRecorder()
			app.Handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != 200 && calls != before {
				t.Fatal("invalid request reached service")
			}
			if tc.status == 200 && (calls != before+1 || response.Header().Get("ETag") != `"1"`) {
				t.Fatal("successful update missing revision")
			}
		})
	}
}

func TestConfigHTTPDomainErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{kb.ErrRevisionConflict, 409, "revision_conflict"},
		{kb.ErrForbidden, 403, "forbidden"},
		{kb.ErrNotFound, 404, "not_found"},
		{kb.ErrConfigProviderNotFound, 404, "provider_not_found"},
		{kb.ErrArchived, 409, "knowledge_base_archived"},
	} {
		service := configTestService{put: func(context.Context, kb.Actor, int64, int32, kb.RetrievalConfig) (kb.KnowledgeConfig, error) {
			return kb.KnowledgeConfig{}, tc.err
		}}
		app := New(Dependencies{Identity: uploadTestIdentity{}, Config: service, InsecureLocalCookies: true})
		request := authenticatedUploadRequest(strings.NewReader(`{"retrieval":{"top_k":8}}`))
		request.Method, request.URL.Path = http.MethodPut, "/api/v1/kbs/7/config"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-Match", `"0"`)
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, request)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != tc.status || body.Error.Code != tc.code {
			t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body.String(), err)
		}
	}
}
