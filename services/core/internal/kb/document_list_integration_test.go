package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestListDocumentsVisibilityPaginationAndVersions(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner := f.actor(f.owner)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	docs := make([]UploadedDocument, 0, 4)
	for i := range 4 {
		doc, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: fmt.Sprintf("file-%d.csv", i), MIME: "text/csv", Content: strings.NewReader(fmt.Sprintf("col\n%d\n", i))})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, doc)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, docs[1].DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=now() WHERE id=$1`, docs[2].DocumentID); err != nil {
		t.Fatal(err)
	}
	var latest int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version
		(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,failure_code,failure_detail,created_by)
		VALUES ($1,$2,$3,decode(repeat('ab',32),'hex'),4,$4,'failed','parse_error','private diagnostic',$5) RETURNING id`,
		f.tenantA, base.ID, docs[0].DocumentID, fmt.Sprintf("test/%d", docs[0].DocumentID), f.owner).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='ready' WHERE id=$1`, docs[0].VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE id=$2`, docs[0].VersionID, docs[0].DocumentID); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Actor{owner, f.actor(f.other)} {
		first, err := f.service.ListDocuments(f.ctx, actor, base.ID, 0, 1)
		if err != nil || len(first.Items) != 1 || first.NextAfter == nil || *first.NextAfter != docs[0].DocumentID {
			t.Fatalf("first page: %+v err=%v", first, err)
		}
		item := first.Items[0]
		if item.ID != docs[0].DocumentID || item.CurrentVersionID == nil || *item.CurrentVersionID != docs[0].VersionID ||
			item.LatestVersionID != latest || item.LatestStatus != "failed" || item.FailureCode == nil || *item.FailureCode != "parse_error" {
			t.Fatalf("version state: %+v", item)
		}
		second, err := f.service.ListDocuments(f.ctx, actor, base.ID, *first.NextAfter, 1)
		if err != nil || len(second.Items) != 1 || second.Items[0].ID != docs[3].DocumentID || second.NextAfter != nil {
			t.Fatalf("second page: %+v err=%v", second, err)
		}
	}
	for _, actor := range []Actor{f.actor(f.third), {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if _, err := f.service.ListDocuments(f.ctx, actor, base.ID, 0, 10); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unauthorized list returned %v", err)
		}
	}
	for _, params := range []struct {
		kb, after int64
		limit     int32
	}{{0, 0, 1}, {base.ID, -1, 1}, {base.ID, 0, 0}, {base.ID, 0, 101}} {
		if _, err := f.service.ListDocuments(f.ctx, owner, params.kb, params.after, params.limit); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid page accepted: %+v err=%v", params, err)
		}
	}
	if err := f.service.Archive(f.ctx, owner, base.ID); err != nil {
		t.Fatal(err)
	}
	if page, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 10); err != nil || len(page.Items) != 2 {
		t.Fatalf("archived base listing: %+v err=%v", page, err)
	}
	corrupt, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = corrupt.Rollback(context.Background()) }()
	// Simulate pre-existing corruption despite the current-version guard.
	if _, err := corrupt.Exec(f.ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := corrupt.Exec(f.ctx, `UPDATE document_version SET status='failed' WHERE id=$1`, docs[0].VersionID); err != nil {
		t.Fatal(err)
	}
	if err := corrupt.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 10); err == nil {
		t.Fatal("failed current version was presented as searchable")
	}
}

func TestListDocumentsWaitsForMembershipRevocation(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := f.service.ListDocuments(f.ctx, viewer, base.ID, 0, 10)
		done <- err
	}()
	<-started
	waitForListingLock(t, f, done, "LockMemberRole")
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked viewer listing: %v", err)
	}
}

func waitForListingLock(t *testing.T, f *fixture, done <-chan error, query string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting bool
		if err := f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=current_user AND wait_event_type='Lock' AND query LIKE $1)`, "%"+query+"%").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("listing returned before revocation committed: %v", err)
		case <-deadline.C:
			t.Fatalf("listing did not wait for %s", query)
		case <-ticker.C:
		}
	}
}

func TestListDocumentsRechecksChangedDocumentAccess(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner := f.actor(f.owner)
	doc, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "secret.csv", MIME: "text/csv", Content: strings.NewReader("secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, Title: "visible.csv", MIME: "text/csv", Content: strings.NewReader("visible\n")})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err := tx.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, f.tenantA, doc.DocumentID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		page, err := f.service.ListDocuments(f.ctx, owner, base.ID, 0, 1)
		if err == nil && (len(page.Items) != 1 || page.Items[0].ID != visible.DocumentID || page.NextAfter != nil) {
			err = fmt.Errorf("wrong listing after access change: %+v", page)
		}
		done <- err
	}()
	waitForListingLock(t, f, done, "ListVisibleDocuments")
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestListDocumentsFailsWhenVersionIsMissing(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO document (tenant_id,kb_id,title,mime) VALUES ($1,$2,'incomplete','text/csv')`, f.tenantA, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ListDocuments(f.ctx, f.actor(f.owner), base.ID, 0, 10); err == nil {
		t.Fatal("missing version was presented as an empty document list")
	}
}
