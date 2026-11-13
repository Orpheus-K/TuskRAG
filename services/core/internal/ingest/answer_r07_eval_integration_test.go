package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

type evalJudgeFunc func(context.Context, string, string, []answer.AnswerEvidence) (eval.CitationJudgment, error)

func (f evalJudgeFunc) Judge(ctx context.Context, q, a string, ev []answer.AnswerEvidence) (eval.CitationJudgment, error) {
	return f(ctx, q, a, ev)
}

func ingestChunkedMarkdownDoc(t *testing.T, f *fixture, space, provider int64, count ingestTokenCounter, doc eval.Document, restricted bool) int64 {
	t.Helper()
	tempFile := filepath.Join(t.TempDir(), doc.Key+".md")
	if err := os.WriteFile(tempFile, []byte(doc.Content), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(tempFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseNativeText(f.ctx, parser.ParseInput{
		File:   file,
		MIME:   "text/markdown",
		Limits: []byte("{}"),
	}, validator)
	if err != nil {
		t.Fatalf("parse %s: %v", doc.Key, err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil {
		t.Fatalf("assemble %s: %v", doc.Key, err)
	}
	plan, err := BuildPlan(f.ctx, PlanBuildRequest{
		Document:         normalized,
		MIME:             "text/markdown",
		EmbeddingSpaceID: space,
		EmbeddingProfile: EmbeddingProfile{ProviderID: provider, Model: "BAAI/bge-m3", Dimensions: 64, Normalized: true, Distance: "cosine"},
		ConfigRevision:   1,
		ParentMaxTokens:  1500,
		ChildTargetMin:   120,
		ChildTargetMax:   220,
		ChildHardMax:     350,
		OverlapMax:       40,
		CountTokens:      count,
		TableHeaders:     sourceTableHeaders("text/markdown", parser.NativeMarkdownProfile, false, normalized),
	})
	if err != nil {
		t.Fatalf("build plan %s: %v", doc.Key, err)
	}

	var docID, versionID int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES($1,$2,$3,'text/markdown') RETURNING id`, f.tenant, f.kbID, doc.Title).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(doc.Content))
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by) VALUES($1,$2,$3,$4,$5,$6,'ready',$7) RETURNING id`, f.tenant, f.kbID, docID, digest[:], len(doc.Content), "e6-"+doc.Key, f.user).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE tenant_id=$2 AND id=$3`, versionID, f.tenant, docID); err != nil {
		t.Fatal(err)
	}

	sectionIDs := make([]int64, len(plan.Sections))
	for i, s := range plan.Sections {
		var parentID any
		if s.ParentIndex >= 0 {
			parentID = sectionIDs[s.ParentIndex]
		}
		if err := f.admin.QueryRow(f.ctx, `INSERT INTO section(tenant_id,kb_id,document_id,version_id,parent_section_id,ordinal,level,heading,heading_path,body_md,token_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			f.tenant, f.kbID, docID, versionID, parentID, i+1, s.Level, s.Heading, s.HeadingPath, s.BodyMarkdown, s.TokenCount).Scan(&sectionIDs[i]); err != nil {
			t.Fatalf("insert section %s[%d]: %v", doc.Key, i, err)
		}
	}

	tag := fmt.Sprintf("kb:%d:all", f.kbID)
	for i, c := range plan.Chunks {
		var cid int64
		if err := f.admin.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,ARRAY[$11]::text[],'text/markdown',now()) RETURNING id`,
			f.tenant, f.kbID, docID, versionID, sectionIDs[c.SectionIndex], i+1, c.Kind, c.Content, c.SearchText, c.TokenCount, tag).Scan(&cid); err != nil {
			t.Fatalf("insert chunk %s[%d]: %v", doc.Key, i, err)
		}
		vec, err := json.Marshal(syntheticVector(c.SearchText))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding) VALUES($1,$2,$3,$4,true,ARRAY[$5]::text[],$6::halfvec)`,
			f.tenant, f.kbID, cid, space, tag, string(vec)); err != nil {
			t.Fatalf("insert vector %s[%d]: %v", doc.Key, i, err)
		}
	}
	if restricted {
		if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, f.tenant, docID); err != nil {
			t.Fatal(err)
		}
	}
	return docID
}

