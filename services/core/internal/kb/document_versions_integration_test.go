package kb

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDocumentVersionUploadIdentityAndIdempotency(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner := f.actor(f.owner)
	content := "城市,限额\n北京,600\n"
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "原始.csv", MIME: "text/csv", Content: strings.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	request := UploadRequest{KBID: base.ID, DocumentID: original.DocumentID, Title: "修订.csv", MIME: "text/csv",
		IdempotencyKey: "version-key", ConfirmFirstRowHeaders: true, Content: strings.NewReader(content)}
	version, err := service.Upload(f.ctx, owner, request)
	if err != nil || version.DocumentID != original.DocumentID || version.VersionID == original.VersionID || version.Title != original.Title || version.Status != "queued" {
		t.Fatalf("confirmed version: %+v err=%v", version, err)
	}
	request.Content = strings.NewReader(content)
	replay, err := service.Upload(f.ctx, owner, request)
	if err != nil || replay.VersionID != version.VersionID || originalCount(t, root) != 2 {
		t.Fatalf("version replay: %+v originals=%d err=%v", replay, originalCount(t, root), err)
	}
	request.Content = strings.NewReader("城市,限额\n上海,700\n")
	if _, err := service.Upload(f.ctx, owner, request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed version reused key: %v", err)
	}
	request.IdempotencyKey = ""
	request.AllowDuplicate = true
	request.Content = strings.NewReader(content)
	if _, err := service.Upload(f.ctx, owner, request); !errors.Is(err, ErrDuplicateContent) {
		t.Fatalf("same document content and decision accepted twice: %v", err)
	}
	request.MIME, request.Content = "text/plain", strings.NewReader("different")
	if _, err := service.Upload(f.ctx, owner, request); !errors.Is(err, ErrVersionFormat) {
		t.Fatalf("version format changed: %v", err)
	}
	var documents, versions, jobs int
	var currentIsNull bool
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM document WHERE tenant_id=$1),
		(SELECT count(*) FROM document_version WHERE tenant_id=$1),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='ingest'),
		(SELECT current_version_id IS NULL FROM document WHERE tenant_id=$1 AND id=$2)`, f.tenantA, original.DocumentID).
		Scan(&documents, &versions, &jobs, &currentIsNull); err != nil || documents != 1 || versions != 2 || jobs != 2 || !currentIsNull || originalCount(t, root) != 2 {
		t.Fatalf("version transaction: documents=%d versions=%d jobs=%d noCurrent=%t err=%v", documents, versions, jobs, currentIsNull, err)
	}
}

func TestDocumentVersionUploadConcurrentAndCrossDocumentDuplicates(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner := f.actor(f.owner)
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "原始.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct{ err error }
	start, results := make(chan struct{}), make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, DocumentID: original.DocumentID, Title: "修订.csv", MIME: "text/csv", Content: strings.NewReader("列\n二\n")})
			results <- outcome{err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	switch {
	case first.err == nil && errors.Is(second.err, ErrDuplicateContent):
	case second.err == nil && errors.Is(first.err, ErrDuplicateContent):
	default:
		t.Fatalf("concurrent duplicate: first=%v second=%v", first.err, second.err)
	}
	if originalCount(t, root) != 2 {
		t.Fatalf("concurrent version left extra originals: %d", originalCount(t, root))
	}
	otherContent := "列\n三\n"
	if _, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "其他.csv", MIME: "text/csv", Content: strings.NewReader(otherContent)}); err != nil {
		t.Fatal(err)
	}
	request := UploadRequest{KBID: base.ID, DocumentID: original.DocumentID, Title: "修订.csv", MIME: "text/csv", Content: strings.NewReader(otherContent)}
	if _, err := service.Upload(f.ctx, owner, request); !errors.Is(err, ErrDuplicateContent) {
		t.Fatalf("duplicate from another document accepted: %v", err)
	}
	request.AllowDuplicate, request.Content = true, strings.NewReader(otherContent)
	if _, err := service.Upload(f.ctx, owner, request); err != nil {
		t.Fatalf("explicit cross-document duplicate rejected: %v", err)
	}
}

func TestDocumentVersionUploadRechecksAuthorization(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, root := documentFixture(t, f)
	owner, editor := f.actor(f.owner), f.actor(f.other)
	original, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "原始.csv", MIME: "text/csv", Content: strings.NewReader("列\n一\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthorizeVersionUpload(f.ctx, editor, base.ID, original.DocumentID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer version upload: %v", err)
	}
	if err := service.AuthorizeVersionUpload(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, base.ID, original.DocumentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign tenant version upload: %v", err)
	}
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Editor); err != nil {
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
		_, err := service.Upload(f.ctx, editor, UploadRequest{KBID: base.ID, DocumentID: original.DocumentID, Title: "修订.csv", MIME: "text/csv", Content: reader})
		done <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("version upload did not start")
	}
	if err := f.service.RemoveMember(f.ctx, owner, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	close(reader.release)
	released = true
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked editor version upload: %v", err)
	}
	var versions, jobs int
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM document_version WHERE tenant_id=$1),
		(SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='ingest')`, f.tenantA).Scan(&versions, &jobs); err != nil || versions != 1 || jobs != 1 || originalCount(t, root) != 1 {
		t.Fatalf("revoked upload persisted: versions=%d jobs=%d originals=%d err=%v", versions, jobs, originalCount(t, root), err)
	}
}
