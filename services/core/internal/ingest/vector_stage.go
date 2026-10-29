package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

var (
	ErrInvalidVector     = errors.New("invalid staged embedding vector")
	ErrVectorsIncomplete = errors.New("staged embedding vectors are incomplete")
)

const maxVectorBatch = 128

// VectorInput values must not be modified while Write is running.
type VectorInput struct {
	ChunkID int64
	Ordinal int
	Values  []float32
}

type VectorStage struct{ queue *jobs.Store }

func NewVectorStage(queue *jobs.Store) (*VectorStage, error) {
	if queue == nil {
		return nil, errors.New("job store is required")
	}
	return &VectorStage{queue: queue}, nil
}

// Write upserts a bounded batch for the staged version, never publishing it.
func (s *VectorStage) Write(ctx context.Context, job jobs.ClaimedJob, identity PlanIdentity, batch []VectorInput) error {
	if s == nil || s.queue == nil || ctx == nil {
		return errors.New("initialized vector stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return fmt.Errorf("invalid vector job: %w", err)
	}
	chunkIDs, err := validateVectorBatch(ctx, identity, batch)
	if err != nil {
		return err
	}
	var space EmbeddingProfile
	err = s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		var status string
		var err error
		space, status, _, err = lockVectorPlan(ctx, queries, job, ids, identity)
		if err != nil {
			return err
		}
		if status != "embedding" {
			return ErrPlanConflict
		}
		return checkVectorChunks(ctx, queries, job.Lease.TenantID, ids, batch, chunkIDs)
	})
	if err != nil {
		return err
	}
	encoded, err := encodeVectorBatch(ctx, batch, space.Dimensions, space.Normalized)
	if err != nil {
		return err
	}
	return s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		current, status, _, err := lockVectorPlan(ctx, queries, job, ids, identity)
		if err != nil {
			return err
		}
		if status != "embedding" || current != space {
			return ErrPlanConflict
		}
		if err := checkVectorChunks(ctx, queries, job.Lease.TenantID, ids, batch, chunkIDs); err != nil {
			return err
		}
		if err := checkExistingVectors(ctx, queries, job.Lease.TenantID, ids, identity, chunkIDs); err != nil {
			return err
		}
		count, err := queries.UpsertPlanVectors(ctx, dbquery.UpsertPlanVectorsParams{
			SpaceID: identity.EmbeddingSpaceID, Items: encoded, TenantID: job.Lease.TenantID,
			KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return safePlanDBError("upsert staged vectors", err)
		}
		if count != int64(len(batch)) {
			return ErrPlanCorrupt
		}
		return nil
	})
}

// Finish verifies every staged chunk has a matching safe vector and advances
// the version to indexing. It does not mark the job or version ready.
func (s *VectorStage) Finish(ctx context.Context, job jobs.ClaimedJob, identity PlanIdentity) error {
	if s == nil || s.queue == nil || ctx == nil {
		return errors.New("initialized vector stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return fmt.Errorf("invalid vector job: %w", err)
	}
	if err := validatePlanIdentity(identity); err != nil {
		return err
	}
	return s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if _, _, _, err := lockVectorPlan(ctx, queries, job, ids, identity); err != nil {
			return err
		}
		sections, chunks, err := planRows(ctx, queries, job.Lease.TenantID, ids)
		if err != nil {
			return err
		}
		if sections != int64(identity.SectionCount) || len(chunks) != int(identity.ChunkCount) {
			return ErrPlanCorrupt
		}
		for index, chunk := range chunks {
			if chunk.ID <= 0 || chunk.Ordinal != int32(index+1) || chunk.IsCurrent || chunk.Disabled ||
				len(chunk.AccessTags) != 1 || chunk.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
				return ErrPlanCorrupt
			}
		}
		counts, err := queries.CountPlanVectors(ctx, dbquery.CountPlanVectorsParams{
			SpaceID: identity.EmbeddingSpaceID, TenantID: job.Lease.TenantID, KbID: ids.KBID,
			DocumentID: ids.DocumentID, VersionID: ids.VersionID, Normalized: identity.EmbeddingProfile.Normalized,
		})
		if err != nil {
			return fmt.Errorf("count staged vectors: %w", err)
		}
		if counts.Valid != counts.Total {
			return ErrPlanCorrupt
		}
		if counts.Total != int64(identity.ChunkCount) {
			return ErrVectorsIncomplete
		}
		updated, err := queries.MarkPlanIndexing(ctx, dbquery.MarkPlanIndexingParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return fmt.Errorf("mark version indexing: %w", err)
		}
		if updated != 1 {
			return errTarget
		}
		return nil
	})
}

func validatePlanIdentity(identity PlanIdentity) error {
	if identity.EmbeddingSpaceID <= 0 || identity.ConfigRevision <= 0 || !validEmbeddingProfile(identity.EmbeddingProfile) || identity.SectionCount <= 0 || identity.SectionCount > maxPlanSections ||
		identity.ChunkCount <= 0 || identity.ChunkCount > maxPlanChunks || identity.Digest != planIdentityDigest(identity) {
		return ErrInvalidPlan
	}
	return nil
}

