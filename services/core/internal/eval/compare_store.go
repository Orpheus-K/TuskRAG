package eval

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"

	"tuskrag.local/core/internal/retrieval"
)

func (s *ReportStore) Compare(ctx context.Context, actor retrieval.Actor, kbID int64, baselineDigest, currentDigest string, offset, limit int32) (_ ReportComparison, resultErr error) {
	if offset < 0 || offset > 10000 || limit < 1 || limit > 50 {
		return ReportComparison{}, ErrReportInvalid
	}
	for _, digest := range []string{baselineDigest, currentDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
			return ReportComparison{}, ErrReportInvalid
		}
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return ReportComparison{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return ReportComparison{}, err
	}
	baseline, err := loadReport(ctx, q, actor.TenantID, kbID, baselineDigest)
	if err != nil {
		return ReportComparison{}, err
	}
	current, err := loadReport(ctx, q, actor.TenantID, kbID, currentDigest)
	if err != nil {
		return ReportComparison{}, err
	}
	expected := map[int64]Evidence{}
	documents := map[int64]bool{}
	for _, report := range []RunReport{baseline.Report, current.Report} {
		bindings, _, _, err := reportBindings(report)
		if err != nil {
			return ReportComparison{}, fmt.Errorf("stored report is corrupt: %w", err)
		}
		for id, item := range bindings {
			if previous, ok := expected[id]; ok && previous != item {
				return ReportComparison{}, ErrReportNotFound
			}
			expected[id] = item
			documents[item.DocumentID] = true
		}
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
	// One lock set covers both reports and all rows, including those outside the
	// requested page. A hidden baseline must not leak through aggregate deltas.
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, expected, false); err != nil {
		return ReportComparison{}, err
	}
	result, err := CompareReports(baseline.Report, current.Report)
	if err != nil {
		return ReportComparison{}, err
	}
	if int(offset) > len(result.Rows) {
		return ReportComparison{}, ErrReportInvalid
	}
	end := min(int(offset+limit), len(result.Rows))
	if end < len(result.Rows) {
		next := int32(end)
		result.NextOffset = &next
	}
	result.Rows = result.Rows[offset:end]
	result.BaselineDigest = baselineDigest
	result.CurrentDigest = currentDigest
	if err := tx.Commit(ctx); err != nil {
		return ReportComparison{}, fmt.Errorf("commit report comparison: %w", err)
	}
	return result, nil
}
