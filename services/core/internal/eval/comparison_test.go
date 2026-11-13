package eval

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestDetailedComparisonAlignmentAndGate(t *testing.T) {
	baseline, current := completeReport(t), completeReport(t)
	current.Rows[0].Metrics.MRRAt10 = 0.98
	current.Summary.Metrics.MRRAt10 = 0.98
	current.Rows[0].Ranked = []Evidence{{ChunkID: 2, DocumentID: 4}}
	slices.Reverse(current.Rows)
	result, err := CompareReports(baseline, current)
	if err != nil || result.Gate != "passed" || math.Abs(result.Delta.MRRAt10+0.02) > 1e-12 {
		t.Fatalf("boundary: %+v %v", result, err)
	}
	if result.Rows[0].ID != "q1" || result.Rows[0].Current.ID != "q1" || !result.Rows[0].EvidenceChanged || result.Rows[1].Delta != nil {
		t.Fatalf("alignment or null metrics: %+v", result.Rows)
	}
	current.Rows[1].Metrics.MRRAt10 = 0.979
	current.Summary.Metrics.MRRAt10 = 0.979
	result, err = CompareReports(baseline, current)
	if err != nil || result.Gate != "regressed" || Compare(baseline, current) == nil {
		t.Fatalf("regression: %+v %v", result, err)
	}
	reverse, err := CompareReports(current, baseline)
	if err != nil || reverse.Gate != "passed" || reverse.Delta.MRRAt10 <= 0 {
		t.Fatalf("direction: %+v %v", reverse, err)
	}
}

func TestDetailedComparisonRejectsMismatchedQuestions(t *testing.T) {
	for name, mutate := range map[string]func(*RunReport){
		"dataset":       func(r *RunReport) { r.DatasetSHA256 = strings.Repeat("d", 64) },
		"model kind":    func(r *RunReport) { r.ModelKind = "real" },
		"mode":          func(r *RunReport) { r.Mode = "vector" },
		"question set":  func(r *RunReport) { r.Rows[0].ID = "different" },
		"answerability": func(r *RunReport) { r.Rows[0].Metrics, r.Rows[1].Metrics = r.Rows[1].Metrics, r.Rows[0].Metrics },
	} {
		t.Run(name, func(t *testing.T) {
			before, after := completeReport(t), completeReport(t)
			mutate(&after)
			if _, err := CompareReports(before, after); err == nil {
				t.Fatal("incomparable full reports accepted")
			}
			if Compare(before, after) == nil {
				t.Fatal("CLI gate accepted mismatched report")
			}
		})
	}
	before, after := completeReport(t), completeReport(t)
	after.Rows[0].Metrics = nil
	after.Summary = Summary{Cases: 2, UnanswerableCases: 2}
	if _, err := CompareReports(after, after); !errors.Is(err, ErrReportsIncomparable) {
		t.Fatalf("no-answer population: %v", err)
	}
	after = completeReport(t)
	after.UnauthorizedEvidence = 1
	if _, err := CompareReports(before, after); !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("unsafe artifact: %v", err)
	}
}
