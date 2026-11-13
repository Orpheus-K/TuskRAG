package kb

import (
	"context"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

type rebuildScope struct {
	service *RebuildService
	lease   jobs.Lease
}

// Target authorizes the current lease and creator for the immutable pending
// model. The returned resolver repeats these checks before every model call.
func (s *RebuildService) Target(ctx context.Context, lease jobs.Lease) (models.EmbeddingBinding, models.EmbeddingScopeResolver, error) {
	resolver := rebuildScope{service: s, lease: lease}
	binding, _, err := resolver.read(ctx)
	if err != nil {
		return models.EmbeddingBinding{}, nil, err
	}
	return binding, resolver, nil
}

func (r rebuildScope) ResolveEmbeddingScope(ctx context.Context, binding models.EmbeddingBinding) (models.EgressScope, error) {
	current, scope, err := r.read(ctx)
	if err != nil {
		return models.EgressScope{}, err
	}
	if current != binding {
		return models.EgressScope{}, models.ErrScopeMismatch
	}
	return scope, nil
}

// RebuildExecution contains the validated target and creator, without credentials.
type RebuildExecution struct {
	Binding models.EmbeddingBinding
	Scope   models.EgressScope
	Target  models.Space
	UserID  int64
}

func (r rebuildScope) read(ctx context.Context) (models.EmbeddingBinding, models.EgressScope, error) {
	var state RebuildExecution
	err := r.service.queue.WithLease(ctx, r.lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		var err error
		state, err = r.service.TargetTx(ctx, tx, r.lease)
		return err
	})
	if err != nil {
		return models.EmbeddingBinding{}, models.EgressScope{}, err
	}
	return state.Binding, state.Scope, nil
}

// TargetTx validates a target inside a queue checkpoint. Callers that also
// lock documents must acquire those locks before this KB/membership check.
func (s *RebuildService) TargetTx(ctx context.Context, tx jobs.EffectsTx, lease jobs.Lease) (RebuildExecution, error) {
	if err := s.queue.CheckCheckpoint(tx, lease); err != nil {
		return RebuildExecution{}, err
	}
	return s.readTargetTx(ctx, tx, lease)
}

func (s *RebuildService) readTargetTx(ctx context.Context, tx jobs.EffectsTx, lease jobs.Lease) (RebuildExecution, error) {
	q := dbquery.New(tx)
	record, err := q.RebuildJob(ctx, dbquery.RebuildJobParams{TenantID: lease.TenantID, JobID: lease.JobID})
	if err != nil {
		return RebuildExecution{}, err
	}
	base, err := lockConfigAccess(ctx, q, Actor{TenantID: record.TenantID, UserID: record.CreatedBy}, record.KbID)
	if err != nil {
		return RebuildExecution{}, err
	}
	if base.Status != "active" {
		return RebuildExecution{}, ErrArchived
	}
	if base.EmbeddingSpaceID != record.SourceSpaceID || base.ConfigRevision != record.SourceRevision {
		return RebuildExecution{}, models.ErrRuntimeChanged
	}
	pending, err := q.RebuildPendingSpace(ctx, dbquery.RebuildPendingSpaceParams{TenantID: record.TenantID, KbID: record.KbID})
	if err != nil {
		return RebuildExecution{}, err
	}
	if !pending.Valid || pending.Int64 != record.TargetSpaceID {
		return RebuildExecution{}, models.ErrRuntimeChanged
	}
	target, err := q.RebuildTarget(ctx, dbquery.RebuildTargetParams{TenantID: record.TenantID, SpaceID: record.TargetSpaceID})
	if err != nil {
		return RebuildExecution{}, err
	}
	if target.ProviderID != record.TargetProviderID || target.Model != record.TargetModel || target.Dims != record.TargetDims || target.Normalized != record.TargetNormalized || target.Distance != record.TargetDistance {
		return RebuildExecution{}, models.ErrRuntimeChanged
	}
	policy, err := q.TenantEgressPolicy(ctx, record.TenantID)
	if err != nil {
		return RebuildExecution{}, err
	}
	return RebuildExecution{
		Binding: models.EmbeddingBinding{TenantID: record.TenantID, KnowledgeBaseID: record.KbID, EmbeddingSpaceID: record.TargetSpaceID, ConfigRevision: record.SourceRevision},
		Scope:   models.EgressScope{TenantID: record.TenantID, KnowledgeBaseID: record.KbID, TenantPolicy: models.EgressPolicy(policy), KnowledgeBasePolicy: models.EgressPolicy(base.EgressPolicy)},
		Target:  models.Space{ID: target.ID, ProviderID: target.ProviderID, Model: target.Model, Dimensions: target.Dims, Normalized: target.Normalized, Distance: target.Distance, Locality: models.Locality(target.Locality)}, UserID: record.CreatedBy,
	}, nil
}
