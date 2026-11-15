package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMCPOAuthBearerDelegationPostgres(t *testing.T) {
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)

	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	defer pool.Close()

	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	svc, err := NewService(pool, hasher, SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	adminPrincipal, err := svc.Bootstrap(ctx, BootstrapRequest{
		TenantName:  "mcp-oauth-" + hex.EncodeToString(nonce[:]),
		DisplayName: "Admin",
		Login:       "admin",
		Password:    []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		for _, tbl := range []string{"oidc_auth_state", "oidc_identity", "oidc_provider", "user_group_member", "user_group", "login_session", "local_account", "audit_log", "app_user"} {
			_, _ = admin.Exec(cctx, "DELETE FROM "+tbl+" WHERE tenant_id = $1", adminPrincipal.TenantID)
		}
		_, _ = admin.Exec(cctx, `DELETE FROM tenant WHERE id = $1`, adminPrincipal.TenantID)
	})

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "mcp-kid-1"
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srvURL,
			"authorization_endpoint": srvURL + "/authorize",
			"token_endpoint":         srvURL + "/token",
			"jwks_uri":               srvURL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		eBytes := big.NewInt(int64(priv.PublicKey.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	if _, err := svc.CreateOIDCProvider(ctx, adminPrincipal, CreateOIDCProviderRequest{
		Name:       "Corp IdP",
		IssuerURL:  srvURL,
		ClientID:   "tuskrag-mcp-client",
		Scopes:     "openid profile email",
		GroupClaim: "groups",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("CreateOIDCProvider: %v", err)
	}

	issuers, err := svc.ListPublicOIDCIssuers(ctx, &adminPrincipal.TenantID)
	if err != nil || len(issuers) != 1 || issuers[0] != srvURL {
		t.Fatalf("ListPublicOIDCIssuers = %v, err=%v", issuers, err)
	}

	// 1. Valid RFC 8707 resource audience + mcp scope + groups
	validJWT := signTestJWT(t, priv, kid, map[string]any{
		"iss":    srvURL,
		"sub":    "delegated-user-99",
		"aud":    "https://tuskrag.example/mcp",
		"scope":  "openid mcp",
		"name":   "Alice Delegated",
		"groups": []string{"finance"},
		"exp":    time.Now().Add(10 * time.Minute).Unix(),
	})
	p, err := svc.AuthenticateMCPBearer(ctx, validJWT, "https://tuskrag.example/mcp", &adminPrincipal.TenantID)
	if err != nil {
		t.Fatalf("AuthenticateMCPBearer valid failed: %v", err)
	}
	if p.TenantID != adminPrincipal.TenantID || p.UserID <= 0 {
		t.Fatalf("unexpected principal: %+v", p)
	}

	// Verify OIDC group synced
	var groupCount int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM user_group_member m JOIN user_group g ON g.id = m.group_id WHERE m.tenant_id = $1 AND m.user_id = $2 AND g.name = 'finance'`, p.TenantID, p.UserID).Scan(&groupCount); err != nil {
		t.Fatal(err)
	}
	if groupCount != 1 {
		t.Fatalf("expected finance group synced, got %d", groupCount)
	}

	// 2. Wrong audience rejected
	wrongAudJWT := signTestJWT(t, priv, kid, map[string]any{
		"iss":   srvURL,
		"sub":   "delegated-user-99",
		"aud":   "https://other-service.example/mcp",
		"scope": "mcp",
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	})
	if _, err := svc.AuthenticateMCPBearer(ctx, wrongAudJWT, "https://tuskrag.example/mcp", &adminPrincipal.TenantID); !errors.Is(err, ErrMCPOAuthInvalidToken) {
		t.Fatalf("expected ErrMCPOAuthInvalidToken for wrong aud, got %v", err)
	}

	// 3. Insufficient scope rejected
	badScopeJWT := signTestJWT(t, priv, kid, map[string]any{
		"iss":   srvURL,
		"sub":   "delegated-user-99",
		"aud":   "https://tuskrag.example/mcp",
		"scope": "email",
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	})
	if _, err := svc.AuthenticateMCPBearer(ctx, badScopeJWT, "https://tuskrag.example/mcp", &adminPrincipal.TenantID); !errors.Is(err, ErrMCPOAuthInsufficientScope) {
		t.Fatalf("expected ErrMCPOAuthInsufficientScope, got %v", err)
	}

	// 4. Disabled user rejected
	if _, err := admin.Exec(ctx, `UPDATE app_user SET disabled = true WHERE tenant_id = $1 AND id = $2`, p.TenantID, p.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateMCPBearer(ctx, validJWT, "https://tuskrag.example/mcp", &adminPrincipal.TenantID); !errors.Is(err, ErrMCPOAuthUserDisabled) {
		t.Fatalf("expected ErrMCPOAuthUserDisabled, got %v", err)
	}
}
