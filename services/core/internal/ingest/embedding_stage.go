package ingest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/secrets"
)

type EmbeddingStage struct {
	queue   *jobs.Store
	pool    *pgxpool.Pool
	vectors *VectorStage
	catalog *models.Catalog
	scopes  *kb.Service
	count   TokenCounter
	limits  models.ClientLimits
	batch   int
}

func NewEmbeddingStage(queue *jobs.Store, pool *pgxpool.Pool, vault *secrets.Vault,
	count TokenCounter, limits models.ClientLimits, batch int) (*EmbeddingStage, error) {
	if queue == nil || !queue.UsesPool(pool) || count == nil || batch <= 0 || batch > maxVectorBatch || batch > limits.MaxBatch {
		return nil, errors.New("valid embedding stage dependencies and batch size are required")
	}
	vectors, err := NewVectorStage(queue)
	if err != nil {
		return nil, err
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		return nil, err
	}
	scopes, err := kb.NewService(pool)
	if err != nil {
		return nil, err
	}
	return &EmbeddingStage{queue: queue, pool: pool, vectors: vectors, catalog: catalog, scopes: scopes,
		count: count, limits: limits, batch: batch}, nil
}

// Run verifies the staged plan and tokenizer, then writes only missing vectors.
// The caller must cancel ctx when the worker loses its lease. Run does not
// complete the job or publish the document version.
func (s *EmbeddingStage) Run(ctx context.Context, job jobs.ClaimedJob, plan PreparedPlan, staged StagedPlan) error {
	if s == nil || s.queue == nil || s.pool == nil || s.vectors == nil || s.catalog == nil || s.scopes == nil || s.count == nil || ctx == nil {
		return errors.New("initialized embedding stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return fmt.Errorf("invalid embedding job: %w", err)
	}
	if err := validateEmbeddingPlan(ctx, plan, staged); err != nil {
		return err
	}
	actor, status, err := s.verifyPlan(ctx, job, ids, staged)
	if err != nil {
		return err
	}
	if status == "indexing" {
		for start := 0; start < len(staged.Chunks); start += s.batch {
			end := min(start+s.batch, len(staged.Chunks))
			pending, err := s.pendingBatch(ctx, job, ids, plan, staged, actor, status, start, end)
			if err != nil {
				return err
			}
			if slices.Contains(pending, true) {
				return ErrVectorsIncomplete
			}
		}
		return s.vectors.Finish(ctx, job, staged.Identity)
	}
	if status != "embedding" {
		return ErrPlanConflict
	}
	auditor, err := models.NewEmbeddingDenialAuditor(s.pool, job.Lease.TenantID, actor)
	if err != nil {
		return fmt.Errorf("initialize model denial auditor: %w", err)
	}
	binding := models.EmbeddingBinding{TenantID: job.Lease.TenantID, KnowledgeBaseID: ids.KBID,
		EmbeddingSpaceID: staged.Identity.EmbeddingSpaceID, ConfigRevision: staged.Identity.ConfigRevision}
	resolver := leasedEmbeddingScope{queue: s.queue, scopes: s.scopes, job: job, ids: ids, identity: staged.Identity, actor: actor}
	client, space, err := s.catalog.OpenEmbeddingClient(ctx, binding, s.limits, resolver, auditor)
	if err != nil {
		return err
	}
	defer client.Close()
	profile := staged.Identity.EmbeddingProfile
	if space.ID != staged.Identity.EmbeddingSpaceID || space.ProviderID != profile.ProviderID || space.Model != profile.Model ||
		space.Dimensions != profile.Dimensions || space.Normalized != profile.Normalized || space.Distance != profile.Distance {
		return ErrPlanConflict
	}
	scope := models.EgressScope{TenantID: binding.TenantID, KnowledgeBaseID: binding.KnowledgeBaseID}
	if _, err := client.CheckTokenizer(ctx, scope, s.count); err != nil {
		return err
	}
	for start := 0; start < len(staged.Chunks); start += s.batch {
		end := min(start+s.batch, len(staged.Chunks))
		pending, err := s.pendingBatch(ctx, job, ids, plan, staged, actor, status, start, end)
		if err != nil {
			return err
		}
		var inputs []string
		var expectedTokens int
		for index := start; index < end; index++ {
			chunk := plan.Chunks[index]
			count, err := checkedTokenCount(ctx, s.count, chunk.SearchText)
			if err != nil {
				return err
			}
			if count != int(chunk.TokenCount) || count > maxTextChunkTokens {
				return ErrPlanConflict
			}
			if pending[index-start] {
				inputs = append(inputs, chunk.SearchText)
				expectedTokens += count
			}
		}
		if len(inputs) == 0 {
			continue
		}
		current, err := s.pendingBatch(ctx, job, ids, plan, staged, actor, status, start, end)
		if err != nil {
			return err
		}
		if !slices.Equal(current, pending) {
			return ErrPlanConflict
		}
		result, err := client.Embed(ctx, scope, inputs)
		if err != nil {
			return err
		}
		if result.Usage == nil {
			return models.ErrTokenizerUsageMissing
		}
		if result.Usage.PromptTokens != expectedTokens {
			return fmt.Errorf("%w: embedding batch", models.ErrTokenizerMismatch)
		}
		if len(result.Vectors) != len(inputs) {
			return models.ErrInvalidResponse
		}
		batch := make([]VectorInput, 0, len(inputs))
		for index, vectorIndex := start, 0; index < end; index++ {
			if pending[index-start] {
				ref := staged.Chunks[index]
				batch = append(batch, VectorInput{ChunkID: ref.ID, Ordinal: ref.Ordinal, Values: result.Vectors[vectorIndex]})
				vectorIndex++
			}
		}
		if err := s.vectors.Write(ctx, job, staged.Identity, batch); err != nil {
			return err
		}
	}
	return s.vectors.Finish(ctx, job, staged.Identity)
}

type leasedEmbeddingScope struct {
	queue    *jobs.Store
	scopes   *kb.Service
	job      jobs.ClaimedJob
	ids      documentIDs
	identity PlanIdentity
	actor    int64
}

func (r leasedEmbeddingScope) ResolveEmbeddingScope(ctx context.Context, binding models.EmbeddingBinding) (models.EgressScope, error) {
	want := models.EmbeddingBinding{TenantID: r.job.Lease.TenantID, KnowledgeBaseID: r.ids.KBID,
		EmbeddingSpaceID: r.identity.EmbeddingSpaceID, ConfigRevision: r.identity.ConfigRevision}
	if binding != want {
		return models.EgressScope{}, models.ErrScopeMismatch
	}
	var scope models.EgressScope
	err := r.queue.WithLease(ctx, r.job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		_, status, actor, err := lockVectorPlan(ctx, dbquery.New(tx), r.job, r.ids, r.identity)
		if err != nil {
			return err
		}
		if status != "embedding" || actor != r.actor {
			return ErrPlanConflict
		}
		scope, err = r.scopes.ResolveEmbeddingScopeTx(ctx, tx, binding)
		return err
	})
	if err != nil {
		return models.EgressScope{}, err
	}
	return scope, nil
}

