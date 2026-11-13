package kb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func TestEvalRunListPaginationAndScope(t *testing.T) {
	w := workerEvalFixture(t)
	f := w.f
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	extra := startScopeDocument(t, f, kbID, false)
	hidden := w.request
	hidden.Config.AllowedDocumentIDs = append(append([]int64{}, hidden.Config.AllowedDocumentIDs...), extra)
	if _, err := w.store.Create(f.ctx, actor, kbID, "hidden", hidden); err != nil {
		t.Fatal(err)
	}
	third, err := w.store.Create(f.ctx, actor, kbID, "third", w.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, extra); err != nil {
		t.Fatal(err)
	}
	first, err := w.store.List(f.ctx, actor, kbID, 0, 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != w.run.ID || first.NextCursor == nil || *first.NextCursor != w.run.ID {
		t.Fatalf("first %+v %v", first, err)
	}
	next, err := w.store.List(f.ctx, actor, kbID, *first.NextCursor, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != third.ID || next.NextCursor != nil {
		t.Fatalf("filtered page %+v %v", next, err)
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.List(f.ctx, actor, kbID, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.List(f.ctx, retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}, kbID, 0, 10); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("foreign %v", err)
	}
}
func TestEvalRunListReturnedEvidence(t *testing.T) {
	w := workerEvalFixture(t)
	job, apply := w.prepare(t)
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); err != nil {
		t.Fatal(err)
	}
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	page, err := w.store.List(w.f.ctx, actor, kbID, 0, 10)
	if err != nil || len(page.Items) != 1 || !page.Items[0].ResultAvailable || page.Items[0].Status != "succeeded" {
		t.Fatalf("completed %+v %v", page, err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end) SELECT tenant_id,kb_id,document_id,version_id,section_id,2,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end FROM chunk WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE chunk SET is_current=false,disabled=true WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
		t.Fatal(err)
	}
	page, err = w.store.List(w.f.ctx, actor, kbID, 0, 10)
	if err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("invalid evidence leaked %+v %v", page, err)
	}
}
func TestEvalRunListRevokedWhileWaiting(t *testing.T) {
	for _, cursor := range []int64{0, math.MaxInt64} {
		t.Run(fmt.Sprint(cursor), func(t *testing.T) {
			w := workerEvalFixture(t)
			f := w.f
			kbID := w.request.Config.KnowledgeBaseID
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
				_, err := w.store.List(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, kbID, cursor, 10)
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
				t.Fatalf("revoked list %v", err)
			}
		})
	}
}
func TestEvalRunListRechecksLookahead(t *testing.T) {
	w := workerEvalFixture(t)
	f := w.f
	actor, kbID := w.request.Config.Actor, w.request.Config.KnowledgeBaseID
	extra := startScopeDocument(t, f, kbID, false)
	request := w.request
	request.Config.AllowedDocumentIDs = append(append([]int64{}, request.Config.AllowedDocumentIDs...), extra)
	if _, err := w.store.Create(f.ctx, actor, kbID, "lookahead", request); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM document WHERE id=$1 FOR UPDATE`, extra); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := w.store.List(f.ctx, actor, kbID, 0, 1); done <- err }()
	waitSetListBlock(t, f, tx, done)
	if _, err := tx.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, extra); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("lookahead disclosed %v", err)
	}
}
