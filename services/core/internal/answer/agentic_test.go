package answer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type stubAgentTools struct {
	calls []string
}

func (s *stubAgentTools) ExecuteTool(_ context.Context, _ retrieval.Actor, allowedKBs []int64, name string, rawArgs json.RawMessage) (any, error) {
	s.calls = append(s.calls, name+":"+string(rawArgs))
	switch name {
	case "search":
		return map[string]any{
			"status": "ok",
			"evidence": []map[string]any{
				{
					"id":           "c_101",
					"content":      "出差住宿报销上限为每晚 500 元。",
					"document_id":  10,
					"version_id":   1,
					"title":        "差旅制度",
					"section_id":   201,
					"heading_path": []string{"住宿标准"},
					"pages":        []int32{3},
				},
			},
			"degraded": []string{},
		}, nil
	case "get_neighbors":
		return map[string]any{
			"citation": map[string]any{
				"document_id":  10,
				"version_id":   1,
				"title":        "差旅制度",
				"section_id":   201,
				"heading_path": []string{"住宿标准"},
			},
			"chunks": []map[string]any{
				{
					"id":      102,
					"content": "一线城市可上浮至 700 元。",
					"pages":   []int32{3},
				},
			},
		}, nil
	default:
		return map[string]any{"allowed_kbs": allowedKBs}, nil
	}
}

type scriptedGenerator struct {
	responses []string
	idx       int
}

func (g *scriptedGenerator) Stream(_ context.Context, _ []models.ChatMessage, emit func(string) error) (models.ChatResult, error) {
	out := `{"action":"final"}`
	if g.idx < len(g.responses) {
		out = g.responses[g.idx]
	}
	g.idx++
	if err := emit(out); err != nil {
		return models.ChatResult{}, err
	}
	return models.ChatResult{
		Finish: "stop",
		Usage:  &models.ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (g *scriptedGenerator) Check(context.Context) error { return nil }
func (g *scriptedGenerator) Close()                      {}

type allowGuard struct{}

func (allowGuard) AuthorizeEvidence(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error) {
	return models.EgressScope{}, nil
}

type noopSearch struct{}

func (noopSearch) Search(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
	return retrieval.Result{}, nil
}

func TestAgenticStreamMultiStepAndCitations(t *testing.T) {
	tools := &stubAgentTools{}
	gen := &scriptedGenerator{
		responses: []string{
			`{"action":"tool","tool":"search","args":{"query":"住宿标准","kb_ids":[999]}}`,
			`{"action":"tool","tool":"get_neighbors","args":{"chunk_id":101,"after":1}}`,
			`{"action":"final"}`,
			`普通城市每晚 500 元[1]，一线城市可上浮至 700 元[2]。`,
		},
	}
	svc := &Service{
		Search:      noopSearch{},
		Guard:       allowGuard{},
		Tools:       tools,
		CountTokens: func(_ context.Context, s string) (int, error) { return len([]rune(s)), nil },
		Open: func(context.Context, retrieval.Actor, int64, func(context.Context) (models.EgressScope, error)) (Generator, error) {
			return gen, nil
		},
		Audit: func(context.Context, retrieval.Actor, Request, string, int, int) error { return nil },
	}

	var events []Event
	err := svc.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 2}, Request{
		KnowledgeBaseID: 7, ModelID: 3, Question: "住宿报销标准是什么？", Mode: "agentic",
	}, func(e Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("agentic stream failed: %v", err)
	}

	// Verify KB scope enforcement on search tool call.
	if len(tools.calls) != 2 || !strings.Contains(tools.calls[0], `"kb_ids":[7]`) {
		t.Fatalf("expected kb_ids enforced to [7], got %v", tools.calls)
	}

	var steps int
	var gotRetrieval *RetrievalEvent
	var gotCitations *Citations
	var gotDone *Done
	var text strings.Builder
	for _, ev := range events {
		switch {
		case ev.Step != nil:
			steps++
		case ev.Retrieval != nil:
			gotRetrieval = ev.Retrieval
		case ev.Delta != nil:
			text.WriteString(ev.Delta.Text)
		case ev.Citations != nil:
			gotCitations = ev.Citations
		case ev.Done != nil:
			gotDone = ev.Done
		}
	}
	if steps != 2 {
		t.Fatalf("expected 2 step events, got %d", steps)
	}
	if gotRetrieval == nil || len(gotRetrieval.Evidence) != 2 {
		t.Fatalf("expected 2 collected evidences, got %+v", gotRetrieval)
	}
	if gotCitations == nil || len(gotCitations.Items) != 2 || gotCitations.InvalidCount != 0 {
		t.Fatalf("unexpected citations: %+v", gotCitations)
	}
	if gotDone == nil || gotDone.Outcome != "answered" {
		t.Fatalf("expected outcome answered, got %+v", gotDone)
	}
	if !strings.Contains(text.String(), "500 元") {
		t.Fatalf("unexpected answer text: %q", text.String())
	}
}

func TestAgenticStreamTruncatesAtMaxSteps(t *testing.T) {
	tools := &stubAgentTools{}
	gen := &scriptedGenerator{
		responses: []string{
			`{"action":"tool","tool":"search","args":{"query":"住宿"}}`,
			`{"action":"tool","tool":"get_neighbors","args":{"chunk_id":101}}`,
			`基于已获证据，住宿上限为 500 元[1]。`,
		},
	}
	svc := &Service{
		Search:      noopSearch{},
		Guard:       allowGuard{},
		Tools:       tools,
		Agent:       AgentLimits{MaxSteps: 2, MaxDuration: 10 * time.Second, MaxEvidenceTokens: 20000},
		CountTokens: func(_ context.Context, s string) (int, error) { return len([]rune(s)), nil },
		Open: func(context.Context, retrieval.Actor, int64, func(context.Context) (models.EgressScope, error)) (Generator, error) {
			return gen, nil
		},
		Audit: func(context.Context, retrieval.Actor, Request, string, int, int) error { return nil },
	}

	var done *Done
	err := svc.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 2}, Request{
		KnowledgeBaseID: 7, ModelID: 3, Question: "住宿？", Mode: "agentic",
	}, func(e Event) error {
		if e.Done != nil {
			done = e.Done
		}
		return nil
	})
	if err != nil {
		t.Fatalf("agentic stream failed: %v", err)
	}
	if done == nil || done.Outcome != "truncated" {
		t.Fatalf("expected outcome=truncated when max_steps hit, got %+v", done)
	}
}
