package eval

import (
	"context"
	"errors"
	"math"
	"testing"

	"tuskrag.local/core/internal/retrieval"
)

type searchFunc func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)

func (f searchFunc) Search(ctx context.Context, a retrieval.Actor, r retrieval.Request) (retrieval.Result, error) {
	return f(ctx, a, r)
}

func TestRunnerRejectsFailuresAndUnauthorizedDebugEvidence(t *testing.T) {
	dataset := Dataset{Version: "test", Documents: []Document{{Key: "a", Title: "A", Content: "fixture"}}, Questions: []Question{{ID: "q", Question: "question", Expected: []string{"a"}, Answerable: true}}}
	config := RunConfig{Actor: retrieval.Actor{TenantID: 1, UserID: 2}, KnowledgeBaseID: 3, Documents: map[string]int64{"a": 4}, AllowedDocumentIDs: []int64{4}, ModelKind: "stub", ModelLabel: "test"}
	for _, tc := range []struct {
		name         string
		result       retrieval.Result
		err          error
		unauthorized int
	}{
		{name: "service failure", err: errors.New("failed")},
		{name: "missing trace", result: retrieval.Result{}},
		{name: "debug leak", result: retrieval.Result{Trace: &retrieval.Trace{Candidates: []retrieval.Candidate{{ChunkID: 1, DocumentID: 99}}}}, unauthorized: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Run(context.Background(), searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
				return tc.result, tc.err
			}), dataset, config, "rrf")
			if err == nil || report.Status != "failed" || report.UnauthorizedEvidence != tc.unauthorized {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestComparisonGate(t *testing.T) {
	baseline := comparisonReport()
	current := baseline
	current.Summary.Metrics = &Metrics{RecallAt20: 0.88, HitAt5: 0.9, MRRAt10: 0.9, NDCGAt10: 0.9}
	if err := Compare(baseline, current); err != nil {
		t.Fatal(err)
	}
	current.Summary.Metrics.RecallAt20 = 0.879
	if Compare(baseline, current) == nil {
		t.Fatal("regression accepted")
	}
	current = baseline
	current.UnauthorizedEvidence = 1
	if Compare(baseline, current) == nil {
		t.Fatal("permission leak accepted")
	}
	current = baseline
	current.ModelKind = "real"
	if Compare(baseline, current) == nil {
		t.Fatal("stub compared to real model")
	}
}

func comparisonReport() RunReport {
	return RunReport{Status: "passed", DatasetSHA256: "same", Mode: "rrf", ModelKind: "stub", Summary: Summary{Cases: 64, AnswerableCases: 54, UnanswerableCases: 10, Metrics: &Metrics{RecallAt20: 0.9, HitAt5: 0.9, MRRAt10: 0.9, NDCGAt10: 0.9}}}
}

func TestComparisonRejectsInvalidSummaries(t *testing.T) {
	for name, mutate := range map[string]func(*RunReport){
		"empty dataset identity":      func(r *RunReport) { r.DatasetSHA256 = "" },
		"unknown mode":                func(r *RunReport) { r.Mode = "unknown" },
		"unknown model kind":          func(r *RunReport) { r.ModelKind = "unknown" },
		"no cases":                    func(r *RunReport) { r.Summary.Cases = 0 },
		"negative count":              func(r *RunReport) { r.Summary.UnanswerableCases = -1 },
		"inconsistent counts":         func(r *RunReport) { r.Summary.AnswerableCases = 53 },
		"no answerable cases":         func(r *RunReport) { r.Summary.AnswerableCases = 0; r.Summary.UnanswerableCases = 64 },
		"missing metrics":             func(r *RunReport) { r.Summary.Metrics = nil },
		"NaN":                         func(r *RunReport) { r.Summary.Metrics.MRRAt10 = math.NaN() },
		"infinity":                    func(r *RunReport) { r.Summary.Metrics.RecallAt20 = math.Inf(1) },
		"negative metric":             func(r *RunReport) { r.Summary.Metrics.HitAt5 = -0.1 },
		"metric above one":            func(r *RunReport) { r.Summary.Metrics.NDCGAt10 = 1.1 },
		"unauthorized evidence":       func(r *RunReport) { r.UnauthorizedEvidence = 1 },
		"negative unauthorized count": func(r *RunReport) { r.UnauthorizedEvidence = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := comparisonReport()
			mutate(&invalid)
			if Compare(invalid, invalid) == nil || Compare(invalid, comparisonReport()) == nil || Compare(comparisonReport(), invalid) == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	current := comparisonReport()
	current.Summary.AnswerableCases--
	current.Summary.UnanswerableCases++
	if Compare(comparisonReport(), current) == nil {
		t.Fatal("different answerable populations accepted")
	}
}
