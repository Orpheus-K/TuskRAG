package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

// ResolveEmbeddingScope is for an already-authorized service operation.
// It does not authorize a user to read the knowledge base or its documents.
func (s *Service) ResolveEmbeddingScope(ctx context.Context, binding models.EmbeddingBinding) (_ models.EgressScope, resultErr error) {
	if s == nil || s.pool == nil || ctx == nil || !validEmbeddingBinding(binding) {
		return models.EgressScope{}, ErrInvalidRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return models.EgressScope{}, fmt.Errorf("begin embedding scope transaction: %w", err)
	}
	defer rollback(tx, &resultErr)
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, binding.TenantID); err != nil {
		return models.EgressScope{}, fmt.Errorf("set embedding scope tenant: %w", err)
	}
	scope, err := readEmbeddingScope(ctx, queries, binding)
	if err != nil {
		return models.EgressScope{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.EgressScope{}, fmt.Errorf("commit embedding scope: %w", err)
	}
	return scope, nil
}

// ResolveEmbeddingScopeTx reads the scope within a caller's tenant-scoped,
// database-only transaction. Authorization remains the caller's responsibility.
func (s *Service) ResolveEmbeddingScopeTx(ctx context.Context, tx jobs.EffectsTx, binding models.EmbeddingBinding) (models.EgressScope, error) {
	if s == nil || s.pool == nil || ctx == nil || tx == nil || !validEmbeddingBinding(binding) {
		return models.EgressScope{}, ErrInvalidRequest
	}
	queries := dbquery.New(tx)
	tenantID, err := queries.CurrentTenant(ctx)
	if err != nil {
		return models.EgressScope{}, fmt.Errorf("read embedding scope tenant: %w", err)
	}
	if tenantID != binding.TenantID {
		return models.EgressScope{}, ErrInvalidRequest
	}
	return readEmbeddingScope(ctx, queries, binding)
}

func validEmbeddingBinding(binding models.EmbeddingBinding) bool {
	return binding.TenantID > 0 && binding.KnowledgeBaseID > 0 && binding.EmbeddingSpaceID > 0 && binding.ConfigRevision > 0
}

func readEmbeddingScope(ctx context.Context, queries *dbquery.Queries, binding models.EmbeddingBinding) (models.EgressScope, error) {
	row, err := queries.ResolveEmbeddingScope(ctx, dbquery.ResolveEmbeddingScopeParams{TenantID: binding.TenantID,
		KbID: binding.KnowledgeBaseID, SpaceID: binding.EmbeddingSpaceID, ConfigRevision: binding.ConfigRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EgressScope{}, ErrNotFound
	}
	if err != nil {
		return models.EgressScope{}, fmt.Errorf("resolve embedding scope: %w", err)
	}
	return models.EgressScope{TenantID: row.TenantID, KnowledgeBaseID: row.KbID,
		TenantPolicy: models.EgressPolicy(row.EgressPolicyMax), KnowledgeBasePolicy: models.EgressPolicy(row.EgressPolicy)}, nil
}
