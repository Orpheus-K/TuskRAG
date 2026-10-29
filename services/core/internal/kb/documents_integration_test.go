package kb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/storage"
)

func documentFixture(t *testing.T, f *fixture) (*DocumentService, *storage.Local, string) {
	t.Helper()
	root := t.TempDir()
	files, err := storage.NewLocal(f.ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	queue, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDocumentService(f.service, files, queue)
	if err != nil {
		t.Fatal(err)
	}
	return service, files, root
}

func TestDocumentCancelPermissionsAndArchivedBase(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner := f.actor(f.owner)
	doc, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", Content: strings.NewReader("列,值\n一,二\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		actor   Actor
		docID   int64
		wantErr error
	}{
		{f.actor(f.other), doc.DocumentID, ErrForbidden},
		{Actor{TenantID: f.tenantB, UserID: f.foreignUser}, doc.DocumentID, ErrNotFound},
		{owner, doc.DocumentID + 1000000, ErrNotFound},
	} {
		if err := service.Cancel(f.ctx, check.actor, base.ID, check.docID, doc.VersionID); !errors.Is(err, check.wantErr) {
			t.Fatalf("cancel authorization: got=%v want=%v", err, check.wantErr)
		}
	}
	var jobState, versionState string
	if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.tenant_id=$1 AND v.id=$2 AND j.dedupe_key=$3`, f.tenantA, doc.VersionID, fmt.Sprintf("document-version:%d", doc.VersionID)).Scan(&jobState, &versionState); err != nil || jobState != "queued" || versionState != "queued" {
		t.Fatalf("denied cancellation changed states: job=%s version=%s err=%v", jobState, versionState, err)
	}
	if err := f.service.Archive(f.ctx, owner, base.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(f.ctx, owner, base.ID, doc.DocumentID, doc.VersionID); err != nil {
		t.Fatal(err)
	}
	var failureCode string
	var allowed, denied int
	if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status,v.failure_code, (SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='document.cancel' AND result='allow'), (SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='document.cancel' AND result='deny') FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.tenant_id=$1 AND v.id=$2 AND j.dedupe_key=$3`, f.tenantA, doc.VersionID, fmt.Sprintf("document-version:%d", doc.VersionID)).Scan(&jobState, &versionState, &failureCode, &allowed, &denied); err != nil || jobState != "cancelled" || versionState != "failed" || failureCode != "cancelled" || allowed != 1 || denied != 2 {
		t.Fatalf("cancel outcome: job=%s version=%s failure=%s allow=%d deny=%d err=%v", jobState, versionState, failureCode, allowed, denied, err)
	}
	if err := service.Cancel(f.ctx, owner, base.ID, doc.DocumentID, doc.VersionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat cancellation: %v", err)
	}
}

type cancelGate struct {
	store              *jobs.Store
	locked, continueCh chan struct{}
}

func (g *cancelGate) EnqueueTx(ctx context.Context, tx pgx.Tx, tenantID int64, request jobs.Request) (int64, error) {
	return g.store.EnqueueTx(ctx, tx, tenantID, request)
}

