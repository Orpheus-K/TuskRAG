package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIKeyLifecycleRateLimitQuotaAndRevocationPostgres(t *testing.T) {
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
	svc, err := NewService(pool, hasher, SessionConfig{
		IdleTimeout:     time.Hour,
		AbsoluteTimeout: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	tName := "apikey-test-" + hex.EncodeToString(nonce[:])
	var tenantID, userID int64
	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, tName).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'API User') RETURNING id`, tenantID).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = admin.Exec(cctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(cctx, `DELETE FROM api_key_usage_monthly WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(cctx, `DELETE FROM api_key WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(cctx, `DELETE FROM app_user WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(cctx, `DELETE FROM tenant WHERE id = $1`, tenantID)
	})

	principal := Principal{TenantID: tenantID, UserID: userID}
	created, err := svc.CreateAPIKey(ctx, principal, CreateAPIKeyRequest{
		Name:              "ci-agent",
		KBIDs:             []int64{42},
		Actions:           []string{"search", "read"},
		IPAllowlist:       []string{"10.0.0.0/8"},
		RateLimitRPM:      2,
		MonthlyTokenQuota: 100,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !strings.HasPrefix(created.Plaintext, "tsk_"+created.Key.Prefix+"_") {
		t.Fatalf("unexpected plaintext format: %q", created.Plaintext)
	}

	// Disallowed IP -> ErrAPIKeyForbidden.
	if _, err := svc.AuthenticateAPIKey(ctx, created.Plaintext, "192.168.1.5"); !errors.Is(err, ErrAPIKeyForbidden) {
		t.Fatalf("expected ErrAPIKeyForbidden for non-allowlisted IP, got %v", err)
	}

	// Allowed IP succeeds (1st and 2nd request), 3rd hits rate limit (RPM=2).
	authKey, err := svc.AuthenticateAPIKey(ctx, created.Plaintext, "10.1.2.3")
	if err != nil {
		t.Fatalf("AuthenticateAPIKey #1: %v", err)
	}
	if !AuthorizeAPIKeyScope(authKey, 42, "search") || AuthorizeAPIKeyScope(authKey, 42, "ingest") || AuthorizeAPIKeyScope(authKey, 99, "search") {
		t.Fatalf("unexpected scope authorization result for %+v", authKey)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, created.Plaintext, "10.1.2.3"); err != nil {
		t.Fatalf("AuthenticateAPIKey #2: %v", err)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, created.Plaintext, "10.1.2.3"); !errors.Is(err, ErrAPIKeyRateLimited) {
		t.Fatalf("expected ErrAPIKeyRateLimited on 3rd request, got %v", err)
	}

	// Quota exceeded check with a higher RPM key.
	qKey, err := svc.CreateAPIKey(ctx, principal, CreateAPIKeyRequest{
		Name:              "quota-agent",
		Actions:           []string{"answer"},
		RateLimitRPM:      100,
		MonthlyTokenQuota: 50,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey quota-agent: %v", err)
	}
	if err := svc.RecordAPIKeyTokenUsage(ctx, tenantID, qKey.Key.ID, 50); err != nil {
		t.Fatalf("RecordAPIKeyTokenUsage: %v", err)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, qKey.Plaintext, "127.0.0.1"); !errors.Is(err, ErrAPIKeyQuotaExceeded) {
		t.Fatalf("expected ErrAPIKeyQuotaExceeded, got %v", err)
	}

	// Revocation invalidates key immediately.
	if err := svc.RevokeAPIKey(ctx, principal, created.Key.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, created.Plaintext, "10.1.2.3"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated after revoke, got %v", err)
	}
}
