package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func addReadableSection(t *testing.T, f *fixture, kbID int64, document UploadedDocument, ordinal int, parent *int64, heading, body string) int64 {
	t.Helper()
	level := 1
	path := []string{heading}
	if parent != nil {
		level = 2
		path = []string{"总则", heading}
	}
	var section int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO section
		(tenant_id,kb_id,document_id,version_id,parent_section_id,ordinal,level,heading,heading_path,page_start,page_end,body_md,token_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$6,$6,$10,4) RETURNING id`, f.tenantA, kbID, document.DocumentID,
		document.VersionID, parent, ordinal, level, heading, path, body).Scan(&section); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk
		(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,page_start,page_end,token_count,is_current,access_tags,doc_mime,doc_updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,'text',$7,$7,$6,$6,4,true,ARRAY[$8]::text[],$9,clock_timestamp())`,
		f.tenantA, kbID, document.DocumentID, document.VersionID, section, ordinal, body, fmt.Sprintf("kb:%d:all", kbID), document.MIME); err != nil {
		t.Fatal(err)
	}
	return section
}

func addReadablePlan(t *testing.T, f *fixture, kbID int64, document UploadedDocument, count int) {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO ingest_plan
		(tenant_id,kb_id,document_id,version_id,embedding_space_id,config_revision,plan_sha256,section_count,chunk_count)
		VALUES ($1,$2,$3,$4,$5,1,decode(repeat('01',32),'hex'),$6,$6)`, f.tenantA, kbID, document.DocumentID, document.VersionID, f.spaceA, count); err != nil {
		t.Fatal(err)
	}
}

func prepareReadableDocument(t *testing.T, f *fixture, service *DocumentService, kbID int64) (UploadedDocument, int64, int64) {
	t.Helper()
	document, err := service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: kbID, Title: "synthetic.pdf", MIME: "application/pdf", Content: strings.NewReader("synthetic pdf")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM ingest_plan WHERE tenant_id=$1`, f.tenantA); err != nil {
			t.Errorf("clean read plans: %v", err)
		}
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM chunk WHERE tenant_id=$1`, f.tenantA); err != nil {
			t.Errorf("clean read chunks: %v", err)
		}
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM section WHERE tenant_id=$1`, f.tenantA); err != nil {
			t.Errorf("clean read sections: %v", err)
		}
	})
	first := addReadableSection(t, f, kbID, document, 1, nil, "总则", "第一节内容。")
	second := addReadableSection(t, f, kbID, document, 2, &first, "条款", "第二节内容。")
	addReadablePlan(t, f, kbID, document, 2)
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='ready' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$2 WHERE tenant_id=$1 AND id=$3`, f.tenantA, document.VersionID, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	return document, first, second
}

func TestDocumentContentsAndSectionAccess(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	document, first, second := prepareReadableDocument(t, f, service, base.ID)
	for _, actor := range []Actor{owner, viewer} {
		page, err := f.service.DocumentContents(f.ctx, actor, document.DocumentID, 0, 1)
		if err != nil || page.VersionID != document.VersionID || len(page.Sections) != 1 || page.Sections[0].ID != first || page.NextAfter == nil || *page.NextAfter != 1 || page.Sections[0].PageStart == nil || *page.Sections[0].PageStart != 1 {
			t.Fatalf("first contents page: %+v err=%v", page, err)
		}
		page, err = f.service.DocumentContents(f.ctx, actor, document.DocumentID, *page.NextAfter, 1)
		if err != nil || len(page.Sections) != 1 || page.Sections[0].ID != second || page.Sections[0].ParentID == nil || *page.Sections[0].ParentID != first || page.NextAfter != nil {
			t.Fatalf("second contents page: %+v err=%v", page, err)
		}
		section, err := f.service.ReadSection(f.ctx, actor, second)
		if err != nil || section.DocumentID != document.DocumentID || section.VersionID != document.VersionID || section.Markdown != "第二节内容。" || section.Section.PageStart == nil || *section.Section.PageStart != 2 {
			t.Fatalf("section: %+v err=%v", section, err)
		}
	}
	for _, actor := range []Actor{f.actor(f.third), {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if result, err := f.service.DocumentContents(f.ctx, actor, document.DocumentID, 0, 50); !errors.Is(err, ErrNotFound) || len(result.Sections) != 0 {
			t.Fatalf("unauthorized contents leaked: %+v err=%v", result, err)
		}
		if result, err := f.service.ReadSection(f.ctx, actor, second); !errors.Is(err, ErrNotFound) || result.Markdown != "" {
			t.Fatalf("unauthorized section leaked: %+v err=%v", result, err)
		}
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET access_tags=ARRAY['other']::text[] WHERE tenant_id=$1 AND section_id=$2`, f.tenantA, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReadSection(f.ctx, owner, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("section with hidden document chunks became visible: %v", err)
	}
	if _, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("contents with hidden document chunks became visible: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET access_tags=ARRAY[$3]::text[] WHERE tenant_id=$1 AND section_id=$2`, f.tenantA, second, fmt.Sprintf("kb:%d:all", base.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET disabled=true,is_current=false WHERE tenant_id=$1 AND section_id=$2`, f.tenantA, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReadSection(f.ctx, owner, second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled section content became visible: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET disabled=false,is_current=true WHERE tenant_id=$1 AND section_id=$2`, f.tenantA, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restricted document contents became visible: %v", err)
	}
	if _, err := f.service.ReadSection(f.ctx, owner, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restricted document section became visible: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='inherit_kb' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReadSection(f.ctx, owner, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted document section became visible: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=NULL WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE section SET heading=repeat('a',8193),heading_path=ARRAY[repeat('a',8193)] WHERE tenant_id=$1 AND id=$2`, f.tenantA, first); err != nil {
		t.Fatal(err)
	}
	if contents, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 50); !errors.Is(err, ErrContentLimit) || len(contents.Sections) != 0 {
		t.Fatalf("oversized heading returned partial contents: %+v err=%v", contents, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE section SET heading='总则',heading_path=ARRAY['总则'] WHERE tenant_id=$1 AND id=$2`, f.tenantA, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE section SET body_md=repeat('a',1048577) WHERE tenant_id=$1 AND id=$2`, f.tenantA, first); err != nil {
		t.Fatal(err)
	}
	if section, err := f.service.ReadSection(f.ctx, owner, first); !errors.Is(err, ErrContentLimit) || section.Markdown != "" {
		t.Fatalf("oversized section returned partial contents: %+v err=%v", section, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE section SET body_md='第一节内容。' WHERE tenant_id=$1 AND id=$2`, f.tenantA, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 51); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid contents page limit: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE ingest_plan SET chunk_count=3 WHERE tenant_id=$1 AND version_id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if section, err := f.service.ReadSection(f.ctx, owner, first); err == nil || section.Markdown != "" {
		t.Fatalf("incomplete plan returned section content: %+v err=%v", section, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE ingest_plan SET chunk_count=2 WHERE tenant_id=$1 AND version_id=$2`, f.tenantA, document.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET mime='text/plain' WHERE tenant_id=$1 AND id=$2`, f.tenantA, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	section, err := f.service.ReadSection(f.ctx, owner, first)
	if err != nil || section.Section.PageStart != nil || section.Section.PageEnd != nil {
		t.Fatalf("logical page was reported as physical: %+v err=%v", section.Section, err)
	}
	var allowed, denied int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		count(*) FILTER (WHERE result='allow'),count(*) FILTER (WHERE result='deny') FROM audit_log
		WHERE tenant_id=$1 AND action IN ('document.toc','section.read')`, f.tenantA).Scan(&allowed, &denied); err != nil || allowed != 7 || denied < 4 {
		t.Fatalf("content audit allow=%d deny=%d err=%v", allowed, denied, err)
	}
}

func TestDocumentContentsWaitsForRevocation(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	document, _, _ := prepareReadableDocument(t, f, service, base.ID)
	gate, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	if _, err := gate.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		contents, err := f.service.DocumentContents(f.ctx, viewer, document.DocumentID, 0, 50)
		if len(contents.Sections) != 0 {
			done <- errors.New("revoked contents were returned")
			return
		}
		done <- err
	}()
	waitForListingLock(t, f, done, "LockMemberRole")
	if err := gate.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked contents result: %v", err)
	}
}

func TestDocumentContentsUsesNewCurrentVersionAfterConcurrentSwitch(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	owner := f.actor(f.owner)
	document, oldSection, _ := prepareReadableDocument(t, f, service, base.ID)
	revision, err := service.Upload(f.ctx, owner, UploadRequest{KBID: base.ID, DocumentID: document.DocumentID, Title: "synthetic.pdf", MIME: "application/pdf", Content: strings.NewReader("synthetic revision")})
	if err != nil {
		t.Fatal(err)
	}
	newSection := addReadableSection(t, f, base.ID, revision, 1, nil, "新版", "新版正文。")
	addReadablePlan(t, f, base.ID, revision, 1)
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='ready' WHERE tenant_id=$1 AND id=$2`, f.tenantA, revision.VersionID); err != nil {
		t.Fatal(err)
	}
	gate, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	if _, err := gate.Exec(f.ctx, `UPDATE document SET current_version_id=$2 WHERE tenant_id=$1 AND id=$3`, f.tenantA, revision.VersionID, document.DocumentID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		contents, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 50)
		if err == nil && (contents.VersionID != revision.VersionID || len(contents.Sections) != 1 || contents.Sections[0].ID != newSection) {
			err = fmt.Errorf("stale contents after version switch: %+v", contents)
		}
		done <- err
	}()
	waitForListingLock(t, f, done, "LockReadableDocument")
	if err := gate.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if section, err := f.service.ReadSection(f.ctx, owner, oldSection); !errors.Is(err, ErrNotFound) || section.Markdown != "" {
		t.Fatalf("old section remained readable: %+v err=%v", section, err)
	}
}

func TestContentAuditFailureReturnsNoContent(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, _, _ := documentFixture(t, f)
	document, sectionID, _ := prepareReadableDocument(t, f, service, base.ID)
	name := fmt.Sprintf("tuskrag_test_content_audit_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON audit_log"); err != nil {
			t.Error(err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+name+"()"); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action IN ('document.toc', 'section.read')) EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	owner := f.actor(f.owner)
	if result, err := f.service.DocumentContents(f.ctx, owner, document.DocumentID, 0, 50); err == nil || len(result.Sections) != 0 {
		t.Fatalf("audit failure exposed contents: %+v err=%v", result, err)
	}
	if result, err := f.service.ReadSection(f.ctx, owner, sectionID); err == nil || result.Markdown != "" {
		t.Fatalf("audit failure exposed section: %+v err=%v", result, err)
	}
	if result, err := f.service.DocumentContents(f.ctx, f.actor(f.third), document.DocumentID, 0, 50); err == nil || errors.Is(err, ErrNotFound) || len(result.Sections) != 0 {
		t.Fatalf("audit failure hid a denied read error: %+v err=%v", result, err)
	}
}
