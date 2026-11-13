package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/retrieval"
)

type answerStreamerFunc func(context.Context, retrieval.Actor, answer.Request, func(answer.Event) error) error

func (f answerStreamerFunc) Stream(ctx context.Context, a retrieval.Actor, r answer.Request, emit func(answer.Event) error) error {
	return f(ctx, a, r, emit)
}

type judgeFunc func(context.Context, string, string, []answer.AnswerEvidence) (CitationJudgment, error)

func (f judgeFunc) Judge(ctx context.Context, q, a string, ev []answer.AnswerEvidence) (CitationJudgment, error) {
	return f(ctx, q, a, ev)
}

func sampleAnswerDataset() AnswerDataset {
	return AnswerDataset{
		Version: "answer-test-v1",
		Documents: []Document{
			{Key: "docA", Title: "A", Content: "北京每晚住宿上限 600 元。"},
			{Key: "docB", Title: "B", Content: "请假需提前三个工作日申请。"},
		},
		Questions: []AnswerQuestion{
			{ID: "q1", Question: "北京住宿标准？", Expected: []string{"docA"}, Answerable: true, Contains: []string{"600"}, Forbidden: []string{"999"}},
			{ID: "q2", Question: "年假需要提前几天申请？", Expected: []string{"docB"}, Answerable: true, Contains: []string{"三"}},
			{ID: "q3", Question: "火星分公司报销额度是多少？", Expected: []string{}, Answerable: false},
		},
	}
}

func sampleAnswerConfig() AnswerRunConfig {
	backtrack := true
	return AnswerRunConfig{
		Actor:               retrieval.Actor{TenantID: 1, UserID: 2},
		KnowledgeBaseID:     3,
		ChatModelID:         4,
		Documents:           map[string]int64{"docA": 10, "docB": 20},
		AllowedDocumentIDs:  []int64{10, 20},
		ModelLabel:          "stub-chat",
		ModelKind:           "stub",
		ParentBacktrack:     &backtrack,
		ContextBudgetTokens: 6000,
	}
}

func TestAnswerDatasetValidationRejectsUnlabeledAndBadExpected(t *testing.T) {
	var unlabeled AnswerQuestion
	if err := json.Unmarshal([]byte(`{"id":"q1","question":"问题","expected":[]}`), &unlabeled); err == nil {
		t.Fatal("missing answerable boolean accepted")
	}

	ds := sampleAnswerDataset()
	ds.Questions[0].Expected = []string{"unknown"}
	if ValidateAnswerDataset(ds) == nil {
		t.Fatal("unknown document key accepted")
	}

	ds = sampleAnswerDataset()
	ds.Questions[2].Expected = []string{"docA"}
	if ValidateAnswerDataset(ds) == nil {
		t.Fatal("unanswerable with expected document accepted")
	}
}

