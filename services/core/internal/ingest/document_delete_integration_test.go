package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/storage"
)

func TestDeletedDocumentCancelsIngestAndPurgesVersions(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	original, originalJob := pipelineUpload(t, f, "text/markdown", "# 原制度\n\n原始申请内容。\n")
	handler, _, model := pipelineStages(t, f, nativePipelineParser(t), false)
	purge, err := NewPurgeStage(f.queue, f.files)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewJobHandler(handler, purge)
	if err != nil {
		t.Fatal(err)
	}
	firstWorker := runJobWorker(t, f, router, router.Expire)
	waitPipelineJob(t, f, firstWorker, originalJob, "succeeded")
	driver, err := retrieval.NewDriver(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	binding, err := driver.Binding(f.ctx, actor, f.kbID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", testVector(1, 0), retrieval.Filters{}, 8)
	if err != nil || len(before) != 1 || before[0].VersionID != original.VersionID {
		t.Fatalf("original was not searchable: %+v err=%v", before, err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	checkDeleteAuditRollback(t, f, documents, driver, actor, binding, original.DocumentID, original.VersionID)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	model.mu.Lock()
	model.entered, model.release = entered, release
	model.mu.Unlock()
	revision, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, DocumentID: original.DocumentID, Title: "revision.md", MIME: "text/markdown",
		Content: strings.NewReader("# 原制度\n\n独特内容Q9需要先申请。\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case err := <-firstWorker:
		t.Fatalf("worker stopped before revision embedding: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := documents.Delete(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, original.DocumentID); err != nil {
		t.Fatal(err)
	}
	after, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", testVector(1, 0), retrieval.Filters{}, 8)
	if err != nil || len(after) != 0 {
		t.Fatalf("deleted document remained searchable: %+v err=%v", after, err)
	}
	var originalPurge, revisionPurge, revisionJob int64
	err = f.admin.QueryRow(f.ctx, `SELECT
		(SELECT id FROM job WHERE tenant_id=$1 AND kind='purge' AND payload->>'version_id'=$2),
		(SELECT id FROM job WHERE tenant_id=$1 AND kind='purge' AND payload->>'version_id'=$3),
		(SELECT id FROM job WHERE tenant_id=$1 AND kind='ingest' AND payload->>'version_id'=$3)`,
		f.tenant, fmt.Sprint(original.VersionID), fmt.Sprint(revision.VersionID)).Scan(&originalPurge, &revisionPurge, &revisionJob)
	if err != nil {
		t.Fatal(err)
	}
	secondWorker := runJobWorker(t, f, router, router.Expire)
	waitPipelineJob(t, f, secondWorker, originalPurge, "succeeded")
	waitPipelineJob(t, f, secondWorker, revisionPurge, "succeeded")
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE tenant_id=$1 AND id=$2`, f.tenant, revisionJob).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("running ingest was not fenced: status=%s err=%v", status, err)
	}
	for _, version := range []int64{original.VersionID, revision.VersionID} {
		assertOriginalExists(t, f, storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: original.DocumentID, VersionID: version}, false)
		assertPurgeState(t, f, version, "", false)
	}
	var deleted, noCurrent bool
	if err := f.admin.QueryRow(f.ctx, `SELECT deleted_at IS NOT NULL,current_version_id IS NULL FROM document WHERE tenant_id=$1 AND id=$2`, f.tenant, original.DocumentID).
		Scan(&deleted, &noCurrent); err != nil || !deleted || !noCurrent {
		t.Fatalf("deleted document state: deleted=%t noCurrent=%t err=%v", deleted, noCurrent, err)
	}
}

func checkDeleteAuditRollback(t *testing.T, f *fixture, documents *kb.DocumentService, driver *retrieval.Driver, actor retrieval.Actor, binding retrieval.Binding, documentID, versionID int64) {
	t.Helper()
	name := fmt.Sprintf("tuskrag_test_delete_audit_%d", f.tenant)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON audit_log"); err != nil {
			t.Error(err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION "+name+"()"); err != nil {
			t.Error(err)
		}
	}()
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action = 'document.delete') EXECUTE FUNCTION %s()", name, f.tenant, name)); err != nil {
		t.Fatal(err)
	}
	if err := documents.Delete(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, documentID); err == nil {
		t.Fatal("delete succeeded after audit failure")
	}
	results, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", testVector(1, 0), retrieval.Filters{}, 8)
	if err != nil || len(results) != 1 || results[0].VersionID != versionID {
		t.Fatalf("audit failure hid the current evidence: %+v err=%v", results, err)
	}
	var purges int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge'`, f.tenant).Scan(&purges); err != nil || purges != 0 {
		t.Fatalf("audit failure left purge jobs: %d err=%v", purges, err)
	}
}

func TestPurgeDeletedDocumentRejectsMismatchedIngest(t *testing.T) {
	f := setup(t)
	original, jobID := pipelineUpload(t, f, "text/plain", "一份测试制度。\n")
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET payload=jsonb_set(payload,'{version_id}',to_jsonb($3::bigint))
		WHERE tenant_id=$1 AND id=$2`, f.tenant, jobID, original.VersionID+1); err != nil {
		t.Fatal(err)
	}
	if err := documents.Delete(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, original.DocumentID); err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.ClaimKindsWithRecovery(f.ctx, f.tenant, "purge-mismatch", time.Minute, []jobs.Kind{jobs.Purge}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := NewPurgeStage(f.queue, f.files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Prepare(f.ctx, job); !errors.Is(err, jobs.ErrDedupeConflict) {
		t.Fatalf("mismatched ingest payload was accepted: %v", err)
	}
	var versionStatus, jobStatus string
	if err := f.admin.QueryRow(f.ctx, `SELECT v.status,j.status FROM document_version v JOIN job j ON j.tenant_id=v.tenant_id
		WHERE v.tenant_id=$1 AND v.id=$2 AND j.id=$3`, f.tenant, original.VersionID, jobID).Scan(&versionStatus, &jobStatus); err != nil || versionStatus != "queued" || jobStatus != "queued" {
		t.Fatalf("mismatched cancellation changed state: version=%s job=%s err=%v", versionStatus, jobStatus, err)
	}
	assertOriginalExists(t, f, storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: original.DocumentID, VersionID: original.VersionID}, true)
}
