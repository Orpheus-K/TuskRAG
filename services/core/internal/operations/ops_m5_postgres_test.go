package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

func TestM5OperationsDiagnoseAndRotateMasterKeyPostgres(t *testing.T) {
	appURL := os.Getenv("TUSKRAG_TEST_DATABASE_URL")
	adminURL := os.Getenv("TUSKRAG_TEST_ADMIN_DATABASE_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("TUSKRAG_TEST_DATABASE_URL and TUSKRAG_TEST_ADMIN_DATABASE_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, adminURL); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("pgxpool.New adminPool: %v", err)
	}
	defer adminPool.Close()

	appPool, err := db.Open(ctx, appURL)
	if err != nil {
		t.Fatalf("Open appPool: %v", err)
	}
	defer appPool.Close()

	setupTx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin setupTx: %v", err)
	}
	defer func() { _ = setupTx.Rollback(context.Background()) }()
	q := dbquery.New(setupTx)
	tenantID, err := q.NextTenantID(ctx)
	if err != nil {
		t.Fatalf("NextTenantID: %v", err)
	}
	if _, err := q.SetTenant(ctx, tenantID); err != nil {
		t.Fatalf("set tenant %d: %v", tenantID, err)
	}
	if err := q.BootstrapTenant(ctx, dbquery.BootstrapTenantParams{
		TenantID: tenantID,
		Name:     fmt.Sprintf("ops-m5-test-%d", time.Now().UnixNano()),
	}); err != nil {
		t.Fatalf("BootstrapTenant: %v", err)
	}
	t.Cleanup(func() {
		tx, cErr := adminPool.Begin(context.Background())
		if cErr == nil {
			_, _ = dbquery.New(tx).SetTenant(context.Background(), tenantID)
			_, _ = tx.Exec(context.Background(), `DELETE FROM oidc_provider WHERE tenant_id = $1`, tenantID)
			_, _ = tx.Exec(context.Background(), `DELETE FROM model_provider WHERE tenant_id = $1`, tenantID)
			_, _ = tx.Exec(context.Background(), `DELETE FROM tenant WHERE id = $1`, tenantID)
			_ = tx.Commit(context.Background())
		}
	})

	oldKey := bytes.Repeat([]byte{0x11}, 32)
	newKey := bytes.Repeat([]byte{0x22}, 32)
	oldVault, err := secrets.NewVault(oldKey)
	if err != nil {
		t.Fatalf("oldVault: %v", err)
	}
	newVault, err := secrets.NewVault(newKey)
	if err != nil {
		t.Fatalf("newVault: %v", err)
	}

	var modelProviderID int64
	if err := setupTx.QueryRow(ctx, `
		INSERT INTO model_provider (tenant_id, name, base_url, locality)
		VALUES ($1, 'm5-rotate-model', 'http://127.0.0.1:8080/v1', 'internal')
		RETURNING id
	`, tenantID).Scan(&modelProviderID); err != nil {
		t.Fatalf("insert model_provider: %v", err)
	}
	modelSealed, err := oldVault.Seal(ctx, tenantID, modelProviderID, []byte("sk-old-model-secret"))
	if err != nil {
		t.Fatalf("Seal model: %v", err)
	}
	if _, err := setupTx.Exec(ctx, `
		UPDATE model_provider SET api_key_id = $1, api_key_nonce = $2, api_key_ciphertext = $3 WHERE id = $4
	`, modelSealed.KeyID, modelSealed.Nonce, modelSealed.Ciphertext, modelProviderID); err != nil {
		t.Fatalf("update model_provider secret: %v", err)
	}

	var oidcProviderID int64
	if err := setupTx.QueryRow(ctx, `
		INSERT INTO oidc_provider (tenant_id, name, issuer_url, client_id, scopes)
		VALUES ($1, 'm5-rotate-oidc', 'https://idp.example.com', 'client-1', 'openid profile')
		RETURNING id
	`, tenantID).Scan(&oidcProviderID); err != nil {
		t.Fatalf("insert oidc_provider: %v", err)
	}
	oidcSealed, err := oldVault.Seal(ctx, tenantID, oidcProviderID, []byte("oidc-old-client-secret"))
	if err != nil {
		t.Fatalf("Seal oidc: %v", err)
	}
	if _, err := setupTx.Exec(ctx, `
		UPDATE oidc_provider SET client_secret_id = $1, client_secret_nonce = $2, client_secret_ciphertext = $3 WHERE id = $4
	`, oidcSealed.KeyID, oidcSealed.Nonce, oidcSealed.Ciphertext, oidcProviderID); err != nil {
		t.Fatalf("update oidc_provider secret: %v", err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatalf("commit setupTx: %v", err)
	}

	diag, err := Diagnose(ctx, appPool, tenantID)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if diag.TenantID != tenantID || diag.DatabaseSchema < db.SchemaVersion {
		t.Fatalf("unexpected Diagnose report: %+v", diag)
	}

	rot, err := RotateMasterKey(ctx, appPool, tenantID, oldVault, newVault)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if rot.ModelProvidersRotated != 1 || rot.OIDCProvidersRotated != 1 || rot.NewKeyID != newVault.KeyID() {
		t.Fatalf("unexpected RotateMasterKey report: %+v", rot)
	}

	verifyTx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin verifyTx: %v", err)
	}
	defer func() { _ = verifyTx.Rollback(ctx) }()
	if _, err := dbquery.New(verifyTx).SetTenant(ctx, tenantID); err != nil {
		t.Fatalf("set tenant verifyTx: %v", err)
	}

	var keyID string
	var nonce, ciphertext []byte
	if err := verifyTx.QueryRow(ctx, `SELECT api_key_id, api_key_nonce, api_key_ciphertext FROM model_provider WHERE id = $1`, modelProviderID).Scan(&keyID, &nonce, &ciphertext); err != nil {
		t.Fatalf("query rotated model_provider: %v", err)
	}
	plain, err := newVault.Open(ctx, tenantID, modelProviderID, secrets.Encrypted{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil || string(plain) != "sk-old-model-secret" {
		t.Fatalf("Open rotated model secret = %q, %v", string(plain), err)
	}
	if _, err := oldVault.Open(ctx, tenantID, modelProviderID, secrets.Encrypted{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext}); err == nil {
		t.Fatal("expected oldVault to fail opening rotated model secret")
	}

	if err := verifyTx.QueryRow(ctx, `SELECT client_secret_id, client_secret_nonce, client_secret_ciphertext FROM oidc_provider WHERE id = $1`, oidcProviderID).Scan(&keyID, &nonce, &ciphertext); err != nil {
		t.Fatalf("query rotated oidc_provider: %v", err)
	}
	oidcPlain, err := newVault.Open(ctx, tenantID, oidcProviderID, secrets.Encrypted{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil || string(oidcPlain) != "oidc-old-client-secret" {
		t.Fatalf("Open rotated oidc secret = %q, %v", string(oidcPlain), err)
	}
}