func TestRunAnswerComputesMetricsAndRejectsUnauthorized(t *testing.T) {
	ds := sampleAnswerDataset()
	cfg := sampleAnswerConfig()

	streamer := answerStreamerFunc(func(_ context.Context, _ retrieval.Actor, req answer.Request, emit func(answer.Event) error) error {
		switch req.Question {
		case "北京住宿标准？":
			if err := emit(answer.Event{Retrieval: &answer.RetrievalEvent{
				Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_1", ChunkIDs: []int64{1, 2}, Content: "北京每晚 600 元。", DocumentID: 10, VersionID: 1, Title: "A", SectionID: 5, HeadingPath: []string{"一"}}},
				Degraded: []string{},
			}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Delta: &answer.Delta{Text: "北京上限 600 元 [1]。"}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{{N: 1, EvidenceID: "c_1"}}}}); err != nil {
				return err
			}
			return emit(answer.Event{Done: &answer.Done{MessageID: "m1", Outcome: "answered", PromptVersion: answer.PromptVersion}})
		case "年假需要提前几天申请？":
			if err := emit(answer.Event{Retrieval: &answer.RetrievalEvent{
				Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_2", ChunkIDs: []int64{3}, Content: "提前三个工作日申请。", DocumentID: 20, VersionID: 1, Title: "B", SectionID: 6, HeadingPath: []string{"二"}}},
				Degraded: []string{},
			}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Delta: &answer.Delta{Text: "提前三个工作日 [1]。"}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{{N: 1, EvidenceID: "c_2"}}}}); err != nil {
				return err
			}
			return emit(answer.Event{Done: &answer.Done{MessageID: "m2", Outcome: "answered", PromptVersion: answer.PromptVersion}})
		default:
			if err := emit(answer.Event{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{}, Degraded: []string{}}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Delta: &answer.Delta{Text: answer.Refusal}}); err != nil {
				return err
			}
			if err := emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{}}}); err != nil {
				return err
			}
			return emit(answer.Event{Done: &answer.Done{MessageID: "m3", Outcome: "refused", PromptVersion: answer.PromptVersion}})
		}
	})

	report, err := RunAnswer(context.Background(), streamer, nil, ds, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAnswerReport(report); err != nil {
		t.Fatalf("report validation failed: %v", err)
	}
	if report.Summary.Metrics.CitationSupportRate != nil {
		t.Fatalf("expected nil citation_support_rate without judge: %v", *report.Summary.Metrics.CitationSupportRate)
	}
	if report.Summary.Metrics.ValidCitationRate != 1 || *report.Summary.Metrics.RefusalCorrectRate != 1 ||
		*report.Summary.Metrics.DeterministicPassRate != 1 || *report.Summary.Metrics.FalseRefusalRate != 0 ||
		*report.Summary.Metrics.UnsupportedRate != 0 || report.Rows[0].EvidenceChunkCounts[0] != 2 {
		t.Fatalf("unexpected report summary/rows: %+v %+v", report.Summary, report.Rows)
	}

	// Verify judge-enabled run records CitationSupportRate accurately.
	cfgWithJudge := cfg
	cfgWithJudge.Judge = &AnswerJudgeConfig{ChatModelID: 9, ModelLabel: "stub-judge"}
	judge := judgeFunc(func(_ context.Context, q, _ string, _ []answer.AnswerEvidence) (CitationJudgment, error) {
		if strings.Contains(q, "北京") {
			return CitationJudgment{TotalClaims: 2, UnsupportedClaims: 0}, nil
		}
		return CitationJudgment{TotalClaims: 2, UnsupportedClaims: 1}, nil
	})
	judgedReport, err := RunAnswer(context.Background(), streamer, judge, ds, cfgWithJudge)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAnswerReport(judgedReport); err != nil {
		t.Fatalf("judged report invalid: %v", err)
	}
	if judgedReport.Summary.Metrics.CitationSupportRate == nil || *judgedReport.Summary.Metrics.CitationSupportRate != 0.75 {
		t.Fatalf("expected 0.75 citation support rate, got %+v", judgedReport.Summary.Metrics.CitationSupportRate)
	}

	// Verify unauthorized document triggers failure and nonzero UnauthorizedEvidence.
	leakStreamer := answerStreamerFunc(func(_ context.Context, _ retrieval.Actor, _ answer.Request, emit func(answer.Event) error) error {
		if err := emit(answer.Event{Retrieval: &answer.RetrievalEvent{
			Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_99", Content: "机密", DocumentID: 999, VersionID: 1, Title: "Leak", SectionID: 1, HeadingPath: []string{}}},
		}}); err != nil {
			return err
		}
		if err := emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{}}}); err != nil {
			return err
		}
		return emit(answer.Event{Done: &answer.Done{MessageID: "m", Outcome: "refused", PromptVersion: answer.PromptVersion}})
	})
	leaked, err := RunAnswer(context.Background(), leakStreamer, nil, ds, cfg)
	if err == nil || leaked.Status != "failed" || leaked.UnauthorizedEvidence != 1 {
		t.Fatalf("expected leak failure, got err=%v report=%+v", err, leaked)
	}
}

