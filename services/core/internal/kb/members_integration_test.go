package kb

import (
	"errors"
	"testing"

	"tuskrag.local/core/internal/models"
)

func TestMembersAndEgressPolicyPostgres(t *testing.T) {
	f := setup(t)

	created, err := f.service.Create(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, CreateRequest{
		Name:             "Members & Egress KB",
		EmbeddingSpaceID: f.spaceA,
		EgressPolicy:     models.Deny,
	})
	if err != nil {
		t.Fatalf("Create KB: %v", err)
	}

	if err := f.service.SetMember(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, f.other, Viewer); err != nil {
		t.Fatalf("SetMember: %v", err)
	}

	members, err := f.service.ListMembers(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID)
	if err != nil {
		t.Fatalf("ListMembers(owner): %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	if _, err := f.service.ListMembers(f.ctx, Actor{TenantID: f.tenantA, UserID: f.other}, created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for viewer ListMembers, got %v", err)
	}

	if _, err := f.service.ListMembers(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-tenant ListMembers, got %v", err)
	}

	users, err := f.service.ListTenantUsers(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner})
	if err != nil {
		t.Fatalf("ListTenantUsers: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 tenantA users, got %d", len(users))
	}
	for _, u := range users {
		if u.ID == f.foreignUser {
			t.Fatalf("cross-tenant user leaked in ListTenantUsers")
		}
	}

	// Tenant A max is deny (default), so internal_only must be denied.
	if _, err := f.service.SetEgressPolicy(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, models.InternalOnly); !errors.Is(err, models.ErrEgressDenied) {
		t.Fatalf("expected ErrEgressDenied, got %v", err)
	}

	// Viewer cannot set egress policy.
	if _, err := f.service.SetEgressPolicy(f.ctx, Actor{TenantID: f.tenantA, UserID: f.other}, created.ID, models.Deny); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for viewer SetEgressPolicy, got %v", err)
	}

	// Owner can set egress policy within tenant max.
	updated, err := f.service.SetEgressPolicy(f.ctx, Actor{TenantID: f.tenantA, UserID: f.owner}, created.ID, models.Deny)
	if err != nil {
		t.Fatalf("SetEgressPolicy(deny): %v", err)
	}
	if updated.EgressPolicy != models.Deny || updated.Role != Owner {
		t.Fatalf("unexpected updated KB: %+v", updated)
	}
}
