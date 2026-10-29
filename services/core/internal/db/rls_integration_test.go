package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTenantIsolationPostgres(t *testing.T) {
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	appConfig, err := pgx.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(adminConfig.Database, "tuskrag_test_") || adminConfig.Database != appConfig.Database ||
		adminConfig.Host != appConfig.Host || adminConfig.Port != appConfig.Port ||
		(adminConfig.Host != "127.0.0.1" && adminConfig.Host != "localhost" && adminConfig.Host != "::1") {
		t.Fatal("integration test requires the same loopback tuskrag_test_ database for both roles")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	}()
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Close(ctx); err != nil {
			t.Errorf("close application connection: %v", err)
		}
	}()
	var unsafeRole bool
	if err := app.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&unsafeRole); err != nil || unsafeRole {
		t.Fatalf("application role must not bypass RLS: unsafe=%t err=%v", unsafeRole, err)
	}
	var vector, search bool
	if err := app.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'vector'), EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_search')`).Scan(&vector, &search); err != nil {
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	readyErr := Check(ctx, pool)
	if vector && search && readyErr != nil {
		t.Fatalf("valid database rejected: %v", readyErr)
	}
	if (!vector || !search) && readyErr == nil {
		t.Fatal("database without required extensions reported ready")
	}
	var superuser bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&superuser); err != nil || !superuser {
		t.Fatalf("test setup requires a PostgreSQL superuser: superuser=%t err=%v", superuser, err)
	}
	if vector && search {
		unsafeConfig, err := pgxpool.ParseConfig(appURL)
		if err != nil {
			t.Fatal(err)
		}
		unsafeConfig.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
		unsafePool, err := pgxpool.NewWithConfig(ctx, unsafeConfig)
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(ctx, unsafePool); err == nil || !strings.Contains(err.Error(), "custom plans: false") {
			t.Fatalf("generic plans must be rejected: %v", err)
		}
		unsafePool.Close()
		applicationPool, err := Open(ctx, appURL)
		if err != nil {
			t.Fatalf("application pool did not enable custom plans: %v", err)
		}
		applicationPool.Close()
		adminPool, err := pgxpool.New(ctx, adminURL)
		if err != nil {
			t.Fatal(err)
		}
		defer adminPool.Close()
		if err := Check(ctx, adminPool); err == nil || !strings.Contains(err.Error(), "non-superuser: false") {
			t.Fatalf("superuser connection must be rejected: %v", err)
		}
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "rls-test-" + hex.EncodeToString(nonce[:])
	var tenantA, tenantB, spaceB int64
	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, name+"-a").Scan(&tenantA); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, table := range []string{"job", "knowledge_base", "embedding_space", "model_provider"} {
			if _, err := admin.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", []int64{tenantA, tenantB}); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(ctx, `DELETE FROM tenant WHERE id = ANY($1)`, []int64{tenantA, tenantB}); err != nil {
			t.Errorf("clean tenants: %v", err)
		}
	}()
	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, name+"-b").Scan(&tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO job (tenant_id, kind, status, dedupe_key, payload) VALUES
		($1, 'ingest', 'queued', $3, '{}'), ($2, 'ingest', 'queued', $3, '{}'),
		($1, 'ingest', 'succeeded', $3, '{}'), ($1, 'reembed', 'queued', $3, '{}')`, tenantA, tenantB, name); err != nil {
		t.Fatalf("independent tenants, kinds, and completed jobs must not collide: %v", err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO job (tenant_id, kind, status, dedupe_key, payload) VALUES ($1, 'ingest', 'running', $2, '{}')`, tenantA, name)
	var duplicate *pgconn.PgError
	if !errors.As(err, &duplicate) || duplicate.Code != "23505" {
		t.Fatalf("active duplicate job must be rejected: %v", err)
	}
	var providerB int64
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality) VALUES ($1, 'test', 'http://localhost', 'internal') RETURNING id`, tenantB).Scan(&providerB); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, normalized, distance) VALUES ($1, $2, 'test', 64, true, 'cosine') RETURNING id`, tenantB, providerB).Scan(&spaceB); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `SELECT id FROM tenant`); err == nil {
		t.Fatal("query without tenant context succeeded")
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback transaction: %v", err)
		}
	}()
	if _, err := tx.Exec(ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(tenantA)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant WHERE id = ANY($1)`, []int64{tenantA, tenantB}).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("tenant visibility = %d, err = %v", visible, err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM job WHERE dedupe_key = $1`, name).Scan(&visible); err != nil || visible != 3 {
		t.Fatalf("tenant job visibility = %d, err = %v", visible, err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status) VALUES ($1, 'invalid', $2, 'deny', 'active')`, tenantA, spaceB)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-tenant embedding space must violate foreign key: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `SELECT id FROM tenant`); err == nil {
		t.Fatal("tenant context remained usable after transaction rollback")
	}
}
