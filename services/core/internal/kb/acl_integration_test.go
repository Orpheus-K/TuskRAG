package kb

import (
	"errors"
	"testing"

	"tuskrag.local/core/internal/models"
)

func TestUserGroupsAndDocumentACLPostgres(t *testing.T) {
	f := setup(t)

	created, err := f.service.Create(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, CreateRequest{
		Name:             "ACL KB",
		EmbeddingSpaceID: f.spaceA,
		EgressPolicy:     models.Deny,
	})
	if err != nil {
		t.Fatalf("Create KB: %v", err)
	}
	if err := f.service.SetMember(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, f.other, Viewer); err != nil {
		t.Fatalf("SetMember(other=viewer): %v", err)
	}
	if err := f.service.SetMember(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, f.third, Viewer); err != nil {
		t.Fatalf("SetMember(third=viewer): %v", err)
	}

	var docID int64
	if err := f.admin.QueryRow(f.ctx, `
		INSERT INTO document (tenant_id, kb_id, title, mime)
		VALUES ($1, $2, 'Restricted Policy', 'text/plain')
		RETURNING id`, f.tenantA, created.ID).Scan(&docID); err != nil {
		t.Fatalf("insert document: %v", err)
	}
	var verID int64
	if err := f.admin.QueryRow(f.ctx, `
		INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by)
		VALUES ($1, $2, $3, decode('0000000000000000000000000000000000000000000000000000000000000001', 'hex'), 12, 'k', 'ready', $4)
		RETURNING id`, f.tenantA, created.ID, docID, f.owner).Scan(&verID); err != nil {
		t.Fatalf("insert document_version: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id = $1 WHERE tenant_id = $2 AND id = $3`, verID, f.tenantA, docID); err != nil {
		t.Fatalf("set current_version_id: %v", err)
	}

	group, err := f.service.CreateGroup(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, "security-team")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := f.service.AddGroupMember(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, group.ID, f.third); err != nil {
		t.Fatalf("AddGroupMember: %v", err)
	}

	// Viewer cannot set document ACL.
	if _, err := f.service.SetDocumentACL(f.ctx, Actor{TenantID: f.tenantA, UserID: f.other}, created.ID, docID, ACLRestricted, []ACLSubject{{Type: SubjectGroup, ID: group.ID}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for viewer SetDocumentACL, got %v", err)
	}

	// Owner restricts document to security-team group.
	acl, err := f.service.SetDocumentACL(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, docID, ACLRestricted, []ACLSubject{{Type: SubjectGroup, ID: group.ID}})
	if err != nil {
		t.Fatalf("SetDocumentACL: %v", err)
	}
	if acl.Mode != ACLRestricted || acl.Revision != 2 || len(acl.Subjects) != 1 {
		t.Fatalf("unexpected ACL: %+v", acl)
	}

	// T08: f.other (not in group) cannot see the document in ListDocuments.
	otherDocs, err := f.service.ListDocuments(f.ctx, Actor{TenantID: f.tenantA, UserID: f.other}, created.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListDocuments(other): %v", err)
	}
	if len(otherDocs.Items) != 0 {
		t.Fatalf("expected 0 visible docs for unauthorized user, got %d", len(otherDocs.Items))
	}

	// f.third (in group) CAN see the document in ListDocuments.
	thirdDocs, err := f.service.ListDocuments(f.ctx, Actor{TenantID: f.tenantA, UserID: f.third}, created.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListDocuments(third): %v", err)
	}
	if len(thirdDocs.Items) != 1 || thirdDocs.Items[0].ID != docID {
		t.Fatalf("expected doc %d visible for group member, got %+v", docID, thirdDocs.Items)
	}

	// T09: Removing f.third from the group revokes access immediately.
	if err := f.service.RemoveGroupMember(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, group.ID, f.third); err != nil {
		t.Fatalf("RemoveGroupMember: %v", err)
	}
	thirdDocsAfter, err := f.service.ListDocuments(f.ctx, Actor{TenantID: f.tenantA, UserID: f.third}, created.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListDocuments(third after removal): %v", err)
	}
	if len(thirdDocsAfter.Items) != 0 {
		t.Fatalf("expected 0 visible docs after group removal, got %d", len(thirdDocsAfter.Items))
	}
}
