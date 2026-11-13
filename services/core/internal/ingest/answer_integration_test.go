package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

func TestCitedAnswerPostgres(t *testing.T) {
	publicationDatabase(t)
	f, job, uploaded, plan, staged := prepareVectorFixture(t)
	t.Cleanup(func() {
		for _, table := range []string{"answer_feedback", "answer_message", "conversation"} {
			if _, err := f.admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id=$1", f.tenant); err != nil {
				t.Error(err)
			}
		}
		_, err := f.admin.Exec(context.Background(), `DELETE FROM chat_model WHERE tenant_id=$1`, f.tenant)
		if err != nil {
			t.Error(err)
		}
		_, err = f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant)
		if err != nil {
			t.Error(err)
		}
	})
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, plan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	finishPublicationVectors(t, f, job, staged)
	if err := f.queue.Complete(f.ctx, job.Lease, preparePublication(t, f, job, plan, staged)); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var silent atomic.Bool
	var chatCalls atomic.Int32
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			var body struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Input) != 1 {
				t.Error("invalid embedding")
				w.WriteHeader(400)
				return
			}
			n, err := counter.Count(r.Context(), body.Input[0])
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": testVector(1, 0)}}, "usage": map[string]int{"prompt_tokens": n, "total_tokens": n}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected model path")
			w.WriteHeader(404)
			return
		}
		chatCalls.Add(1)
		var body struct {
			Messages []models.ChatMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 2 {
			t.Error("invalid chat prompt")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if silent.Load() {
			started <- struct{}{}
			<-r.Context().Done()
			stopped <- struct{}{}
			return
		}
		if strings.Contains(body.Messages[0].Content, "只返回 JSON") {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"question\\\":\\\"申请流程\\\"}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if !strings.Contains(body.Messages[1].Content, "evidence") {
			t.Error("answer prompt without evidence")
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"请按申请流程办理 [1]。\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":88}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	var provider, modelID int64
	if err = f.admin.QueryRow(f.ctx, `SELECT provider_id FROM embedding_space WHERE tenant_id=$1 AND id=$2`, f.tenant, plan.EmbeddingSpaceID).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if _, err = f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	if err = f.admin.QueryRow(f.ctx, `INSERT INTO chat_model(tenant_id,provider_id,model) VALUES($1,$2,'chat') RETURNING id`, f.tenant, provider).Scan(&modelID); err != nil {
		t.Fatal(err)
	}
	search, err := retrieval.NewService(f.pool, nil, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	service, err := answer.NewService(f.pool, nil, search, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	service.CheckInterval = 20 * time.Millisecond
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	request := answer.Request{KnowledgeBaseID: f.kbID, ModelID: modelID, Question: "申请"}
	var evidence []answer.AnswerEvidence
	var final *answer.Done
	err = service.Stream(f.ctx, actor, request, func(e answer.Event) error {
		if e.Retrieval != nil {
			evidence = e.Retrieval.Evidence
		}
		if e.Done != nil {
			final = e.Done
		}
		return nil
	})
	if err != nil || final == nil || final.Outcome != "answered" || len(evidence) != 1 || len(evidence[0].ChunkIDs) != 2 || evidence[0].Content != "申请。\n\n复核。" || final.Usage.TotalTokens != 88 {
		t.Fatalf("answer err=%v final=%+v evidence=%+v", err, final, evidence)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	source, err := knowledge.ReadSection(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, evidence[0].SectionID)
	if err != nil || source.VersionID != uploaded.VersionID {
		t.Fatalf("source unavailable: %+v %v", source, err)
	}
	var audited int
	if err = f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='answer.generate' AND details->>'outcome'='answered'`, f.tenant).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit %d %v", audited, err)
	}
	conversations, savedID := testSavedConversations(t, f, service, modelID, knowledge, &silent, started, stopped)
	before := chatCalls.Load()
	if err = service.Stream(f.ctx, retrieval.Actor{TenantID: f.foreign, UserID: f.user}, request, func(answer.Event) error { t.Error("cross-tenant event"); return nil }); !errors.Is(err, retrieval.ErrNotFound) || chatCalls.Load() != before {
		t.Fatalf("tenant leak: %v", err)
	}
	empty, err := knowledge.Create(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.CreateRequest{Name: "empty answer kb", EmbeddingSpaceID: plan.EmbeddingSpaceID, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	emptyRequest := request
	emptyRequest.KnowledgeBaseID = empty.ID
	final = nil
	if err = service.Stream(f.ctx, actor, emptyRequest, func(e answer.Event) error {
		if e.Done != nil {
			final = e.Done
		}
		return nil
	}); err != nil || final == nil || final.Outcome != "refused" || chatCalls.Load() != before {
		t.Fatalf("no evidence called model: %+v %v", final, err)
	}
	var viewer int64
	if err = f.admin.QueryRow(f.ctx, `INSERT INTO app_user(tenant_id,display_name)VALUES($1,'answer viewer')RETURNING id`, f.tenant).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	owner := kb.Actor{TenantID: f.tenant, UserID: f.user}
	if err = knowledge.SetMember(f.ctx, owner, f.kbID, viewer, kb.Viewer); err != nil {
		t.Fatal(err)
	}
	silent.Store(true)
	finished := make(chan error, 1)
	go func() {
		finished <- service.Stream(f.ctx, retrieval.Actor{TenantID: f.tenant, UserID: viewer}, request, func(e answer.Event) error {
			if e.Delta != nil || e.Done != nil {
				t.Error("revoked stream returned content")
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal("model not started")
	}
	if err = knowledge.RemoveMember(f.ctx, owner, f.kbID, viewer); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if !errors.Is(err, retrieval.ErrNotFound) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation not propagated")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream not cancelled")
	}
	found, err := search.Search(f.ctx, actor, retrieval.Request{KnowledgeBaseID: f.kbID, Query: "申请", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=false,disabled=true WHERE tenant_id=$1 AND id=$2`, f.tenant, found.Candidates[0].ChunkID); err != nil {
		t.Fatal(err)
	}
	guard, err := retrieval.NewDriver(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = guard.AuthorizeEvidence(f.ctx, actor, f.kbID, found.Candidates); !errors.Is(err, retrieval.ErrEvidenceChanged) {
		t.Fatalf("disabled evidence accepted: %v", err)
	}
	if _, err = conversations.Get(f.ctx, actor, savedID); !errors.Is(err, retrieval.ErrEvidenceChanged) {
		t.Fatalf("saved stale evidence exposed: %v", err)
	}
}