func (g *cancelGate) LockActiveTx(ctx context.Context, tx pgx.Tx, tenantID int64, kind jobs.Kind, key string, payload json.RawMessage) (int64, error) {
	id, err := g.store.LockActiveTx(ctx, tx, tenantID, kind, key, payload)
	if err == nil {
		close(g.locked)
		select {
		case <-g.continueCh:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return id, err
}

func (g *cancelGate) CancelTx(ctx context.Context, tx pgx.Tx, tenantID, jobID int64) (jobs.Kind, error) {
	return g.store.CancelTx(ctx, tx, tenantID, jobID)
}

func TestDocumentCancelRechecksRevokedMembership(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, files, _ := documentFixture(t, f)
	owner, editor := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Editor); err != nil {
		t.Fatal(err)
	}
	doc, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", Content: strings.NewReader("列,值\n一,二\n")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	gate := &cancelGate{store: store, locked: make(chan struct{}), continueCh: make(chan struct{})}
	service, err = NewDocumentService(f.service, files, gate)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Cancel(f.ctx, editor, base.ID, doc.DocumentID, doc.VersionID) }()
	select {
	case <-gate.locked:
	case <-f.ctx.Done():
		t.Fatal("cancellation did not lock job")
	}
	if err := f.service.RemoveMember(f.ctx, owner, base.ID, f.other); err != nil {
		close(gate.continueCh)
		t.Fatal(err)
	}
	close(gate.continueCh)
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked editor cancelled document: %v", err)
	}
	var jobState, versionState string
	if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.tenant_id=$1 AND v.id=$2 AND j.dedupe_key=$3`, f.tenantA, doc.VersionID, fmt.Sprintf("document-version:%d", doc.VersionID)).Scan(&jobState, &versionState); err != nil || jobState != "queued" || versionState != "queued" {
		t.Fatalf("revoked cancellation changed states: job=%s version=%s err=%v", jobState, versionState, err)
	}
}

func TestDocumentCancelRollsBackWhenAuditFails(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	doc, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", Content: strings.NewReader("列,值\n一,二\n")})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("tuskrag_test_cancel_audit_%d", f.tenantA)
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
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action = 'document.cancel') EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(f.ctx, f.actor(f.owner), base.ID, doc.DocumentID, doc.VersionID); err == nil {
		t.Fatal("cancellation succeeded despite audit failure")
	}
	var jobState, versionState string
	if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.tenant_id=$1 AND v.id=$2 AND j.dedupe_key=$3`, f.tenantA, doc.VersionID, fmt.Sprintf("document-version:%d", doc.VersionID)).Scan(&jobState, &versionState); err != nil || jobState != "queued" || versionState != "queued" {
		t.Fatalf("unaudited cancellation persisted: job=%s version=%s err=%v", jobState, versionState, err)
	}
}

func originalCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "original" {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDocumentUploadIsAtomicAndDeduplicated(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, files, root := documentFixture(t, f)
	const content = "城市,住宿上限\n北京,600\n上海,600\n"
	type outcome struct {
		document UploadedDocument
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			document, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "差旅.csv", MIME: "text/csv", Content: strings.NewReader(content)})
			results <- outcome{document, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	var created UploadedDocument
	switch {
	case first.err == nil && errors.Is(second.err, ErrDuplicateContent):
		created = first.document
	case second.err == nil && errors.Is(first.err, ErrDuplicateContent):
		created = second.document
	default:
		t.Fatalf("concurrent duplicate upload: first=%+v second=%+v", first, second)
	}
	if created.DocumentID <= 0 || created.VersionID <= 0 || created.Status != "queued" || created.ByteSize != int64(len(content)) {
		t.Fatalf("invalid uploaded document: %+v", created)
	}
	original, err := files.Open(f.ctx, storage.Key{TenantID: f.tenantA, KBID: base.ID, DocumentID: created.DocumentID, VersionID: created.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(original)
	closeErr := original.Close()
	if readErr != nil || closeErr != nil || string(data) != content {
		t.Fatalf("original mismatch: read=%v close=%v", readErr, closeErr)
	}
	var documents, versions, queued int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document WHERE tenant_id=$1), (SELECT count(*) FROM document_version WHERE tenant_id=$1), (SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='ingest' AND status='queued')`, f.tenantA).Scan(&documents, &versions, &queued); err != nil || documents != 1 || versions != 1 || queued != 1 || originalCount(t, root) != 1 {
		t.Fatalf("atomic upload: documents=%d versions=%d jobs=%d originals=%d err=%v", documents, versions, queued, originalCount(t, root), err)
	}
	duplicate, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "副本.csv", MIME: "text/csv", AllowDuplicate: true, Content: strings.NewReader(content)})
	if err != nil || duplicate.DocumentID == created.DocumentID {
		t.Fatalf("explicit duplicate: %+v err=%v", duplicate, err)
	}
}

func TestDocumentUploadIdempotency(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	const content = "姓名,部门\n张三,财务\n李四,技术\n"
	type outcome struct {
		document UploadedDocument
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			document, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "人员.csv", MIME: "text/csv", IdempotencyKey: "upload-key", Content: strings.NewReader(content)})
			results <- outcome{document, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.document.DocumentID <= 0 || first.document.DocumentID != second.document.DocumentID || first.document.VersionID != second.document.VersionID {
		t.Fatalf("upload replay: first=%+v second=%+v", first, second)
	}
	if originalCount(t, root) != 1 {
		t.Fatalf("replay left extra originals: %d", originalCount(t, root))
	}
	_, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "其他.csv", MIME: "text/csv", IdempotencyKey: "upload-key", Content: strings.NewReader(content)})
	if !errors.Is(err, ErrIdempotencyConflict) || originalCount(t, root) != 1 {
		t.Fatalf("conflicting upload: err=%v originals=%d", err, originalCount(t, root))
	}
}

func TestUploadTableHeaderConfirmationIsImmutableAndIdempotent(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner := f.actor(f.owner)
	content := "城市,限额\n北京,600\n"
	request := UploadRequest{KBID: base.ID, Title: "表.csv", MIME: "text/csv", IdempotencyKey: "table-key", Content: strings.NewReader(content)}
	document, err := service.Upload(f.ctx, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload, err := json.Marshal(struct {
		KBID           int64
		Title          string
		MIME           string
		ContentSHA256  [sha256.Size]byte
		AllowDuplicate bool
	}{base.ID, "表.csv", "text/csv", sha256.Sum256([]byte(content)), false})
	if err != nil {
		t.Fatal(err)
	}
	legacyHash := sha256.Sum256(legacyPayload)
	var storedHash []byte
	if err := f.admin.QueryRow(f.ctx, `SELECT request_hash FROM idempotency_record WHERE tenant_id=$1 AND request_key='table-key'`, f.tenantA).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, legacyHash[:]) {
		t.Fatalf("unconfirmed request changed the existing idempotency hash: %v", err)
	}
	request.Content = strings.NewReader(content)
	request.ConfirmFirstRowHeaders = true
	if _, err := service.Upload(f.ctx, owner, request); !errors.Is(err, ErrIdempotencyConflict) || originalCount(t, root) != 1 {
		t.Fatalf("changed confirmation replay: %v", err)
	}
	request.IdempotencyKey, request.AllowDuplicate, request.Content = "confirmed-table-key", true, strings.NewReader(content)
	confirmed, err := service.Upload(f.ctx, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Content = strings.NewReader(content)
	replay, err := service.Upload(f.ctx, owner, request)
	if err != nil || replay.VersionID != confirmed.VersionID || originalCount(t, root) != 2 {
		t.Fatalf("confirmed upload replay: %+v %v", replay, err)
	}
	var first, second bool
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT confirm_first_row_headers FROM document_version WHERE tenant_id=$1 AND id=$2),
		(SELECT confirm_first_row_headers FROM document_version WHERE tenant_id=$1 AND id=$3)`, f.tenantA, document.VersionID, confirmed.VersionID).Scan(&first, &second); err != nil || first || !second {
		t.Fatalf("stored confirmation: first=%t second=%t err=%v", first, second, err)
	}
	_, err = f.admin.Exec(f.ctx, `UPDATE document_version SET confirm_first_row_headers=false WHERE tenant_id=$1 AND id=$2`, f.tenantA, confirmed.VersionID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("confirmed version was mutable: %v", err)
	}
	if err := service.Cancel(f.ctx, owner, base.ID, confirmed.DocumentID, confirmed.VersionID); err != nil {
		t.Fatalf("cancel confirmed upload: %v", err)
	}
}

func TestDocumentUploadPermissionsAndTenantIsolation(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	request := func() UploadRequest {
		return UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", Content: strings.NewReader("列,值\n一,二\n")}
	}
	if _, err := service.Upload(f.ctx, viewer, request()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer upload: %v", err)
	}
	if _, err := service.Upload(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, request()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign upload: %v", err)
	}
	created, err := service.Upload(f.ctx, owner, request())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Archive(f.ctx, owner, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(f.ctx, owner, request()); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived upload: %v", err)
	}
	if originalCount(t, root) != 1 {
		t.Fatalf("denied uploads left originals: %d", originalCount(t, root))
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	var documents, versions int
	if err := tx.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document WHERE id=$1), (SELECT count(*) FROM document_version WHERE id=$2)`, created.DocumentID, created.VersionID).Scan(&documents, &versions); err != nil || documents != 0 || versions != 0 {
		t.Fatalf("foreign document visible: documents=%d versions=%d err=%v", documents, versions, err)
	}
	_, err = tx.Exec(f.ctx, `INSERT INTO document (tenant_id,kb_id,title,mime) VALUES ($1,$2,'foreign','text/csv')`, f.tenantA, base.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-tenant insert not denied by RLS: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(f.ctx, `INSERT INTO document (tenant_id,kb_id,title,mime) VALUES ($1,$2,'foreign','text/csv')`, f.tenantB, base.ID)
	pgErr = nil
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-tenant knowledge base foreign key not enforced: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
}

type pausedReader struct {
	once             sync.Once
	started, release chan struct{}
	input            io.Reader
}

func (r *pausedReader) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.started); <-r.release })
	return r.input.Read(data)
}

func TestDocumentUploadRechecksMembership(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner, editor := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Editor); err != nil {
		t.Fatal(err)
	}
	reader := &pausedReader{started: make(chan struct{}), release: make(chan struct{}), input: strings.NewReader("列,值\n一,二\n")}
	released := false
	t.Cleanup(func() {
		if !released {
			close(reader.release)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := service.Upload(f.ctx, editor, UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", Content: reader})
		done <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	if err := f.service.RemoveMember(f.ctx, owner, base.ID, f.other); err != nil {
		close(reader.release)
		released = true
		t.Fatal(err)
	}
	close(reader.release)
	released = true
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked editor upload: %v", err)
	}
	var documents, queued int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document WHERE tenant_id=$1), (SELECT count(*) FROM job WHERE tenant_id=$1)`, f.tenantA).Scan(&documents, &queued); err != nil || documents != 0 || queued != 0 || originalCount(t, root) != 0 {
		t.Fatalf("revoked upload persisted: documents=%d jobs=%d originals=%d err=%v", documents, queued, originalCount(t, root), err)
	}
}

