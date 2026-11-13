package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

type ACLMode string

const (
	ACLInheritKB  ACLMode = "inherit_kb"
	ACLRestricted ACLMode = "restricted"
)

type ACLSubjectType string

const (
	SubjectUser  ACLSubjectType = "user"
	SubjectGroup ACLSubjectType = "group"
)

type ACLSubject struct {
	Type ACLSubjectType
	ID   int64
}

type DocumentACL struct {
	DocumentID int64
	Mode       ACLMode
	Revision   int32
	Subjects   []ACLSubject
}

type UserGroup struct {
	ID        int64
	Name      string
	Source    string
	CreatedAt time.Time
}

type GroupMember struct {
	UserID      int64
	DisplayName string
	CreatedAt   time.Time
}

func (s *Service) ListGroups(ctx context.Context, actor Actor) (_ []UserGroup, resultErr error) {
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	exists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: actor.UserID})
	if err != nil {
		return nil, fmt.Errorf("check actor: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := queries.ListUserGroups(ctx, actor.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	out := make([]UserGroup, 0, len(rows))
	for _, row := range rows {
		out = append(out, UserGroup{ID: row.ID, Name: row.Name, Source: row.Source, CreatedAt: row.CreatedAt.Time})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit user group list: %w", err)
	}
	return out, nil
}

func (s *Service) CreateGroup(ctx context.Context, actor Actor, name string) (_ UserGroup, resultErr error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return UserGroup{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return UserGroup{}, err
	}
	defer rollback(tx, &resultErr)
	exists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: actor.UserID})
	if err != nil {
		return UserGroup{}, fmt.Errorf("check actor: %w", err)
	}
	if !exists {
		return UserGroup{}, ErrNotFound
	}
	row, err := queries.CreateUserGroup(ctx, dbquery.CreateUserGroupParams{
		TenantID: actor.TenantID, Name: name, Source: "manual",
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return UserGroup{}, ErrNameTaken
	}
	if err != nil {
		return UserGroup{}, fmt.Errorf("create user group: %w", err)
	}
	if err := audit(ctx, queries, actor, row.ID, 0, "", "group.create", "allow"); err != nil {
		return UserGroup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UserGroup{}, fmt.Errorf("commit user group create: %w", err)
	}
	return UserGroup{ID: row.ID, Name: row.Name, Source: row.Source, CreatedAt: row.CreatedAt.Time}, nil
}

func (s *Service) DeleteGroup(ctx context.Context, actor Actor, groupID int64) (resultErr error) {
	if groupID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	exists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: actor.UserID})
	if err != nil {
		return fmt.Errorf("check actor: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	affected, err := queries.DeleteUserGroup(ctx, dbquery.DeleteUserGroupParams{TenantID: actor.TenantID, GroupID: groupID})
	if err != nil {
		return fmt.Errorf("delete user group: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `NOTIFY acl_changed`); err != nil {
		return fmt.Errorf("notify acl_changed: %w", err)
	}
	if err := audit(ctx, queries, actor, groupID, 0, "", "group.delete", "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit user group delete: %w", err)
	}
	return nil
}

func (s *Service) ListGroupMembers(ctx context.Context, actor Actor, groupID int64) (_ []GroupMember, resultErr error) {
	if groupID <= 0 {
		return nil, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	exists, err := queries.GroupExists(ctx, dbquery.GroupExistsParams{TenantID: actor.TenantID, GroupID: groupID})
	if err != nil {
		return nil, fmt.Errorf("check group: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := queries.ListUserGroupMembers(ctx, dbquery.ListUserGroupMembersParams{TenantID: actor.TenantID, GroupID: groupID})
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	out := make([]GroupMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, GroupMember{UserID: row.UserID, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt.Time})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit group member list: %w", err)
	}
	return out, nil
}

func (s *Service) AddGroupMember(ctx context.Context, actor Actor, groupID, userID int64) (resultErr error) {
	if groupID <= 0 || userID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	gExists, err := queries.GroupExists(ctx, dbquery.GroupExistsParams{TenantID: actor.TenantID, GroupID: groupID})
	if err != nil {
		return fmt.Errorf("check group: %w", err)
	}
	uExists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: userID})
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}
	if !gExists || !uExists {
		return ErrNotFound
	}
	if err := queries.AddUserGroupMember(ctx, dbquery.AddUserGroupMemberParams{
		TenantID: actor.TenantID, GroupID: groupID, UserID: userID,
	}); err != nil {
		return fmt.Errorf("add group member: %w", err)
	}
	if _, err := tx.Exec(ctx, `NOTIFY acl_changed`); err != nil {
		return fmt.Errorf("notify acl_changed: %w", err)
	}
	if err := audit(ctx, queries, actor, groupID, userID, "", "group.member.add", "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit add group member: %w", err)
	}
	return nil
}

