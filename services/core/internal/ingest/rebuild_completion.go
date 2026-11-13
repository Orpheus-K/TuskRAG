package ingest

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
)

// Completion returns DB-only effects. Run must finish first; the final check
// proves current coverage rather than trusting the earlier traversal's count.
func (s *RebuildEmbedding) Completion(job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if job.Kind != jobs.Reembed {
		return nil, errors.New("expected rebuild job")
	}
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		return s.rebuild.CompleteEffects(ctx, tx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx, state kb.RebuildExecution) error {
			if err := indexing.Check(ctx, tx, state.Target.ID, state.Target.Dimensions, state.Target.Distance); err != nil {
				return err
			}
			valid, err := dbquery.New(tx).RebuildCoverageValid(ctx, dbquery.RebuildCoverageValidParams{TenantID: job.Lease.TenantID, KbID: state.Binding.KnowledgeBaseID, SpaceID: state.Target.ID, Dimensions: pgtype.Int4{Int32: state.Target.Dimensions, Valid: true}, Normalized: state.Target.Normalized})
			if err != nil {
				return err
			}
			if !valid {
				return ErrVectorsIncomplete
			}
			return nil
		})
	}, nil
}
