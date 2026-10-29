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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type indexFixture struct {
	tenant, kb, document, version, space, section, chunk int64
}

func TestIndexSchemaPostgres(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(ctx); err != nil {
			t.Errorf("close application connection: %v", err)
		}
	})
	var superuser, unsafeRole bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&superuser); err != nil || !superuser {
		t.Fatalf("test setup requires a PostgreSQL superuser: superuser=%t err=%v", superuser, err)
	}
	if err := app.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&unsafeRole); err != nil || unsafeRole {
		t.Fatalf("application role must not bypass RLS: unsafe=%t err=%v", unsafeRole, err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "index-test-" + hex.EncodeToString(nonce[:])
	var tenants []int64
	t.Cleanup(func() {
		for _, table := range []string{"chunk_vector", "chunk", "ingest_plan", "section", "document_version", "document", "knowledge_base", "embedding_space", "model_provider", "app_user"} {
			if _, err := admin.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", tenants); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(ctx, `DELETE FROM tenant WHERE id = ANY($1)`, tenants); err != nil {
			t.Errorf("clean tenants: %v", err)
		}
	})
	createFixture := func(suffix string) indexFixture {
		t.Helper()
		var f indexFixture
		if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, name+suffix).Scan(&f.tenant); err != nil {
			t.Fatal(err)
		}
		tenants = append(tenants, f.tenant)
		var user, provider int64
		if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'test') RETURNING id`, f.tenant).Scan(&user); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality) VALUES ($1, 'test', 'http://localhost', 'internal') RETURNING id`, f.tenant).Scan(&provider); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, normalized, distance) VALUES ($1, $2, 'test', 64, true, 'cosine') RETURNING id`, f.tenant, provider).Scan(&f.space); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status) VALUES ($1, 'test', $2, 'deny', 'active') RETURNING id`, f.tenant, f.space).Scan(&f.kb); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime) VALUES ($1, $2, 'test', 'text/plain') RETURNING id`, f.tenant, f.kb).Scan(&f.document); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, $4, 1, $5, 'indexing', $6) RETURNING id`, f.tenant, f.kb, f.document, make([]byte, 32), name+suffix, user).Scan(&f.version); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 0, '', '{}', 'body', 1) RETURNING id`, f.tenant, f.kb, f.document, f.version).Scan(&f.section); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, token_count, access_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', 'body', 'body', 1, ARRAY['kb:test'], 'text/plain', now()) RETURNING id`, f.tenant, f.kb, f.document, f.version, f.section).Scan(&f.chunk); err != nil {
			t.Fatal(err)
		}
		return f
	}
	a, b := createFixture("-a"), createFixture("-b")
	vector := "[" + strings.Repeat("1,", 63) + "1]"
	for _, f := range []indexFixture{a, b} {
		if _, err := admin.Exec(ctx, `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, access_tags, embedding) VALUES ($1, $2, $3, $4, ARRAY['kb:test'], $5::halfvec)`, f.tenant, f.kb, f.chunk, f.space, vector); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO ingest_plan (tenant_id, kb_id, document_id, version_id, embedding_space_id, config_revision, plan_sha256, section_count, chunk_count) VALUES ($1, $2, $3, $4, $5, 1, $6, 1, 1)`, f.tenant, f.kb, f.document, f.version, f.space, make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	var extraChunk int64
	if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, token_count, access_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 2, 'text', 'body', 'body', 1, ARRAY['kb:test'], 'text/plain', now()) RETURNING id`, a.tenant, a.kb, a.document, a.version, a.section).Scan(&extraChunk); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"section", "chunk", "chunk_vector", "ingest_plan"} {
		if _, err := app.Exec(ctx, "SELECT count(*) FROM "+table); err == nil {
			t.Fatalf("%s query without tenant context succeeded", table)
		}
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(a.tenant)); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		table string
		want  int
	}{{"section", 1}, {"chunk", 2}, {"chunk_vector", 1}, {"ingest_plan", 1}} {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+check.table+" WHERE tenant_id = ANY($1)", tenants).Scan(&count); err != nil || count != check.want {
			t.Fatalf("%s visible rows = %d, want %d, err = %v", check.table, count, check.want, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, parent_section_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, $5, 2, 1, 'child', ARRAY['child'], 'body', 1)`, a.tenant, a.kb, a.document, a.version, a.section); err != nil {
		t.Fatalf("valid application insert: %v", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 3, 0, '', '{}', 'body', 1)`, b.tenant, b.kb, b.document, b.version)
	checkIndexPGCode(t, err, "42501")
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `SELECT count(*) FROM section`); err == nil {
		t.Fatal("tenant context remained usable after rollback")
	}
	var planUpdate bool
	if err := app.QueryRow(ctx, `SELECT has_table_privilege(current_user, 'ingest_plan', 'UPDATE')`).Scan(&planUpdate); err != nil || planUpdate {
		t.Fatalf("application can update immutable plan: allowed=%t err=%v", planUpdate, err)
	}
	_, err = admin.Exec(ctx, `UPDATE ingest_plan SET plan_sha256=$1 WHERE tenant_id=$2 AND version_id=$3`, []byte{1}, a.tenant, a.version)
	checkIndexPGCode(t, err, "23514")
	_, err = admin.Exec(ctx, `UPDATE ingest_plan SET embedding_space_id=$1 WHERE tenant_id=$2 AND version_id=$3`, b.space, a.tenant, a.version)
	checkIndexPGCode(t, err, "23503")
	sectionSQL := `INSERT INTO section (tenant_id, kb_id, document_id, version_id, parent_section_id, ordinal, level, heading, heading_path, page_start, page_end, body_md, token_count) VALUES ($1, $2, $3, $4, $5, 30, 1, 'child', $6::text[], $7, $8, 'body', 1)`
	chunkSQL := `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, token_count, is_current, disabled, access_tags, doc_mime, doc_updated_at, page_start, page_end) VALUES ($1, $2, $3, $4, $5, 30, 'text', 'body', 'body', $6, $7, $8, $9::text[], 'text/plain', now(), $10, $11)`
	vectorSQL := `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, access_tags, embedding) VALUES ($1, $2, $3, $4, $5::text[], $6::halfvec)`
	cases := []struct {
		name, query, code string
		args              []any
	}{
		{"foreign parent", sectionSQL, "23503", []any{a.tenant, a.kb, a.document, a.version, b.section, "{child}", nil, nil}},
		{"section partial page start", sectionSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, "{child}", 1, nil}},
		{"section partial page end", sectionSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, "{child}", nil, 1}},
		{"heading path lower bound", sectionSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, "[0:0]={child}", nil, nil}},
		{"foreign section", chunkSQL, "23503", []any{a.tenant, a.kb, a.document, a.version, b.section, 1, false, false, "{kb:test}", nil, nil}},
		{"chunk too long", chunkSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, 801, false, false, "{kb:test}", nil, nil}},
		{"disabled current chunk", chunkSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, 1, true, true, "{kb:test}", nil, nil}},
		{"empty chunk tags", chunkSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, 1, false, false, "{}", nil, nil}},
		{"chunk tags lower bound", chunkSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, 1, false, false, "[0:0]={kb:test}", nil, nil}},
		{"chunk partial pages", chunkSQL, "23514", []any{a.tenant, a.kb, a.document, a.version, a.section, 1, false, false, "{kb:test}", 1, nil}},
		{"foreign embedding space", vectorSQL, "23503", []any{a.tenant, a.kb, extraChunk, b.space, "{kb:test}", vector}},
		{"vector dimension mismatch", vectorSQL, "23503", []any{a.tenant, a.kb, extraChunk, a.space, "{kb:test}", "[" + strings.Repeat("1,", 62) + "1]"}},
		{"zero vector", vectorSQL, "23514", []any{a.tenant, a.kb, extraChunk, a.space, "{kb:test}", "[" + strings.Repeat("0,", 63) + "0]"}},
		{"empty vector tags", vectorSQL, "23514", []any{a.tenant, a.kb, extraChunk, a.space, "{}", vector}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := admin.Exec(ctx, tc.query, tc.args...)
			checkIndexPGCode(t, err, tc.code)
		})
	}
	if _, err := admin.Exec(ctx, `DELETE FROM chunk WHERE tenant_id = $1 AND id = $2`, a.tenant, a.chunk); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id = ANY($1)`, tenants).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("vector cascade should retain only tenant B: count=%d err=%v", remaining, err)
	}
}

func checkIndexPGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("PostgreSQL error code = %v, want %s", err, code)
	}
}
