package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/jobs"
)

func deadIngest(t *testing.T, f *fixture, document UploadedDocument, code string) int64 {
	t.Helper()
	var jobID int64
	if err := f.admin.QueryRow(f.ctx, `UPDATE job SET status='dead', attempts=max_attempts, last_error=$3, finished_at=clock_timestamp()
		WHERE tenant_id=$1 AND kind='ingest' AND dedupe_key=$2 RETURNING id`, f.tenantA, fmt.Sprintf("document-version:%d", document.VersionID), code).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='failed',failure_code=$3 WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID, code); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func TestDocumentJobRetryPermissionsConcurrencyAndHistory(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	document, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "retry.md", MIME: "text/markdown", Content: strings.NewReader("# 内容\n")})
	if err != nil {
		t.Fatal(err)
	}
	jobID := deadIngest(t, f, document, string(jobs.ModelUnavailable))
	job, err := service.GetJob(f.ctx, owner, jobID)
	if err != nil || !job.Retryable || job.Status != "dead" || job.DocumentID != document.DocumentID || job.VersionID != document.VersionID {
		t.Fatalf("failed job: %+v err=%v", job, err)
	}
	page, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 10)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Retryable || page.Items[0].LatestJobID == nil || *page.Items[0].LatestJobID != jobID {
		t.Fatalf("editor job listing: %+v err=%v", page, err)
	}
	page, err = f.service.ListDocuments(f.ctx, viewer, base.ID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Retryable || page.Items[0].LatestJobID != nil {
		t.Fatalf("viewer job listing: %+v err=%v", page, err)
	}
	for _, check := range []struct {
		actor Actor
		want  error
	}{{viewer, ErrForbidden}, {f.actor(f.third), ErrNotFound}, {Actor{TenantID: f.tenantB, UserID: f.foreignUser}, ErrNotFound}} {
		if _, err := service.GetJob(f.ctx, check.actor, jobID); !errors.Is(err, check.want) {
			t.Fatalf("job visibility: got=%v want=%v", err, check.want)
		}
		if _, err := service.RetryJob(f.ctx, check.actor, jobID); !errors.Is(err, check.want) {
			t.Fatalf("job retry access: got=%v want=%v", err, check.want)
		}
	}
	start := make(chan struct{})
	type outcome struct {
		id  int64
		err error
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			<-start
			id, err := service.RetryJob(f.ctx, owner, jobID)
			results <- outcome{id, err}
		})
	}
	close(start)
	group.Wait()
	close(results)
	var created int64
	var successes, conflicts int
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			created = result.id
		case errors.Is(result.err, ErrJobNotRetryable):
			conflicts++
		default:
			t.Fatalf("concurrent retry: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 || created <= jobID {
		t.Fatalf("concurrent retry successes=%d conflicts=%d created=%d", successes, conflicts, created)
	}
	var versionStatus, oldStatus, newStatus string
	var attempts, history, auditCount int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),
		(SELECT status FROM job WHERE tenant_id=$1 AND id=$3),
		(SELECT status FROM job WHERE tenant_id=$1 AND id=$4),
		(SELECT attempts FROM job WHERE tenant_id=$1 AND id=$4),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='ingest' AND dedupe_key=$5),
		(SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='job.retry' AND object_id=$4 AND result='allow')`,
		f.tenantA, document.VersionID, jobID, created, fmt.Sprintf("document-version:%d", document.VersionID)).
		Scan(&versionStatus, &oldStatus, &newStatus, &attempts, &history, &auditCount); err != nil || versionStatus != "queued" || oldStatus != "dead" || newStatus != "queued" || attempts != 0 || history != 2 || auditCount != 1 {
		t.Fatalf("retry state version=%s old=%s new=%s attempts=%d history=%d audit=%d err=%v", versionStatus, oldStatus, newStatus, attempts, history, auditCount, err)
	}
	job, err = service.GetJob(f.ctx, owner, created)
	if err != nil || job.Retryable || job.Status != "queued" {
		t.Fatalf("queued job: %+v err=%v", job, err)
	}
}

type rejectRetryQueue struct{ DocumentQueue }

func (q rejectRetryQueue) EnqueueTx(context.Context, pgx.Tx, int64, jobs.Request) (int64, error) {
	return 0, errors.New("injected enqueue failure")
}

func TestDocumentJobRetryRollsBackAndRejectsNonrecoverableOrOldVersions(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, files, _ := documentFixture(t, f)
	owner := f.actor(f.owner)
	document, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "retry.md", MIME: "text/markdown", Content: strings.NewReader("# 原版\n")})
	if err != nil {
		t.Fatal(err)
	}
	jobID := deadIngest(t, f, document, string(jobs.Timeout))
	broken, err := NewDocumentService(f.service, files, rejectRetryQueue{service.queue})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.RetryJob(f.ctx, owner, jobID); err == nil {
		t.Fatal("enqueue failure was reported as success")
	}
	var status, failure string
	if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID).Scan(&status, &failure); err != nil || status != "failed" || failure != string(jobs.Timeout) {
		t.Fatalf("retry rollback status=%s failure=%s err=%v", status, failure, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET payload=jsonb_set(payload,'{version_id}',to_jsonb($3::bigint)) WHERE tenant_id=$1 AND id=$2`, f.tenantA, jobID, document.VersionID+1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 10); err == nil {
		t.Fatal("mismatched job was presented as a valid document status")
	}
	if _, err := service.RetryJob(f.ctx, owner, jobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched job could be retried: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET payload=jsonb_set(payload,'{version_id}',to_jsonb($3::bigint)) WHERE tenant_id=$1 AND id=$2`, f.tenantA, jobID, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET last_error='encrypted' WHERE tenant_id=$1 AND id=$2`, f.tenantA, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET failure_code='encrypted' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if job, err := service.GetJob(f.ctx, owner, jobID); err != nil || job.Retryable {
		t.Fatalf("encrypted job: %+v err=%v", job, err)
	}
	if _, err := service.RetryJob(f.ctx, owner, jobID); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("retried encrypted file: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET last_error='timeout' WHERE tenant_id=$1 AND id=$2`, f.tenantA, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET failure_code='timeout' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, DocumentID: document.DocumentID, Title: "retry.md", MIME: "text/markdown", Content: strings.NewReader("# 新版\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryJob(f.ctx, owner, jobID); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("retried an older version: %v", err)
	}
}

