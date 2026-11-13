package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
)

func TestValidateReportCommand(t *testing.T) {
	report := eval.RunReport{
		DatasetVersion: "v1", DatasetSHA256: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("b", 64),
		ModelLabel: "fixture", ModelKind: "stub", Mode: "rrf", Status: "passed",
		Summary:   eval.Summary{Cases: 1, UnanswerableCases: 1},
		Rows:      []eval.RunRow{{ID: "q1", TimingsMS: map[string]float64{"total": 1}}},
		LatencyMS: map[string][2]float64{"total": {1, 1}},
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"complete":      string(data),
		"trailing":      string(data) + " {}",
		"truncated":     string(data[:len(data)-1]),
		"unknown field": `{"unknown":true}`,
		"empty":         `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			err := runEval(context.Background(), []string{"validate", path})
			if (err == nil) != (name == "complete") {
				t.Fatalf("validate error=%v", err)
			}
		})
	}
}

func TestValidateSetCommand(t *testing.T) {
	for name, content := range map[string]string{
		"valid":     `{"name":"验收集","cases":[{"id":"q1","question":"未知？","answerable":false,"expected":[],"tags":[]}]}`,
		"unlabeled": `{"name":"验收集","cases":[{"id":"q1","question":"未知？","expected":[],"tags":[]}]}`,
		"empty":     `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "set.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			err := runEval(context.Background(), []string{"validate-set", path})
			if (err == nil) != (name == "valid") {
				t.Fatalf("validate-set: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("validation changed source")
			}
		})
	}
}

func TestRunSetCommandRejectsUnpinnedBinding(t *testing.T) {
	for _, content := range []string{`{"set_id":9,"revision":0}`, `{"set_id":0,"revision":1}`, `{"set_id":9,"revision":1,"unexpected":true}`} {
		path := filepath.Join(t.TempDir(), "binding.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runEval(context.Background(), []string{"run-set", path, "missing-config.json", "rrf", "report.json"}); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
}

func TestAnswerValidateAndCompareCommands(t *testing.T) {
	hit := true
	rate1 := 1.0
	rate0 := 0.0
	base := eval.AnswerReport{
		DatasetVersion: "v1", DatasetSHA256: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("b", 64),
		ModelLabel: "fixture", ModelKind: "stub", PromptVersion: "grounded-v3",
		Assembly: eval.AnswerAssemblyReport{ParentBacktrack: true, ContextBudgetTokens: 6000},
		Status:   "passed",
		Summary: eval.AnswerSummary{
			Cases: 2, AnswerableCases: 1, UnanswerableCases: 1,
			Metrics: eval.AnswerMetrics{
				ValidCitationRate: 1, SourceIntegrityRate: 1,
				EvidenceHitRate: &rate1, RefusalCorrectRate: &rate1, FalseRefusalRate: &rate0, UnsupportedRate: &rate0,
			},
		},
		Rows: []eval.AnswerRow{
			{ID: "q1", Answerable: true, Outcome: "answered", ValidCitation: true, SourceIntegrity: true, EvidenceHit: &hit, EvidenceIDs: []string{"c_1"}, EvidenceChunkCounts: []int{2}, EvidenceDocuments: []int64{10}, Degraded: []string{}, CitationCount: 1, TimingsMS: map[string]float64{"total": 5}},
			{ID: "q2", Answerable: false, Outcome: "refused", ValidCitation: true, SourceIntegrity: true, EvidenceIDs: []string{}, EvidenceChunkCounts: []int{}, EvidenceDocuments: []int64{}, Degraded: []string{}, TimingsMS: map[string]float64{"total": 2}},
		},
		LatencyMS: map[string][2]float64{"total": {2, 5}},
	}
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.json")
	curPath := filepath.Join(dir, "cur.json")
	badPath := filepath.Join(dir, "bad.json")
	rawBase, _ := json.Marshal(base)
	if err := os.WriteFile(basePath, rawBase, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEval(context.Background(), []string{"answer-validate", basePath}); err != nil {
		t.Fatalf("answer-validate failed: %v", err)
	}
	if err := os.WriteFile(badPath, append(rawBase, []byte(" trailing")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEval(context.Background(), []string{"answer-validate", badPath}); err == nil {
		t.Fatal("trailing answer report accepted")
	}

	regressed := base
	regressed.Rows = append([]eval.AnswerRow(nil), base.Rows...)
	regressed.Rows[0].Outcome = "refused"
	regressed.Rows[0].CitationCount = 0
	regressed.Summary.Metrics.FalseRefusalRate = &rate1
	rawReg, _ := json.Marshal(regressed)
	if err := os.WriteFile(curPath, rawReg, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEval(context.Background(), []string{"answer-compare", basePath, basePath}); err != nil {
		t.Fatalf("self compare failed: %v", err)
	}
	if err := runEval(context.Background(), []string{"answer-compare", basePath, curPath}); err == nil {
		t.Fatal("regression passed CLI gate")
	}
}