func (s *Service) RemoveGroupMember(ctx context.Context, actor Actor, groupID, userID int64) (resultErr error) {
	if groupID <= 0 || userID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	affected, err := queries.RemoveUserGroupMember(ctx, dbquery.RemoveUserGroupMemberParams{
		TenantID: actor.TenantID, GroupID: groupID, UserID: userID,
	})
	if err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `NOTIFY acl_changed`); err != nil {
		return fmt.Errorf("notify acl_changed: %w", err)
	}
	if err := audit(ctx, queries, actor, groupID, userID, "", "group.member.remove", "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove group member: %w", err)
	}
	return nil
}

func (s *Service) GetDocumentACL(ctx context.Context, actor Actor, kbID, docID int64) (_ DocumentACL, resultErr error) {
	if kbID <= 0 || docID <= 0 {
		return DocumentACL{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return DocumentACL{}, err
	}
	defer rollback(tx, &resultErr)
	if err := requireAdminOrOwner(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return DocumentACL{}, deny(ctx, tx, queries, actor, kbID, 0, "", "document.acl.read", err)
		}
		return DocumentACL{}, err
	}
	doc, err := queries.LockDocumentForACL(ctx, dbquery.LockDocumentForACLParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DocumentACL{}, ErrNotFound
	}
	if err != nil {
		return DocumentACL{}, fmt.Errorf("lock document for acl read: %w", err)
	}
	rows, err := queries.GetDocumentACL(ctx, dbquery.GetDocumentACLParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID,
	})
	if err != nil {
		return DocumentACL{}, fmt.Errorf("read document acl: %w", err)
	}
	subjects := make([]ACLSubject, 0, len(rows))
	for _, row := range rows {
		subjects = append(subjects, ACLSubject{Type: ACLSubjectType(row.SubjectType), ID: row.SubjectID})
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentACL{}, fmt.Errorf("commit document acl read: %w", err)
	}
	return DocumentACL{DocumentID: doc.ID, Mode: ACLMode(doc.AclMode), Revision: doc.AclRevision, Subjects: subjects}, nil
}

