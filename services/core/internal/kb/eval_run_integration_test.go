package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/retrieval"
)

func runFixture(t *testing.T) (*fixture, *eval.RunStore, eval.RunRequest) {
	t.Helper()
	f, base, sets, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	set, err := sets.Create(f.ctx, actor, base.ID, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	store, err := eval.NewRunStore(f.pool, queue)
	if err != nil {
		t.Fatal(err)
	}
	search, err := retrieval.NewService(f.pool, nil, func(context.Context, string) (int, error) { return 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := search.Snapshot(f.ctx, actor, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM eval_run WHERE tenant_id=$1`, f.tenantA); err != nil {
			t.Error(err)
		}
	})
	return f, store, eval.RunRequest{Set: eval.SetBinding{SetID: set.ID, Revision: set.Revision}, Mode: "rrf", Config: eval.RunConfig{Actor: actor, KnowledgeBaseID: base.ID, AllowedDocumentIDs: []int64{definition.Cases[0].Expected[0].DocumentID}, ModelKind: "stub", ModelLabel: "test", Configuration: &snapshot}}
}

func TestEvalRunAtomicIdempotencyAndImmutableSnapshot(t *testing.T) {
	f, store, request := runFixture(t)
	actor, kbID := request.Config.Actor, request.Config.KnowledgeBaseID
	type outcome struct {
		run eval.StoredRun
		err error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() { run, err := store.Create(f.ctx, actor, kbID, "same", request); results <- outcome{run, err} }()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.run.ID <= 0 || a.run.ID != b.run.ID || a.run.JobID != b.run.JobID || a.run.CreatedAt != b.run.CreatedAt {
		t.Fatalf("duplicate run: %+v %+v", a, b)
	}
	var payload []byte
	var kind, status string
	if err := f.admin.QueryRow(f.ctx, `SELECT kind,status,payload FROM job WHERE id=$1`, a.run.JobID).Scan(&kind, &status, &payload); err != nil {
		t.Fatal(err)
	}
	var binding eval.RunJobPayload
	if err := json.Unmarshal(payload, &binding); err != nil || binding.RunID != a.run.ID || kind != "eval_run" || status != "queued" || strings.Contains(string(payload), "question") || len(payload) > 64 {
		t.Fatalf("invalid queued reference: %s %s %s %v", kind, status, payload, err)
	}
	for _, table := range []string{"eval_run", "job"} {
		var count int
		if err := f.admin.QueryRow(f.ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", f.tenantA).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count %d %v", table, count, err)
		}
	}
	var audits int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='eval.run.create'`, f.tenantA).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits %d %v", audits, err)
	}
	changed := request
	changed.Mode = "vector"
	if _, err := store.Create(f.ctx, actor, kbID, "same", changed); !errors.Is(err, eval.ErrRunIdempotencyConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	sets, err := eval.NewSetStore(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := sets.Get(f.ctx, actor, kbID, request.Set.SetID, 1)
	if err != nil {
		t.Fatal(err)
	}
	definition.Definition.Name = "next revision"
	if _, err := sets.Update(f.ctx, actor, kbID, request.Set.SetID, 1, definition.Definition); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET status='failed',finished_at=now() WHERE id=$1`, a.run.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PutConfig(f.ctx, f.actor(f.owner), kbID, 0, RetrievalConfig{TopK: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(f.ctx, actor, kbID, "new-stale", request); !errors.Is(err, retrieval.ErrConfigurationChanged) {
		t.Fatalf("stale new run: %v", err)
	}
	retry, err := store.Create(f.ctx, actor, kbID, "same", request)
	if err != nil || retry.ID != a.run.ID || retry.Request.Set.Revision != 1 || retry.Request.Config.Configuration.RetrievalRevision != 0 {
		t.Fatalf("retry replaced original snapshot: %+v %v", retry, err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id=$1`, retry.JobID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("terminal retry requeued: %s %v", status, err)
	}
	read, err := store.Get(f.ctx, actor, kbID, a.run.ID)
	if err != nil || read.ConfigDigest != a.run.ConfigDigest {
		t.Fatalf("read original: %+v %v", read, err)
	}
	for _, statement := range []string{"UPDATE eval_run SET mode='vector'", "DELETE FROM eval_run"} {
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantA)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, statement); err == nil {
			t.Fatal("app can mutate run snapshot")
		}
		if err := tx.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM eval_run`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS %d %v", count, err)
	}
}

func TestEvalRunScopeAndArchive(t *testing.T) {
	f, store, request := runFixture(t)
	actor, kbID := request.Config.Actor, request.Config.KnowledgeBaseID
	saved, err := store.Create(f.ctx, actor, kbID, "original", request)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{Viewer, Editor} {
		if err := f.service.SetMember(f.ctx, f.actor(f.owner), kbID, f.other, role); err != nil {
			t.Fatal(err)
		}
		other := request
		other.Config.Actor.UserID = f.other
		if _, err := store.Create(f.ctx, other.Config.Actor, kbID, "forbidden", other); !errors.Is(err, eval.ErrSetForbidden) {
			t.Fatalf("role %s create: %v", role, err)
		}
		if _, err := store.Get(f.ctx, other.Config.Actor, kbID, saved.ID); !errors.Is(err, eval.ErrSetForbidden) {
			t.Fatalf("role %s read: %v", role, err)
		}
	}
	foreign := request
	foreign.Config.Actor = retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}
	if _, err := store.Create(f.ctx, foreign.Config.Actor, kbID, "foreign", foreign); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("foreign create: %v", err)
	}
	if _, err := store.Get(f.ctx, foreign.Config.Actor, kbID, saved.ID); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	invalid := request
	invalid.Config.AllowedDocumentIDs = nil
	if _, err := store.Create(f.ctx, actor, kbID, "missing-target", invalid); !errors.Is(err, eval.ErrInvalidCase) {
		t.Fatalf("missing expected target scope: %v", err)
	}
	invalid.Config.AllowedDocumentIDs = []int64{request.Config.AllowedDocumentIDs[0], 999999999}
	if _, err := store.Create(f.ctx, actor, kbID, "hidden-scope", invalid); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("hidden scope: %v", err)
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(f.ctx, actor, kbID, saved.ID); err != nil {
		t.Fatalf("archived read: %v", err)
	}
	if _, err := store.Create(f.ctx, actor, kbID, "original", request); !errors.Is(err, eval.ErrSetArchived) {
		t.Fatalf("archived retry: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(f.ctx, actor, kbID, saved.ID); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("revoked target read: %v", err)
	}
}

func TestEvalRunAuditRollsBackJobAndReservation(t *testing.T) {
	f, store, request := runFixture(t)
	name := fmt.Sprintf("run_audit_failure_%d", f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='eval.run.create' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)
	if _, err := f.admin.Exec(f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.Create(f.ctx, request.Config.Actor, request.Config.KnowledgeBaseID, "rollback", request); err == nil {
		t.Fatal("audit failure accepted")
	}
	if _, err := store.Start(f.ctx, request.Config.Actor, request.Config.KnowledgeBaseID, "rollback-start", eval.StartRequest{SetID: request.Set.SetID, Revision: request.Set.Revision, Mode: request.Mode}); err == nil {
		t.Fatal("start audit failure accepted")
	}
	for _, table := range []string{"eval_run", "job"} {
		var count int
		if err := f.admin.QueryRow(f.ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", f.tenantA).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s survived rollback: %d %v", table, count, err)
		}
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM idempotency_record WHERE tenant_id=$1 AND operation LIKE 'eval.run.%'`, f.tenantA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reservation survived rollback: %d %v", count, err)
	}
}

func TestEvalRunRevokedWhileCreating(t *testing.T) {
	f, store, request := runFixture(t)
	kbID := request.Config.KnowledgeBaseID
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), kbID, f.other, Admin); err != nil {
		t.Fatal(err)
	}
	request.Config.Actor.UserID = f.other
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, kbID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := store.Create(f.ctx, request.Config.Actor, kbID, "revoked", request); done <- err }()
	waitSetListBlock(t, f, tx, done)
	if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, kbID, f.other); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("revocation ignored: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked job queued: %d %v", count, err)
	}
}
