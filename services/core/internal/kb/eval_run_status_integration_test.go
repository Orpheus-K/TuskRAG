package kb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/retrieval"
)

func TestEvalRunStatusLifecycle(t *testing.T) {
	w := workerEvalFixture(t)
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	status, err := w.store.Status(w.f.ctx, actor, kbID, w.run.ID)
	if err != nil || status.Status != "queued" || status.Attempts != 0 || status.ResultAvailable || status.FailureCode != nil || status.FinishedAt != nil {
		t.Fatalf("queued: %+v %v", status, err)
	}
	if _, err := w.store.GetResult(w.f.ctx, actor, kbID, w.run.ID); !errors.Is(err, eval.ErrRunResultUnavailable) {
		t.Fatalf("pending: %v", err)
	}
	job, apply := w.prepare(t)
	status, err = w.store.Status(w.f.ctx, actor, kbID, w.run.ID)
	if err != nil || status.Status != "running" || status.Attempts != 1 || status.ResultAvailable {
		t.Fatalf("running: %+v %v", status, err)
	}
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); err != nil {
		t.Fatal(err)
	}
	status, err = w.store.Status(w.f.ctx, actor, kbID, w.run.ID)
	if err != nil || status.Status != "succeeded" || !status.ResultAvailable || status.FinishedAt == nil || status.JobID != job.Lease.JobID {
		t.Fatalf("completed: %+v %v", status, err)
	}
	if err := w.f.service.Archive(w.f.ctx, w.f.actor(w.f.owner), kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Status(w.f.ctx, actor, kbID, w.run.ID); err != nil {
		t.Fatalf("archive read: %v", err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end) SELECT tenant_id,kb_id,document_id,version_id,section_id,2,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end FROM chunk WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE chunk SET is_current=false,disabled=true WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Status(w.f.ctx, actor, kbID, w.run.ID); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("invalid evidence disclosed: %v", err)
	}
}

func TestEvalRunStatusFailedAndCancelled(t *testing.T) {
	for _, expected := range []string{"failed", "dead", "cancelled"} {
		t.Run(expected, func(t *testing.T) {
			w := workerEvalFixture(t)
			if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE job SET max_attempts=1 WHERE id=$1`, w.run.JobID); err != nil {
				t.Fatal(err)
			}
			job, err := w.queue.Claim(w.f.ctx, w.f.tenantA, "status", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if expected == "cancelled" {
				if err := w.queue.CancelWithEffects(w.f.ctx, w.f.tenantA, w.run.JobID, func(context.Context, jobs.EffectsTx, jobs.Kind) error { return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				code := jobs.Internal
				if expected == "dead" {
					code = jobs.ModelUnavailable
				}
				if _, err := w.queue.Fail(w.f.ctx, job.Lease, code); err != nil {
					t.Fatal(err)
				}
			}
			status, err := w.store.Status(w.f.ctx, w.request.Config.Actor, w.request.Config.KnowledgeBaseID, w.run.ID)
			if err != nil || status.Status != expected || status.ResultAvailable || status.Attempts != 1 || status.MaxAttempts != 1 || status.FinishedAt == nil {
				t.Fatalf("state %+v %v", status, err)
			}
			if expected != "cancelled" && status.FailureCode == nil {
				t.Fatal("failure code missing")
			}
			if _, err := w.store.GetResult(w.f.ctx, w.request.Config.Actor, w.request.Config.KnowledgeBaseID, w.run.ID); !errors.Is(err, eval.ErrRunResultUnavailable) {
				t.Fatalf("unsuccessful result: %v", err)
			}
		})
	}
}

func TestEvalRunStatusRevokedWhileReading(t *testing.T) {
	for _, resultRead := range []bool{false, true} {
		t.Run(fmt.Sprint(resultRead), func(t *testing.T) {
			w := workerEvalFixture(t)
			kbID := w.request.Config.KnowledgeBaseID
			if err := w.f.service.SetMember(w.f.ctx, w.f.actor(w.f.owner), kbID, w.f.other, Admin); err != nil {
				t.Fatal(err)
			}
			tx, err := w.f.admin.Begin(w.f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(w.f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, kbID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				a := retrieval.Actor{TenantID: w.f.tenantA, UserID: w.f.other}
				var err error
				if resultRead {
					_, err = w.store.GetResult(w.f.ctx, a, kbID, w.run.ID)
				} else {
					_, err = w.store.Status(w.f.ctx, a, kbID, w.run.ID)
				}
				done <- err
			}()
			waitSetListBlock(t, w.f, tx, done)
			if _, err := tx.Exec(w.f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, w.f.tenantA, kbID, w.f.other); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(w.f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, eval.ErrSetNotFound) {
				t.Fatalf("revoked read disclosed metadata: %v", err)
			}
		})
	}
}
