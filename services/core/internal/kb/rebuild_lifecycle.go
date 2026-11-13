package kb

import (
	"context"
	"errors"
	"fmt"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

// Cancel locks the job before the KB, matching worker completion and failure.
// Authorization failure rolls back both cancellation and its notification.
func (s *RebuildService) Cancel(ctx context.Context, actor Actor, kbID, jobID int64) error {
	if kbID <= 0 || jobID <= 0 || actor.TenantID <= 0 || actor.UserID <= 0 {
		return ErrInvalidRequest
	}
	err := s.queue.CancelWithEffects(ctx, actor.TenantID, jobID, func(ctx context.Context, tx jobs.EffectsTx, kind jobs.Kind) error {
		if kind != jobs.Reembed {
			return ErrNotFound
		}
		q := dbquery.New(tx)
		if _, err := lockConfigAccess(ctx, q, actor, kbID); err != nil {
			return err
		}
		if _, err := readRebuild(ctx, q, actor.TenantID, kbID, jobID); err != nil {
			return err
		}
		if err := releaseRebuild(ctx, q, actor.TenantID, jobID); err != nil {
			return err
		}
		return q.AuditRebuildCancellation(ctx, dbquery.AuditRebuildCancellationParams{TenantID: actor.TenantID, UserID: actor.UserID, KbID: kbID, JobID: jobID})
	})
	if errors.Is(err, jobs.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// Fail retains the reservation for retry and releases it atomically on a terminal failure.
func (s *RebuildService) Fail(ctx context.Context, lease jobs.Lease, code jobs.FailureCode) (jobs.Status, error) {
	return s.queue.FailWithEffects(ctx, lease, code, func(ctx context.Context, tx jobs.EffectsTx, status jobs.Status) error {
		return s.FailureEffects(ctx, tx, lease, status)
	})
}

// FailureEffects runs only in the matching queue failure transaction.
func (s *RebuildService) FailureEffects(ctx context.Context, tx jobs.EffectsTx, lease jobs.Lease, status jobs.Status) error {
	if err := s.queue.CheckFailure(tx, lease, status); err != nil {
		return err
	}
	q := dbquery.New(tx)
	if _, err := q.RebuildJob(ctx, dbquery.RebuildJobParams{TenantID: lease.TenantID, JobID: lease.JobID}); err != nil {
		return fmt.Errorf("read failed rebuild: %w", err)
	}
	if status == jobs.Queued {
		return nil
	}
	return releaseRebuild(ctx, q, lease.TenantID, lease.JobID)
}

// Expire is passed to ClaimKindsWithRecovery; the store fences its transaction.
func (s *RebuildService) Expire(ctx context.Context, tx jobs.EffectsTx, job jobs.ExpiredJob) error {
	if err := s.queue.CheckExpiration(tx, job); err != nil {
		return err
	}
	if job.Kind != jobs.Reembed {
		return errors.New("expected expired rebuild")
	}
	return releaseRebuild(ctx, dbquery.New(tx), job.TenantID, job.ID)
}

func releaseRebuild(ctx context.Context, q *dbquery.Queries, tenantID, jobID int64) error {
	count, err := q.ReleaseRebuildSpace(ctx, dbquery.ReleaseRebuildSpaceParams{TenantID: tenantID, JobID: jobID})
	if err != nil {
		return fmt.Errorf("release rebuild reservation: %w", err)
	}
	if count != 1 {
		return errors.New("rebuild reservation does not match")
	}
	return nil
}

// CompleteEffects holds the KB lock while the index owner verifies coverage,
// then switches the binding with audit in the matching job-completion transaction.
// verify must not acquire document locks: publishers and deleters acquire the KB
// before changing current evidence, so their commits serialize with this check.
func (s *RebuildService) CompleteEffects(ctx context.Context, tx jobs.EffectsTx, lease jobs.Lease, verify func(context.Context, jobs.EffectsTx, RebuildExecution) error) error {
	if verify == nil {
		return errors.New("rebuild coverage verifier is required")
	}
	if err := s.queue.CheckCompletion(tx, lease); err != nil {
		return err
	}
	state, err := s.readTargetTx(ctx, tx, lease)
	if err != nil {
		return err
	}
	if err := verify(ctx, tx, state); err != nil {
		return err
	}
	q := dbquery.New(tx)
	count, err := q.SwitchRebuildSpace(ctx, dbquery.SwitchRebuildSpaceParams{TenantID: lease.TenantID, JobID: lease.JobID})
	if err != nil {
		return err
	}
	if count != 1 {
		return models.ErrRuntimeChanged
	}
	return q.AuditRebuildCompletion(ctx, dbquery.AuditRebuildCompletionParams{TenantID: lease.TenantID, JobID: lease.JobID})
}
