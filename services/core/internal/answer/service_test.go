package answer

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type searchFunc func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)

func (f searchFunc) Search(c context.Context, a retrieval.Actor, r retrieval.Request) (retrieval.Result, error) {
	return f(c, a, r)
}

type guardFunc func(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error)

func (f guardFunc) AuthorizeEvidence(c context.Context, a retrieval.Actor, k int64, e []retrieval.Candidate) (models.EgressScope, error) {
	return f(c, a, k, e)
}

type generatorFunc func(context.Context, []models.ChatMessage, func(string) error) (models.ChatResult, error)

func (f generatorFunc) Stream(c context.Context, m []models.ChatMessage, e func(string) error) (models.ChatResult, error) {
	return f(c, m, e)
}
func (f generatorFunc) Close()                      {}
func (f generatorFunc) Check(context.Context) error { return nil }
func fixture(generator generatorFunc) *Service {
	return &Service{
		Search: searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
			return retrieval.Result{Candidates: []retrieval.Candidate{{ChunkID: 1, DocumentID: 2, VersionID: 3, SectionID: 4, Content: "申请期限为五天。", Title: "制度"}}}, nil
		}),
		Guard: guardFunc(func(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error) {
			return models.EgressScope{TenantID: 1, KnowledgeBaseID: 1, TenantPolicy: models.Deny, KnowledgeBasePolicy: models.Deny}, nil
		}),
		Open: func(context.Context, retrieval.Actor, int64, func(context.Context) (models.EgressScope, error)) (Generator, error) {
			return generator, nil
		},
		CountTokens: func(_ context.Context, text string) (int, error) {
			return utf8.RuneCountInString(text), nil
		},
		Assembly:      DefaultAssemblyOptions(),
		Audit:         func(context.Context, retrieval.Actor, Request, string, int, int) error { return nil },
		CheckInterval: time.Millisecond,
	}
}
func TestAnswerGroundingOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, text, finish, outcome string
		invalid, citations          int
	}{
		{"cited", "五天 [1]。", "stop", "answered", 0, 1}, {"bad citation", "五天 [99]。", "stop", "unsupported", 1, 0},
		{"refusal", Refusal, "stop", "refused", 0, 0}, {"truncated", "五天 [1]。", "length", "truncated", 0, 1},
		{"refusal with citation", Refusal + " 不应引用。[1]", "stop", "refused", 1, 0},
		{"refusal inline", Refusal + " 不应引用。[1]", "stop", "refused", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := fixture(generatorFunc(func(_ context.Context, m []models.ChatMessage, emit func(string) error) (models.ChatResult, error) {
				if len(m) != 2 || !strings.Contains(m[1].Content, "申请期限") || !strings.Contains(m[0].Content, "不可信") {
					t.Fatal("prompt missing grounding")
				}
				if tc.name == "refusal inline" {
					if err := emit(tc.text); err != nil {
						return models.ChatResult{}, err
					}
				}
				for _, r := range tc.text {
					if tc.name == "refusal inline" {
						break
					}
					if err := emit(string(r)); err != nil {
						return models.ChatResult{}, err
					}
				}
				return models.ChatResult{Finish: tc.finish}, nil
			}))
			var output strings.Builder
			var done *Done
			var citations *Citations
			err := service.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, Request{KnowledgeBaseID: 1, ModelID: 1, Question: "期限？"}, func(e Event) error {
				if e.Delta != nil {
					output.WriteString(e.Delta.Text)
				}
				if e.Done != nil {
					done = e.Done
				}
				if e.Citations != nil {
					citations = e.Citations
				}
				return nil
			})
			if err != nil || done == nil || done.Outcome != tc.outcome || citations.InvalidCount != tc.invalid || len(citations.Items) != tc.citations || strings.Contains(output.String(), "[99]") {
				t.Fatalf("err=%v done=%+v citations=%+v text=%s", err, done, citations, output.String())
			}
			if tc.outcome == "refused" && strings.Contains(output.String(), "[1]") {
				t.Fatal("refusal emitted a citation")
			}
		})
	}
}
func TestAnswerEmptyEvidenceDoesNotCallModel(t *testing.T) {
	service := fixture(nil)
	service.Search = searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{}, nil
	})
	service.Open = func(context.Context, retrieval.Actor, int64, func(context.Context) (models.EgressScope, error)) (Generator, error) {
		t.Fatal("model called without evidence")
		return nil, nil
	}
	var done *Done
	err := service.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, Request{KnowledgeBaseID: 1, ModelID: 1, Question: "不存在"}, func(e Event) error {
		if e.Done != nil {
			done = e.Done
		}
		return nil
	})
	if err != nil || done == nil || done.Outcome != "refused" {
		t.Fatalf("%v %+v", err, done)
	}
}
func TestAnswerRevocationCancelsSilentModel(t *testing.T) {
	var revoked atomic.Bool
	started := make(chan struct{})
	stopped := make(chan struct{})
	service := fixture(generatorFunc(func(ctx context.Context, _ []models.ChatMessage, _ func(string) error) (models.ChatResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return models.ChatResult{}, ctx.Err()
	}))
	service.Guard = guardFunc(func(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error) {
		if revoked.Load() {
			return models.EgressScope{}, retrieval.ErrNotFound
		}
		return models.EgressScope{}, nil
	})
	finished := make(chan error, 1)
	go func() {
		finished <- service.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, Request{KnowledgeBaseID: 1, ModelID: 1, Question: "期限"}, func(e Event) error {
			if e.Done != nil || e.Delta != nil {
				t.Error("emitted after revocation")
			}
			return nil
		})
	}()
	<-started
	revoked.Store(true)
	select {
	case err := <-finished:
		if !errors.Is(err, retrieval.ErrNotFound) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation not checked")
	}
	<-stopped
}
func TestCitationFilterBoundaries(t *testing.T) {
	f := newCitationFilter(2)
	var out strings.Builder
	for _, s := range []string{"内容 [", "1", "]，[9", "] [2", ""} {
		out.WriteString(f.Push(s, false))
	}
	out.WriteString(f.Push("", true))
	if out.String() != "内容 [1]， " || f.invalid != 2 || len(f.used) != 1 {
		t.Fatalf("%q %+v", out.String(), f)
	}
}
func TestContextBudgetKeepsWholeEvidence(t *testing.T) {
	service := fixture(nil)
	service.Assembly = AssemblyOptions{ParentBacktrack: false, ContextBudgetTokens: 100}
	candidates := []retrieval.Candidate{
		{ChunkID: 1, DocumentID: 2, VersionID: 3, SectionID: 10, Ordinal: 1, Content: strings.Repeat("甲", 40)},
		{ChunkID: 2, DocumentID: 2, VersionID: 3, SectionID: 11, Ordinal: 1, Content: strings.Repeat("乙", 90)},
		{ChunkID: 3, DocumentID: 2, VersionID: 3, SectionID: 12, Ordinal: 1, Content: "small"},
	}
	selected, evidence, limited, err := service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if !limited || len(selected) != 1 || selected[0].ChunkID != 1 || len(evidence) != 1 || evidence[0].ID != "c_1" {
		t.Fatalf("%+v %+v %t", selected, evidence, limited)
	}
}