type ingestTokenCounter = func(context.Context, string) (int, error)

func TestR07ContextAssemblyAndE6GenerationEvalPostgres(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f.ctx = ctx

	rawDataset, err := os.ReadFile("../../../../tests/eval/r07-e6-zh.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset eval.AnswerDataset
	if err := json.Unmarshal(rawDataset, &dataset); err != nil {
		t.Fatal(err)
	}
	if err := eval.ValidateAnswerDataset(dataset); err != nil {
		t.Fatal(err)
	}

	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Deterministic stub HTTP gateway for embeddings, rerank, and grounded chat completions.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			var req struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Input) != 1 {
				w.WriteHeader(400)
				return
			}
			n, err := counter.Count(r.Context(), req.Input[0])
			if err != nil {
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  []any{map[string]any{"index": 0, "embedding": syntheticVector(req.Input[0])}},
				"usage": map[string]int{"prompt_tokens": n, "total_tokens": n},
			})
		case "/v1/rerank":
			var req struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			results := make([]map[string]any, len(req.Documents))
			for i, d := range req.Documents {
				results[i] = map[string]any{"index": i, "relevance_score": syntheticOverlap(req.Query, d)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case "/v1/chat/completions":
			var req struct {
				Messages []models.ChatMessage `json:"messages"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 {
				w.WriteHeader(400)
				return
			}
			var payload struct {
				Evidence []answer.AnswerEvidence `json:"evidence"`
				Question string                  `json:"question"`
			}
			if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
				w.WriteHeader(400)
				return
			}
			reply := synthesizeGroundedReply(payload.Question, payload.Evidence)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", reply)
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
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
	var chatModelID int64
	if err := f.admin.QueryRow(ctx, `INSERT INTO chat_model(tenant_id,provider_id,model) VALUES($1,$2,'stub-e6-chat') RETURNING id`, f.tenant, provider).Scan(&chatModelID); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, table := range []string{"answer_feedback", "answer_message", "conversation"} {
			_, _ = f.admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id=$1", f.tenant)
		}
		_, _ = f.admin.Exec(context.Background(), `DELETE FROM chat_model WHERE tenant_id=$1`, f.tenant)
		_, _ = f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant)
	})

	docMap := make(map[string]int64, len(dataset.Documents))
	var allowedIDs []int64
	for _, doc := range dataset.Documents {
		docID := ingestChunkedMarkdownDoc(t, f, space, provider, counter.Count, doc, false)
		docMap[doc.Key] = docID
		allowedIDs = append(allowedIDs, docID)
	}
	ingestChunkedMarkdownDoc(t, f, space, provider, counter.Count, eval.Document{
		Key: "restricted_secret", Title: "受限文档", Content: "# 受限文档\n\n董事长薪资与火星基地秘密预算为 9999 万元。",
	}, true)
	setupForeignSearchFixture(t, f)

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
	answers, err := answer.NewService(f.pool, nil, search, counter.Count)
	if err != nil {
		t.Fatal(err)
	}

	// 1) Verify real PG parent backtracking vs off, plus multi-chunk conversation persistence & reload.
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	var onEvidence []answer.AnswerEvidence
	if err := answers.Stream(ctx, actor, answer.Request{KnowledgeBaseID: f.kbID, ModelID: chatModelID, Question: "北京住宿报销上限及实习生外包例外规定？"}, func(ev answer.Event) error {
		if ev.Retrieval != nil {
			onEvidence = ev.Retrieval.Evidence
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var promoted *answer.AnswerEvidence
	for i := range onEvidence {
		if onEvidence[i].DocumentID == docMap["travel_exceptions"] && strings.Contains(onEvidence[i].Content, "实习生") && len(onEvidence[i].ChunkIDs) >= 2 {
			promoted = &onEvidence[i]
			break
		}
	}
	if promoted == nil {
		t.Fatalf("expected parent backtracking to merge travel_exceptions section chunks: %+v", onEvidence)
	}

	// Verify Conversations.Stream -> Get preserves exact multi-chunk stitched evidence without storing text.
	convs, err := answer.NewConversations(f.pool, answers)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := convs.Create(ctx, actor, answer.NewConversation{KnowledgeBaseID: f.kbID, ModelID: chatModelID, Title: "R07 持久化验证"})
	if err != nil {
		t.Fatal(err)
	}
	var streamEv []answer.AnswerEvidence
	if err := convs.Stream(ctx, actor, conv.ID, "北京住宿报销上限及实习生外包例外规定？", func(ev answer.Event) error {
		if ev.Retrieval != nil {
			streamEv = ev.Retrieval.Evidence
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := convs.Get(ctx, actor, conv.ID)
	if err != nil || len(reloaded.Messages) != 1 || len(reloaded.Messages[0].Evidence) != len(streamEv) {
		t.Fatalf("conversation reload mismatch: err=%v reloaded=%+v", err, reloaded)
	}
	for i := range streamEv {
		if reloaded.Messages[0].Evidence[i].Content != streamEv[i].Content || !slices.Equal(reloaded.Messages[0].Evidence[i].ChunkIDs, streamEv[i].ChunkIDs) {
			t.Fatalf("reloaded evidence %d differs:\ngot:  %+v\nwant: %+v", i, reloaded.Messages[0].Evidence[i], streamEv[i])
		}
	}

	// 2) Run E6 ablation matrix across ParentBacktrack on/off and 4k/6k/10k budgets.
	runE6 := func(backtrack bool, budget int, withJudge bool) eval.AnswerReport {
		answers.Assembly = answer.AssemblyOptions{ParentBacktrack: backtrack, ContextBudgetTokens: budget}
		cfg := eval.AnswerRunConfig{
			Actor:               actor,
			KnowledgeBaseID:     f.kbID,
			ChatModelID:         chatModelID,
			Documents:           docMap,
			AllowedDocumentIDs:  allowedIDs,
			ModelLabel:          "stub-e6-chat",
			ModelKind:           "stub",
			ParentBacktrack:     &backtrack,
			ContextBudgetTokens: budget,
		}
		var judge eval.CitationJudge
		if withJudge {
			cfg.Judge = &eval.AnswerJudgeConfig{ChatModelID: chatModelID, ModelLabel: "stub-judge", PromptVersion: eval.JudgePromptVersion}
			judge = evalJudgeFunc(func(_ context.Context, _ string, _ string, _ []answer.AnswerEvidence) (eval.CitationJudgment, error) {
				return eval.CitationJudgment{TotalClaims: 1, UnsupportedClaims: 0}, nil
			})
		}
		report, err := eval.RunAnswer(ctx, answers, judge, dataset, cfg)
		if err != nil {
			t.Fatalf("E6 run(backtrack=%t, budget=%d) failed: %v", backtrack, budget, err)
		}
		if err := eval.ValidateAnswerReport(report); err != nil {
			t.Fatalf("E6 validate failed: %v", err)
		}
		if report.UnauthorizedEvidence != 0 {
			t.Fatalf("unauthorized evidence leaked: %d", report.UnauthorizedEvidence)
		}
		return report
	}

	off6k := runE6(false, 6000, true)
	on4k := runE6(true, 4000, true)
	on6k := runE6(true, 6000, true)
	on10k := runE6(true, 10000, true)

	t.Logf("E6 off-6k det=%.2f ref=%.2f falseRef=%.2f", *off6k.Summary.Metrics.DeterministicPassRate, *off6k.Summary.Metrics.RefusalCorrectRate, *off6k.Summary.Metrics.FalseRefusalRate)
	t.Logf("E6 on-4k  det=%.2f on-6k det=%.2f on-10k det=%.2f", *on4k.Summary.Metrics.DeterministicPassRate, *on6k.Summary.Metrics.DeterministicPassRate, *on10k.Summary.Metrics.DeterministicPassRate)

	if *on6k.Summary.Metrics.DeterministicPassRate < *off6k.Summary.Metrics.DeterministicPassRate {
		t.Fatalf("parent backtracking reduced deterministic pass rate: on=%.2f off=%.2f", *on6k.Summary.Metrics.DeterministicPassRate, *off6k.Summary.Metrics.DeterministicPassRate)
	}
	cmp, err := eval.CompareAnswerReports(off6k, on6k)
	if err != nil || cmp.Gate == "regressed" {
		t.Fatalf("unexpected E6 comparison gate: cmp=%+v err=%v", cmp, err)
	}
	if dir := os.Getenv("TUSKRAG_E6_REPORT_DIR"); dir != "" {
		for name, rep := range map[string]eval.AnswerReport{"e6-off-6k.json": off6k, "e6-on-4k.json": on4k, "e6-on-6k.json": on6k, "e6-on-10k.json": on10k} {
			raw, err := json.MarshalIndent(rep, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 3) Disabling one sibling chunk in travel_exceptions must suppress parent backtracking for that section.
	var disabledID int64
	if err := f.admin.QueryRow(ctx, `SELECT id FROM chunk WHERE tenant_id=$1 AND document_id=$2 AND content LIKE '%财务负责人与分管副总裁双签%'`, f.tenant, docMap["travel_exceptions"]).Scan(&disabledID); err != nil {
		t.Fatalf("locate sibling chunk to disable: %v", err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE chunk SET is_current=false, disabled=true WHERE tenant_id=$1 AND id=$2`, f.tenant, disabledID); err != nil {
		t.Fatal(err)
	}
	answers.Assembly = answer.DefaultAssemblyOptions()
	var afterDisable []answer.AnswerEvidence
	if err := answers.Stream(ctx, actor, answer.Request{KnowledgeBaseID: f.kbID, ModelID: chatModelID, Question: "北京地区每晚住宿报销上限是多少？"}, func(ev answer.Event) error {
		if ev.Retrieval != nil {
			afterDisable = ev.Retrieval.Evidence
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range afterDisable {
		if slices.Contains(ev.ChunkIDs, disabledID) || strings.Contains(ev.Content, "财务负责人与分管副总裁双签") {
			t.Fatalf("disabled sibling chunk leaked via parent backtracking: %+v", ev)
		}
	}
}

func synthesizeGroundedReply(question string, evidence []answer.AnswerEvidence) string {
	if len(evidence) == 0 {
		return answer.Refusal
	}
	switch {
	case strings.Contains(question, "火星") || strings.Contains(question, "P8"):
		return answer.Refusal
	case strings.Contains(question, "北京") && strings.Contains(question, "实习生"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "600") && strings.Contains(e.Content, "实习生") {
				return fmt.Sprintf("北京每晚 600 元，试用期八折，实习生须特批，外包按合作协议执行 [%d]。", e.N)
			}
			if strings.Contains(e.Content, "600") {
				return fmt.Sprintf("北京每晚 600 元 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "乌鲁木齐"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "470") && strings.Contains(e.Content, "520") {
				return fmt.Sprintf("乌鲁木齐住宿 470 元、伙食 115 元，拉萨住宿 520 元、伙食 130 元 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "报销单"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "15") && strings.Contains(e.Content, "5") {
				return fmt.Sprintf("差旅结束后 15 个自然日内提交，终审通过后 5 个工作日内打款 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "脱敏评测样本"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "纯合成") && strings.Contains(e.Content, "信息安全负责人") {
				return fmt.Sprintf("经信息安全负责人书面签批的纯合成评测语料可用于加密预发布对比 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "疑似被盗"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "10 分钟") && strings.Contains(e.Content, "30 分钟") {
				return fmt.Sprintf("应在 10 分钟内报告，高危事件每 30 分钟更新一次 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "30,000"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "财务负责人") && strings.Contains(e.Content, "3 家") {
				return fmt.Sprintf("由财务负责人审批，需至少 3 家供应商报价 [%d]。", e.N)
			}
		}
	case strings.Contains(question, "全量备份"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "02:00") && strings.Contains(e.Content, "15 分钟") {
				return fmt.Sprintf("每天 02:00 全量备份，每 15 分钟归档 WAL [%d]。", e.N)
			}
		}
	case strings.Contains(question, "离职交接"):
		for _, e := range evidence {
			if strings.Contains(e.Content, "门禁卡") && strings.Contains(e.Content, "VPN") {
				return fmt.Sprintf("包含门禁卡回收、VPN 证书吊销、权限移除及资产归还 [%d]。", e.N)
			}
		}
	}
	return answer.Refusal
}