func validateEmbeddingPlan(ctx context.Context, plan PreparedPlan, staged StagedPlan) error {
	hashes, err := validatePreparedPlan(ctx, plan)
	if err != nil {
		return err
	}
	want := PlanIdentity{Digest: hashes.Digest, ContentDigest: hashes.Content, EmbeddingSpaceID: plan.EmbeddingSpaceID,
		EmbeddingProfile: plan.EmbeddingProfile, ConfigRevision: plan.ConfigRevision, SectionCount: int32(len(plan.Sections)), ChunkCount: int32(len(plan.Chunks))}
	if staged.Identity != want || len(staged.Chunks) != len(plan.Chunks) {
		return ErrPlanConflict
	}
	seen := make(map[int64]struct{}, len(staged.Chunks))
	for index, chunk := range staged.Chunks {
		if chunk.ID <= 0 || chunk.Ordinal != index+1 {
			return ErrPlanCorrupt
		}
		if _, exists := seen[chunk.ID]; exists {
			return ErrPlanCorrupt
		}
		seen[chunk.ID] = struct{}{}
	}
	return nil
}

func (s *EmbeddingStage) verifyPlan(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, staged StagedPlan) (int64, string, error) {
	var actor int64
	var status string
	err := s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		var err error
		_, status, actor, err = lockVectorPlan(ctx, queries, job, ids, staged.Identity)
		if err != nil {
			return err
		}
		sections, refs, err := planRows(ctx, queries, job.Lease.TenantID, ids)
		if err != nil {
			return err
		}
		if actor <= 0 || sections != int64(staged.Identity.SectionCount) || len(refs) != len(staged.Chunks) {
			return ErrPlanCorrupt
		}
		for index, ref := range refs {
			if ref.ID != staged.Chunks[index].ID || int(ref.Ordinal) != staged.Chunks[index].Ordinal || ref.IsCurrent || ref.Disabled ||
				len(ref.AccessTags) != 1 || ref.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
				return ErrPlanCorrupt
			}
		}
		return nil
	})
	return actor, status, err
}

func (s *EmbeddingStage) pendingBatch(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, plan PreparedPlan, staged StagedPlan, actor int64, expectedStatus string, start, end int) ([]bool, error) {
	pending := make([]bool, end-start)
	chunkIDs := make([]int64, end-start)
	for index := range chunkIDs {
		chunkIDs[index] = staged.Chunks[start+index].ID
	}
	err := s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		_, status, currentActor, err := lockVectorPlan(ctx, queries, job, ids, staged.Identity)
		if err != nil {
			return err
		}
		if status != expectedStatus || currentActor != actor {
			return ErrPlanConflict
		}
		rows, err := queries.LoadEmbeddingBatch(ctx, dbquery.LoadEmbeddingBatchParams{TenantID: job.Lease.TenantID,
			KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID, SpaceID: staged.Identity.EmbeddingSpaceID,
			Normalized: staged.Identity.EmbeddingProfile.Normalized, ChunkIds: chunkIDs})
		if err != nil {
			return fmt.Errorf("load embedding batch: %w", err)
		}
		if len(rows) != end-start {
			return ErrPlanCorrupt
		}
		for index, row := range rows {
			chunk, ref := plan.Chunks[start+index], staged.Chunks[start+index]
			if row.ID != ref.ID || int(row.Ordinal) != ref.Ordinal || int(row.SectionOrdinal) != chunk.SectionIndex+1 ||
				row.Kind != chunk.Kind || row.Content != chunk.Content || row.SearchText != chunk.SearchText || row.TokenCount != chunk.TokenCount ||
				!row.PageStart.Valid || row.PageStart.Int32 != chunk.PageStart || !row.PageEnd.Valid || row.PageEnd.Int32 != chunk.PageEnd ||
				row.IsCurrent || row.Disabled || len(row.AccessTags) != 1 || row.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
				return ErrPlanCorrupt
			}
			if row.HasVector && (row.VectorCurrent || !row.VectorNormValid || len(row.VectorAccessTags) != 1 || row.VectorAccessTags[0] != row.AccessTags[0]) {
				return ErrPlanCorrupt
			}
			pending[index] = !row.HasVector
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pending, nil
}