func TestDocumentJobRetryRechecksRevokedEditor(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner, editor := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Editor); err != nil {
		t.Fatal(err)
	}
	document, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "retry.md", MIME: "text/markdown", Content: strings.NewReader("# 内容\n")})
	if err != nil {
		t.Fatal(err)
	}
	jobID := deadIngest(t, f, document, string(jobs.LeaseExpired))
	gate, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	if _, err := gate.Exec(f.ctx, `SELECT id FROM job WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, f.tenantA, jobID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.RetryJob(f.ctx, editor, jobID)
		done <- err
	}()
	waitForListingLock(t, f, done, "LockJobForControl")
	if err := f.service.RemoveMember(f.ctx, owner, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked editor retried job: %v", err)
	}
	var state string
	var history int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='ingest' AND dedupe_key=$3)`, f.tenantA, document.VersionID, fmt.Sprintf("document-version:%d", document.VersionID)).Scan(&state, &history); err != nil || state != "failed" || history != 1 {
		t.Fatalf("revoked retry changed state=%s history=%d err=%v", state, history, err)
	}
}

func TestPurgeJobVisibilityRetryPermissionsAndHistory(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	document, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "purge-target.md", MIME: "text/markdown", Content: strings.NewReader("# 清理测试\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(f.ctx, owner, base.ID, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	var purgeID int64
	if err := f.admin.QueryRow(f.ctx, `UPDATE job SET status='dead', attempts=max_attempts, last_error=$3, finished_at=clock_timestamp()
		WHERE tenant_id=$1 AND kind='purge' AND dedupe_key=$2 RETURNING id`,
		f.tenantA, fmt.Sprintf("version:%d", document.VersionID), string(jobs.StorageUnavailable)).Scan(&purgeID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='purging' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		actor Actor
		want  error
	}{{viewer, ErrForbidden}, {f.actor(f.third), ErrNotFound}, {Actor{TenantID: f.tenantB, UserID: f.foreignUser}, ErrNotFound}} {
		if _, err := service.ListPurgeJobs(f.ctx, check.actor, base.ID, 0, 20); !errors.Is(err, check.want) {
			t.Fatalf("purge list access: got=%v want=%v", err, check.want)
		}
		if _, err := service.GetJob(f.ctx, check.actor, purgeID); !errors.Is(err, check.want) {
			t.Fatalf("purge get access: got=%v want=%v", err, check.want)
		}
		if _, err := service.RetryJob(f.ctx, check.actor, purgeID); !errors.Is(err, check.want) {
			t.Fatalf("purge retry access: got=%v want=%v", err, check.want)
		}
	}
	page, err := service.ListPurgeJobs(f.ctx, owner, base.ID, 0, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != purgeID || !page.Items[0].DocumentDeleted || !page.Items[0].Retryable {
		t.Fatalf("purge list: %+v err=%v", page, err)
	}
	job, err := service.GetJob(f.ctx, owner, purgeID)
	if err != nil || job.Kind != string(jobs.Purge) || !job.Retryable || job.Status != "dead" {
		t.Fatalf("purge get: %+v err=%v", job, err)
	}
	start := make(chan struct{})
	type outcome struct {
		id  int64
		err error
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			<-start
			id, err := service.RetryJob(f.ctx, owner, purgeID)
			results <- outcome{id, err}
		})
	}
	close(start)
	group.Wait()
	close(results)
	var created int64
	var successes, conflicts int
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			created = result.id
		case errors.Is(result.err, ErrJobNotRetryable):
			conflicts++
		default:
			t.Fatalf("concurrent purge retry: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 || created <= purgeID {
		t.Fatalf("concurrent purge retry successes=%d conflicts=%d created=%d", successes, conflicts, created)
	}
	var versionStatus, oldStatus, newStatus string
	var history, auditCount int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),
		(SELECT status FROM job WHERE tenant_id=$1 AND id=$3),
		(SELECT status FROM job WHERE tenant_id=$1 AND id=$4),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge' AND dedupe_key=$5),
		(SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='job.retry' AND object_id=$4 AND result='allow')`,
		f.tenantA, document.VersionID, purgeID, created, fmt.Sprintf("version:%d", document.VersionID)).
		Scan(&versionStatus, &oldStatus, &newStatus, &history, &auditCount); err != nil || versionStatus != "purging" || oldStatus != "dead" || newStatus != "queued" || history != 2 || auditCount != 1 {
		t.Fatalf("purge retry state version=%s old=%s new=%s history=%d audit=%d err=%v", versionStatus, oldStatus, newStatus, history, auditCount, err)
	}
	afterPage, err := service.ListPurgeJobs(f.ctx, owner, base.ID, 0, 20)
	if err != nil || len(afterPage.Items) != 2 || afterPage.Items[0].ID != created || afterPage.Items[0].Retryable || afterPage.Items[1].ID != purgeID || afterPage.Items[1].Retryable {
		t.Fatalf("purge list after retry: %+v err=%v", afterPage, err)
	}
}