func TestDocumentUploadRollsBackWhenAuditFails(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	name := fmt.Sprintf("tuskrag_test_document_audit_%d", f.tenantA)
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
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action = 'document.upload') EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	_, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", IdempotencyKey: "audit-key", Content: strings.NewReader("列,值\n一,二\n")})
	if err == nil {
		t.Fatal("upload succeeded despite audit failure")
	}
	var documents, versions, queued, records int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document WHERE tenant_id=$1), (SELECT count(*) FROM document_version WHERE tenant_id=$1), (SELECT count(*) FROM job WHERE tenant_id=$1), (SELECT count(*) FROM idempotency_record WHERE tenant_id=$1 AND operation='document.upload')`, f.tenantA).Scan(&documents, &versions, &queued, &records); err != nil || documents != 0 || versions != 0 || queued != 0 || records != 0 || originalCount(t, root) != 0 {
		t.Fatalf("unaudited upload persisted: documents=%d versions=%d jobs=%d records=%d originals=%d err=%v", documents, versions, queued, records, originalCount(t, root), err)
	}
}

func TestDocumentUploadKeepsOriginalWhenCommitOutcomeIsUncertain(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	name := fmt.Sprintf("tuskrag_test_document_commit_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON document"); err != nil {
			t.Error(err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION "+name+"()"); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE CONSTRAINT TRIGGER %s AFTER INSERT ON document DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.tenant_id = %d) EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	_, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: "file.csv", MIME: "text/csv", IdempotencyKey: "commit-key", Content: strings.NewReader("列,值\n一,二\n")})
	if !errors.Is(err, ErrCommitUncertain) || originalCount(t, root) != 1 {
		t.Fatalf("uncertain commit: err=%v originals=%d", err, originalCount(t, root))
	}
	var documents int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM document WHERE tenant_id=$1`, f.tenantA).Scan(&documents); err != nil || documents != 0 {
		t.Fatalf("commit trigger outcome: documents=%d err=%v", documents, err)
	}
}
