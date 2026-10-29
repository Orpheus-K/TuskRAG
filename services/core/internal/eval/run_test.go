package eval

import (
	"context"
	"errors"
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
	baseline := RunReport{Status: "passed", DatasetSHA256: "same", Mode: "rrf", ModelKind: "stub", Summary: Summary{Cases: 64, Metrics: &Metrics{RecallAt20: 0.9, HitAt5: 0.9, MRRAt10: 0.9, NDCGAt10: 0.9}}}
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
