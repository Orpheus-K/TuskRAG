package ingest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/tokenizer"
)

// Explicit opt-in only: ordinary integration tests and CI never call external models.
// This fixture publishes synthetic documents directly; it does not claim T01 ingestion.
func TestRealModelEvaluation(t *testing.T) {
	if os.Getenv("TUSKRAG_REAL_EVAL") != "siliconflow-synthetic-only" {
		t.Skip("real evaluation requires explicit synthetic-only opt-in")
	}
	key, err := os.ReadFile(os.Getenv("TUSKRAG_REAL_EVAL_KEY_FILE"))
	if err != nil {
		t.Fatal("read real evaluation credential")
	}
	defer clear(key)
	key = []byte(strings.TrimSpace(string(key)))
	if len(key) == 0 {
		t.Fatal("empty real evaluation credential")
	}
	directory := os.Getenv("TUSKRAG_EVAL_REPORT_DIR")
	if directory == "" {
		t.Fatal("report directory is required")
	}
	publicationDatabase(t)
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
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
	if err := eval.ValidateDataset(dataset); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var provider, space int64
	if err := f.admin.QueryRow(ctx, `SELECT e.provider_id,e.id FROM embedding_space e JOIN knowledge_base k ON k.tenant_id=e.tenant_id AND k.embedding_space_id=e.id WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&provider, &space); err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewVault(master)
	clear(master)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Seal(ctx, f.tenant, provider, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE model_provider SET base_url='https://api.siliconflow.cn', locality='external',api_key_id=$1,api_key_nonce=$2,api_key_ciphertext=$3 WHERE tenant_id=$4 AND id=$5`, encrypted.KeyID, encrypted.Nonce, encrypted.Ciphertext, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE embedding_space SET model='BAAI/bge-m3',dims=1024 WHERE tenant_id=$1 AND id=$2`, f.tenant, space); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE tenant SET egress_policy_max='allow' WHERE id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
	auditor, err := models.NewEmbeddingDenialAuditor(f.pool, f.tenant, f.user)
	if err != nil {
		t.Fatal(err)
	}
	client, err := models.NewEmbeddingClient(models.Provider{ID: provider, BaseURL: "https://api.siliconflow.cn", Locality: models.External, APIKey: string(key)}, models.EmbeddingConfig{Model: "BAAI/bge-m3", Dimensions: 1024, Normalize: true, MaxBatch: 1, MaxRequestBytes: 65536, MaxResponseBytes: 131072, Timeout: 30 * time.Second, Concurrency: 1}, auditor)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	scope := models.EgressScope{TenantID: f.tenant, KnowledgeBaseID: f.kbID, TenantPolicy: models.Allow, KnowledgeBasePolicy: models.Allow}
	check, err := client.CheckTokenizer(ctx, scope, counter.Count)
	writeRealReport(t, directory, "tokenizer", struct {
		Samples int    `json:"samples"`
		Error   string `json:"error,omitempty"`
	}{check.Samples, errorText(err)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tokenizer probes passed: %d", check.Samples)
	config := eval.RunConfig{Actor: retrieval.Actor{TenantID: f.tenant, UserID: f.user}, KnowledgeBaseID: f.kbID, Documents: map[string]int64{}, ModelLabel: "SiliconFlow BAAI/bge-m3 + BAAI/bge-reranker-v2-m3 (provider revision undisclosed)", ModelKind: "real", Reranker: &retrieval.RerankOptions{ProviderID: provider, Model: "BAAI/bge-reranker-v2-m3", TopN: 30}}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	for _, doc := range append(dataset.Documents, eval.Document{Key: "restricted", Title: "不可见薪酬", Content: "董事长私人电话号码和员工股票期权的行权价格。"}) {
		result, err := client.Embed(ctx, scope, []string{doc.Content})
		if err != nil {
			t.Fatal(err)
		}
		n, err := counter.Count(ctx, doc.Content)
		if err != nil {
			t.Fatal(err)
		}
		if result.Usage == nil || result.Usage.PromptTokens != n {
			t.Fatalf("document %s token usage mismatch", doc.Key)
		}
		id := insertEvaluationDocument(t, f, space, doc, doc.Key == "restricted", result.Vectors[0])
		if doc.Key != "restricted" {
			config.Documents[doc.Key] = id
			config.AllowedDocumentIDs = append(config.AllowedDocumentIDs, id)
		}
	}
	setupForeignSearchFixture(t, f)
	if err := indexing.Provision(ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	service, err := retrieval.NewService(f.pool, vault, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []struct {
		mode string
		topN int
	}{{"lexical", 30}, {"vector", 30}, {"rrf", 30}, {"rerank", 20}, {"rerank", 30}, {"rerank", 50}} {
		name := fmt.Sprintf("%s-%d", run.mode, run.topN)
		t.Run(name, func(t *testing.T) {
			config.Reranker.TopN = run.topN
			report, err := eval.Run(ctx, service, dataset, config, run.mode)
			writeRealReport(t, directory, name, report)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %+v unauthorized=%d", name, report.Summary, report.UnauthorizedEvidence)
		})
	}
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func writeRealReport(t *testing.T, directory, name string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(directory, name+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}
