package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/retrieval"
)

func TestRebuildCompletionAtomicSwitch(t *testing.T) {
	publicationDatabase(t)
	f, job, target, stage, _, plan := prepareRebuildEmbedding(t)
	effects, err := stage.Completion(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.queue.WithLease(f.ctx, job.Lease, effects); !errors.Is(err, jobs.ErrCompletionRequired) {
		t.Fatalf("checkpoint switched %v", err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, indexing.ErrIndexMissing) {
		t.Fatalf("missing index %v", err)
	}
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, target); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, ErrVectorsIncomplete) {
		t.Fatalf("incomplete coverage %v", err)
	}
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, plan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	driver, err := retrieval.NewDriver(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	beforeBinding, err := driver.Binding(f.ctx, actor, f.kbID)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	// A newly visible current chunk after traversal must prevent the switch.
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, f.kbID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.queue.Complete(f.ctx, job.Lease, effects) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := tx.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, f.admin.PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("completion did not wait: %v", err)
		case <-tick.C:
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	var extra int64
	if err := tx.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at) SELECT tenant_id,kb_id,document_id,version_id,section_id,99,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at FROM chunk WHERE tenant_id=$1 ORDER BY id LIMIT 1 RETURNING id`, f.tenant).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVectorsIncomplete) {
		t.Fatalf("new chunk uncovered %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM chunk WHERE id=$1`, extra); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("rebuild_switch_%d", f.tenant)
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='kb.rebuild.complete' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenant, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_log; DROP FUNCTION IF EXISTS %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if err := f.queue.Complete(f.ctx, job.Lease, effects); err == nil {
		t.Fatal("audit failure accepted")
	}
	var old bool
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id=$1 AND k.pending_space_id=$2 AND k.config_revision=$3 AND j.status='running' FROM knowledge_base k JOIN job j ON j.id=$4 WHERE k.id=$5`, plan.EmbeddingSpaceID, target, plan.ConfigRevision, job.Lease.JobID, f.kbID).Scan(&old); err != nil || !old {
		t.Fatalf("partial switch %t %v", old, err)
	}
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
		t.Fatal(err)
	}
	before, err := driver.Recall(f.ctx, actor, f.kbID, beforeBinding, "申请", testVector(1, 0), retrieval.Filters{}, 8)
	if err != nil || len(before) != 2 {
		t.Fatalf("before recall %d %v", len(before), err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); err != nil {
		t.Fatal(err)
	}
	var switched bool
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id=$1 AND k.pending_space_id IS NULL AND k.config_revision=$2 AND j.status='succeeded' AND j.finished_at IS NOT NULL FROM knowledge_base k JOIN job j ON j.id=$3 WHERE k.id=$4`, target, plan.ConfigRevision+1, job.Lease.JobID, f.kbID).Scan(&switched); err != nil || !switched {
		t.Fatalf("switch %t %v", switched, err)
	}
	afterBinding, err := driver.Binding(f.ctx, actor, f.kbID)
	if err != nil || afterBinding.SpaceID != target || afterBinding.Revision != beforeBinding.Revision+1 {
		t.Fatalf("new read binding %+v %v", afterBinding, err)
	}
	after, err := driver.Recall(f.ctx, actor, f.kbID, afterBinding, "申请", testVector(1, 0), retrieval.Filters{}, 8)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("recall changed before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err := driver.Recall(f.ctx, actor, f.kbID, beforeBinding, "申请", testVector(1, 0), retrieval.Filters{}, 8); !errors.Is(err, retrieval.ErrBindingChanged) {
		t.Fatalf("stale retrieval binding %v", err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late complete %v", err)
	}
}
