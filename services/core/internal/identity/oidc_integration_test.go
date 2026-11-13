package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func signTestJWT(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdrBytes, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	payBytes, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCPKCEGroupSyncAndLocalDisablePostgres(t *testing.T) {
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
		TenantName:  "oidc-test-" + hex.EncodeToString(nonce[:]),
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
	const kid = "test-kid-1"
	var expectedChallenge, expectedNonce string
	var loginRound atomic.Int32

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
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		verifier := r.FormValue("code_verifier")
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != expectedChallenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		groups := []string{"eng", "finance"}
		if loginRound.Load() == 2 {
			groups = []string{"eng", "legal"} // finance removed, legal added (T20)
		}
		idToken := signTestJWT(t, priv, kid, map[string]any{
			"iss":    srvURL,
			"sub":    "oidc-sub-42",
			"aud":    "tuskrag-web",
			"nonce":  expectedNonce,
			"name":   "SSO Alice",
			"groups": groups,
			"exp":    time.Now().Add(10 * time.Minute).Unix(),
		})
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	// Cannot disable local login before an enabled OIDC provider exists
	if _, err := svc.SetDisableLocalLogin(ctx, adminPrincipal, true); !errors.Is(err, ErrOIDCCannotDisable) {
		t.Fatalf("SetDisableLocalLogin without OIDC err = %v, want ErrOIDCCannotDisable", err)
	}

	provider, err := svc.CreateOIDCProvider(ctx, adminPrincipal, CreateOIDCProviderRequest{
		Name:       "Corp SSO",
		IssuerURL:  srvURL,
		ClientID:   "tuskrag-web",
		GroupClaim: "groups",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateOIDCProvider: %v", err)
	}

	// Round 1: first OIDC login creates user + syncs groups ["eng", "finance"]
	loginRound.Store(1)
	authURLStr, err := svc.StartOIDCLogin(ctx, adminPrincipal.TenantID, provider.ID, "http://localhost/callback")
	if err != nil {
		t.Fatalf("StartOIDCLogin 1: %v", err)
	}
	u1, _ := url.Parse(authURLStr)
	expectedChallenge = u1.Query().Get("code_challenge")
	expectedNonce = u1.Query().Get("nonce")
	sess1, err := svc.CompleteOIDCLogin(ctx, u1.Query().Get("state"), "code-1")
	if err != nil || sess1.Principal.UserID <= 0 {
		t.Fatalf("CompleteOIDCLogin 1: %+v, err=%v", sess1, err)
	}

	// Add a manual group to verify OIDC sync preserves manual groups
	var manualGroupID int64
	if err := admin.QueryRow(ctx, `INSERT INTO user_group (tenant_id, name, source) VALUES ($1, 'manual-vip', 'manual') RETURNING id`, adminPrincipal.TenantID).Scan(&manualGroupID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO user_group_member (tenant_id, group_id, user_id) VALUES ($1, $2, $3)`, adminPrincipal.TenantID, manualGroupID, sess1.Principal.UserID); err != nil {
		t.Fatal(err)
	}

	// Round 2: second OIDC login maps same user, updates OIDC groups to ["eng", "legal"], keeps "manual-vip"
	loginRound.Store(2)
	authURLStr2, err := svc.StartOIDCLogin(ctx, adminPrincipal.TenantID, provider.ID, "http://localhost/callback")
	if err != nil {
		t.Fatalf("StartOIDCLogin 2: %v", err)
	}
	u2, _ := url.Parse(authURLStr2)
	expectedChallenge = u2.Query().Get("code_challenge")
	expectedNonce = u2.Query().Get("nonce")
	sess2, err := svc.CompleteOIDCLogin(ctx, u2.Query().Get("state"), "code-2")
	if err != nil || sess2.Principal.UserID != sess1.Principal.UserID {
		t.Fatalf("CompleteOIDCLogin 2 user mismatch: %+v vs %+v, err=%v", sess2, sess1, err)
	}

	rows, err := admin.Query(ctx, `SELECT g.name FROM user_group_member m JOIN user_group g ON g.tenant_id = m.tenant_id AND g.id = m.group_id WHERE m.tenant_id = $1 AND m.user_id = $2 ORDER BY g.name`, adminPrincipal.TenantID, sess1.Principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotGroups []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		gotGroups = append(gotGroups, name)
	}
	if strings.Join(gotGroups, ",") != "eng,legal,manual-vip" {
		t.Fatalf("synced groups = %v, want [eng legal manual-vip]", gotGroups)
	}

	// Enable disable_local_login -> local Login rejected with ErrLocalLoginDisabled
	if disabled, err := svc.SetDisableLocalLogin(ctx, adminPrincipal, true); err != nil || !disabled {
		t.Fatalf("SetDisableLocalLogin = %t, err=%v", disabled, err)
	}
	if _, err := svc.Login(ctx, adminPrincipal.TenantID, "admin", []byte("correct horse battery staple")); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("local Login after disable err = %v, want ErrLocalLoginDisabled", err)
	}
}
