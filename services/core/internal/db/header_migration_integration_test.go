package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestHeaderConfirmationUpgradePreservesExistingVersions(t *testing.T) {
	names := []string{"TUSKRAG_TEST_ADMIN_URL", "TUSKRAG_TEST_APP_URL", "TUSKRAG_TEST_MIGRATOR_URL"}
	urls := make([]*url.URL, len(names))
	for i, name := range names {
		if os.Getenv(name) == "" {
			t.Skip("upgrade test requires isolated PostgreSQL URLs")
		}
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u == nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
			(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") ||
			!strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tuskrag_test_") {
			t.Fatal("upgrade test requires loopback tuskrag_test_ database URLs")
		}
		if i > 0 && (u.Host != urls[0].Host || u.Path != urls[0].Path) {
			t.Fatal("upgrade test URLs must address the same isolated database")
		}
		urls[i] = u
	}
	if urls[1].User.Username() != "tuskrag_app" || urls[2].User.Username() == "" {
		t.Fatal("upgrade test requires application and migration roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := pgx.Connect(ctx, urls[0].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "tuskrag_test_header_upgrade_" + hex.EncodeToString(nonce[:])
	owner := urls[2].User.Username()
	if _, err := root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" OWNER "+pgx.Identifier{owner}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop owned upgrade database: %v", err)
		}
	})
	for _, u := range urls {
		u.Path = "/" + name
	}
	admin, err := pgx.Connect(ctx, urls[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	for _, statement := range []string{"CREATE EXTENSION vector", "CREATE EXTENSION pg_search", "ALTER SCHEMA public OWNER TO " + pgx.Identifier{owner}.Sanitize(), "REVOKE CREATE ON SCHEMA public FROM PUBLIC"} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	migratorConfig, err := pgx.ParseConfig(urls[2].String())
	if err != nil {
		t.Fatal(err)
	}
	database := sql.OpenDB(stdlib.GetConnector(*migratorConfig))
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, database, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	var tenant, user, provider, space, base, document, version int64
	statements := []struct {
		query string
		args  func() []any
		id    *int64
	}{
		{`INSERT INTO tenant(name) VALUES ('header upgrade') RETURNING id`, func() []any { return nil }, &tenant},
		{`INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'owner') RETURNING id`, func() []any { return []any{tenant} }, &user},
		{`INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES ($1,'test','http://localhost','internal') RETURNING id`, func() []any { return []any{tenant} }, &provider},
		{`INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) VALUES ($1,$2,'test',64,true,'cosine') RETURNING id`, func() []any { return []any{tenant, provider} }, &space},
		{`INSERT INTO knowledge_base(tenant_id,name,embedding_space_id,egress_policy,status) VALUES ($1,'test',$2,'deny','active') RETURNING id`, func() []any { return []any{tenant, space} }, &base},
		{`INSERT INTO document(tenant_id,kb_id,title,mime) VALUES ($1,$2,'old.csv','text/csv') RETURNING id`, func() []any { return []any{tenant, base} }, &document},
		{`INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by) VALUES ($1,$2,$3,$4,1,'old-file','queued',$5) RETURNING id`,
			func() []any { return []any{tenant, base, document, make([]byte, 32), user} }, &version},
	}
	for _, statement := range statements {
		if err := admin.QueryRow(ctx, statement.query, statement.args()...).Scan(statement.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpContext(ctx, database, "migrations"); err != nil {
		t.Fatal(err)
	}
	var jobIndex bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='job_ingest_history')`).Scan(&jobIndex); err != nil || !jobIndex {
		t.Fatalf("ingest job history index present=%t err=%v", jobIndex, err)
	}
	var confirmed bool
	if err := admin.QueryRow(ctx, `SELECT confirm_first_row_headers FROM document_version WHERE tenant_id=$1 AND id=$2`, tenant, version).Scan(&confirmed); err != nil || confirmed {
		t.Fatalf("old version confirmation=%t err=%v", confirmed, err)
	}
	_, err = admin.Exec(ctx, `UPDATE document_version SET confirm_first_row_headers=true WHERE tenant_id=$1 AND id=$2`, tenant, version)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("old version confirmation was mutable: %v", err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by,confirm_first_row_headers)
		VALUES ($1,$2,$3,$4,1,'confirmed-file','queued',$5,true)`, tenant, base, document, make([]byte, 32), user)
	if err != nil {
		t.Fatalf("new header decision for existing bytes failed after upgrade: %v", err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by,confirm_first_row_headers)
		VALUES ($1,$2,$3,$4,1,'duplicate-file','queued',$5,true)`, tenant, base, document, make([]byte, 32), user)
	pgErr = nil
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate version decision accepted after upgrade: %v", err)
	}
	pool, err := Open(ctx, urls[1].String())
	if err != nil {
		t.Fatalf("upgraded database readiness: %v", err)
	}
	pool.Close()
}
