package eval

import (
	"errors"
	"fmt"
	"slices"
)

var ErrReportsIncomparable = errors.New("evaluation reports are not comparable")

type RowComparison struct {
	ID              string   `json:"id"`
	Baseline        RunRow   `json:"baseline"`
	Current         RunRow   `json:"current"`
	Delta           *Metrics `json:"delta"`
	EvidenceChanged bool     `json:"evidence_changed"`
}

type ReportComparison struct {
	BaselineDigest string          `json:"baseline_digest"`
	CurrentDigest  string          `json:"current_digest"`
	Gate           string          `json:"gate" enum:"passed,regressed"`
	Baseline       Summary         `json:"baseline"`
	Current        Summary         `json:"current"`
	Delta          Metrics         `json:"delta"`
	Rows           []RowComparison `json:"rows"`
	TotalRows      int             `json:"total_rows"`
	NextOffset     *int32          `json:"next_offset" nullable:"true"`
}

// CompareReports aligns full artifacts by question ID, never by array position.
// Delta is current minus baseline. A regressed result is still a valid comparison.
func CompareReports(baseline, current RunReport) (ReportComparison, error) {
	for _, report := range []RunReport{baseline, current} {
		if err := ValidateReport(report); err != nil {
			return ReportComparison{}, fmt.Errorf("%w: %w", ErrReportInvalid, err)
		}
	}
	if !sameComparisonPopulation(baseline, current) || baseline.Summary.AnswerableCases == 0 {
		return ReportComparison{}, ErrReportsIncomparable
	}
	aligned, err := alignComparisonRows(baseline, current)
	if err != nil {
		return ReportComparison{}, err
	}
	delta := metricDifference(*baseline.Summary.Metrics, *current.Summary.Metrics)
	result := ReportComparison{Gate: "passed", Baseline: baseline.Summary, Current: current.Summary, Delta: delta, Rows: make([]RowComparison, 0, len(baseline.Rows)), TotalRows: len(baseline.Rows)}
	if metricRegression(delta) {
		result.Gate = "regressed"
	}
	for _, before := range baseline.Rows {
		after := aligned[before.ID]
		row := RowComparison{ID: before.ID, Baseline: before, Current: after, EvidenceChanged: !slices.Equal(before.Fused, after.Fused) || !slices.Equal(before.Ranked, after.Ranked)}
		if before.Metrics != nil {
			value := metricDifference(*before.Metrics, *after.Metrics)
			row.Delta = &value
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

func sameComparisonPopulation(a, b RunReport) bool {
	return a.DatasetSHA256 == b.DatasetSHA256 && a.Mode == b.Mode && a.ModelKind == b.ModelKind && a.Summary.Cases == b.Summary.Cases && a.Summary.AnswerableCases == b.Summary.AnswerableCases
}

func alignComparisonRows(a, b RunReport) (map[string]RunRow, error) {
	rows := make(map[string]RunRow, len(b.Rows))
	for _, row := range b.Rows {
		rows[row.ID] = row
	}
	for _, row := range a.Rows {
		other, ok := rows[row.ID]
		if !ok || (row.Metrics == nil) != (other.Metrics == nil) {
			return nil, ErrReportsIncomparable
		}
	}
	return rows, nil
}

func metricDifference(a, b Metrics) Metrics {
	return Metrics{RecallAt20: b.RecallAt20 - a.RecallAt20, HitAt5: b.HitAt5 - a.HitAt5, MRRAt10: b.MRRAt10 - a.MRRAt10, NDCGAt10: b.NDCGAt10 - a.NDCGAt10}
}

func metricRegression(delta Metrics) bool {
	for _, value := range []float64{delta.RecallAt20, delta.HitAt5, delta.MRRAt10, delta.NDCGAt10} {
		if value < -0.02-1e-12 {
			return true
		}
	}
	return false
}
