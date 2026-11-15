package answer

import (
	"context"
	"strings"
	"testing"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

func TestClassifyQueryComplexity(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"北京差旅住宿上限是多少？", "standard"},
		{"对比北京和上海的差旅住宿上限差异", "agentic"},
		{"第12条关于违约金的规定是什么？", "agentic"},
		{"ERR-404 错误码表示什么含义？", "agentic"},
		{"报销流程是什么？审批人是谁？", "agentic"},
	}
	for _, tc := range cases {
		if got := classifyQueryComplexity(tc.q); got != tc.want {
			t.Fatalf("classifyQueryComplexity(%q) = %q, want %q", tc.q, got, tc.want)
		}
	}
}

type routingTestGenerator struct {
	responses []string
	idx       int
}

func (g *routingTestGenerator) Stream(_ context.Context, _ []models.ChatMessage, onDelta func(string) error) (models.ChatResult, error) {
	resp := g.responses[g.idx]
	if g.idx < len(g.responses)-1 {
		g.idx++
	}
	if err := onDelta(resp); err != nil {
		return models.ChatResult{}, err
	}
	return models.ChatResult{Finish: "stop", Usage: &models.ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}, nil
}
func (g *routingTestGenerator) Check(context.Context) error { return nil }
func (g *routingTestGenerator) Close()                      {}

type agenticTestSearch struct{}

func (agenticTestSearch) Search(_ context.Context, _ retrieval.Actor, _ retrieval.Request) (retrieval.Result, error) {
	return retrieval.Result{
		RetrievalRevision: 1,
		Candidates: []retrieval.Candidate{{
			ChunkID:    101,
			DocumentID: 1,
			VersionID:  1,
			SectionID:  10,
			Title:      "差旅制度",
			Content:    "北京住宿上限 600 元/天。",
			TrustTier:  "trusted",
		}},
	}, nil
}

func (agenticTestSearch) ExactMatch(_ context.Context, _ retrieval.Actor, _ int64, pattern string, _ bool, _ int) ([]retrieval.Candidate, error) {
	if !strings.Contains("ERR-404 未找到资源", pattern) {
		return nil, nil
	}
	return []retrieval.Candidate{{
		ChunkID:    102,
		DocumentID: 2,
		VersionID:  1,
		SectionID:  20,
		Title:      "错误码",
		Content:    "ERR-404 未找到资源。",
		TrustTier:  "standard",
	}}, nil
}

type routingTestGuard struct{}

func (routingTestGuard) AuthorizeEvidence(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error) {
	return models.EgressScope{}, nil
}

func TestAgenticEarlyStopOnDuplicateAndFinal(t *testing.T) {
	gen := &routingTestGenerator{
		responses: []string{
			`{"action":"tool","tool":"exact_match","args":{"pattern":"ERR-404"}}`,
			// Planner repeats the exact same tool call -> should stop early, NOT run 6 steps or truncate
			`{"action":"tool","tool":"exact_match","args":{"pattern":"ERR-404"}}`,
			`ERR-404 表示未找到资源 [1]。`,
		},
	}
	svc := &Service{
		Search:      agenticTestSearch{},
		Guard:       routingTestGuard{},
		Open:        func(context.Context, retrieval.Actor, int64, func(context.Context) (models.EgressScope, error)) (Generator, error) { return gen, nil },
		Audit:       func(context.Context, retrieval.Actor, Request, string, int, int) error { return nil },
		CountTokens: func(_ context.Context, s string) (int, error) { return len([]rune(s)), nil },
		Agent:       AgentLimits{MaxSteps: 6},
	}

	var steps int
	var done *Done
	var ev []AnswerEvidence
	err := svc.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, Request{
		KnowledgeBaseID: 1,
		ModelID:         1,
		Question:        "ERR-404 是什么？",
		Mode:            "auto",
	}, func(e Event) error {
		if e.Step != nil {
			steps++
		}
		if e.Retrieval != nil {
			ev = e.Retrieval.Evidence
		}
		if e.Done != nil {
			done = e.Done
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if steps != 1 {
		t.Fatalf("expected 1 step before duplicate stop, got %d", steps)
	}
	if done == nil || done.Outcome != "answered" {
		t.Fatalf("expected answered outcome (not truncated), got %+v", done)
	}
	if len(ev) != 1 || ev[0].TrustTier != "standard" {
		t.Fatalf("expected 1 evidence with trust_tier=standard, got %+v", ev)
	}
}
