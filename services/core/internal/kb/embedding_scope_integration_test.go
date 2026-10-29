package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/models"
)

func TestEmbeddingClientUsesDatabasePolicyBeforeEachRequest(t *testing.T) {
	f := setup(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		vector := make([]float32, 64)
		vector[0] = 1
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=(
		SELECT provider_id FROM embedding_space WHERE tenant_id=$2 AND id=$3)`, server.URL, f.tenantA, f.spaceA); err != nil {
		t.Fatal(err)
	}
	base, err := f.service.Create(f.ctx, f.actor(f.owner), CreateRequest{Name: "runtime scope", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := models.NewCatalog(f.pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := models.EmbeddingBinding{TenantID: f.tenantA, KnowledgeBaseID: base.ID, EmbeddingSpaceID: f.spaceA, ConfigRevision: base.ConfigRevision}
	limits := models.ClientLimits{MaxBatch: 1, MaxRequestBytes: 1024, MaxResponseBytes: 8192, Timeout: 5 * time.Second, Concurrency: 1}
	auditor, err := models.NewEmbeddingDenialAuditor(f.pool, f.tenantA, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := catalog.OpenEmbeddingClient(f.ctx, binding, limits, f.service, auditor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	forged := models.EgressScope{TenantID: f.tenantA, KnowledgeBaseID: base.ID, TenantPolicy: models.Allow, KnowledgeBasePolicy: models.Allow}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE tenant_id=$1 AND id=$2`, f.tenantA, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); !errors.Is(err, models.ErrEgressDenied) || calls.Load() != 0 {
		t.Fatalf("forged policy reached model: err=%v calls=%d", err, calls.Load())
	}
	var auditID, actor, provider int64
	var details []byte
	if err := f.admin.QueryRow(f.ctx, `SELECT id, actor_user_id, object_id, details FROM audit_log
		WHERE tenant_id=$1 AND action='model.call' AND result='deny'`, f.tenantA).Scan(&auditID, &actor, &provider, &details); err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		KnowledgeBaseID     int64  `json:"knowledge_base_id"`
		Operation           string `json:"operation"`
		TenantPolicy        string `json:"tenant_policy"`
		KnowledgeBasePolicy string `json:"knowledge_base_policy"`
		Locality            string `json:"locality"`
	}
	if err := json.Unmarshal(details, &recorded); err != nil || actor != f.owner || provider <= 0 || recorded.KnowledgeBaseID != base.ID ||
		recorded.Operation != "embeddings" || recorded.TenantPolicy != "deny" || recorded.KnowledgeBasePolicy != "allow" || recorded.Locality != "internal" ||
		strings.Contains(string(details), "synthetic") {
		t.Fatalf("invalid model denial audit: actor=%d provider=%d details=%s err=%v", actor, provider, details, err)
	}
	foreignAuditor, err := models.NewEmbeddingDenialAuditor(f.pool, f.tenantA, f.foreignUser)
	if err != nil {
		t.Fatal(err)
	}
	denied := models.DeniedCall{TenantID: f.tenantA, KnowledgeBaseID: base.ID, ProviderID: provider, TenantPolicy: models.Deny,
		KnowledgeBasePolicy: models.Allow, Locality: models.Internal, Operation: "embeddings"}
	if err := foreignAuditor.RecordModelDenied(f.ctx, denied); !errors.Is(err, models.ErrAuditSubjectUnavailable) {
		t.Fatalf("foreign audit actor accepted: %v", err)
	}
	denied.TenantID = f.tenantB
	if err := auditor.RecordModelDenied(f.ctx, denied); err == nil {
		t.Fatal("foreign audit event accepted")
	}
	_, err = f.admin.Exec(f.ctx, `INSERT INTO audit_log (tenant_id,actor_user_id,action,object_type,object_id,result,details)
		VALUES ($1,$2,'model.call','model_provider',$3,'deny','[]'::jsonb)`, f.tenantA, f.owner, provider)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.Code != "23514" {
		t.Fatalf("non-object audit details accepted: %v", err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE id=$1`, auditID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign tenant saw model denial audit: count=%d err=%v", visible, err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	trigger := fmt.Sprintf("tuskrag_test_model_denial_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+trigger+" ON audit_log"); err != nil {
			t.Errorf("remove denial audit trigger: %v", err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION "+trigger+"()"); err != nil {
			t.Errorf("remove denial audit function: %v", err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d AND NEW.action = 'model.call') EXECUTE FUNCTION %s()", trigger, f.tenantA, trigger)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); err == nil || errors.Is(err, models.ErrEgressDenied) || calls.Load() != 0 {
		t.Fatalf("audit failure was hidden or model was called: err=%v calls=%d", err, calls.Load())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE tenant SET egress_policy_max='allow' WHERE id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); err != nil || calls.Load() != 1 {
		t.Fatalf("updated policy did not allow model: err=%v calls=%d", err, calls.Load())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET model='changed' WHERE tenant_id=$1 AND id=$2`, f.tenantA, f.spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); !errors.Is(err, models.ErrRuntimeChanged) || calls.Load() != 1 {
		t.Fatalf("changed embedding model reached service: err=%v calls=%d", err, calls.Load())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET model='test' WHERE tenant_id=$1 AND id=$2`, f.tenantA, f.spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=(
		SELECT provider_id FROM embedding_space WHERE tenant_id=$2 AND id=$3)`, server.URL+"/changed", f.tenantA, f.spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); !errors.Is(err, models.ErrRuntimeChanged) || calls.Load() != 1 {
		t.Fatalf("changed provider endpoint reached service: err=%v calls=%d", err, calls.Load())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET config_revision=config_revision+1 WHERE tenant_id=$1 AND id=$2`, f.tenantA, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); !errors.Is(err, ErrNotFound) || calls.Load() != 1 {
		t.Fatalf("stale configuration reached model: err=%v calls=%d", err, calls.Load())
	}
	forged.KnowledgeBaseID++
	if _, err := client.Embed(f.ctx, forged, []string{"synthetic"}); !errors.Is(err, models.ErrScopeMismatch) || calls.Load() != 1 {
		t.Fatalf("foreign knowledge base reached model: err=%v calls=%d", err, calls.Load())
	}
}
