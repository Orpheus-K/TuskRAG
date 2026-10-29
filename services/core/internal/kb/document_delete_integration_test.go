package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
)

func TestDeleteDocumentHidesAndQueuesPurge(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "original.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(f.ctx, viewer, base.ID, original.DocumentID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer deleted a document: %v", err)
	}
	if err := service.Delete(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, base.ID, original.DocumentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign tenant deleted a document: %v", err)
	}
	if err := service.Delete(f.ctx, owner, base.ID, original.DocumentID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(f.ctx, owner, base.ID, original.DocumentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted document remained editable: %v", err)
	}
	page, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted document remained listed: %+v err=%v", page, err)
	}
	if err := service.AuthorizeVersionUpload(f.ctx, owner, base.ID, original.DocumentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted document accepted a new version: %v", err)
	}
	var deleted, noCurrent bool
	var purges, audits int
	err = f.admin.QueryRow(f.ctx, `SELECT deleted_at IS NOT NULL,current_version_id IS NULL,
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge' AND status='queued' AND payload->>'version_id'=$3),
		(SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='document.delete' AND object_id=$2 AND result='allow')
		FROM document WHERE tenant_id=$1 AND id=$2`, f.tenantA, original.DocumentID, fmt.Sprint(original.VersionID)).
		Scan(&deleted, &noCurrent, &purges, &audits)
	if err != nil || !deleted || !noCurrent || purges != 1 || audits != 1 || originalCount(t, root) != 1 {
		t.Fatalf("delete transaction: deleted=%t noCurrent=%t purges=%d audits=%d originals=%d err=%v", deleted, noCurrent, purges, audits, originalCount(t, root), err)
	}
}

func TestDeleteDocumentHandlesExistingPurgeJob(t *testing.T) {
	for _, matches := range []bool{true, false} {
		t.Run(fmt.Sprintf("matching=%t", matches), func(t *testing.T) {
			f := setup(t)
			base := f.create(t)
			service, _, root := documentFixture(t, f)
			owner := f.actor(f.owner)
			original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "original.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
			if err != nil {
				t.Fatal(err)
			}
			queue, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			version := original.VersionID
			if !matches {
				version++
			}
			payload, err := json.Marshal(struct {
				KBID       int64 `json:"kb_id"`
				DocumentID int64 `json:"document_id"`
				VersionID  int64 `json:"version_id"`
			}{base.ID, original.DocumentID, version})
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("version:%d", original.VersionID)
			if _, err := queue.Enqueue(f.ctx, f.tenantA, jobs.Request{Kind: jobs.Purge, DedupeKey: &key, Payload: payload, MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			err = service.Delete(f.ctx, owner, base.ID, original.DocumentID)
			if matches && err != nil || !matches && err == nil {
				t.Fatalf("existing purge matches=%t: %v", matches, err)
			}
			var deleted bool
			var purges int
			if err := f.admin.QueryRow(f.ctx, `SELECT deleted_at IS NOT NULL,
				(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge' AND status IN ('queued','running'))
				FROM document WHERE tenant_id=$1 AND id=$2`, f.tenantA, original.DocumentID).Scan(&deleted, &purges); err != nil || deleted != matches || purges != 1 || originalCount(t, root) != 1 {
				t.Fatalf("existing purge state: deleted=%t purges=%d originals=%d err=%v", deleted, purges, originalCount(t, root), err)
			}
		})
	}
}

func TestDeleteDocumentFencesConcurrentVersionUpload(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner := f.actor(f.owner)
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "original.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &pausedReader{started: make(chan struct{}), release: make(chan struct{}), input: strings.NewReader("列\n二\n")}
	released := false
	t.Cleanup(func() {
		if !released {
			close(reader.release)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, DocumentID: original.DocumentID, Title: "revision.csv", MIME: "text/csv", Content: reader})
		done <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("version upload did not start")
	}
	if err := service.Delete(f.ctx, owner, base.ID, original.DocumentID); err != nil {
		t.Fatal(err)
	}
	close(reader.release)
	released = true
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("concurrent version upload survived deletion: %v", err)
	}
	var versions, purges int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document_version WHERE tenant_id=$1),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge')`, f.tenantA).Scan(&versions, &purges); err != nil || versions != 1 || purges != 1 || originalCount(t, root) != 1 {
		t.Fatalf("concurrent upload left state: versions=%d purges=%d originals=%d err=%v", versions, purges, originalCount(t, root), err)
	}
}

func TestDeleteDocumentRollsBackIfAuditFails(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner := f.actor(f.owner)
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "original.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("tuskrag_test_delete_audit_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON audit_log"); err != nil {
			t.Error(err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION "+name+"()"); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action = 'document.delete') EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(f.ctx, owner, base.ID, original.DocumentID); err == nil {
		t.Fatal("document delete succeeded without audit")
	}
	var deleted bool
	var purges int
	if err := f.admin.QueryRow(f.ctx, `SELECT deleted_at IS NOT NULL,
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge')
		FROM document WHERE tenant_id=$1 AND id=$2`, f.tenantA, original.DocumentID).Scan(&deleted, &purges); err != nil || deleted || purges != 0 || originalCount(t, root) != 1 {
		t.Fatalf("audit failure did not roll back: deleted=%t purges=%d originals=%d err=%v", deleted, purges, originalCount(t, root), err)
	}
}
