//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/tokenizer"
)

func testRebuildProcess(t *testing.T, ctx context.Context, admin *pgx.Conn, migratorURL, address string, client *http.Client, cookie *http.Cookie, csrf string, actor kb.Actor, kbID int64) {
	t.Helper()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var block atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "test" || len(request.Input) == 0 {
			t.Error("invalid rebuild model request")
			w.WriteHeader(400)
			return
		}
		if block.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		data := make([]any, 0, len(request.Input))
		tokens := 0
		for i, input := range request.Input {
			count, err := counter.Count(r.Context(), input)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			tokens += count
			vector := make([]float32, 64)
			vector[0] = 1
			data = append(data, map[string]any{"index": i, "embedding": vector})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}}); err != nil {
			t.Error(err)
		}
	}))
	defer func() { once.Do(func() { close(release) }); model.Close() }()
	var provider, source int64
	var revision int32
	if err := admin.QueryRow(ctx, `SELECT embedding_space_id,config_revision FROM knowledge_base WHERE id=$1`, kbID).Scan(&source, &revision); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES($1,'rebuild-process',$2,'internal') RETURNING id`, actor.TenantID, model.URL).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	newSpace := func() int64 {
		var id int64
		if err := admin.QueryRow(ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) VALUES($1,$2,'test',64,true,'cosine') RETURNING id`, actor.TenantID, provider).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := indexing.Provision(ctx, migratorURL, actor.TenantID, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	request := func(method, path, body, key string, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(out)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil {
			t.Fatalf("%s %s status=%d decode=%v close=%v", method, path, response.StatusCode, decodeErr, closeErr)
		}
	}
	path := fmt.Sprintf("/api/v1/kbs/%d/rebuilds", kbID)
	target := newSpace()
	body := fmt.Sprintf(`{"target_space_id":%d,"revision":%d}`, target, revision)
	var created, replay kb.Rebuild
	request(http.MethodPost, path, body, "process-success", &created)
	request(http.MethodPost, path, body, "process-success", &replay)
	if created.JobID != replay.JobID || created.SourceSpaceID != source || created.TargetSpaceID != target {
		t.Fatal("rebuild create/replay binding mismatch")
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	wait := func(job int64, want string) kb.Rebuild {
		t.Helper()
		for {
			var state kb.Rebuild
			request(http.MethodGet, fmt.Sprintf("%s/%d", path, job), "", "", &state)
			switch string(state.Status) {
			case "succeeded", "failed", "dead", "cancelled":
				if string(state.Status) != want {
					t.Fatalf("rebuild terminal=%s code=%v want=%s", state.Status, state.FailureCode, want)
				}
				return state
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	finished := wait(created.JobID, "succeeded")
	if finished.FinishedAt == nil {
		t.Fatal("missing terminal timestamp")
	}
	var switched bool
	var expected, actual int
	if err := admin.QueryRow(ctx, `SELECT k.embedding_space_id=$1 AND k.config_revision=$2 AND k.pending_space_id IS NULL,(SELECT count(*) FROM chunk c WHERE c.kb_id=k.id AND c.is_current),(SELECT count(*) FROM chunk_vector v WHERE v.kb_id=k.id AND v.space_id=$1 AND v.is_current) FROM knowledge_base k WHERE k.id=$3`, target, revision+1, kbID).Scan(&switched, &expected, &actual); err != nil || !switched || expected == 0 || expected != actual {
		t.Fatalf("switch=%t coverage=%d/%d err=%v", switched, actual, expected, err)
	}
	block.Store(true)
	cancelledTarget := newSpace()
	request(http.MethodPost, path, fmt.Sprintf(`{"target_space_id":%d,"revision":%d}`, cancelledTarget, revision+1), "process-cancel", &created)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var ack struct {
		Status string `json:"status"`
	}
	request(http.MethodPost, fmt.Sprintf("%s/%d/cancel", path, created.JobID), "", "", &ack)
	if ack.Status != "cancelled" {
		t.Fatal("cancellation not acknowledged")
	}
	once.Do(func() { close(release) })
	wait(created.JobID, "cancelled")
	if err := admin.QueryRow(ctx, `SELECT k.embedding_space_id=$1 AND k.config_revision=$2 AND k.pending_space_id IS NULL,(SELECT count(*) FROM chunk_vector WHERE space_id=$3) FROM knowledge_base k WHERE id=$4`, target, revision+1, cancelledTarget, kbID).Scan(&switched, &actual); err != nil || !switched || actual != 0 {
		t.Fatalf("cancel changed binding or wrote vectors: binding=%t vectors=%d err=%v", switched, actual, err)
	}
	t.Logf("production API/all worker rebuild: covered_chunks=%d success+stable_replay+cancel passed", expected)
}
