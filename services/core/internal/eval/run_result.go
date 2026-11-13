package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

// GetResult returns only a committed server execution, with current evidence
// visibility rechecked in the same transaction. Imports never populate this table.
func (s *RunStore) GetResult(ctx context.Context, actor retrieval.Actor, kbID, id int64) (_ StoredReport, resultErr error) {
	if id <= 0 {
		return StoredReport{}, ErrInvalidCase
	}
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return StoredReport{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return StoredReport{}, err
	}
	row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredReport{}, ErrRunNotFound
	}
	if err != nil {
		return StoredReport{}, err
	}
	run, err := decodeStoredRun(row)
	if err != nil {
		return StoredReport{}, err
	}
	stored, err := loadRunResult(ctx, q, actor.TenantID, kbID, id)
	if errors.Is(err, ErrRunResultUnavailable) {
		if accessErr := s.checkExecution(ctx, q, actor, run, nil, false); accessErr != nil {
			return StoredReport{}, accessErr
		}
	}
	if err != nil {
		return StoredReport{}, err
	}
	// Reading historical results checks the reader, not the original job owner.
	if err := s.checkExecution(ctx, q, actor, run, &stored.Report, false); err != nil {
		return StoredReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredReport{}, err
	}
	return stored, nil
}

func loadRunResult(ctx context.Context, q *dbquery.Queries, tenantID, kbID, id int64) (StoredReport, error) {
	result, err := q.GetEvalRunResult(ctx, dbquery.GetEvalRunResultParams{TenantID: tenantID, KbID: kbID, RunID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredReport{}, ErrRunResultUnavailable
	}
	if err != nil {
		return StoredReport{}, err
	}
	stored := StoredReport{Digest: result.Digest, Source: "executed", CreatedAt: result.CreatedAt.Time}
	if err := json.Unmarshal(result.Report, &stored.Report); err != nil {
		return StoredReport{}, err
	}
	data, err := json.Marshal(stored.Report)
	if err != nil {
		return StoredReport{}, err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != stored.Digest {
		return StoredReport{}, ErrReportInvalid
	}
	return stored, nil
}
