package eval

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

type RunComparison struct {
	BaselineRunID int64            `json:"baseline_run_id"`
	CurrentRunID  int64            `json:"current_run_id"`
	Source        string           `json:"source"`
	Comparison    ReportComparison `json:"comparison"`
}

func (s *RunStore) Compare(ctx context.Context, actor retrieval.Actor, kbID, baselineID, currentID int64, offset, limit int32) (_ RunComparison, resultErr error) {
	if baselineID <= 0 || currentID <= 0 || offset < 0 || offset > 10000 || limit < 1 || limit > 50 {
		return RunComparison{}, ErrInvalidCase
	}
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return RunComparison{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return RunComparison{}, err
	}
	runs := make([]StoredRun, 0, 2)
	reports := make([]StoredReport, 0, 2)
	documents := map[int64]bool{}
	evidence := map[int64]Evidence{}
	missing := false
	for _, id := range []int64{baselineID, currentID} {
		row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return RunComparison{}, ErrRunNotFound
		}
		if err != nil {
			return RunComparison{}, err
		}
		run, err := decodeStoredRun(row)
		if err != nil {
			return RunComparison{}, err
		}
		report, err := loadRunResult(ctx, q, actor.TenantID, kbID, id)
		if errors.Is(err, ErrRunResultUnavailable) {
			missing = true
		} else if err != nil {
			return RunComparison{}, err
		}
		if err == nil {
			bindings, _, _, err := reportBindings(report.Report)
			if err != nil {
				return RunComparison{}, err
			}
			for chunk, item := range bindings {
				if prior, ok := evidence[chunk]; ok && prior != item {
					return RunComparison{}, ErrReportNotFound
				}
				evidence[chunk] = item
				documents[item.DocumentID] = true
			}
		}
		for _, document := range run.Request.Config.AllowedDocumentIDs {
			documents[document] = true
		}
		runs = append(runs, run)
		reports = append(reports, report)
	}
	docs := make([]int64, 0, len(documents))
	chunks := make([]int64, 0, len(evidence))
	for id := range documents {
		docs = append(docs, id)
	}
	for id := range evidence {
		chunks = append(chunks, id)
	}
	slices.Sort(docs)
	slices.Sort(chunks)
	// Cover both full scopes before per-run checks, including unavailable results
	// and rows outside the requested comparison page.
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, evidence, false); err != nil {
		return RunComparison{}, err
	}
	for i, run := range runs {
		var report *RunReport
		if reports[i].Digest != "" {
			report = &reports[i].Report
		}
		if err := s.checkExecution(ctx, q, actor, run, report, false); err != nil {
			return RunComparison{}, err
		}
	}
	if missing {
		return RunComparison{}, ErrRunResultUnavailable
	}
	comparison, err := CompareReports(reports[0].Report, reports[1].Report)
	if err != nil {
		return RunComparison{}, err
	}
	if int(offset) > len(comparison.Rows) {
		return RunComparison{}, ErrInvalidCase
	}
	end := min(int(offset+limit), len(comparison.Rows))
	if end < len(comparison.Rows) {
		next := int32(end)
		comparison.NextOffset = &next
	}
	comparison.Rows = comparison.Rows[offset:end]
	comparison.BaselineDigest = reports[0].Digest
	comparison.CurrentDigest = reports[1].Digest
	if err := tx.Commit(ctx); err != nil {
		return RunComparison{}, err
	}
	return RunComparison{BaselineRunID: baselineID, CurrentRunID: currentID, Source: "executed", Comparison: comparison}, nil
}
