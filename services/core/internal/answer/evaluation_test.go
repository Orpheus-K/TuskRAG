package answer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type evaluationAuditor struct{}

func (evaluationAuditor) RecordModelDenied(context.Context, models.DeniedCall) error { return nil }

// Real-provider evaluation is opt-in. The external credential file is never
// copied to a report; test logs contain only case IDs and aggregate outcomes.
func TestRealAnswerPromptEvaluation(t *testing.T) {
	path := os.Getenv("TUSKRAG_TEST_CHAT_CONFIG")
	if path == "" {
		t.Skip("real chat evaluation requires an external credential file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read external chat configuration")
	}
	var config struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if json.Unmarshal(data, &config) != nil || config.BaseURL == "" || config.Model == "" {
		t.Fatal("invalid external chat configuration")
	}
	clear(data)
	casesData, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tests", "eval", "answer-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dataset struct {
		Version string `json:"version"`
		Cases   []struct {
			ID, Question, Evidence string
			Answerable             bool
			Contains, Forbidden    []string
		} `json:"cases"`
	}
	if err = json.Unmarshal(casesData, &dataset); err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID         string `json:"id"`
		Answerable bool   `json:"answerable"`
		Outcome    string `json:"outcome"`
		Citations  int    `json:"citations"`
		Invalid    int    `json:"invalid_citations"`
		Passed     bool   `json:"passed"`
		DurationMS int64  `json:"duration_ms"`
	}
	report := struct {
		Dataset, Model, Prompt    string
		Rows                      []row
		Answered, Refused, Passed int
	}{Dataset: dataset.Version, Model: config.Model, Prompt: PromptVersion, Rows: []row{}}
	for _, tc := range dataset.Cases {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		scope := models.EgressScope{TenantID: 1, KnowledgeBaseID: 1, TenantPolicy: models.Allow, KnowledgeBasePolicy: models.Allow}
		service := fixture(nil)
		service.Search = searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
			return retrieval.Result{Candidates: []retrieval.Candidate{{ChunkID: 1, DocumentID: 1, VersionID: 1, SectionID: 1, Title: "评测资料", Content: tc.Evidence}}}, nil
		})
		service.Guard = guardFunc(func(context.Context, retrieval.Actor, int64, []retrieval.Candidate) (models.EgressScope, error) {
			return scope, nil
		})
		service.Open = func(_ context.Context, _ retrieval.Actor, _ int64, resolve func(context.Context) (models.EgressScope, error)) (Generator, error) {
			return models.NewChatClient(models.Provider{ID: 1, BaseURL: config.BaseURL, Locality: models.External, APIKey: config.APIKey}, config.Model, 90*time.Second, resolve, evaluationAuditor{})
		}
		var text strings.Builder
		var final *Done
		var cites *Citations
		start := time.Now()
		err := service.Stream(ctx, retrieval.Actor{TenantID: 1, UserID: 1}, Request{KnowledgeBaseID: 1, ModelID: 1, Question: tc.Question}, func(e Event) error {
			if e.Delta != nil {
				text.WriteString(e.Delta.Text)
			}
			if e.Done != nil {
				final = e.Done
			}
			if e.Citations != nil {
				cites = e.Citations
			}
			return nil
		})
		cancel()
		item := row{ID: tc.ID, Answerable: tc.Answerable, DurationMS: time.Since(start).Milliseconds()}
		if err == nil && final != nil && cites != nil {
			item.Outcome = final.Outcome
			item.Citations = len(cites.Items)
			item.Invalid = cites.InvalidCount
			item.Passed = (tc.Answerable && final.Outcome == "answered" && item.Citations > 0) || (!tc.Answerable && final.Outcome == "refused" && item.Citations == 0)
			item.Passed = item.Passed && item.Invalid == 0
			for _, word := range tc.Contains {
				item.Passed = item.Passed && strings.Contains(text.String(), word)
			}
			for _, word := range tc.Forbidden {
				item.Passed = item.Passed && !strings.Contains(text.String(), word)
			}
			if final.Outcome == "answered" {
				report.Answered++
			}
			if final.Outcome == "refused" {
				report.Refused++
			}
		}
		if item.Passed {
			report.Passed++
		} else {
			t.Errorf("case %s failed: outcome=%s citations=%d invalid=%d error=%v", tc.ID, item.Outcome, item.Citations, item.Invalid, err)
		}
		report.Rows = append(report.Rows, item)
	}
	if path := os.Getenv("TUSKRAG_TEST_CHAT_REPORT"); path != "" {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("prompt=%s cases=%d passed=%d answered=%d refused=%d", PromptVersion, len(report.Rows), report.Passed, report.Answered, report.Refused)
}