func validateVectorBatch(ctx context.Context, identity PlanIdentity, batch []VectorInput) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validatePlanIdentity(identity); err != nil {
		return nil, err
	}
	if len(batch) == 0 || len(batch) > maxVectorBatch {
		return nil, ErrInvalidVector
	}
	ids := make([]int64, len(batch))
	seen := make(map[int64]struct{}, len(batch))
	for index, item := range batch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.ChunkID <= 0 || item.Ordinal <= 0 || item.Ordinal > int(identity.ChunkCount) || len(item.Values) < 64 || len(item.Values) > 4000 {
			return nil, ErrInvalidVector
		}
		if _, exists := seen[item.ChunkID]; exists {
			return nil, ErrInvalidVector
		}
		seen[item.ChunkID] = struct{}{}
		ids[index] = item.ChunkID
		var norm float64
		for _, value := range item.Values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, ErrInvalidVector
			}
			norm = math.Hypot(norm, float64(value))
		}
		if norm == 0 {
			return nil, ErrInvalidVector
		}
	}
	return ids, nil
}

func lockVectorPlan(ctx context.Context, queries *dbquery.Queries, job jobs.ClaimedJob, ids documentIDs, identity PlanIdentity) (EmbeddingProfile, string, int64, error) {
	if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
		return EmbeddingProfile{}, "", 0, err
	}
	target, err := checkPlanTarget(ctx, queries, job.Lease.TenantID, ids, PreparedPlan{
		EmbeddingSpaceID: identity.EmbeddingSpaceID, EmbeddingProfile: identity.EmbeddingProfile, ConfigRevision: identity.ConfigRevision,
	})
	if err != nil {
		return EmbeddingProfile{}, "", 0, err
	}
	plan, err := queries.LoadIngestPlan(ctx, dbquery.LoadIngestPlanParams{
		TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EmbeddingProfile{}, "", 0, ErrPlanCorrupt
	}
	if err != nil {
		return EmbeddingProfile{}, "", 0, fmt.Errorf("load vector plan: %w", err)
	}
	if plan.EmbeddingSpaceID != identity.EmbeddingSpaceID || plan.ConfigRevision != identity.ConfigRevision ||
		plan.SectionCount != identity.SectionCount || plan.ChunkCount != identity.ChunkCount || !bytes.Equal(plan.PlanSha256, identity.Digest[:]) {
		return EmbeddingProfile{}, "", 0, ErrPlanConflict
	}
	space := EmbeddingProfile{ProviderID: target.ProviderID, Model: target.Model, Dimensions: target.Dims,
		Normalized: target.Normalized, Distance: target.Distance}
	return space, target.Status, target.CreatedBy, nil
}

func checkVectorChunks(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, batch []VectorInput, chunkIDs []int64) error {
	rows, err := queries.LockStagedChunks(ctx, dbquery.LockStagedChunksParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID, ChunkIds: chunkIDs,
	})
	if err != nil {
		return fmt.Errorf("lock staged chunks: %w", err)
	}
	if len(rows) != len(batch) {
		return ErrInvalidVector
	}
	ordinals := make(map[int64]int, len(batch))
	for _, item := range batch {
		ordinals[item.ChunkID] = item.Ordinal
	}
	for _, row := range rows {
		if ordinals[row.ID] != int(row.Ordinal) {
			return ErrInvalidVector
		}
		if row.IsCurrent || row.Disabled || len(row.AccessTags) != 1 || row.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
			return ErrPlanCorrupt
		}
	}
	return nil
}

func checkExistingVectors(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, identity PlanIdentity, chunkIDs []int64) error {
	rows, err := queries.LockPlanVectors(ctx, dbquery.LockPlanVectorsParams{
		TenantID: tenantID, KbID: ids.KBID, SpaceID: identity.EmbeddingSpaceID, ChunkIds: chunkIDs,
	})
	if err != nil {
		return fmt.Errorf("lock staged vectors: %w", err)
	}
	for _, row := range rows {
		if row.IsCurrent || len(row.AccessTags) != 1 || row.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
			return ErrPlanCorrupt
		}
	}
	return nil
}

type vectorPlanRow struct {
	ID        int64  `json:"id"`
	Ordinal   int    `json:"ordinal"`
	Embedding string `json:"embedding"`
}

func encodeVectorBatch(ctx context.Context, batch []VectorInput, dimensions int32, normalize bool) ([]byte, error) {
	rows := make([]vectorPlanRow, len(batch))
	for index, item := range batch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(item.Values) != int(dimensions) {
			return nil, ErrInvalidVector
		}
		value, err := encodeHalfVector(item.Values, normalize)
		if err != nil {
			return nil, err
		}
		rows[index] = vectorPlanRow{ID: item.ChunkID, Ordinal: item.Ordinal, Embedding: value}
	}
	return encodePlanRows(ctx, len(rows), func(index int) any { return rows[index] })
}

func encodeHalfVector(values []float32, normalize bool) (string, error) {
	var norm float64
	for _, value := range values {
		norm = math.Hypot(norm, float64(value))
	}
	if norm == 0 || math.IsInf(norm, 0) || math.IsNaN(norm) {
		return "", ErrInvalidVector
	}
	encoded := make([]byte, 0, len(values)*12+2)
	encoded = append(encoded, '[')
	representable := false
	for index, value := range values {
		if normalize {
			value = float32(float64(value) / norm)
		}
		if math.Abs(float64(value)) > 65504 || math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", ErrInvalidVector
		}
		if math.Abs(float64(value)) > math.Ldexp(1, -25) {
			representable = true
		}
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded = strconv.AppendFloat(encoded, float64(value), 'g', -1, 32)
	}
	if !representable {
		return "", ErrInvalidVector
	}
	encoded = append(encoded, ']')
	return string(encoded), nil
}
