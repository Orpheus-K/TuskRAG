package eval

import (
	"context"
	"math"
	"testing"

	"tuskrag.local/core/internal/retrieval"
)

func completeReport(t *testing.T) RunReport {
	t.Helper()
	dataset := Dataset{Version: "test", Documents: []Document{{Key: "a", Title: "A", Content: "fixture"}}, Questions: []Question{
		{ID: "q1", Question: "question", Expected: []string{"a"}, Answerable: true},
		{ID: "q2", Question: "unanswerable"},
	}}
	config := RunConfig{Actor: retrieval.Actor{TenantID: 1, UserID: 2}, KnowledgeBaseID: 3, Documents: map[string]int64{"a": 4}, AllowedDocumentIDs: []int64{4}, ModelKind: "stub", ModelLabel: "test"}
	report, err := Run(context.Background(), searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{Trace: &retrieval.Trace{Candidates: []retrieval.Candidate{{ChunkID: 1, DocumentID: 4}}, TimingsMS: map[string]float64{"recall_rrf": 2}}}, nil
	}), dataset, config, "rrf")
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestValidateCompleteReport(t *testing.T) {
	report := completeReport(t)
	if err := ValidateReport(report); err != nil {
		t.Fatal(err)
	}
	if err := Compare(report, report); err != nil {
		t.Fatal(err)
	}
	// A complete no-answer dataset is displayable, but remains unsuitable for a
	// retrieval regression comparison, which requires answerable questions.
	report.Rows[0].Metrics = nil
	report.Summary = Summary{Cases: 2, UnanswerableCases: 2}
	if err := ValidateReport(report); err != nil {
		t.Fatal(err)
	}
	if Compare(report, report) == nil {
		t.Fatal("no-answer report accepted for retrieval comparison")
	}
}

func TestRejectInconsistentCompleteReports(t *testing.T) {
	for name, mutate := range map[string]func(*RunReport){
		"failed":                  func(r *RunReport) { r.Status = "failed" },
		"unsafe":                  func(r *RunReport) { r.UnauthorizedEvidence = 1 },
		"digest":                  func(r *RunReport) { r.ConfigSHA256 = "123" },
		"missing row":             func(r *RunReport) { r.Rows = r.Rows[:1] },
		"duplicate ID":            func(r *RunReport) { r.Rows[1].ID = r.Rows[0].ID },
		"empty ID":                func(r *RunReport) { r.Rows[0].ID = " " },
		"invalid location":        func(r *RunReport) { r.Rows[0].Fused[0].DocumentID = -1 },
		"inconsistent location":   func(r *RunReport) { r.Rows[0].Ranked = []Evidence{{ChunkID: 1, DocumentID: 9}} },
		"missing metric":          func(r *RunReport) { r.Rows[0].Metrics = nil },
		"NaN metric":              func(r *RunReport) { r.Rows[0].Metrics.MRRAt10 = math.NaN() },
		"out of range":            func(r *RunReport) { r.Rows[0].Metrics.RecallAt20 = 2 },
		"wrong average":           func(r *RunReport) { r.Summary.Metrics.MRRAt10 = 0.2 },
		"missing summary metrics": func(r *RunReport) { r.Summary.Metrics = nil },
		"invalid counts":          func(r *RunReport) { r.Summary.AnswerableCases = -1 },
		"missing total":           func(r *RunReport) { delete(r.Rows[0].TimingsMS, "total") },
		"infinite timing":         func(r *RunReport) { r.Rows[0].TimingsMS["total"] = math.Inf(1) },
		"negative timing":         func(r *RunReport) { r.Rows[0].TimingsMS["total"] = -1 },
		"missing stage":           func(r *RunReport) { delete(r.LatencyMS, "total") },
		"extra stage":             func(r *RunReport) { r.LatencyMS["other"] = [2]float64{0, 0} },
		"wrong percentile":        func(r *RunReport) { r.LatencyMS["recall_rrf"] = [2]float64{1, 2} },
		"NaN percentile":          func(r *RunReport) { r.LatencyMS["total"] = [2]float64{math.NaN(), 1} },
		"degraded rerank":         func(r *RunReport) { r.Mode = "rerank"; r.Rows[0].Degraded = []string{"rerank"} },
	} {
		t.Run(name, func(t *testing.T) {
			report := completeReport(t)
			mutate(&report)
			if ValidateReport(report) == nil || Compare(report, report) == nil {
				t.Fatal("invalid full report accepted")
			}
		})
	}
}

func TestSummaryOnlyReportIsNotComplete(t *testing.T) {
	report := comparisonReport()
	if ValidateReport(report) == nil {
		t.Fatal("summary-only baseline accepted as complete")
	}
	if err := Compare(report, report); err != nil {
		t.Fatal(err)
	}
}
