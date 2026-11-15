package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

func ValidTrustTier(tier string) bool {
	return tier == "trusted" || tier == "standard" || tier == "untrusted"
}

func (s *Service) SetKnowledgeBaseTrustTier(ctx context.Context, actor Actor, kbID int64, tier string) error {
	if !ValidTrustTier(tier) || kbID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &err)

	role, err := queries.GetMemberRole(ctx, dbquery.GetMemberRoleParams{
		TenantID: actor.TenantID,
		KbID:     kbID,
		UserID:   actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check kb role: %w", err)
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	updated, err := queries.SetKnowledgeBaseTrustTier(ctx, dbquery.SetKnowledgeBaseTrustTierParams{
		TenantID:  actor.TenantID,
		KbID:      kbID,
		TrustTier: tier,
	})
	if err != nil {
		return fmt.Errorf("set kb trust tier: %w", err)
	}
	if updated == 0 {
		return ErrNotFound
	}
	if err := queries.AuditKnowledgeBaseAction(ctx, dbquery.AuditKnowledgeBaseActionParams{
		TenantID: actor.TenantID,
		UserID:   actor.UserID,
		Action:   "kb.trust_tier.update",
		KbID:     kbID,
		NewRole:  pgtype.Text{String: tier, Valid: true},
		Result:   "allow",
	}); err != nil {
		return fmt.Errorf("audit kb trust tier: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Service) SetDocumentTrustTier(ctx context.Context, actor Actor, kbID, docID int64, tier string) error {
	if tier != "" && !ValidTrustTier(tier) {
		return ErrInvalidRequest
	}
	if kbID <= 0 || docID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &err)

	role, err := queries.GetMemberRole(ctx, dbquery.GetMemberRoleParams{
		TenantID: actor.TenantID,
		KbID:     kbID,
		UserID:   actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check kb role: %w", err)
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	var nullableTier pgtype.Text
	if tier != "" {
		nullableTier = pgtype.Text{String: tier, Valid: true}
	}
	updated, err := queries.SetDocumentTrustTier(ctx, dbquery.SetDocumentTrustTierParams{
		TenantID:   actor.TenantID,
		KbID:       kbID,
		DocumentID: docID,
		TrustTier:  nullableTier,
	})
	if err != nil {
		return fmt.Errorf("set document trust tier: %w", err)
	}
	if updated == 0 {
		return ErrNotFound
	}
	if err := queries.AuditKnowledgeBaseAction(ctx, dbquery.AuditKnowledgeBaseActionParams{
		TenantID: actor.TenantID,
		UserID:   actor.UserID,
		Action:   "document.trust_tier.update",
		KbID:     kbID,
		NewRole:  pgtype.Text{String: tier, Valid: tier != ""},
		Result:   "allow",
	}); err != nil {
		return fmt.Errorf("audit document trust tier: %w", err)
	}
	return tx.Commit(ctx)
}
