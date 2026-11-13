package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/retrieval"
)

type stubIdentityService struct{}

func (stubIdentityService) Login(context.Context, int64, string, []byte) (identity.Session, error) {
	return identity.Session{}, identity.ErrUnauthenticated
}
func (stubIdentityService) Authenticate(context.Context, string) (identity.Session, error) {
	return identity.Session{}, identity.ErrUnauthenticated
}
func (stubIdentityService) Logout(context.Context, string) error { return nil }

type stubAPIKeyService struct {
	authFn func(context.Context, string, string) (identity.AuthenticatedAPIKey, error)
}

func (s stubAPIKeyService) CreateAPIKey(context.Context, identity.Principal, identity.CreateAPIKeyRequest) (identity.CreatedAPIKey, error) {
	return identity.CreatedAPIKey{}, nil
}
func (s stubAPIKeyService) ListAPIKeys(context.Context, identity.Principal) ([]identity.APIKey, error) {
	return nil, nil
}
func (s stubAPIKeyService) RevokeAPIKey(context.Context, identity.Principal, int64) error {
	return nil
}
func (s stubAPIKeyService) AuthenticateAPIKey(ctx context.Context, rawKey, clientIP string) (identity.AuthenticatedAPIKey, error) {
	return s.authFn(ctx, rawKey, clientIP)
}
func (s stubAPIKeyService) RecordAPIKeyTokenUsage(context.Context, int64, int64, int64) error {
	return nil
}

func TestAPIKeyBearerAuthScopeRateLimitQuotaAndCrossTenant(t *testing.T) {
	app := New(Dependencies{
		Identity: stubIdentityService{},
		APIKeys: stubAPIKeyService{
			authFn: func(_ context.Context, rawKey, _ string) (identity.AuthenticatedAPIKey, error) {
				switch rawKey {
				case "tsk_ratelimit":
					return identity.AuthenticatedAPIKey{}, identity.ErrAPIKeyRateLimited
				case "tsk_quota":
					return identity.AuthenticatedAPIKey{}, identity.ErrAPIKeyQuotaExceeded
				case "tsk_read_only":
					return identity.AuthenticatedAPIKey{
						ID:        1,
						Principal: identity.Principal{TenantID: 10, UserID: 20},
						KBIDs:     []int64{1},
						Actions:   []string{"read"},
					}, nil
				case "tsk_search_ok":
					return identity.AuthenticatedAPIKey{
						ID:        2,
						Principal: identity.Principal{TenantID: 10, UserID: 20},
						KBIDs:     []int64{1},
						Actions:   []string{"search"},
					}, nil
				default:
					return identity.AuthenticatedAPIKey{}, identity.ErrUnauthenticated
				}
			},
		},
		Search: searchServiceFunc(func(_ context.Context, actor retrieval.Actor, req retrieval.Request) (retrieval.Result, error) {
			if req.KnowledgeBaseID == 999 {
				return retrieval.Result{}, retrieval.ErrNotFound
			}
			return retrieval.Result{}, nil
		}),
	})

	t.Run("rate limit returns 429 with Retry-After", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"query":"hi","kb_ids":[1]}`))
		req.Header.Set("Authorization", "Bearer tsk_ratelimit")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
			t.Fatalf("got status=%d Retry-After=%q, want 429 with 60", w.Code, w.Header().Get("Retry-After"))
		}
	})

	t.Run("monthly quota returns 429 with X-Quota-Type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"query":"hi","kb_ids":[1]}`))
		req.Header.Set("Authorization", "Bearer tsk_quota")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("X-Quota-Type") != "monthly_tokens" {
			t.Fatalf("got status=%d X-Quota-Type=%q, want 429 with monthly_tokens", w.Code, w.Header().Get("X-Quota-Type"))
		}
	})

	t.Run("scope denied returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"query":"hi","kb_ids":[1]}`))
		req.Header.Set("Authorization", "Bearer tsk_read_only")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("got status=%d, want 403", w.Code)
		}
	})

	t.Run("valid scope succeeds and cross-tenant KB returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"query":"hi","kb_ids":[1]}`))
		req.Header.Set("Authorization", "Bearer tsk_search_ok")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("got status=%d body=%s, want 200", w.Code, w.Body.String())
		}

		crossReq := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"query":"hi","kb_ids":[999]}`))
		crossReq.Header.Set("Authorization", "Bearer tsk_search_ok")
		crossReq.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		app.Handler.ServeHTTP(w2, crossReq)
		if w2.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant KB got status=%d body=%s, want 404", w2.Code, w2.Body.String())
		}
	})
}
