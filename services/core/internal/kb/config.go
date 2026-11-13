package kb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

var ErrRevisionConflict = errors.New("knowledge base configuration revision changed")
var ErrConfigProviderNotFound = errors.New("configuration model provider not found")

// RetrievalConfig contains only settings supported by the retrieval pipeline.
type RetrievalConfig struct {
	TopK     int                      `json:"top_k" minimum:"1" maximum:"30"`
	Reranker *retrieval.RerankOptions `json:"reranker,omitempty"`
}

// Revision zero with a nil Retrieval means no configuration has been saved.
// This revision is independent of the ingest/embedding config_revision.
type KnowledgeConfig struct {
	Revision  int32            `json:"revision"`
	Retrieval *RetrievalConfig `json:"retrieval,omitempty"`
}

func (s *Service) GetConfig(ctx context.Context, actor Actor, kbID int64) (_ KnowledgeConfig, resultErr error) {
	if kbID <= 0 {
		return KnowledgeConfig{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	defer rollback(tx, &resultErr)
	if _, err := lockConfigAccess(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return KnowledgeConfig{}, deny(ctx, tx, queries, actor, kbID, 0, "", "kb.config.read", err)
		}
		return KnowledgeConfig{}, err
	}
	config, err := readConfig(ctx, queries, actor.TenantID, kbID)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KnowledgeConfig{}, fmt.Errorf("commit configuration read: %w", err)
	}
	return config, nil
}

func (s *Service) PutConfig(ctx context.Context, actor Actor, kbID int64, expected int32, config RetrievalConfig) (_ KnowledgeConfig, resultErr error) {
	if kbID <= 0 || expected < 0 || expected == math.MaxInt32 || !validRetrievalConfig(config) {
		return KnowledgeConfig{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	defer rollback(tx, &resultErr)
	base, err := lockConfigAccess(ctx, queries, actor, kbID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		return KnowledgeConfig{}, deny(ctx, tx, queries, actor, kbID, 0, "", "kb.config.update", err)
	}
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if base.Status != "active" {
		return KnowledgeConfig{}, ErrArchived
	}
	previous, err := readConfig(ctx, queries, actor.TenantID, kbID)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if previous.Revision != expected {
		return KnowledgeConfig{}, ErrRevisionConflict
	}
	params := dbquery.InsertRetrievalConfigParams{TenantID: actor.TenantID, KbID: kbID,
		Revision: expected + 1, TopK: int32(config.TopK), CreatedBy: actor.UserID}
	if r := config.Reranker; r != nil {
		locality, err := queries.RetrievalConfigProvider(ctx, dbquery.RetrievalConfigProviderParams{TenantID: actor.TenantID, ProviderID: r.ProviderID})
		if errors.Is(err, pgx.ErrNoRows) {
			return KnowledgeConfig{}, ErrConfigProviderNotFound
		}
		if err != nil {
			return KnowledgeConfig{}, fmt.Errorf("read configuration provider: %w", err)
		}
		tenantPolicy, err := queries.TenantEgressPolicy(ctx, actor.TenantID)
		if err != nil {
			return KnowledgeConfig{}, fmt.Errorf("read configuration egress policy: %w", err)
		}
		within, err := models.PolicyWithin(models.EgressPolicy(tenantPolicy), models.EgressPolicy(base.EgressPolicy))
		if err != nil {
			return KnowledgeConfig{}, fmt.Errorf("validate configuration egress policy: %w", err)
		}
		if !within || (locality == string(models.External) && base.EgressPolicy != string(models.Allow)) {
			return KnowledgeConfig{}, deny(ctx, tx, queries, actor, kbID, 0, "", "kb.config.update", models.ErrEgressDenied)
		}
		params.RerankProviderID = pgtype.Int8{Int64: r.ProviderID, Valid: true}
		params.RerankModel = pgtype.Text{String: r.Model, Valid: true}
		params.RerankTopN = pgtype.Int4{Int32: int32(r.TopN), Valid: true}
		params.RerankFallback = r.Fallback
		if r.MinScore != nil {
			params.RerankMinScore = pgtype.Float8{Float64: *r.MinScore, Valid: true}
		}
	}
	row, err := queries.InsertRetrievalConfig(ctx, params)
	if err != nil {
		return KnowledgeConfig{}, fmt.Errorf("insert configuration revision: %w", err)
	}
	if err := queries.AuditRetrievalConfig(ctx, dbquery.AuditRetrievalConfigParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, Revision: row.Revision}); err != nil {
		return KnowledgeConfig{}, fmt.Errorf("audit configuration update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KnowledgeConfig{}, fmt.Errorf("commit configuration update: %w", err)
	}
	return configFromRow(row), nil
}

func validRetrievalConfig(config RetrievalConfig) bool {
	if config.TopK < 1 || config.TopK > 30 {
		return false
	}
	r := config.Reranker
	return r == nil || (r.ProviderID > 0 && utf8.ValidString(r.Model) && strings.TrimSpace(r.Model) != "" && len(r.Model) <= 200 &&
		(r.TopN == 20 || r.TopN == 30 || r.TopN == 50) &&
		(r.MinScore == nil || (!math.IsNaN(*r.MinScore) && !math.IsInf(*r.MinScore, 0))))
}

func lockConfigAccess(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID int64) (dbquery.GetVisibleKnowledgeBaseRow, error) {
	// Use the same KB -> membership lock order as archive and member changes.
	if _, err := queries.LockVisibleKnowledgeBase(ctx, dbquery.LockVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID}); errors.Is(err, pgx.ErrNoRows) {
		return dbquery.GetVisibleKnowledgeBaseRow{}, ErrNotFound
	} else if err != nil {
		return dbquery.GetVisibleKnowledgeBaseRow{}, fmt.Errorf("lock configuration knowledge base: %w", err)
	}
	role, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbquery.GetVisibleKnowledgeBaseRow{}, ErrNotFound
	}
	if err != nil {
		return dbquery.GetVisibleKnowledgeBaseRow{}, fmt.Errorf("lock configuration membership: %w", err)
	}
	if Role(role) != Owner && Role(role) != Admin {
		return dbquery.GetVisibleKnowledgeBaseRow{}, ErrForbidden
	}
	return queries.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
}

func readConfig(ctx context.Context, queries *dbquery.Queries, tenantID, kbID int64) (KnowledgeConfig, error) {
	row, err := queries.LatestRetrievalConfig(ctx, dbquery.LatestRetrievalConfigParams{TenantID: tenantID, KbID: kbID})
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeConfig{}, nil
	}
	if err != nil {
		return KnowledgeConfig{}, fmt.Errorf("read configuration revision: %w", err)
	}
	return configFromRow(row), nil
}

func configFromRow(row dbquery.KbRetrievalConfigRevision) KnowledgeConfig {
	config := &RetrievalConfig{TopK: int(row.TopK)}
	if row.RerankProviderID.Valid {
		config.Reranker = &retrieval.RerankOptions{ProviderID: row.RerankProviderID.Int64, Model: row.RerankModel.String,
			TopN: int(row.RerankTopN.Int32), Fallback: row.RerankFallback}
		if row.RerankMinScore.Valid {
			score := row.RerankMinScore.Float64
			config.Reranker.MinScore = &score
		}
	}
	return KnowledgeConfig{Revision: row.Revision, Retrieval: config}
}
