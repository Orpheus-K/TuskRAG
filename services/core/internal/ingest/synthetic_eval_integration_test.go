package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

// This is a retrieval smoke baseline. Its character-hash model is deliberately
// independent of expected answers and is not evidence of real model quality.
func TestSyntheticZHEvaluation(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM eval_run WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f.ctx = ctx
	data, err := os.ReadFile("../../../../tests/eval/synthetic-zh.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset eval.Dataset
	if err := json.Unmarshal(data, &dataset); err != nil {
		t.Fatal(err)
	}
	if len(dataset.Questions) < 60 {
		t.Fatal("synthetic-zh requires at least 60 cases")
	}
	if err := eval.ValidateDataset(dataset); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input     []string `json:"input"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		var body any
		switch r.URL.Path {
		case "/v1/embeddings":
			if len(request.Input) != 1 {
				w.WriteHeader(400)
				return
			}
			n, err := counter.Count(r.Context(), request.Input[0])
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			body = map[string]any{"data": []any{map[string]any{"index": 0, "embedding": syntheticVector(request.Input[0])}}, "usage": map[string]int{"prompt_tokens": n, "total_tokens": n}}
		case "/v1/rerank":
			results := []map[string]any{}
			for i, doc := range request.Documents {
				results = append(results, map[string]any{"index": i, "relevance_score": syntheticOverlap(request.Query, doc)})
			}
			body = map[string]any{"results": results}
		default:
			w.WriteHeader(404)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	var provider, space int64
	if err := f.admin.QueryRow(ctx, `SELECT e.provider_id,e.id FROM embedding_space e JOIN knowledge_base k ON k.tenant_id=e.tenant_id AND k.embedding_space_id=e.id WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&provider, &space); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	config := eval.RunConfig{Actor: retrieval.Actor{TenantID: f.tenant, UserID: f.user}, KnowledgeBaseID: f.kbID, Documents: map[string]int64{}, ModelLabel: "character-hash-64+bigram-overlap-v1", ModelKind: "stub", Reranker: &retrieval.RerankOptions{ProviderID: provider, Model: "synthetic-reranker", TopN: 30}}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	for _, doc := range dataset.Documents {
		id := insertSyntheticDocument(t, f, space, doc, false)
		config.Documents[doc.Key] = id
		config.AllowedDocumentIDs = append(config.AllowedDocumentIDs, id)
	}
	insertSyntheticDocument(t, f, space, eval.Document{Key: "restricted", Title: "不可见薪酬", Content: "董事长私人电话号码和员工股票期权的行权价格。"}, true)
	setupForeignSearchFixture(t, f)
	if err := indexing.Provision(ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	service, err := retrieval.NewService(f.pool, nil, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	var revision int32
	sets, err := eval.NewSetStore(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	definition := eval.SetDefinition{Name: "synthetic-zh pinned"}
	for _, q := range dataset.Questions {
		item := eval.CaseDefinition{ID: q.ID, Question: q.Question, Answerable: q.Answerable, Tags: q.Tags}
		for _, key := range q.Expected {
			item.Expected = append(item.Expected, eval.ExpectedLocation{DocumentID: config.Documents[key]})
		}
		definition.Cases = append(definition.Cases, item)
	}
	pinned, err := sets.Create(ctx, config.Actor, f.kbID, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	setConfig := config
	setConfig.Documents = nil
	for _, mode := range []string{"lexical", "vector", "rrf", "rerank"} {
		t.Run(mode, func(t *testing.T) {
			report, err := eval.Run(ctx, service, dataset, config, mode)
			if err != nil {
				t.Fatal(err)
			}
			if report.UnauthorizedEvidence != 0 || report.Summary.Cases != 64 || report.Summary.AnswerableCases != 54 {
				t.Fatalf("invalid report: %+v", report.Summary)
			}
			t.Logf("%s: %+v unauthorized=%d", mode, report.Summary.Metrics, report.UnauthorizedEvidence)
			settings := kb.RetrievalConfig{TopK: 30}
			if mode == "rerank" {
				settings.Reranker = config.Reranker
			}
			saved, err := knowledge.PutConfig(ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, revision, settings)
			if err != nil {
				t.Fatal(err)
			}
			revision = saved.Revision
			inherited, err := eval.Run(ctx, inheritedConfigSearcher{service: service, revision: revision}, dataset, config, mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := eval.Compare(report, inherited); err != nil {
				t.Fatal(err)
			}
			if *report.Summary.Metrics != *inherited.Summary.Metrics {
				t.Fatalf("same settings changed metrics: direct=%+v inherited=%+v", report.Summary, inherited.Summary)
			}
			for i, row := range report.Rows {
				if !slices.Equal(row.Fused, inherited.Rows[i].Fused) || !slices.Equal(row.Ranked, inherited.Rows[i].Ranked) {
					t.Fatalf("case %s evidence changed", row.ID)
				}
			}
			t.Logf("%s saved revision=%d: identical metrics/evidence, unauthorized=%d", mode, revision, inherited.UnauthorizedEvidence)
			snapshot, err := service.Snapshot(ctx, config.Actor, f.kbID)
			if err != nil {
				t.Fatal(err)
			}
			setConfig.Configuration = &snapshot
			bound, err := eval.RunSet(ctx, service, sets, eval.SetBinding{SetID: pinned.ID, Revision: pinned.Revision}, setConfig, mode)
			if err != nil {
				t.Fatal(err)
			}
			if bound.UnauthorizedEvidence != 0 || *bound.Summary.Metrics != *report.Summary.Metrics || bound.Summary.Cases != report.Summary.Cases {
				t.Fatalf("pinned set changed metrics: %+v %+v", bound.Summary, report.Summary)
			}
			for i, row := range report.Rows {
				if !slices.Equal(row.Fused, bound.Rows[i].Fused) || !slices.Equal(row.Ranked, bound.Rows[i].Ranked) {
					t.Fatalf("pinned case %s changed evidence", row.ID)
				}
			}
			if err := eval.ValidateReport(bound); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s pinned set: identical metrics/evidence, unauthorized=%d", mode, bound.UnauthorizedEvidence)
			runs, err := eval.NewRunStore(f.pool, f.queue)
			if err != nil {
				t.Fatal(err)
			}
			run, err := runs.Create(ctx, config.Actor, f.kbID, "synthetic-job-"+mode, eval.RunRequest{Set: eval.SetBinding{SetID: pinned.ID, Revision: pinned.Revision}, Mode: mode, Config: setConfig})
			if err != nil {
				t.Fatal(err)
			}
			handler, err := eval.NewRunHandler(runs, service)
			if err != nil {
				t.Fatal(err)
			}
			job, err := f.queue.ClaimKindsWithRecovery(ctx, f.tenant, "synthetic-eval", time.Minute, []jobs.Kind{jobs.EvalRun}, nil)
			if err != nil {
				t.Fatal(err)
			}
			apply, err := handler.Prepare(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.queue.Complete(ctx, job.Lease, apply); err != nil {
				t.Fatal(err)
			}
			executed, err := runs.GetResult(ctx, config.Actor, f.kbID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if executed.Source != "executed" || *executed.Report.Summary.Metrics != *bound.Summary.Metrics || executed.Report.UnauthorizedEvidence != 0 {
				t.Fatalf("job changed metrics: %+v", executed)
			}
			for i, row := range bound.Rows {
				if !slices.Equal(row.Fused, executed.Report.Rows[i].Fused) || !slices.Equal(row.Ranked, executed.Report.Rows[i].Ranked) {
					t.Fatalf("job changed case %s", row.ID)
				}
			}
			t.Logf("%s queued run: identical metrics/evidence, unauthorized=0", mode)
			if directory := os.Getenv("TUSKRAG_EVAL_REPORT_DIR"); directory != "" {
				encodedJob, err := json.MarshalIndent(executed.Report, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "synthetic-zh-"+mode+"-job.json"), append(encodedJob, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.MarshalIndent(bound, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "synthetic-zh-"+mode+"-set.json"), append(encoded, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				encoded, err = json.MarshalIndent(report, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "synthetic-zh-"+mode+".json"), append(encoded, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				encoded, err = json.MarshalIndent(inherited, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "synthetic-zh-"+mode+"-saved.json"), append(encoded, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Deliberately exercise inheritance while keeping the same evaluation scorer.
type inheritedConfigSearcher struct {
	service  *retrieval.Service
	revision int32
}

func (s inheritedConfigSearcher) Search(ctx context.Context, actor retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
	request.Limit, request.Rerank, request.RerankEnabled = 0, nil, nil
	result, err := s.service.Search(ctx, actor, request)
	if err == nil && result.RetrievalRevision != s.revision {
		return retrieval.Result{}, fmt.Errorf("unexpected retrieval revision: %d", result.RetrievalRevision)
	}
	return result, err
}

func insertSyntheticDocument(t *testing.T, f *fixture, space int64, doc eval.Document, restricted bool) int64 {
	return insertEvaluationDocument(t, f, space, doc, restricted, syntheticVector(doc.Content))
}

func insertEvaluationDocument(t *testing.T, f *fixture, space int64, doc eval.Document, restricted bool, embedding []float32) int64 {
	t.Helper()
	var id, version, section, chunk int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES($1,$2,$3,'text/markdown') RETURNING id`, f.tenant, f.kbID, doc.Title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(doc.Content))
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by) VALUES($1,$2,$3,$4,$5,$6,'ready',$7) RETURNING id`, f.tenant, f.kbID, id, digest[:], len(doc.Content), "synthetic-"+doc.Key, f.user).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE tenant_id=$2 AND id=$3`, version, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO section(tenant_id,kb_id,document_id,version_id,ordinal,level,heading,heading_path,body_md,token_count) VALUES($1,$2,$3,$4,1,1,$5,ARRAY[$5]::text[],$6,1) RETURNING id`, f.tenant, f.kbID, id, version, doc.Title, doc.Content).Scan(&section); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("kb:%d:all", f.kbID)
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at) VALUES($1,$2,$3,$4,$5,1,'text',$6,$6,1,true,ARRAY[$7]::text[],'text/markdown',now()) RETURNING id`, f.tenant, f.kbID, id, version, section, doc.Content, tag).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	vector, err := json.Marshal(embedding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding) VALUES($1,$2,$3,$4,true,ARRAY[$5]::text[],$6::halfvec)`, f.tenant, f.kbID, chunk, space, tag, string(vector)); err != nil {
		t.Fatal(err)
	}
	if restricted {
		if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, f.tenant, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func syntheticVector(text string) []float32 {
	values := make([]float32, 64)
	for _, r := range strings.ToLower(text) {
		hash := sha256.Sum256([]byte(string(r)))
		values[int(hash[0])%len(values)]++
	}
	var norm float64
	for _, v := range values {
		norm += float64(v * v)
	}
	if norm == 0 {
		values[0] = 1
		return values
	}
	for i := range values {
		values[i] /= float32(math.Sqrt(norm))
	}
	return values
}
func syntheticOverlap(query, document string) float64 {
	runes := []rune(query)
	var score float64
	for i := 0; i+1 < len(runes); i++ {
		if strings.Contains(document, string(runes[i:i+2])) {
			score++
		}
	}
	return score
}
