package ingest

import (
	"context"
	"errors"

	"tuskrag.local/core/internal/jobs"
)

type JobHandler struct {
	ingest *IngestHandler
	purge  *PurgeStage
}

var _ jobs.Handler = (*JobHandler)(nil)

func NewJobHandler(ingest *IngestHandler, purge *PurgeStage) (*JobHandler, error) {
	if ingest == nil || purge == nil || ingest.queue == nil || ingest.queue != purge.queue {
		return nil, errors.New("ingest and purge stages must share a job store")
	}
	return &JobHandler{ingest: ingest, purge: purge}, nil
}

func (h *JobHandler) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if h == nil || h.ingest == nil || h.purge == nil {
		return nil, errors.New("initialized job handler is required")
	}
	switch job.Kind {
	case jobs.Ingest:
		return h.ingest.Prepare(ctx, job)
	case jobs.Purge:
		return h.purge.Prepare(ctx, job)
	default:
		return nil, errors.New("unsupported worker job kind")
	}
}

func (h *JobHandler) Expire(ctx context.Context, tx jobs.EffectsTx, job jobs.ExpiredJob) error {
	if h == nil || h.ingest == nil || h.ingest.parse == nil || h.purge == nil {
		return errors.New("initialized job handler is required")
	}
	switch job.Kind {
	case jobs.Ingest:
		return h.ingest.parse.Expire(ctx, tx, job)
	case jobs.Purge:
		// A purging version remains marked for operator retry after exhaustion.
		return nil
	default:
		return errors.New("unsupported exhausted job kind")
	}
}
