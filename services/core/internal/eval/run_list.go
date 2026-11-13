package eval

import (
	"context"
	"errors"
	"slices"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

type RunPage struct {
	Items      []RunStatus `json:"items"`
	NextCursor *int64      `json:"next_cursor" nullable:"true"`
}

func (s *RunStore) List(ctx context.Context, actor retrieval.Actor, kbID, cursor int64, limit int32) (_ RunPage, resultErr error) {
	if cursor < 0 || limit < 1 || limit > 10 {
		return RunPage{}, ErrInvalidCase
	}
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return RunPage{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return RunPage{}, err
	}
	ids, err := q.ListVisibleEvalRunIDs(ctx, dbquery.ListVisibleEvalRunIDsParams{TenantID: actor.TenantID, KbID: kbID, Cursor: cursor, PageSize: limit + 1})
	if err != nil {
		return RunPage{}, err
	}
	page := RunPage{Items: make([]RunStatus, 0, len(ids))}
	runs := make([]StoredRun, 0, len(ids))
	reports := make([]*RunReport, 0, len(ids))
	documents := map[int64]bool{}
	evidence := map[int64]Evidence{}
	for _, id := range ids {
		row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
		if err != nil {
			return RunPage{}, err
		}
		run, err := decodeStoredRun(row)
		if err != nil {
			return RunPage{}, err
		}
		state, err := q.GetEvalRunState(ctx, dbquery.GetEvalRunStateParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
		if err != nil {
			return RunPage{}, err
		}
		if (state.Status == "succeeded") != state.HasResult {
			return RunPage{}, errors.New("evaluation job and result state disagree")
		}
		var report *RunReport
		if state.HasResult {
			stored, err := loadRunResult(ctx, q, actor.TenantID, kbID, id)
			if err != nil {
				return RunPage{}, err
			}
			report = &stored.Report
			bindings, _, _, err := reportBindings(*report)
			if err != nil {
				return RunPage{}, err
			}
			for chunk, item := range bindings {
				if previous, ok := evidence[chunk]; ok && previous != item {
					return RunPage{}, ErrReportInvalid
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
		item := RunStatus{ID: id, JobID: run.JobID, Set: run.Request.Set, Mode: run.Request.Mode, Status: state.Status, Attempts: state.Attempts, MaxAttempts: state.MaxAttempts, ResultAvailable: state.HasResult, CreatedAt: run.CreatedAt}
		if state.LastError.Valid {
			item.FailureCode = &state.LastError.String
		}
		if state.FinishedAt.Valid {
			item.FinishedAt = &state.FinishedAt.Time
		}
		page.Items = append(page.Items, item)
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
	// Lock the union including lookahead before any per-run check. Empty pages
	// also recheck membership so revocation cannot masquerade as an empty list.
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, evidence, false); err != nil {
		return RunPage{}, err
	}
	for i, run := range runs {
		if err := s.checkExecution(ctx, q, actor, run, reports[i], false); err != nil {
			return RunPage{}, err
		}
	}
	if len(page.Items) > int(limit) {
		page.Items = page.Items[:limit]
		next := page.Items[len(page.Items)-1].ID
		page.NextCursor = &next
	}
	if err := tx.Commit(ctx); err != nil {
		return RunPage{}, err
	}
	return page, nil
}