func (s *Service) SetDocumentACL(ctx context.Context, actor Actor, kbID, docID int64, mode ACLMode, subjects []ACLSubject) (_ DocumentACL, resultErr error) {
	if kbID <= 0 || docID <= 0 || (mode != ACLInheritKB && mode != ACLRestricted) {
		return DocumentACL{}, ErrInvalidRequest
	}
	if mode == ACLInheritKB {
		subjects = nil
	}
	deduped := make([]ACLSubject, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	for _, sub := range subjects {
		if sub.ID <= 0 || (sub.Type != SubjectUser && sub.Type != SubjectGroup) {
			return DocumentACL{}, ErrInvalidRequest
		}
		key := string(sub.Type) + ":" + strconv.FormatInt(sub.ID, 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, sub)
	}

	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return DocumentACL{}, err
	}
	defer rollback(tx, &resultErr)
	if err := requireAdminOrOwner(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return DocumentACL{}, deny(ctx, tx, queries, actor, kbID, 0, "", "document.acl.set", err)
		}
		return DocumentACL{}, err
	}
	if _, err := queries.LockDocumentForACL(ctx, dbquery.LockDocumentForACLParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return DocumentACL{}, ErrNotFound
	} else if err != nil {
		return DocumentACL{}, fmt.Errorf("lock document for acl: %w", err)
	}

	for _, sub := range deduped {
		var ok bool
		if sub.Type == SubjectUser {
			ok, err = queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: sub.ID})
		} else {
			ok, err = queries.GroupExists(ctx, dbquery.GroupExistsParams{TenantID: actor.TenantID, GroupID: sub.ID})
		}
		if err != nil {
			return DocumentACL{}, fmt.Errorf("verify acl subject: %w", err)
		}
		if !ok {
			return DocumentACL{}, ErrNotFound
		}
	}

	rev, err := queries.UpdateDocumentACLMode(ctx, dbquery.UpdateDocumentACLModeParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID, AclMode: string(mode),
	})
	if err != nil {
		return DocumentACL{}, fmt.Errorf("update document acl mode: %w", err)
	}
	if err := queries.DeleteDocumentACL(ctx, dbquery.DeleteDocumentACLParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID,
	}); err != nil {
		return DocumentACL{}, fmt.Errorf("clear document acl: %w", err)
	}
	for _, sub := range deduped {
		if err := queries.InsertDocumentACL(ctx, dbquery.InsertDocumentACLParams{
			TenantID: actor.TenantID, KbID: kbID, DocumentID: docID, SubjectType: string(sub.Type), SubjectID: sub.ID,
		}); err != nil {
			return DocumentACL{}, fmt.Errorf("insert document acl: %w", err)
		}
	}

	accessTags := computeDocumentAccessTags(kbID, docID, mode, deduped)
	if err := queries.RefreshChunkAccessTags(ctx, dbquery.RefreshChunkAccessTagsParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID, AccessTags: accessTags,
	}); err != nil {
		return DocumentACL{}, fmt.Errorf("refresh chunk access_tags: %w", err)
	}
	if err := queries.RefreshChunkVectorAccessTags(ctx, dbquery.RefreshChunkVectorAccessTagsParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: docID, AccessTags: accessTags,
	}); err != nil {
		return DocumentACL{}, fmt.Errorf("refresh chunk_vector access_tags: %w", err)
	}

	payload, err := json.Marshal(struct {
		KbID        int64 `json:"kb_id"`
		DocumentID  int64 `json:"document_id"`
		ACLRevision int32 `json:"acl_revision"`
	}{KbID: kbID, DocumentID: docID, ACLRevision: rev})
	if err != nil {
		return DocumentACL{}, fmt.Errorf("marshal acl_refresh payload: %w", err)
	}
	if _, err := queries.EnqueueACLRefreshJob(ctx, dbquery.EnqueueACLRefreshJobParams{
		TenantID:  actor.TenantID,
		DedupeKey: pgtype.Text{String: fmt.Sprintf("acl:%d:%d", kbID, docID), Valid: true},
		Payload:   payload,
	}); err != nil {
		return DocumentACL{}, fmt.Errorf("enqueue acl_refresh job: %w", err)
	}
	if _, err := tx.Exec(ctx, `NOTIFY acl_changed`); err != nil {
		return DocumentACL{}, fmt.Errorf("notify acl_changed: %w", err)
	}
	if err := audit(ctx, queries, actor, docID, 0, "", "document.acl.set", "allow"); err != nil {
		return DocumentACL{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentACL{}, fmt.Errorf("commit document acl: %w", err)
	}
	return DocumentACL{DocumentID: docID, Mode: mode, Revision: rev, Subjects: deduped}, nil
}

func computeDocumentAccessTags(kbID, docID int64, mode ACLMode, subjects []ACLSubject) []string {
	if mode == ACLInheritKB {
		return []string{"kb:" + strconv.FormatInt(kbID, 10) + ":all"}
	}
	if len(subjects) == 0 {
		return []string{"doc:" + strconv.FormatInt(docID, 10) + ":none"}
	}
	tags := make([]string, 0, len(subjects))
	for _, sub := range subjects {
		if sub.Type == SubjectUser {
			tags = append(tags, "u:"+strconv.FormatInt(sub.ID, 10))
		} else {
			tags = append(tags, "g:"+strconv.FormatInt(sub.ID, 10))
		}
	}
	return tags
}

func requireAdminOrOwner(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID int64) error {
	if _, err := queries.LockVisibleKnowledgeBase(ctx, dbquery.LockVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID}); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock knowledge base: %w", err)
	}
	role, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock actor membership: %w", err)
	}
	if Role(role) != Owner && Role(role) != Admin {
		return ErrForbidden
	}
	return nil
}
