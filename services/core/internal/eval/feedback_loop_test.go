package eval

import (
	"context"
	"strings"
	"testing"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type costTestStreamer struct{}

func (costTestStreamer) Stream(_ context.Context, _ retrieval.Actor, _ answer.Request, emit func(answer.Event) error) error {
	if err := emit(answer.Event{Retrieval: &answer.RetrievalEvent{
		Evidence: []answer.AnswerEvidence{{N: 1, ID: "c_1", DocumentID: 10, Content: "北京住宿上限 600 元/天。"}},
		Degraded: []string{},
	}}); err != nil {
		return err
	}
	if err := emit(answer.Event{Delta: &answer.Delta{Text: "北京住宿上限为 600 元/天 [1]。"}}); err != nil {
		return err
	}
	if err := emit(answer.Event{Citations: &answer.Citations{Items: []answer.Citation{{N: 1, EvidenceID: "c_1"}}}}); err != nil {
		return err
	}
	return emit(answer.Event{Done: &answer.Done{
		MessageID:     "msg-1",
		Outcome:       "answered",
		PromptVersion: answer.PromptVersion,
		Usage:         &models.ChatUsage{PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200},
	}})
}

func TestRunAnswerTokenAndCostAndAcceptanceReport(t *testing.T) {
	dataset := AnswerDataset{
		Version: "cost-v1",
		Documents: []Document{
			{Key: "doc1", Title: "差旅制度", Content: "北京住宿上限 600 元/天。"},
		},
		Questions: []AnswerQuestion{
			{
				ID:         "q1",
				Question:   "北京住宿上限是多少？",
				Expected:   []string{"doc1"},
				Answerable: true,
				Contains:   []string{"600"},
			},
		},
	}
	report, err := RunAnswer(context.Background(), costTestStreamer{}, nil, dataset, AnswerRunConfig{
		Actor:               retrieval.Actor{TenantID: 1, UserID: 1},
		KnowledgeBaseID:     1,
		ChatModelID:         1,
		Documents:           map[string]int64{"doc1": 10},
		AllowedDocumentIDs:  []int64{10},
		ModelLabel:          "test-chat",
		ModelKind:           "stub",
		InputPricePer1MUSD:  0.50,
		OutputPricePer1MUSD: 1.50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.TotalTokens != 1200 || report.Summary.AvgTokensPerQuestion != 1200 {
		t.Fatalf("unexpected token summary: %+v", report.Summary)
	}
	// 1000*0.50/1e6 + 200*1.50/1e6 = 0.0005 + 0.0003 = 0.0008
	if report.Summary.EstimatedCostUSD < 0.00079 || report.Summary.EstimatedCostUSD > 0.00081 {
		t.Fatalf("unexpected cost: %v", report.Summary.EstimatedCostUSD)
	}
	md := RenderAcceptanceReportMarkdown(nil, &report)
	if !strings.Contains(md, "TuskRAG 入场评测与验收报告") || !strings.Contains(md, "1200") {
		t.Fatalf("unexpected acceptance markdown:\n%s", md)
	}
}
