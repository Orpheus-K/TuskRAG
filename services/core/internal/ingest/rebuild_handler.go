package ingest

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

var _ jobs.Handler = (*RebuildEmbedding)(nil)

func (s *RebuildEmbedding) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if job.Kind != jobs.Reembed {
		return nil, errors.New("expected rebuild job")
	}
	if err := s.Run(ctx, job); err != nil {
		return nil, s.failure(ctx, job, err)
	}
	complete, err := s.Completion(job)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := complete(ctx, tx); err != nil {
			return s.failure(ctx, job, err)
		}
		return nil
	}, nil
}

func (s *RebuildEmbedding) Expire(ctx context.Context, tx jobs.EffectsTx, job jobs.ExpiredJob) error {
	return s.rebuild.Expire(ctx, tx, job)
}

func (s *RebuildEmbedding) failure(ctx context.Context, job jobs.ClaimedJob, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, jobs.ErrLeaseLost) {
		return err
	}
	var code jobs.FailureCode
	var upstream *models.UpstreamError
	switch {
	case errors.As(err, &upstream):
		code = jobs.ModelRejected
		if upstream.Retryable {
			code = jobs.ModelUnavailable
		}
	case errors.Is(err, context.DeadlineExceeded):
		code = jobs.Timeout
	case errors.Is(err, indexing.ErrIndexMissing), errors.Is(err, indexing.ErrIndexBusy), errors.Is(err, indexing.ErrIndexInvalid), errors.Is(err, indexing.ErrIndexConflict):
		code = jobs.IndexUnavailable
	case errors.Is(err, ErrVectorsIncomplete), errors.Is(err, ErrPlanConflict), errors.Is(err, pgx.ErrNoRows):
		code = jobs.RebuildIncomplete
	case errors.Is(err, kb.ErrNotFound), errors.Is(err, kb.ErrForbidden):
		code = jobs.PermissionRevoked
	case errors.Is(err, kb.ErrArchived), errors.Is(err, models.ErrRuntimeChanged), errors.Is(err, models.ErrRuntimeUnavailable):
		code = jobs.ConfigurationChanged
	case errors.Is(err, models.ErrEgressDenied):
		code = jobs.EgressDenied
	case errors.Is(err, models.ErrTokenizerMismatch), errors.Is(err, models.ErrTokenizerUsageMissing):
		code = jobs.TokenizerMismatch
	case errors.Is(err, models.ErrInvalidResponse), errors.Is(err, ErrInvalidVector), errors.Is(err, ErrInvalidTokenCount):
		code = jobs.ModelInvalid
	case errors.Is(err, indexing.ErrUnsupportedDistance):
		code = jobs.UnsupportedEmbedding
	case errors.Is(err, ErrPlanCorrupt), errors.Is(err, ErrInvalidPlan):
		code = jobs.Internal
	default:
		return err
	}
	return &jobs.ProcessingFailure{Code: code, Cause: err, Apply: func(ctx context.Context, tx jobs.EffectsTx, status jobs.Status) error {
		return s.rebuild.FailureEffects(ctx, tx, job.Lease, status)
	}}
}
