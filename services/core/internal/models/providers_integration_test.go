package models

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/secrets"
)

func TestModelProviderCRUDAndConnectivityPostgres(t *testing.T) {
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

	var masterKey [32]byte
	if _, err := rand.Read(masterKey[:]); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewVault(masterKey[:])
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	principal, err := identities.Bootstrap(ctx, identity.BootstrapRequest{
		TenantName:  "provider-test-" + hex.EncodeToString(nonce[:]),
		DisplayName: "Admin",
		Login:       "admin",
		Password:    []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		for _, tbl := range []string{"login_session", "local_account", "audit_log", "chat_model", "embedding_space", "model_provider", "app_user"} {
			_, _ = admin.Exec(cctx, "DELETE FROM "+tbl+" WHERE tenant_id = $1", principal.TenantID)
		}
		_, _ = admin.Exec(cctx, `DELETE FROM tenant WHERE id = $1`, principal.TenantID)
	})

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	created, err := catalog.CreateProvider(ctx, principal.TenantID, principal.UserID, ProviderInput{
		Name:     "Local Gateway",
		BaseURL:  srv.URL + "/v1",
		Locality: Internal,
		APIKey:   []byte("sk-provider-secret"),
	})
	if err != nil || !created.HasCredential {
		t.Fatalf("CreateProvider = %+v, err=%v", created, err)
	}

	probe, err := catalog.TestProviderConnectivity(ctx, principal.TenantID, principal.UserID, created.ID)
	if err != nil || !probe.Reachable || probe.StatusCode != 200 || gotAuth != "Bearer sk-provider-secret" {
		t.Fatalf("TestProviderConnectivity = %+v, gotAuth=%q, err=%v", probe, gotAuth, err)
	}

	updated, err := catalog.UpdateProvider(ctx, principal.TenantID, principal.UserID, created.ID, ProviderInput{
		Name:     "Local Gateway v2",
		BaseURL:  srv.URL + "/v1",
		Locality: Internal,
	})
	if err != nil || updated.Name != "Local Gateway v2" || !updated.HasCredential {
		t.Fatalf("UpdateProvider = %+v, err=%v", updated, err)
	}

	// Reference by embedding_space -> DeleteProvider returns ErrProviderInUse
	if _, err := admin.Exec(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, normalized, distance) VALUES ($1, $2, 'bge-m3', 1024, true, 'cosine')`, principal.TenantID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DeleteProvider(ctx, principal.TenantID, principal.UserID, created.ID); !errors.Is(err, ErrProviderInUse) {
		t.Fatalf("DeleteProvider in use err = %v, want ErrProviderInUse", err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM embedding_space WHERE tenant_id = $1`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DeleteProvider(ctx, principal.TenantID, principal.UserID, created.ID); err != nil {
		t.Fatalf("DeleteProvider err = %v", err)
	}
}
