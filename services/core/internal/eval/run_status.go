package eval

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

var ErrRunResultUnavailable = errors.New("evaluation run has no committed successful result")

type RunStatus struct {
	ID              int64      `json:"id"`
	JobID           int64      `json:"job_id"`
	Set             SetBinding `json:"set"`
	Mode            string     `json:"mode" enum:"lexical,vector,rrf,rerank"`
	Status          string     `json:"status" enum:"queued,running,succeeded,failed,dead,cancelled"`
	Attempts        int32      `json:"attempts"`
	MaxAttempts     int32      `json:"max_attempts"`
	FailureCode     *string    `json:"failure_code"`
	ResultAvailable bool       `json:"result_available"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// Status returns no configuration body or questions. A successful result's
// evidence is checked before exposing its availability, just as GetResult does.
func (s *RunStore) Status(ctx context.Context, actor retrieval.Actor, kbID, id int64) (_ RunStatus, resultErr error) {
	if id <= 0 {
		return RunStatus{}, ErrInvalidCase
	}
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return RunStatus{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return RunStatus{}, err
	}
	row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return RunStatus{}, ErrRunNotFound
	}
	if err != nil {
		return RunStatus{}, err
	}
	run, err := decodeStoredRun(row)
	if err != nil {
		return RunStatus{}, err
	}
	state, err := q.GetEvalRunState(ctx, dbquery.GetEvalRunStateParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
	if err != nil {
		return RunStatus{}, err
	}
	if (state.Status == "succeeded") != state.HasResult {
		return RunStatus{}, errors.New("evaluation job and result state disagree")
	}
	var report *RunReport
	if state.HasResult {
		stored, err := loadRunResult(ctx, q, actor.TenantID, kbID, id)
		if err != nil {
			return RunStatus{}, err
		}
		report = &stored.Report
	}
	if err := s.checkExecution(ctx, q, actor, run, report, false); err != nil {
		return RunStatus{}, err
	}
	result := RunStatus{ID: id, JobID: run.JobID, Set: run.Request.Set, Mode: run.Request.Mode, Status: state.Status, Attempts: state.Attempts, MaxAttempts: state.MaxAttempts, ResultAvailable: state.HasResult, CreatedAt: run.CreatedAt}
	if state.LastError.Valid {
		result.FailureCode = &state.LastError.String
	}
	if state.FinishedAt.Valid {
		result.FinishedAt = &state.FinishedAt.Time
	}
	if err := tx.Commit(ctx); err != nil {
		return RunStatus{}, err
	}
	return result, nil
}
