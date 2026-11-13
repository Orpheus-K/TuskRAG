package ingest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

// RebuildBatch is an immutable snapshot. Model calls run outside its DB transaction.
type RebuildBatch struct {
	state                                 kb.RebuildExecution
	jobID, documentID, versionID, afterID int64
	limit                                 int32
	rows                                  []dbquery.ReadRebuildChunksRow
}

// Inputs returns a copy, so callers cannot change the content bound to Write.
func (b RebuildBatch) Inputs() []string {
	result := make([]string, len(b.rows))
	for i, row := range b.rows {
		result[i] = row.SearchText
	}
	return result
}
func (b RebuildBatch) LastID() int64 {
	if len(b.rows) == 0 {
		return b.afterID
	}
	return b.rows[len(b.rows)-1].ID
}

type RebuildVectors struct {
	queue   *jobs.Store
	rebuild *kb.RebuildService
}

func NewRebuildVectors(queue *jobs.Store, rebuild *kb.RebuildService) (*RebuildVectors, error) {
	if queue == nil || rebuild == nil {
		return nil, errors.New("rebuild and queue are required")
	}
	return &RebuildVectors{queue: queue, rebuild: rebuild}, nil
}

func (s *RebuildVectors) Read(ctx context.Context, lease jobs.Lease, binding models.EmbeddingBinding, documentID, afterID int64, limit int32) (RebuildBatch, error) {
	if documentID <= 0 || afterID < 0 || limit < 1 || limit > maxVectorBatch || binding.TenantID != lease.TenantID {
		return RebuildBatch{}, ErrInvalidPlan
	}
	batch := RebuildBatch{jobID: lease.JobID, documentID: documentID, afterID: afterID, limit: limit}
	err := s.queue.WithLease(ctx, lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		q := dbquery.New(tx)
		current, err := q.LockRebuildDocument(ctx, dbquery.LockRebuildDocumentParams{TenantID: lease.TenantID, KbID: binding.KnowledgeBaseID, DocumentID: documentID})
		if err != nil {
			return err
		}
		if !current.Valid {
			return ErrPlanConflict
		}
		batch.versionID = current.Int64
		batch.state, err = s.rebuild.TargetTx(ctx, tx, lease)
		if err != nil {
			return err
		}
		if batch.state.Binding != binding {
			return models.ErrScopeMismatch
		}
		batch.rows, err = readRebuildRows(ctx, q, batch)
		return err
	})
	if err != nil {
		return RebuildBatch{}, err
	}
	return batch, nil
}

func readRebuildRows(ctx context.Context, q *dbquery.Queries, b RebuildBatch) ([]dbquery.ReadRebuildChunksRow, error) {
	rows, err := q.ReadRebuildChunks(ctx, dbquery.ReadRebuildChunksParams{TenantID: b.state.Binding.TenantID, KbID: b.state.Binding.KnowledgeBaseID, DocumentID: b.documentID, VersionID: b.versionID, AfterID: b.afterID, PageSize: b.limit})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if !row.IsCurrent || row.Disabled || len(row.AccessTags) != 1 || row.AccessTags[0] != fmt.Sprintf("kb:%d:all", b.state.Binding.KnowledgeBaseID) {
			return nil, ErrPlanConflict
		}
	}
	return rows, nil
}

// Write rechecks the exact current content and target before an atomic upsert.
// Target vectors coexist with the old space; the active KB binding is unchanged.
func (s *RebuildVectors) Write(ctx context.Context, lease jobs.Lease, b RebuildBatch, values [][]float32) error {
	if lease.JobID != b.jobID || len(b.rows) == 0 || len(b.rows) > maxVectorBatch || len(values) != len(b.rows) || lease.TenantID != b.state.Binding.TenantID {
		return ErrInvalidVector
	}
	inputs := make([]VectorInput, len(values))
	for i, row := range b.rows {
		inputs[i] = VectorInput{ChunkID: row.ID, Ordinal: int(row.Ordinal), Values: values[i]}
	}
	encoded, err := encodeVectorBatch(ctx, inputs, b.state.Target.Dimensions, b.state.Target.Normalized)
	if err != nil {
		return err
	}
	return s.queue.WithLease(ctx, lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		q := dbquery.New(tx)
		state, err := s.checkBatch(ctx, tx, lease, b)
		if err != nil {
			return err
		}
		if state.Scope != b.state.Scope {
			return models.ErrRuntimeChanged
		}
		count, err := q.UpsertRebuildVectors(ctx, dbquery.UpsertRebuildVectorsParams{TenantID: lease.TenantID, KbID: state.Binding.KnowledgeBaseID, DocumentID: b.documentID, VersionID: b.versionID, SpaceID: state.Binding.EmbeddingSpaceID, Items: encoded})
		if err != nil {
			return safePlanDBError("write rebuilt vectors", err)
		}
		if count != int64(len(inputs)) {
			return ErrPlanConflict
		}
		return nil
	})
}

func (s *RebuildVectors) checkBatch(ctx context.Context, tx jobs.EffectsTx, lease jobs.Lease, b RebuildBatch) (kb.RebuildExecution, error) {
	if lease.JobID != b.jobID || lease.TenantID != b.state.Binding.TenantID {
		return kb.RebuildExecution{}, ErrInvalidPlan
	}
	q := dbquery.New(tx)
	current, err := q.LockRebuildDocument(ctx, dbquery.LockRebuildDocumentParams{TenantID: lease.TenantID, KbID: b.state.Binding.KnowledgeBaseID, DocumentID: b.documentID})
	if err != nil {
		return kb.RebuildExecution{}, err
	}
	if !current.Valid || current.Int64 != b.versionID {
		return kb.RebuildExecution{}, ErrPlanConflict
	}
	state, err := s.rebuild.TargetTx(ctx, tx, lease)
	if err != nil {
		return kb.RebuildExecution{}, err
	}
	if state.Binding != b.state.Binding || state.Target != b.state.Target || state.UserID != b.state.UserID {
		return kb.RebuildExecution{}, models.ErrRuntimeChanged
	}
	rows, err := readRebuildRows(ctx, q, b)
	if err != nil {
		return kb.RebuildExecution{}, err
	}
	if !slices.EqualFunc(rows, b.rows, func(a, b dbquery.ReadRebuildChunksRow) bool {
		return a.ID == b.ID && a.Ordinal == b.Ordinal && a.SearchText == b.SearchText && a.TokenCount == b.TokenCount && slices.Equal(a.AccessTags, b.AccessTags)
	}) {
		return kb.RebuildExecution{}, ErrPlanConflict
	}

	return state, nil
}

func (s *RebuildVectors) validate(ctx context.Context, lease jobs.Lease, b RebuildBatch) (models.EgressScope, error) {
	var state kb.RebuildExecution
	err := s.queue.WithLease(ctx, lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		var err error
		state, err = s.checkBatch(ctx, tx, lease, b)
		return err
	})
	if err != nil {
		return models.EgressScope{}, err
	}
	return state.Scope, nil
}
