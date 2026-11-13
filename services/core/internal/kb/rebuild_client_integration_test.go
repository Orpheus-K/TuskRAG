package kb

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

func TestRebuildClientRechecksBeforeSendingContent(t *testing.T) {
	for _, change := range []string{"cancel", "revoke", "model", "egress"} {
		t.Run(change, func(t *testing.T) {
			f, s, base, target := rebuildFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string   `json:"model"`
					Input []string `json:"input"`
				}
				if r.Method != "POST" || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "replacement" || len(request.Input) != 1 || request.Input[0] != "synthetic rebuild content" {
					t.Error("unexpected rebuild model request")
					w.WriteHeader(400)
					return
				}
				calls.Add(1)
				vector := make([]float32, 64)
				vector[0] = 1
				if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2`, server.URL, f.tenantA); err != nil {
				t.Fatal(err)
			}
			r, err := s.Create(f.ctx, f.actor(f.owner), base.ID, "client", RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision})
			if err != nil {
				t.Fatal(err)
			}
			job, err := s.queue.ClaimKindsWithRecovery(f.ctx, f.tenantA, "client-worker", time.Minute, []jobs.Kind{jobs.Reembed}, s.Expire)
			if err != nil {
				t.Fatal(err)
			}
			binding, resolver, err := s.Target(f.ctx, job.Lease)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := models.NewCatalog(f.pool, nil)
			if err != nil {
				t.Fatal(err)
			}
			auditor, err := models.NewEmbeddingDenialAuditor(f.pool, f.tenantA, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			limits := models.ClientLimits{MaxBatch: 1, MaxRequestBytes: 1024, MaxResponseBytes: 8192, Timeout: time.Second * 5, Concurrency: 1}
			client, space, err := catalog.OpenEmbeddingClient(f.ctx, binding, limits, resolver, auditor)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			if space.ID != target || space.Model != "replacement" {
				t.Fatalf("wrong runtime %+v", space)
			}
			scope := models.EgressScope{TenantID: f.tenantA, KnowledgeBaseID: base.ID, TenantPolicy: models.Allow, KnowledgeBasePolicy: models.Allow}
			if _, err := client.Embed(f.ctx, scope, []string{"synthetic rebuild content"}); err != nil || calls.Load() != 1 {
				t.Fatalf("first call %v count=%d", err, calls.Load())
			}
			var want error
			switch change {
			case "cancel":
				err = s.Cancel(f.ctx, f.actor(f.owner), base.ID, r.JobID)
				want = jobs.ErrLeaseLost
			case "revoke":
				_, err = f.admin.Exec(f.ctx, `DELETE FROM kb_membership WHERE kb_id=$1 AND user_id=$2`, base.ID, f.owner)
				want = ErrNotFound
			case "model":
				_, err = f.admin.Exec(f.ctx, `UPDATE embedding_space SET model='changed' WHERE id=$1`, target)
				want = models.ErrRuntimeChanged
			case "egress":
				_, err = f.admin.Exec(f.ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE id=$1`, base.ID)
				want = models.ErrEgressDenied
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Embed(f.ctx, scope, []string{"synthetic rebuild content"}); !errors.Is(err, want) || calls.Load() != 1 {
				t.Fatalf("changed state reached model: err=%v want=%v calls=%d", err, want, calls.Load())
			}
			if change == "egress" {
				var count int
				if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='model.call' AND result='deny'`, f.tenantA).Scan(&count); err != nil || count != 1 {
					t.Fatalf("denial audit count=%d err=%v", count, err)
				}
			}
		})
	}
}
