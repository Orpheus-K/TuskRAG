package models

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
)

var ErrAuditSubjectUnavailable = errors.New("model audit subject is unavailable")

type EmbeddingDenialAuditor struct {
	pool     *pgxpool.Pool
	tenantID int64
	userID   int64
}

// NewEmbeddingDenialAuditor binds audit records to an authenticated user or a
// verified job's initiating user. It does not authorize that user to read data.
func NewEmbeddingDenialAuditor(pool *pgxpool.Pool, tenantID, userID int64) (*EmbeddingDenialAuditor, error) {
	if pool == nil || tenantID <= 0 || userID <= 0 {
		return nil, errors.New("database pool, tenant and audit user are required")
	}
	return &EmbeddingDenialAuditor{pool: pool, tenantID: tenantID, userID: userID}, nil
}

func (a *EmbeddingDenialAuditor) RecordModelDenied(ctx context.Context, call DeniedCall) (resultErr error) {
	if a == nil || a.pool == nil || ctx == nil || call.TenantID != a.tenantID || call.KnowledgeBaseID <= 0 || call.ProviderID <= 0 ||
		(call.Operation != "embeddings" && call.Operation != "rerank") || (call.Locality != Internal && call.Locality != External) {
		return errors.New("invalid denied embedding call")
	}
	within, err := PolicyWithin(call.TenantPolicy, call.KnowledgeBasePolicy)
	if err != nil || (within && (call.KnowledgeBasePolicy == Allow || call.Locality == Internal)) {
		return errors.New("invalid denied embedding policy")
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin model denial audit: %w", err)
	}
	defer rollbackCatalog(tx, &resultErr)
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, a.tenantID); err != nil {
		return fmt.Errorf("set model denial audit tenant: %w", err)
	}
	count, err := queries.AuditDeniedModelCall(ctx, dbquery.AuditDeniedModelCallParams{
		TenantID: a.tenantID, UserID: a.userID, KbID: call.KnowledgeBaseID, ProviderID: call.ProviderID,
		Operation: call.Operation, TenantPolicy: string(call.TenantPolicy), KbPolicy: string(call.KnowledgeBasePolicy), Locality: string(call.Locality),
	})
	if err != nil {
		return fmt.Errorf("write model denial audit: %w", err)
	}
	if count != 1 {
		return ErrAuditSubjectUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit model denial audit: %w", err)
	}
	return nil
}
