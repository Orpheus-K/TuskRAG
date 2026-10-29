package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/storage"
)

type failingDeleteStore struct {
	storage.Store
	entered chan struct{}
	release <-chan struct{}
}

type expireAfterDeleteStore struct {
	storage.Store
	expire func() error
}

func (s *expireAfterDeleteStore) Delete(ctx context.Context, key storage.Key) error {
	if err := s.Store.Delete(ctx, key); err != nil {
		return err
	}
	return s.expire()
}

func (s *failingDeleteStore) Delete(ctx context.Context, key storage.Key) error {
	close(s.entered)
	select {
	case <-s.release:
		return errors.New("injected storage failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPurgeSupersededVersionPostgres(t *testing.T) {
	publicationDatabase(t)
	f, firstJob, uploaded, firstPlan, firstStaged := prepareVectorFixture(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
			t.Errorf("clear purge test pointer: %v", err)
		}
	})
	finishPublicationVectors(t, f, firstJob, firstStaged)
	if err := indexing.Provision(f.ctx, mustTestMigratorURL(t), f.tenant, firstPlan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, firstJob.Lease, preparePublication(t, f, firstJob, firstPlan, firstStaged)); err != nil {
		t.Fatal(err)
	}
	key := storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}
	assertOriginalExists(t, f, key, true)
	assertPurgeGuard(t, f, `UPDATE document_version SET status='purging' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID)
	assertPurgeGuardWithShadow(t, f, uploaded.DocumentID, uploaded.VersionID, true)
	stage, err := NewPurgeStage(f.queue, f.files)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(documentIDs{KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.Enqueue(f.ctx, f.tenant, jobs.Request{Kind: jobs.Purge, Payload: payload, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	unsafeJob, err := f.queue.Claim(f.ctx, f.tenant, "purge-current-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Prepare(f.ctx, unsafeJob); !errors.Is(err, ErrPurgeUnsafe) {
		t.Fatalf("current version purge accepted: %v", err)
	}
	assertOriginalExists(t, f, key, true)
	if status, err := f.queue.Fail(f.ctx, unsafeJob.Lease, jobs.Internal); err != nil || status != jobs.Failed {
		t.Fatalf("reject unsafe cleanup job: status=%s err=%v", status, err)
	}
	secondJob, secondPlan, secondStaged, secondVersion := prepareRevision(t, f, uploaded.DocumentID, uploaded.VersionID, firstPlan, "purge-second")
	if err := f.queue.Complete(f.ctx, secondJob.Lease, preparePublication(t, f, secondJob, secondPlan, secondStaged)); err != nil {
		t.Fatal(err)
	}
	purgeJob, err := f.queue.Claim(f.ctx, f.tenant, "purge-old-test", time.Minute)
	if err != nil || purgeJob.Kind != jobs.Purge {
		t.Fatalf("claim old version cleanup: kind=%s err=%v", purgeJob.Kind, err)
	}
	checkMissingPurgeGuard(t, f, stage, purgeJob, key)
	stale := purgeJob
	stale.Lease.Generation++
	if _, err := stage.Prepare(f.ctx, stale); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale cleanup lease accepted: %v", err)
	}
	forged := purgeJob
	forged.Payload, err = json.Marshal(documentIDs{KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: secondVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Prepare(f.ctx, forged); !errors.Is(err, ErrPurgeJobMismatch) {
		t.Fatalf("forged cleanup target accepted: %v", err)
	}
	assertOriginalExists(t, f, key, true)
	path, err := key.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET file_key='invalid-key' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Prepare(f.ctx, purgeJob); !errors.Is(err, ErrPurgeUnsafe) {
		t.Fatalf("noncanonical file key accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET file_key=$3 WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID, path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=true WHERE tenant_id=$1 AND id=$2`, f.tenant, firstStaged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Prepare(f.ctx, purgeJob); !errors.Is(err, ErrPurgeUnsafe) {
		t.Fatalf("still-current chunk purge accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=false WHERE tenant_id=$1 AND id=$2`, f.tenant, firstStaged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	assertOriginalExists(t, f, key, true)
	entered, release := make(chan struct{}), make(chan struct{})
	failing := &failingDeleteStore{Store: f.files, entered: entered, release: release}
	failingStage, err := NewPurgeStage(f.queue, failing)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	done := make(chan struct{})
	result := make(chan error, 1)
	defer func() {
		releaseOnce.Do(func() { close(release) })
		<-done
	}()
	go func() {
		defer close(done)
		_, prepareErr := failingStage.Prepare(f.ctx, purgeJob)
		result <- prepareErr
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("cleanup failed before file deletion: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	assertPurgeGuard(t, f, `UPDATE document_version SET status='ready' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID)
	assertPurgeGuard(t, f, `UPDATE document SET current_version_id=$2 WHERE tenant_id=$1 AND id=$3`, f.tenant, uploaded.VersionID, uploaded.DocumentID)
	assertOriginalExists(t, f, key, true)
	releaseOnce.Do(func() { close(release) })
	err = <-result
	var failure *jobs.ProcessingFailure
	if !errors.As(err, &failure) || failure.Code != jobs.StorageUnavailable {
		t.Fatalf("storage failure was not retryable: %v", err)
	}
	assertPurgeState(t, f, uploaded.VersionID, "purging", true)
	assertOriginalExists(t, f, key, true)
	if status, err := f.queue.Fail(f.ctx, purgeJob.Lease, jobs.StorageUnavailable); err != nil || status != jobs.Queued {
		t.Fatalf("retry cleanup: status=%s err=%v", status, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET run_after=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, f.tenant, purgeJob.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.queue.Claim(f.ctx, f.tenant, "purge-retry-test", time.Minute)
	if err != nil || retry.Lease.JobID != purgeJob.Lease.JobID || retry.Lease.Generation <= purgeJob.Lease.Generation {
		t.Fatalf("reclaim cleanup: job=%d generation=%d err=%v", retry.Lease.JobID, retry.Lease.Generation, err)
	}
	expiring := &expireAfterDeleteStore{Store: f.files, expire: func() error {
		_, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, retry.Lease.JobID)
		return err
	}}
	expiringStage, err := NewPurgeStage(f.queue, expiring)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := expiringStage.Prepare(f.ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	assertOriginalExists(t, f, key, false)
	assertPurgeGuard(t, f, `UPDATE document_version SET status='ready' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID)
	assertPurgeGuard(t, f, `UPDATE document SET current_version_id=$2 WHERE tenant_id=$1 AND id=$3`, f.tenant, uploaded.VersionID, uploaded.DocumentID)
	assertPurgeGuardWithShadow(t, f, uploaded.DocumentID, uploaded.VersionID, false)
	if err := f.queue.Complete(f.ctx, retry.Lease, effects); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired lease committed cleanup: %v", err)
	}
	assertPurgeState(t, f, uploaded.VersionID, "purging", true)
	resumed, err := f.queue.Claim(f.ctx, f.tenant, "purge-resume-test", time.Minute)
	if err != nil || resumed.Lease.JobID != retry.Lease.JobID || resumed.Lease.Generation <= retry.Lease.Generation {
		t.Fatalf("resume expired cleanup: job=%d generation=%d err=%v", resumed.Lease.JobID, resumed.Lease.Generation, err)
	}
	effects, err = stage.Prepare(f.ctx, resumed)
	if err != nil {
		t.Fatalf("resume after original deletion: %v", err)
	}
	if err := f.queue.WithLease(f.ctx, resumed.Lease, effects); !errors.Is(err, jobs.ErrCompletionRequired) {
		t.Fatalf("unfenced cleanup accepted: %v", err)
	}
	injected := errors.New("injected cleanup rollback")
	err = f.queue.Complete(f.ctx, resumed.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := effects(ctx, tx); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("cleanup rollback: %v", err)
	}
	assertPurgeState(t, f, uploaded.VersionID, "purging", true)
	checkPurgeAuditFailure(t, f, resumed, uploaded.VersionID, effects)
	effects, err = stage.Prepare(f.ctx, resumed)
	if err != nil {
		t.Fatalf("resume after file deletion: %v", err)
	}
	if err := f.queue.Complete(f.ctx, resumed.Lease, effects); err != nil {
		t.Fatalf("complete old version cleanup: %v", err)
	}
	assertPurgeState(t, f, uploaded.VersionID, "", false)
	assertOriginalExists(t, f, key, false)
	assertPublicationState(t, f, uploaded.DocumentID, secondVersion, secondVersion, "ready", "succeeded", 2)
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, secondVersion, "审批")
}

func checkMissingPurgeGuard(t *testing.T, f *fixture, stage *PurgeStage, job jobs.ClaimedJob, key storage.Key) {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `ALTER TABLE public.document_version DISABLE TRIGGER document_version_status_guard`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), `ALTER TABLE public.document_version ENABLE TRIGGER document_version_status_guard`); err != nil {
			t.Errorf("restore version guard: %v", err)
		}
	}()
	if _, err := stage.Prepare(f.ctx, job); !errors.Is(err, db.ErrVersionGuards) {
		t.Fatalf("cleanup proceeded without version guard: %v", err)
	}
	pool, err := db.Open(f.ctx, os.Getenv("TUSKRAG_TEST_APP_URL"))
	if pool != nil {
		pool.Close()
	}
	if !errors.Is(err, db.ErrVersionGuards) {
		t.Fatalf("database was ready without version guard: %v", err)
	}
	assertOriginalExists(t, f, key, true)
}

func assertPurgeGuardWithShadow(t *testing.T, f *fixture, document, version int64, current bool) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenant)); err != nil {
		t.Fatal(err)
	}
	if current {
		if _, err := tx.Exec(f.ctx, `CREATE TEMP TABLE document (tenant_id bigint, kb_id bigint, id bigint, current_version_id bigint) ON COMMIT DROP`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `INSERT INTO pg_temp.document VALUES ($1,$2,$3,NULL)`, f.tenant, f.kbID, document); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := tx.Exec(f.ctx, `CREATE TEMP TABLE document_version (tenant_id bigint, kb_id bigint, document_id bigint, id bigint, status text) ON COMMIT DROP`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `INSERT INTO pg_temp.document_version VALUES ($1,$2,$3,$4,'ready')`, f.tenant, f.kbID, document, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(f.ctx, `SET LOCAL search_path = pg_temp, public`); err != nil {
		t.Fatal(err)
	}
	if current {
		_, err = tx.Exec(f.ctx, `UPDATE public.document_version SET status='purging' WHERE tenant_id=$1 AND id=$2`, f.tenant, version)
	} else {
		_, err = tx.Exec(f.ctx, `UPDATE public.document SET current_version_id=$2 WHERE tenant_id=$1 AND id=$3`, f.tenant, version, document)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23514" {
		t.Fatalf("temporary table bypassed version guard: %v", err)
	}
}

func assertPurgeGuard(t *testing.T, f *fixture, statement string, args ...any) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenant)); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(f.ctx, statement, args...)
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23514" {
		t.Fatalf("purging guard did not reject unsafe transition: %v", err)
	}
}

func checkPurgeAuditFailure(t *testing.T, f *fixture, job jobs.ClaimedJob, version int64, effects func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `REVOKE INSERT ON audit_log FROM tuskrag_app`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), `GRANT INSERT ON audit_log TO tuskrag_app`); err != nil {
			t.Errorf("restore audit permission: %v", err)
		}
	}()
	if err := f.queue.Complete(f.ctx, job.Lease, effects); err == nil || !strings.Contains(err.Error(), "audit purged version") {
		t.Fatalf("audit failure did not roll back cleanup: %v", err)
	}
	assertPurgeState(t, f, version, "purging", true)
}

func mustTestMigratorURL(t *testing.T) string {
	t.Helper()
	value := os.Getenv("TUSKRAG_TEST_MIGRATOR_URL")
	if value == "" {
		t.Fatal("test migrator URL is required")
	}
	return value
}

func assertOriginalExists(t *testing.T, f *fixture, key storage.Key, want bool) {
	t.Helper()
	got, err := f.files.Exists(f.ctx, key)
	if err != nil || got != want {
		t.Fatalf("original exists=%t want=%t err=%v", got, want, err)
	}
}

func assertPurgeState(t *testing.T, f *fixture, version int64, status string, exists bool) {
	t.Helper()
	var gotStatus string
	var versions, plans, chunks, sections, artifacts, audits int
	err := f.admin.QueryRow(f.ctx, `SELECT
		COALESCE((SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),''),
		(SELECT count(*) FROM document_version WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND object_type='document_version' AND object_id=$2 AND action='document.purge')`,
		f.tenant, version).Scan(&gotStatus, &versions, &plans, &chunks, &sections, &artifacts, &audits)
	if err != nil {
		t.Fatal(err)
	}
	if exists && (gotStatus != status || versions != 1 || plans != 1 || chunks != 2 || sections != 2 || artifacts != 1 || audits != 0) {
		t.Fatalf("pending cleanup status=%s versions=%d plans=%d chunks=%d sections=%d artifacts=%d audits=%d", gotStatus, versions, plans, chunks, sections, artifacts, audits)
	}
	if !exists && (gotStatus != "" || versions != 0 || plans != 0 || chunks != 0 || sections != 0 || artifacts != 0 || audits != 1) {
		t.Fatalf("completed cleanup status=%s versions=%d plans=%d chunks=%d sections=%d artifacts=%d audits=%d", gotStatus, versions, plans, chunks, sections, artifacts, audits)
	}
}
