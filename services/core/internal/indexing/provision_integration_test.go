package indexing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
)

type indexFixture struct {
	ctx                           context.Context
	admin, app, migrator          *pgx.Conn
	adminURL, appURL, migratorURL string
	tenantA, tenantB, spaceA, kbA int64
	kbB, chunkA, vectorSpaceB     int64
}

// This test creates and drops only a freshly named database on a local test server.
func newIndexFixture(t *testing.T) *indexFixture {
	t.Helper()
	urls := []string{os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL"), os.Getenv("TUSKRAG_TEST_MIGRATOR_URL")}
	if urls[0] == "" && urls[1] == "" && urls[2] == "" {
		t.Skip("index integration test requires isolated PostgreSQL URLs")
	}
	parsed := make([]*url.URL, len(urls))
	for i, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u == nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
			(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") ||
			!strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tuskrag_test_") {
			t.Fatal("index integration test requires three local tuskrag_test_ database URLs")
		}
		parsed[i] = u
		if i > 0 && (u.Host != parsed[0].Host || u.Path != parsed[0].Path) {
			t.Fatal("index test URLs must address the same isolated database")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	root, err := pgx.Connect(ctx, urls[0])
	if err != nil {
		t.Fatalf("connect isolated test administrator: %v", err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "tuskrag_test_index_" + hex.EncodeToString(random[:])
	var exists bool
	if err := root.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil || exists {
		t.Fatalf("fresh test database check: exists=%t err=%v", exists, err)
	}
	owner := parsed[2].User.Username()
	if owner == "" {
		t.Fatal("test migration role is required")
	}
	if _, err := root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" OWNER "+pgx.Identifier{owner}.Sanitize()); err != nil {
		t.Fatalf("create isolated index test database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop owned index test database: %v", err)
		}
	})
	for _, u := range parsed {
		u.Path = "/" + name
	}
	f := &indexFixture{ctx: ctx, adminURL: parsed[0].String(), appURL: parsed[1].String(), migratorURL: parsed[2].String()}
	f.admin, err = pgx.Connect(ctx, f.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.admin.Close(context.Background()) })
	for _, statement := range []string{"CREATE EXTENSION vector", "CREATE EXTENSION pg_search", "ALTER SCHEMA public OWNER TO " + pgx.Identifier{owner}.Sanitize(), "REVOKE CREATE ON SCHEMA public FROM PUBLIC"} {
		if _, err := f.admin.Exec(ctx, statement); err != nil {
			t.Fatalf("initialize isolated index test database: %v", err)
		}
	}
	if err := db.Migrate(ctx, f.migratorURL); err != nil {
		t.Fatalf("migrate isolated index test database: %v", err)
	}
	f.app, err = pgx.Connect(ctx, f.appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.app.Close(context.Background()) })
	f.migrator, err = pgx.Connect(ctx, f.migratorURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.migrator.Close(context.Background()) })
	f.tenantA, f.spaceA, f.kbA, f.chunkA = f.seed(t, "a", false)
	f.tenantB, f.vectorSpaceB, f.kbB, _ = f.seed(t, "b", true)
	return f
}

