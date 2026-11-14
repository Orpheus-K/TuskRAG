package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/tokenizer"
)

func TestRealAgenticVsStandardAblationPostgres(t *testing.T) {
	cfgPath := os.Getenv("TUSKRAG_TEST_CHAT_CONFIG")
	if cfgPath == "" {
		t.Skip("real LLM E7 ablation requires TUSKRAG_TEST_CHAT_CONFIG")
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var chatCfg struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(cfgBytes, &chatCfg); err != nil || chatCfg.BaseURL == "" || chatCfg.Model == "" {
		t.Fatal("invalid TUSKRAG_TEST_CHAT_CONFIG")
	}
	clear(cfgBytes)

	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			var req struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			type embItem struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}
			totalTokens := 0
			data := make([]embItem, len(req.Input))
			for i, text := range req.Input {
				n, countErr := counter.Count(r.Context(), text)
				if countErr != nil {
					w.WriteHeader(500)
					return
				}
				totalTokens += n
				data[i] = embItem{Index: i, Embedding: syntheticVector(text)}
			}
			resp := struct {
				Data  []embItem      `json:"data"`
				Usage map[string]int `json:"usage"`
			}{
				Data:  data,
				Usage: map[string]int{"prompt_tokens": totalTokens, "total_tokens": totalTokens},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/v1/rerank":
			var req struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
				TopN      int      `json:"top_n"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			type rerankItem struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			}
			results := make([]rerankItem, len(req.Documents))
			for i := range req.Documents {
				results[i] = rerankItem{Index: i, RelevanceScore: 1.0 - float64(i)*0.01}
			}
			if req.TopN > 0 && req.TopN < len(results) {
				results = results[:req.TopN]
			}
			_ = json.NewEncoder(w).Encode(struct {
				Results []rerankItem `json:"results"`
			}{Results: results})
		default:
			w.WriteHeader(404)
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
	if _, err := f.admin.Exec(ctx, `UPDATE tenant SET egress_policy_max='allow' WHERE id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}

	vault, err := secrets.NewVault(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	chatBaseURL := strings.TrimSuffix(strings.TrimSpace(chatCfg.BaseURL), "/v1")
	var chatProviderID int64
	if err := f.admin.QueryRow(ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES($1,'real-e7-chat',$2,'external') RETURNING id`, f.tenant, chatBaseURL).Scan(&chatProviderID); err != nil {
		t.Fatal(err)
	}
	if chatCfg.APIKey != "" {
		enc, sealErr := vault.Seal(ctx, f.tenant, chatProviderID, []byte(chatCfg.APIKey))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if _, err := f.admin.Exec(ctx, `UPDATE model_provider SET api_key_ciphertext=$1,api_key_nonce=$2,api_key_id=$3 WHERE tenant_id=$4 AND id=$5`, enc.Ciphertext, enc.Nonce, enc.KeyID, f.tenant, chatProviderID); err != nil {
			t.Fatal(err)
		}
	}
	var chatModelID int64
	if err := f.admin.QueryRow(ctx, `INSERT INTO chat_model(tenant_id,provider_id,model) VALUES($1,$2,$3) RETURNING id`, f.tenant, chatProviderID, chatCfg.Model).Scan(&chatModelID); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, table := range []string{"answer_feedback", "answer_message", "conversation"} {
			_, _ = f.admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id=$1", f.tenant)
		}
		_, _ = f.admin.Exec(context.Background(), `DELETE FROM chat_model WHERE tenant_id=$1`, f.tenant)
		_, _ = f.admin.Exec(context.Background(), `DELETE FROM model_provider WHERE tenant_id=$1 AND id<>$2`, f.tenant, provider)
		_, _ = f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant)
	})

	corpus := []eval.Document{
		{
			Key:   "doc-codenames",
			Title: "系统代号与服务映射表",
			Content: "# 系统代号与服务映射表\n\n" +
				"核心入口代号“玄武网关”对应的内部服务编号为 `svc-xuanwu-07`。\n\n" +
				"分布式热点缓存代号“青龙缓存”对应的内部服务编号为 `svc-qinglong-03`。\n",
		},
		{
			Key:   "doc-sla",
			Title: "服务 SLA 与值班归属手册",
			Content: "# 服务 SLA 与值班归属手册\n\n" +
				"服务编号 `svc-xuanwu-07` 的 P0 级故障恢复时间目标（RTO）为 `12分钟`，第一值班归属团队为 `基础架构SRE一组`。\n\n" +
				"服务编号 `svc-qinglong-03` 的 P1 级故障恢复时间目标（RTO）为 `25分钟`，第一值班归属团队为 `数据平台二组`。\n",
		},
		{
			Key:   "doc-escalation",
			Title: "应急升级窗口与降级开关规范",
			Content: "# 应急升级窗口与降级开关规范\n\n" +
				"`基础架构SRE一组` 接报后须在 `8分钟` 应急升级窗口内响应，必要时启用降级开关 `EDGE_FALLBACK_STRICT`。\n\n" +
				"`数据平台二组` 接报后须在 `15分钟` 应急升级窗口内响应，必要时启用降级开关 `CACHE_BYPASS_READONLY`。\n",
		},
		{
			Key:   "doc-freeze",
			Title: "2026年Q4核心变更冻结规定",
			Content: "# 2026年Q4核心变更冻结规定\n\n" +
				"2026年Q4期间，所有入口网关类服务（含玄武网关）在 `11月10日至11月20日` 执行变更冻结；若需紧急发布，必须由 `首席架构师林舟` 书面审批。\n",
		},
	}

	for _, doc := range corpus {
		ingestChunkedMarkdownDoc(t, f, space, provider, counter.Count, doc, false)
	}
	if err := indexing.Provision(ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
		t.Fatal(err)
	}

	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := knowledge.PutConfig(ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, 0, kb.RetrievalConfig{
		TopK:     15,
		Reranker: &retrieval.RerankOptions{ProviderID: provider, Model: "stub-rerank", TopN: 30, Fallback: false},
	}); err != nil {
		t.Fatal(err)
	}

	search, err := retrieval.NewService(f.pool, nil, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	answerSvc, err := answer.NewService(f.pool, vault, search, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	answerSvc.Assembly = answer.AssemblyOptions{ParentBacktrack: true, ContextBudgetTokens: 6000}
	answerSvc.Agent = answer.AgentLimits{
		MaxSteps:          6,
		MaxDuration:       45 * time.Second,
		MaxEvidenceTokens: 20000,
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}

	type multiHopCase struct {
		ID            string
		Question      string
		ExpectedFacts []string
	}
	cases := []multiHopCase{
		{
			ID:            "mh-1",
			Question:      "玄武网关发生 P0 故障时，对应的 RTO、值班归属团队以及应急升级窗口和降级开关分别是什么？",
			ExpectedFacts: []string{"12分钟", "基础架构SRE一组", "8分钟", "EDGE_FALLBACK_STRICT"},
		},
		{
			ID:            "mh-2",
			Question:      "青龙缓存触发 P1 告警时，由哪个团队负责、RTO 是多少、对应降级开关是什么？",
			ExpectedFacts: []string{"数据平台二组", "25分钟", "CACHE_BYPASS_READONLY"},
		},
		{
			ID:            "mh-3",
			Question:      "玄武网关在 2026年Q4 变更冻结期间如需紧急变更，需要谁审批，且对应的服务编号是什么？",
			ExpectedFacts: []string{"林舟", "svc-xuanwu-07"},
		},
		{
			ID:            "mh-4",
			Question:      "基础架构SRE一组与数据平台二组各自负责哪个服务编号，两者的应急升级窗口分别是多少分钟？",
			ExpectedFacts: []string{"svc-xuanwu-07", "svc-qinglong-03", "8分钟", "15分钟"},
		},
	}

	type modeStats struct {
		mode          string
		totalFacts    int
		hitFacts      int
		validCitCases int
		totalSteps    int
		truncated     int
		latenciesMs   []int64
	}

	runMode := func(mode string) modeStats {
		stats := modeStats{mode: mode}
		for _, tc := range cases {
			started := time.Now()
			var text strings.Builder
			var evidenceIDs map[string]struct{}
			var citedIDs []string
			var stepCount int
			var truncated bool
			err := answerSvc.Stream(ctx, actor, answer.Request{
				KnowledgeBaseID: f.kbID,
				ModelID:         chatModelID,
				Mode:            mode,
				Question:        tc.Question,
			}, func(ev answer.Event) error {
				if ev.Retrieval != nil {
					evidenceIDs = make(map[string]struct{}, len(ev.Retrieval.Evidence))
					for _, item := range ev.Retrieval.Evidence {
						evidenceIDs[item.ID] = struct{}{}
					}
				}
				if ev.Step != nil {
					stepCount++
				}
				if ev.Delta != nil {
					text.WriteString(ev.Delta.Text)
				}
				if ev.Citations != nil {
					for _, c := range ev.Citations.Items {
						citedIDs = append(citedIDs, c.EvidenceID)
					}
				}
				if ev.Done != nil {
					truncated = ev.Done.Outcome == "truncated"
				}
				return nil
			})
			elapsedMs := time.Since(started).Milliseconds()
			stats.latenciesMs = append(stats.latenciesMs, elapsedMs)
			if err != nil {
				t.Fatalf("mode=%s case=%s Stream error: %v", mode, tc.ID, err)
			}

			hits := countFactHits(text.String(), tc.ExpectedFacts)
			stats.totalFacts += len(tc.ExpectedFacts)
			stats.hitFacts += hits
			stats.totalSteps += stepCount
			if truncated {
				stats.truncated++
			}

			citOK := len(citedIDs) > 0
			for _, id := range citedIDs {
				if _, ok := evidenceIDs[id]; !ok {
					citOK = false
					break
				}
			}
			if citOK {
				stats.validCitCases++
			}

			t.Logf("E7_CASE mode=%s id=%s facts=%d/%d cit_ok=%v citations=%d evidence=%d steps=%d truncated=%v latency_ms=%d",
				mode, tc.ID, hits, len(tc.ExpectedFacts), citOK, len(citedIDs), len(evidenceIDs), stepCount, truncated, elapsedMs)
		}
		return stats
	}

	std := runMode("standard")
	ag := runMode("agentic")

	logSummary := func(s modeStats) {
		sorted := slices.Clone(s.latenciesMs)
		slices.Sort(sorted)
		var sum int64
		for _, v := range sorted {
			sum += v
		}
		avg := float64(sum) / float64(len(sorted))
		p50 := sorted[len(sorted)/2]
		p95 := sorted[len(sorted)-1]
		t.Logf("E7_SUMMARY model=%s mode=%s fact_recall=%.4f (%d/%d) valid_cit_rate=%.4f (%d/%d) avg_steps=%.2f truncated=%d avg_ms=%.1f p50_ms=%d p95_ms=%d",
			chatCfg.Model, s.mode,
			float64(s.hitFacts)/float64(s.totalFacts), s.hitFacts, s.totalFacts,
			float64(s.validCitCases)/float64(len(cases)), s.validCitCases, len(cases),
			float64(s.totalSteps)/float64(len(cases)), s.truncated,
			avg, p50, p95)
	}
	logSummary(std)
	logSummary(ag)

	if ag.hitFacts == 0 {
		t.Fatalf("agentic mode returned 0 fact hits")
	}
}
