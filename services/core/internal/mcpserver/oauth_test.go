package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
)

type stubOAuthAuth struct {
	principal identity.Principal
	err       error
	issuers   []string
	lastRes   string
}

func (s *stubOAuthAuth) AuthenticateMCPBearer(_ context.Context, rawToken, expectedResource string, _ *int64) (identity.Principal, error) {
	s.lastRes = expectedResource
	if s.err != nil {
		return identity.Principal{}, s.err
	}
	if rawToken != "valid-jwt" {
		return identity.Principal{}, identity.ErrMCPOAuthInvalidToken
	}
	return s.principal, nil
}

func (s *stubOAuthAuth) ListPublicOIDCIssuers(_ context.Context, _ *int64) ([]string, error) {
	return s.issuers, nil
}

type stubExactSearch struct {
	stubSearch
}

func (s *stubExactSearch) ExactMatch(_ context.Context, _ retrieval.Actor, kbID int64, pattern string, _ bool, _ int) ([]retrieval.Candidate, error) {
	if kbID != 1 || !strings.Contains("ERR-404: 找不到文件", pattern) {
		return nil, nil
	}
	return []retrieval.Candidate{{
		ChunkID:    88,
		DocumentID: 10,
		VersionID:  20,
		SectionID:  50,
		Title:      "错误码手册",
		Content:    "ERR-404: 找不到文件",
		TrustTier:  "trusted",
	}}, nil
}

func TestMCPOAuthProtectedResourceMetadataAndDelegation(t *testing.T) {
	oauth := &stubOAuthAuth{
		principal: identity.Principal{TenantID: 7, UserID: 42},
		issuers:   []string{"https://idp.example.com"},
	}
	srv := NewServer(Dependencies{
		OAuth:     oauth,
		Knowledge: &stubKnowledge{},
		Reader:    &stubReader{},
		Search:    &stubExactSearch{},
	})

	// 1. Metadata endpoint
	metaReq := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	metaReq.Host = "rag.example.com"
	metaRec := httptest.NewRecorder()
	srv.ServeProtectedResourceMetadata(metaRec, metaReq)
	if metaRec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200", metaRec.Code)
	}
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(metaRec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Resource != "http://rag.example.com/mcp" || len(meta.AuthorizationServers) != 1 || meta.AuthorizationServers[0] != "https://idp.example.com" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	// 2. Missing bearer includes WWW-Authenticate resource_metadata
	unauthReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	unauthReq.Host = "rag.example.com"
	unauthRec := httptest.NewRecorder()
	srv.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", unauthRec.Code)
	}
	if !strings.Contains(unauthRec.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("missing WWW-Authenticate resource_metadata: %q", unauthRec.Header().Get("WWW-Authenticate"))
	}

	// 3. Delegated OAuth bearer executes exact_match and marks content_role=untrusted_data
	callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exact_match","arguments":{"pattern":"ERR-404"}}}`
	callReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(callBody))
	callReq.Host = "rag.example.com"
	callReq.Header.Set("Authorization", "Bearer valid-jwt")
	callRec := httptest.NewRecorder()
	srv.ServeHTTP(callRec, callReq)
	if callRec.Code != http.StatusOK {
		t.Fatalf("call status = %d, body=%s", callRec.Code, callRec.Body.String())
	}
	if oauth.lastRes != "http://rag.example.com/mcp" {
		t.Fatalf("expected resource http://rag.example.com/mcp, got %q", oauth.lastRes)
	}
	if !strings.Contains(callRec.Body.String(), "untrusted_data") || !strings.Contains(callRec.Body.String(), "trusted") {
		t.Fatalf("expected untrusted_data and trust_tier in response: %s", callRec.Body.String())
	}

	// 4. Insufficient scope returns 403
	oauth.err = identity.ErrMCPOAuthInsufficientScope
	scopeReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(callBody))
	scopeReq.Header.Set("Authorization", "Bearer valid-jwt")
	scopeRec := httptest.NewRecorder()
	srv.ServeHTTP(scopeRec, scopeReq)
	if scopeRec.Code != http.StatusForbidden {
		t.Fatalf("insufficient scope status = %d, want 403", scopeRec.Code)
	}
}

var _ kb.KnowledgeBase