func (f *indexFixture) seed(t *testing.T, suffix string, current bool) (int64, int64, int64, int64) {
	t.Helper()
	var tenant, user, provider, space, kb, document, version, section, chunk int64
	steps := []struct {
		sql  string
		args []any
		out  *int64
	}{
		{`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, []any{"index-" + suffix}, &tenant},
	}
	for _, step := range steps {
		if err := f.admin.QueryRow(f.ctx, step.sql, step.args...).Scan(step.out); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(out *int64, sql string, args ...any) {
		t.Helper()
		if err := f.admin.QueryRow(f.ctx, sql, args...).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	insert(&user, `INSERT INTO app_user (tenant_id,display_name) VALUES ($1,'test') RETURNING id`, tenant)
	insert(&provider, `INSERT INTO model_provider (tenant_id,name,base_url,locality) VALUES ($1,'test','http://localhost','internal') RETURNING id`, tenant)
	insert(&space, `INSERT INTO embedding_space (tenant_id,provider_id,model,dims,normalized,distance) VALUES ($1,$2,'test',64,true,'cosine') RETURNING id`, tenant, provider)
	insert(&kb, `INSERT INTO knowledge_base (tenant_id,name,embedding_space_id,egress_policy,status) VALUES ($1,'test',$2,'deny','active') RETURNING id`, tenant, space)
	insert(&document, `INSERT INTO document (tenant_id,kb_id,title,mime) VALUES ($1,$2,'test','text/plain') RETURNING id`, tenant, kb)
	insert(&version, `INSERT INTO document_version (tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by)
		VALUES ($1,$2,$3,decode(repeat('00',32),'hex'),1,'test','indexing',$4) RETURNING id`, tenant, kb, document, user)
	insert(&section, `INSERT INTO section (tenant_id,kb_id,document_id,version_id,ordinal,level,heading,heading_path,body_md,token_count)
		VALUES ($1,$2,$3,$4,1,0,'','{}','差旅住宿标准',1) RETURNING id`, tenant, kb, document, version)
	insert(&chunk, `INSERT INTO chunk (tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at)
		VALUES ($1,$2,$3,$4,$5,1,'text','差旅住宿标准','差旅住宿标准',1,$6,ARRAY['kb:' || $2::bigint::text || ':all'],'text/plain',now()) RETURNING id`, tenant, kb, document, version, section, current)
	vector := "[1" + strings.Repeat(",0", 63) + "]"
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector (tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding)
		VALUES ($1,$2,$3,$4,$5,ARRAY['kb:' || $2::bigint::text || ':all'],$6::halfvec)`, tenant, kb, chunk, space, current, vector); err != nil {
		t.Fatal(err)
	}
	return tenant, space, kb, chunk
}

func TestProvisionIndexesOnIsolatedPostgres(t *testing.T) {
	f := newIndexFixture(t)
	if err := Provision(f.ctx, f.adminURL, f.tenantA, f.spaceA); !errors.Is(err, ErrProvisioner) {
		t.Fatalf("superuser accepted: %v", err)
	}
	if err := Provision(f.ctx, f.appURL, f.tenantA, f.spaceA); !errors.Is(err, ErrProvisioner) {
		t.Fatalf("application role accepted: %v", err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantB, f.spaceA); !errors.Is(err, ErrSpaceMissing) {
		t.Fatalf("foreign space accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET distance='ip' WHERE tenant_id=$1 AND id=$2`, f.tenantB, f.vectorSpaceB); err != nil {
		t.Fatal(err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantB, f.vectorSpaceB); !errors.Is(err, ErrUnsupportedDistance) {
		t.Fatalf("unsupported distance accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET distance='cosine' WHERE tenant_id=$1 AND id=$2`, f.tenantB, f.vectorSpaceB); err != nil {
		t.Fatal(err)
	}
	if err := f.check(f.tenantA, f.spaceA, 64); !errors.Is(err, ErrIndexMissing) {
		t.Fatalf("missing index accepted: %v", err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		group.Go(func() { errs <- Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA) })
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent index provisioning: %v", err)
		}
	}
	if err := f.check(f.tenantA, f.spaceA, 64); err != nil {
		t.Fatalf("check provisioned indexes: %v", err)
	}
	f.testProvisioningLock(t)
	if err := f.check(f.tenantA, f.spaceA, 65); !errors.Is(err, ErrIndexConflict) {
		t.Fatalf("wrong dimensions accepted: %v", err)
	}
	f.assertLexical(t, f.tenantA, f.kbA, 0)
	f.assertLexical(t, f.tenantB, f.kbB, 1)
	f.assertLexical(t, f.tenantB, f.kbA, 0)
	f.assertVector(t, f.tenantA, f.kbA, f.spaceA, 0)
	f.switchVisibility(t)
	f.assertLexical(t, f.tenantA, f.kbA, 1)
	f.assertLexical(t, f.tenantB, f.kbA, 0)
	f.assertVector(t, f.tenantA, f.kbA, f.spaceA, 1)
	f.assertVector(t, f.tenantB, f.kbA, f.spaceA, 0)
	f.probeGenericPlan(t)
	f.assertApplicationPoolIsolation(t)
	f.testConflict(t)
	f.testInterruptedBuild(t)
}

func (f *indexFixture) testProvisioningLock(t *testing.T) {
	t.Helper()
	var locked bool
	if err := f.migrator.QueryRow(f.ctx, `SELECT pg_try_advisory_lock(841695642190183::bigint)`).Scan(&locked); err != nil || !locked {
		t.Fatalf("acquire test provision lock: locked=%t err=%v", locked, err)
	}
	if err := f.check(f.tenantA, f.spaceA, 64); !errors.Is(err, ErrIndexBusy) {
		t.Fatalf("index in use while provisioning: %v", err)
	}
	if err := f.migrator.QueryRow(f.ctx, `SELECT pg_advisory_unlock(841695642190183::bigint)`).Scan(&locked); err != nil || !locked {
		t.Fatalf("release test provision lock: unlocked=%t err=%v", locked, err)
	}
	tx, err := f.app.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := Check(f.ctx, tx, f.spaceA, 64, "cosine"); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(f.ctx, 250*time.Millisecond)
	defer cancel()
	if err := Provision(deadline, f.migratorURL, f.tenantA, f.spaceA); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provisioning did not wait for active index user: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA); err != nil {
		t.Fatalf("provisioning did not resume after index use: %v", err)
	}
}

// The control uses the unsafe mode explicitly so the protected pool is tested
// against the same prepared-query and tenant-switch sequence.
func (f *indexFixture) probeGenericPlan(t *testing.T) {
	t.Helper()
	conn, err := pgx.Connect(f.ctx, f.appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(f.ctx, `SET plan_cache_mode=force_generic_plan`); err != nil {
		t.Fatal(err)
	}
	foreign := 0
	for i := range 6 {
		tenant := f.tenantA
		if i%2 == 1 {
			tenant = f.tenantB
		}
		tx, err := conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SET LOCAL enable_seqscan=off`); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(f.ctx, `SELECT id,tenant_id,kb_id FROM chunk WHERE kb_id=$1 AND is_current AND search_text ||| '住宿'`, f.kbA)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, rowTenant, rowKB int64
			if err := rows.Scan(&id, &rowTenant, &rowKB); err != nil {
				t.Fatal(err)
			}
			if rowTenant != tenant || rowKB != f.kbA {
				foreign++
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	var genericPlans int64
	if err := conn.QueryRow(f.ctx, `SELECT COALESCE(sum(generic_plans),0)::bigint FROM pg_prepared_statements
		WHERE statement LIKE 'SELECT id,tenant_id,kb_id FROM chunk WHERE kb_id=%'`).Scan(&genericPlans); err != nil {
		t.Fatal(err)
	}
	t.Logf("unsafe generic-plan control: generic executions=%d, foreign rows=%d", genericPlans, foreign)
}

func (f *indexFixture) assertApplicationPoolIsolation(t *testing.T) {
	t.Helper()
	pool, err := db.Open(f.ctx, f.appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Reuse one physical connection and one prepared query beyond PostgreSQL's
	// generic-plan threshold while alternating tenant contexts.
	for i := range 12 {
		tenant, want := f.tenantA, 1
		if i%2 == 1 {
			tenant, want = f.tenantB, 0
		}
		tx, err := conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SET LOCAL enable_seqscan=off`); err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := tx.QueryRow(f.ctx, `SELECT current_setting('plan_cache_mode')`).Scan(&mode); err != nil || mode != "force_custom_plan" {
			t.Fatalf("unsafe application plan mode: %q err=%v", mode, err)
		}
		rows, err := tx.Query(f.ctx, `SELECT id,tenant_id,kb_id FROM chunk WHERE kb_id=$1 AND is_current AND search_text ||| '住宿'`, f.kbA)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for rows.Next() {
			var id, rowTenant, rowKB int64
			if err := rows.Scan(&id, &rowTenant, &rowKB); err != nil {
				t.Fatal(err)
			}
			if rowTenant != tenant || rowKB != f.kbA {
				t.Fatalf("application pool returned a foreign row: %d/%d/%d", id, rowTenant, rowKB)
			}
			got++
		}
		if err := rows.Err(); err != nil || got != want {
			t.Fatalf("application pool isolation iteration=%d got=%d want=%d err=%v", i, got, want, err)
		}
		if err := tx.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *indexFixture) check(tenant, space int64, dims int32) error {
	tx, err := f.app.Begin(f.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SET LOCAL search_path = pg_catalog, public`); err != nil {
		return err
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
		return err
	}
	return Check(f.ctx, tx, space, dims, "cosine")
}

func (f *indexFixture) assertLexical(t *testing.T, tenant, kb int64, want int) {
	t.Helper()
	tx, err := f.app.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"SET LOCAL enable_seqscan=off", "SET LOCAL plan_cache_mode=force_custom_plan"} {
		if _, err := tx.Exec(f.ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(f.ctx, `SELECT id,tenant_id,kb_id FROM chunk WHERE kb_id=$1 AND is_current AND search_text ||| '住宿'`, kb)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, rowTenant, rowKB int64
		if err := rows.Scan(&id, &rowTenant, &rowKB); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d/%d/%d", id, rowTenant, rowKB))
		if rowTenant != tenant || rowKB != kb {
			t.Fatalf("lexical result crosses tenant or knowledge base: %d/%d/%d", id, rowTenant, rowKB)
		}
	}
	if err := rows.Err(); err != nil || len(got) != want {
		var tenantSetting string
		var ordinary int
		_ = tx.QueryRow(f.ctx, `SELECT current_setting('tuskrag.tenant_id')`).Scan(&tenantSetting)
		_ = tx.QueryRow(f.ctx, `SELECT count(*) FROM chunk WHERE kb_id=$1 AND is_current`, kb).Scan(&ordinary)
		planRows, planErr := tx.Query(f.ctx, `EXPLAIN (COSTS OFF) SELECT id,tenant_id,kb_id FROM chunk WHERE kb_id=$1 AND is_current AND search_text ||| '住宿'`, kb)
		var plan []string
		if planErr == nil {
			for planRows.Next() {
				var line string
				if planRows.Scan(&line) == nil {
					plan = append(plan, line)
				}
			}
		}
		t.Fatalf("lexical visibility tenant=%d setting=%s kb=%d got=%v want=%d ordinary=%d plan=%v err=%v", tenant, tenantSetting, kb, got, want, ordinary, plan, err)
	}
}

func (f *indexFixture) assertVector(t *testing.T, tenant, kb, space int64, want int) {
	t.Helper()
	tx, err := f.app.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"SET LOCAL enable_seqscan=off", "SET LOCAL enable_sort=off", "SET LOCAL plan_cache_mode=force_custom_plan"} {
		if _, err := tx.Exec(f.ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	vector := "[1" + strings.Repeat(",0", 63) + "]"
	sql := `SELECT chunk_id FROM chunk_vector WHERE kb_id=$1 AND space_id=$2 AND is_current
		ORDER BY (embedding::halfvec(64)) <=> $3::halfvec(64) LIMIT 1`
	rows, err := tx.Query(f.ctx, "EXPLAIN (COSTS OFF) "+sql, kb, space, vector)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), fmt.Sprintf("Index Scan using tuskrag_chunk_vector_hnsw_s%d", space)) {
		t.Fatalf("HNSW plan missing: %s", plan.String())
	}
	rows, err = tx.Query(f.ctx, sql, kb, space, vector)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for rows.Next() {
		got++
	}
	if err := rows.Err(); err != nil || got != want {
		t.Fatalf("vector visibility tenant=%d kb=%d got=%d want=%d err=%v", tenant, kb, got, want, err)
	}
}

func (f *indexFixture) switchVisibility(t *testing.T) {
	t.Helper()
	writer, err := pgx.Connect(f.ctx, f.appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close(context.Background()) }()
	for _, commit := range []bool{false, true} {
		tx, err := writer.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantA)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `UPDATE chunk SET is_current=true WHERE id=$1`, f.chunkA); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `UPDATE chunk_vector SET is_current=true WHERE chunk_id=$1`, f.chunkA); err != nil {
			t.Fatal(err)
		}
		f.assertLexical(t, f.tenantA, f.kbA, 0)
		f.assertVector(t, f.tenantA, f.kbA, f.spaceA, 0)
		if commit {
			err = tx.Commit(f.ctx)
		} else {
			err = tx.Rollback(f.ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func (f *indexFixture) testConflict(t *testing.T) {
	t.Helper()
	for _, sql := range []string{
		"DROP INDEX CONCURRENTLY " + lexicalIndexName,
		"CREATE INDEX CONCURRENTLY " + lexicalIndexName + " ON public.chunk (id)",
	} {
		if _, err := f.migrator.Exec(f.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA); !errors.Is(err, ErrIndexConflict) {
		t.Fatalf("conflicting index accepted: %v", err)
	}
	var exists bool
	if err := f.admin.QueryRow(f.ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+lexicalIndexName).Scan(&exists); err != nil || !exists {
		t.Fatalf("conflicting index was removed: exists=%t err=%v", exists, err)
	}
	if _, err := f.migrator.Exec(f.ctx, "DROP INDEX CONCURRENTLY "+lexicalIndexName); err != nil {
		t.Fatal(err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA); err != nil {
		t.Fatalf("restore lexical index: %v", err)
	}
}

func (f *indexFixture) testInterruptedBuild(t *testing.T) {
	t.Helper()
	name := fmt.Sprintf("tuskrag_chunk_vector_hnsw_s%d", f.spaceA)
	if _, err := f.migrator.Exec(f.ctx, "DROP INDEX CONCURRENTLY "+name); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `UPDATE chunk_vector SET access_tags=access_tags WHERE chunk_id=$1`, f.chunkA); err != nil {
		t.Fatal(err)
	}
	buildCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Provision(buildCtx, f.migratorURL, f.tenantA, f.spaceA) }()
	deadline := time.Now().Add(20 * time.Second)
	created := false
	for time.Now().Before(deadline) {
		if err := f.migrator.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_index i ON i.indexrelid=c.oid WHERE c.relname=$1 AND NOT i.indisvalid)`, name).Scan(&created); err != nil {
			t.Fatal(err)
		}
		if created {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !created {
		cancel()
		_ = tx.Rollback(f.ctx)
		if err := <-result; err == nil {
			t.Log("index build completed before cancellation")
		}
		t.Fatal("build did not expose an invalid index before cancellation")
	}
	var backend int32
	if err := f.migrator.QueryRow(f.ctx, `SELECT pid FROM pg_stat_activity
		WHERE datname=current_database() AND usename=current_user AND state='active'
		AND query LIKE $1 AND pid <> pg_backend_pid()`, "CREATE INDEX CONCURRENTLY "+name+" %").Scan(&backend); err != nil {
		t.Fatalf("find owned index build: %v", err)
	}
	var signalled bool
	if err := f.migrator.QueryRow(f.ctx, `SELECT pg_cancel_backend($1)`, backend).Scan(&signalled); err != nil || !signalled {
		t.Fatalf("cancel owned index build: signalled=%t err=%v", signalled, err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("interrupted build unexpectedly succeeded")
		}
	case <-time.After(10 * time.Second):
		cancel()
		_ = tx.Rollback(f.ctx)
		if err := <-result; err == nil {
			t.Log("index build completed before cancellation")
		}
		t.Fatal("cancelled build did not stop")
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("invalid index accepted or removed: %v", err)
	}
	if _, err := f.migrator.Exec(f.ctx, "DROP INDEX CONCURRENTLY "+name); err != nil {
		t.Fatal(err)
	}
	if err := Provision(f.ctx, f.migratorURL, f.tenantA, f.spaceA); err != nil {
		t.Fatalf("restore interrupted index: %v", err)
	}
}
