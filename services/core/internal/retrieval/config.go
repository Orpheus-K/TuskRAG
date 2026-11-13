package retrieval

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
)

var ErrRerankUnavailable = errors.New("reranking requires a configured provider and model")

var ErrConfigurationChanged = errors.New("retrieval configuration no longer matches snapshot")

// ConfigurationSnapshot identifies immutable settings, not document contents or
// provider-side model weights. Explicit request overrides remain caller-owned.
type ConfigurationSnapshot struct {
	Embedding         Binding `json:"embedding"`
	RetrievalRevision int32   `json:"retrieval_revision"`
}

func (s *Service) Snapshot(ctx context.Context, actor Actor, kbID int64) (ConfigurationSnapshot, error) {
	if s == nil || s.driver == nil || ctx == nil {
		return ConfigurationSnapshot{}, ErrInvalidQuery
	}
	configuration, err := s.driver.searchConfig(ctx, actor, kbID)
	if err != nil {
		return ConfigurationSnapshot{}, err
	}
	return configuration.snapshot(), nil
}

type searchConfiguration struct {
	Binding  Binding
	Revision int32
	Limit    int
	Reranker *RerankOptions
}

func (c searchConfiguration) snapshot() ConfigurationSnapshot {
	return ConfigurationSnapshot{Embedding: c.Binding, RetrievalRevision: c.Revision}
}

// searchConfig snapshots immutable retrieval settings under the same membership
// and KB locks used by evidence rechecks. No lock is held during model calls.
func (d *Driver) searchConfig(ctx context.Context, actor Actor, kbID int64) (_ searchConfiguration, resultErr error) {
	tx, queries, err := d.begin(ctx, actor, kbID)
	if err != nil {
		return searchConfiguration{}, err
	}
	defer rollback(tx, &resultErr)
	bound, err := queries.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return searchConfiguration{}, ErrNotFound
	}
	if err != nil {
		return searchConfiguration{}, fmt.Errorf("read search configuration binding: %w", err)
	}
	row, err := queries.LatestRetrievalConfig(ctx, dbquery.LatestRetrievalConfigParams{TenantID: actor.TenantID, KbID: kbID})
	revision, limit := int32(0), 8
	var reranker *RerankOptions
	if err == nil {
		revision, limit = row.Revision, int(row.TopK)
		if row.RerankProviderID.Valid {
			reranker = &RerankOptions{ProviderID: row.RerankProviderID.Int64, Model: row.RerankModel.String, TopN: int(row.RerankTopN.Int32), Fallback: row.RerankFallback}
			if row.RerankMinScore.Valid {
				score := row.RerankMinScore.Float64
				reranker.MinScore = &score
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return searchConfiguration{}, fmt.Errorf("read search configuration revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return searchConfiguration{}, fmt.Errorf("commit search configuration snapshot: %w", err)
	}
	return searchConfiguration{Binding: Binding{SpaceID: bound.EmbeddingSpaceID, Revision: bound.ConfigRevision, Dimensions: bound.Dims, Distance: bound.Distance, Normalized: bound.Normalized}, Revision: revision, Limit: limit, Reranker: reranker}, nil
}

func resolveSearchOptions(request Request, limit int, reranker *RerankOptions) (Request, error) {
	if request.Limit == 0 {
		request.Limit = limit
	}
	if request.RerankEnabled != nil && !*request.RerankEnabled {
		if request.Rerank != nil {
			return Request{}, ErrInvalidQuery
		}
		return request, nil
	}
	if request.Rerank == nil {
		request.Rerank = reranker
	}
	if request.RerankEnabled != nil && *request.RerankEnabled && request.Rerank == nil {
		return Request{}, ErrRerankUnavailable
	}
	return request, nil
}
