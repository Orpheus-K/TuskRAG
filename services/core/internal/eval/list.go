package eval

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

type ReportListItem struct {
	Digest         string    `json:"digest"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	DatasetVersion string    `json:"dataset_version"`
	ModelLabel     string    `json:"model_label"`
	ModelKind      string    `json:"model_kind"`
	Mode           string    `json:"mode"`
	Summary        Summary   `json:"summary"`
}

type ReportPage struct {
	Items      []ReportListItem `json:"items"`
	NextCursor *string          `json:"next_cursor" nullable:"true"`
}

// List uses stable digest ordering, independent of caller-controlled report
// labels. Refresh from an empty cursor to include newly imported reports.
func (s *ReportStore) List(ctx context.Context, actor retrieval.Actor, kbID int64, cursor string, limit int32) (_ ReportPage, resultErr error) {
	if limit < 1 || limit > 10 {
		return ReportPage{}, ErrReportInvalid
	}
	if cursor != "" {
		decoded, err := hex.DecodeString(cursor)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != cursor {
			return ReportPage{}, ErrReportInvalid
		}
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return ReportPage{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return ReportPage{}, err
	}
	rows, err := q.ListVisibleEvalReports(ctx, dbquery.ListVisibleEvalReportsParams{TenantID: actor.TenantID, KbID: kbID, Cursor: cursor, PageSize: limit + 1})
	if err != nil {
		return ReportPage{}, fmt.Errorf("list evaluation reports: %w", err)
	}
	page := ReportPage{Items: make([]ReportListItem, 0, len(rows))}
	expected := map[int64]Evidence{}
	documents := map[int64]bool{}
	for _, row := range rows {
		var report RunReport
		if err := json.Unmarshal(row.Metadata, &report); err != nil {
			return ReportPage{}, fmt.Errorf("decode report metadata: %w", err)
		}
		var evidence []Evidence
		if err := json.Unmarshal(row.Evidence, &evidence); err != nil {
			return ReportPage{}, fmt.Errorf("decode report bindings: %w", err)
		}
		for _, item := range evidence {
			if previous, ok := expected[item.ChunkID]; ok && previous != item {
				return ReportPage{}, ErrReportNotFound
			}
			expected[item.ChunkID] = item
			documents[item.DocumentID] = true
		}
		page.Items = append(page.Items, ReportListItem{Digest: row.Digest, Source: row.Source, CreatedAt: row.CreatedAt.Time, DatasetVersion: report.DatasetVersion, ModelLabel: report.ModelLabel, ModelKind: report.ModelKind, Mode: report.Mode, Summary: report.Summary})
	}
	docs := make([]int64, 0, len(documents))
	chunks := make([]int64, 0, len(expected))
	for id := range documents {
		docs = append(docs, id)
	}
	for id := range expected {
		chunks = append(chunks, id)
	}
	slices.Sort(docs)
	slices.Sort(chunks)
	// Also recheck an empty page: revocation must not become a successful empty list.
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, expected, false); err != nil {
		return ReportPage{}, err
	}
	if len(page.Items) > int(limit) {
		page.Items = page.Items[:limit]
		cursor := page.Items[len(page.Items)-1].Digest
		page.NextCursor = &cursor
	}
	if err := tx.Commit(ctx); err != nil {
		return ReportPage{}, fmt.Errorf("commit report listing: %w", err)
	}
	return page, nil
}
