package ingest

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/secrets"
)

type RebuildEmbedding struct {
	queue   *jobs.Store
	pool    *pgxpool.Pool
	rebuild *kb.RebuildService
	vectors *RebuildVectors
	catalog *models.Catalog
	count   TokenCounter
	limits  models.ClientLimits
	batch   int32
}

func NewRebuildEmbedding(queue *jobs.Store, pool *pgxpool.Pool, vault *secrets.Vault, count TokenCounter, limits models.ClientLimits, batch int32) (*RebuildEmbedding, error) {
	if !queue.UsesPool(pool) || count == nil || batch < 1 || batch > maxVectorBatch || int(batch) > limits.MaxBatch {
		return nil, errors.New("valid rebuild embedding dependencies and batch size are required")
	}
	rebuild, err := kb.NewRebuildService(pool, queue)
	if err != nil {
		return nil, err
	}
	vectors, err := NewRebuildVectors(queue, rebuild)
	if err != nil {
		return nil, err
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		return nil, err
	}
	return &RebuildEmbedding{queue: queue, pool: pool, rebuild: rebuild, vectors: vectors, catalog: catalog, count: count, limits: limits, batch: batch}, nil
}

// Run fills the target space without completing the job or switching the KB.
// The worker must cancel ctx when its lease is lost. Final coverage is checked
// separately because document publication may continue during this traversal.
func (s *RebuildEmbedding) Run(ctx context.Context, job jobs.ClaimedJob) error {
	if job.Kind != jobs.Reembed {
		return errors.New("expected rebuild job")
	}
	var state kb.RebuildExecution
	if err := s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		var err error
		state, err = s.rebuild.TargetTx(ctx, tx, job.Lease)
		return err
	}); err != nil {
		return err
	}
	_, base, err := s.rebuild.Target(ctx, job.Lease)
	if err != nil {
		return err
	}
	auditor, err := models.NewEmbeddingDenialAuditor(s.pool, job.Lease.TenantID, state.UserID)
	if err != nil {
		return err
	}
	resolver := &rebuildCallScope{base: base, vectors: s.vectors, lease: job.Lease}
	client, space, err := s.catalog.OpenEmbeddingClient(ctx, state.Binding, s.limits, resolver, auditor)
	if err != nil {
		return err
	}
	defer client.Close()
	if space.ID != state.Target.ID || space.Model != state.Target.Model || space.Dimensions != state.Target.Dimensions || space.Normalized != state.Target.Normalized || space.ProviderID != state.Target.ProviderID || space.Distance != state.Target.Distance {
		return models.ErrRuntimeChanged
	}
	scope := models.EgressScope{TenantID: state.Binding.TenantID, KnowledgeBaseID: state.Binding.KnowledgeBaseID}
	if _, err := client.CheckTokenizer(ctx, scope, s.count); err != nil {
		return err
	}
	var after int64
	for {
		var documents []dbquery.ListRebuildDocumentsRow
		err := s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
			current, err := s.rebuild.TargetTx(ctx, tx, job.Lease)
			if err != nil {
				return err
			}
			if current.Binding != state.Binding {
				return models.ErrRuntimeChanged
			}
			documents, err = dbquery.New(tx).ListRebuildDocuments(ctx, dbquery.ListRebuildDocumentsParams{TenantID: job.Lease.TenantID, KbID: state.Binding.KnowledgeBaseID, AfterID: after})
			return err
		})
		if err != nil {
			return err
		}
		if len(documents) == 0 {
			return nil
		}
		for _, document := range documents {
			if document.AclMode != "inherit_kb" {
				return ErrPlanConflict
			}
			if err := s.embedDocument(ctx, job.Lease, state.Binding, document.ID, client, resolver, scope); err != nil {
				return err
			}
			after = document.ID
		}
	}
}

func (s *RebuildEmbedding) embedDocument(ctx context.Context, lease jobs.Lease, binding models.EmbeddingBinding, documentID int64, client *models.EmbeddingClient, resolver *rebuildCallScope, scope models.EgressScope) error {
	var after, version int64
	for {
		batch, err := s.vectors.Read(ctx, lease, binding, documentID, after, s.batch)
		if err != nil {
			return err
		}
		if version != 0 && version != batch.versionID {
			return ErrPlanConflict
		}
		version = batch.versionID
		if len(batch.rows) == 0 {
			if after == 0 {
				return ErrPlanCorrupt
			}
			return nil
		}
		inputs := batch.Inputs()
		expected := 0
		for _, input := range inputs {
			count, err := checkedTokenCount(ctx, s.count, input)
			if err != nil {
				return err
			}
			// Recount with the target tokenizer; stored counts describe the source model.
			if count > maxTextChunkTokens {
				return models.ErrTokenizerMismatch
			}
			expected += count
		}
		// This run-local resolver is used synchronously. Every retry validates the
		// exact batch before sending content; no DB lock spans the network call.
		resolver.batch = &batch
		result, err := client.Embed(ctx, scope, inputs)
		resolver.batch = nil
		if err != nil {
			return err
		}
		if result.Usage == nil {
			return models.ErrTokenizerUsageMissing
		}
		if result.Usage.PromptTokens != expected {
			return models.ErrTokenizerMismatch
		}
		if len(result.Vectors) != len(inputs) {
			return models.ErrInvalidResponse
		}
		if err := s.vectors.Write(ctx, lease, batch, result.Vectors); err != nil {
			return err
		}
		after = batch.LastID()
	}
}

type rebuildCallScope struct {
	base    models.EmbeddingScopeResolver
	vectors *RebuildVectors
	lease   jobs.Lease
	batch   *RebuildBatch
}

func (r *rebuildCallScope) ResolveEmbeddingScope(ctx context.Context, binding models.EmbeddingBinding) (models.EgressScope, error) {
	if r.batch == nil {
		return r.base.ResolveEmbeddingScope(ctx, binding)
	}
	if r.batch.state.Binding != binding {
		return models.EgressScope{}, models.ErrScopeMismatch
	}
	return r.vectors.validate(ctx, r.lease, *r.batch)
}