func TestCompareAnswerReportsGate(t *testing.T) {
	ds := sampleAnswerDataset()
	cfg := sampleAnswerConfig()
	baseStream := answerStreamerFunc(func(_ context.Context, _ retrieval.Actor, req answer.Request, emit func(answer.Event) error) error {
		if req.Question == "火星分公司报销额度是多少？" {
			_ = emit(answer.Event{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{}}})
			_ = emit(answer.Event{Delta: &answer.Delta{Text: answer.Refusal}})
			_ = emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{}}})
			return emit(answer.Event{Done: &answer.Done{MessageID: "m", Outcome: "refused", PromptVersion: answer.PromptVersion}})
		}
		docID := int64(10)
		text := "北京上限 600 元 [1]。"
		if strings.Contains(req.Question, "年假") {
			docID = 20
			text = "提前三个工作日 [1]。"
		}
		_ = emit(answer.Event{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_1", Content: "原文", DocumentID: docID, VersionID: 1, Title: "T", SectionID: 1, HeadingPath: []string{}}}}})
		_ = emit(answer.Event{Delta: &answer.Delta{Text: text}})
		_ = emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{{N: 1, EvidenceID: "c_1"}}}})
		return emit(answer.Event{Done: &answer.Done{MessageID: "m", Outcome: "answered", PromptVersion: answer.PromptVersion}})
	})
	baseline, err := RunAnswer(context.Background(), baseStream, nil, ds, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Regression on negative metric (false refusal rate jumps from 0 to 0.5).
	regressedStream := answerStreamerFunc(func(ctx context.Context, a retrieval.Actor, req answer.Request, emit func(answer.Event) error) error {
		if strings.Contains(req.Question, "年假") {
			_ = emit(answer.Event{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_1", Content: "原文", DocumentID: 20, VersionID: 1, Title: "T", SectionID: 1, HeadingPath: []string{}}}}})
			_ = emit(answer.Event{Delta: &answer.Delta{Text: answer.Refusal}})
			_ = emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{}}})
			return emit(answer.Event{Done: &answer.Done{MessageID: "m", Outcome: "refused", PromptVersion: answer.PromptVersion}})
		}
		return baseStream(ctx, a, req, emit)
	})
	current, err := RunAnswer(context.Background(), regressedStream, nil, ds, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := CompareAnswerReports(baseline, current)
	if err != nil || cmp.Gate != "regressed" {
		t.Fatalf("expected regressed gate, got cmp=%+v err=%v", cmp, err)
	}

	// Mismatched judge presence must be rejected as incomparable.
	judged, err := RunAnswer(context.Background(), baseStream, judgeFunc(func(context.Context, string, string, []answer.AnswerEvidence) (CitationJudgment, error) {
		return CitationJudgment{TotalClaims: 1, UnsupportedClaims: 0}, nil
	}), ds, func() AnswerRunConfig {
		c := cfg
		c.Judge = &AnswerJudgeConfig{ChatModelID: 1, ModelLabel: "j", PromptVersion: JudgePromptVersion}
		return c
	}())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompareAnswerReports(baseline, judged); !errors.Is(err, ErrReportsIncomparable) {
		t.Fatalf("expected incomparable error, got %v", err)
	}
}

func TestDecodeCitationJudgmentValidation(t *testing.T) {
	if j, err := decodeCitationJudgment(`{"total_claims": 3, "unsupported_claims": 1}`); err != nil || j.TotalClaims != 3 || j.UnsupportedClaims != 1 {
		t.Fatalf("valid judgment rejected: %+v %v", j, err)
	}
	for _, bad := range []string{
		`{"total_claims": 1, "unsupported_claims": 2}`,
		`{"total_claims": -1, "unsupported_claims": 0}`,
		`{"total_claims": 1}`,
		`{"total_claims": 1, "unsupported_claims": 0, "extra": true}`,
		`{"total_claims": 1, "unsupported_claims": 0} trailing`,
	} {
		if _, err := decodeCitationJudgment(bad); err == nil {
			t.Fatalf("invalid judgment accepted: %s", bad)
		}
	}
}
