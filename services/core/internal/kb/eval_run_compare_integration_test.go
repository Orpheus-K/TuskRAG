package kb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func completeComparisonRun(t *testing.T, w evalWorkerFixture) {
	t.Helper()
	job, apply := w.prepare(t)
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); err != nil {
		t.Fatal(err)
	}
}
func TestEvalRunComparisonDifferenceAndPagination(t *testing.T) {
	w := workerEvalFixture(t)
	completeComparisonRun(t, w)
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	request := w.request
	request.Config.ModelLabel = "second configuration"
	current, err := w.store.Create(w.f.ctx, actor, kbID, "current", request)
	if err != nil {
		t.Fatal(err)
	}
	w.result = retrieval.Result{Trace: &retrieval.Trace{}}
	completeComparisonRun(t, w)
	result, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, current.ID, 0, 1)
	if err != nil || result.Source != "executed" || result.BaselineRunID != w.run.ID || result.CurrentRunID != current.ID || result.Comparison.Gate != "regressed" || result.Comparison.Delta.RecallAt20 != -1 || result.Comparison.TotalRows != 2 || len(result.Comparison.Rows) != 1 || result.Comparison.NextOffset == nil || *result.Comparison.NextOffset != 1 {
		t.Fatalf("comparison %+v %v", result, err)
	}
	reverse, err := w.store.Compare(w.f.ctx, actor, kbID, current.ID, w.run.ID, 1, 1)
	if err != nil || reverse.Comparison.Gate != "passed" || reverse.Comparison.Delta.RecallAt20 != 1 || len(reverse.Comparison.Rows) != 1 || reverse.Comparison.NextOffset != nil {
		t.Fatalf("reverse %+v %v", reverse, err)
	}
	if _, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, current.ID, 3, 1); !errors.Is(err, eval.ErrInvalidCase) {
		t.Fatalf("offset %v", err)
	}
	self, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, w.run.ID, 0, 20)
	if err != nil || self.Comparison.Delta.RecallAt20 != 0 || self.Comparison.Gate != "passed" {
		t.Fatalf("self %+v %v", self, err)
	}
	request.Mode = "lexical"
	other, err := w.store.Create(w.f.ctx, actor, kbID, "mode", request)
	if err != nil {
		t.Fatal(err)
	}
	completeComparisonRun(t, w)
	if _, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, other.ID, 0, 20); !errors.Is(err, eval.ErrReportsIncomparable) {
		t.Fatalf("mode %v", err)
	}
	if err := w.f.service.Archive(w.f.ctx, w.f.actor(w.f.owner), kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, current.ID, 0, 20); err != nil {
		t.Fatalf("archived %v", err)
	}
	if _, err := w.store.Compare(w.f.ctx, retrieval.Actor{TenantID: w.f.tenantB, UserID: w.f.foreignUser}, kbID, w.run.ID, current.ID, 0, 20); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("tenant %v", err)
	}
}
func TestEvalRunComparisonChecksOffPageEvidence(t *testing.T) {
	w := workerEvalFixture(t)
	completeComparisonRun(t, w)
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	current, err := w.store.Create(w.f.ctx, actor, kbID, "current", w.request)
	if err != nil {
		t.Fatal(err)
	}
	w.result = retrieval.Result{Trace: &retrieval.Trace{}}
	completeComparisonRun(t, w)
	var chunk int64
	if err := w.f.admin.QueryRow(w.f.ctx, `SELECT id FROM chunk WHERE tenant_id=$1 ORDER BY id LIMIT 1`, w.f.tenantA).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end) SELECT tenant_id,kb_id,document_id,version_id,section_id,2,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end FROM chunk WHERE id=$1`, chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE chunk SET is_current=false,disabled=true WHERE id=$1`, chunk); err != nil {
		t.Fatal(err)
	}
	// Even an empty requested page must not reveal aggregates from hidden evidence.
	if _, err := w.store.Compare(w.f.ctx, actor, kbID, w.run.ID, current.ID, 2, 1); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("off-page evidence %v", err)
	}
}
func TestEvalRunComparisonMissingAndRevoked(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			w := workerEvalFixture(t)
			f := w.f
			kbID := w.request.Config.KnowledgeBaseID
			if complete {
				completeComparisonRun(t, w)
			} else if _, err := w.store.Compare(f.ctx, w.request.Config.Actor, kbID, w.run.ID, w.run.ID, 0, 20); !errors.Is(err, eval.ErrRunResultUnavailable) {
				t.Fatalf("pending %v", err)
			}
			if err := f.service.SetMember(f.ctx, f.actor(f.owner), kbID, f.other, Admin); err != nil {
				t.Fatal(err)
			}
			tx, err := f.admin.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, kbID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := w.store.Compare(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, kbID, w.run.ID, w.run.ID, 0, 20)
				done <- err
			}()
			waitSetListBlock(t, f, tx, done)
			if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, kbID, f.other); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, eval.ErrReportNotFound) {
				t.Fatalf("revoked comparison %v", err)
			}
		})
	}
}
