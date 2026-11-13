package kb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

func TestConfigRevisionHistoryAndConcurrency(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	actor := f.actor(f.owner)
	initial, err := f.service.GetConfig(f.ctx, actor, base.ID)
	if err != nil || initial.Revision != 0 || initial.Retrieval != nil {
		t.Fatalf("initial config=%+v err=%v", initial, err)
	}
	var provider int64
	if err := f.admin.QueryRow(f.ctx, `SELECT provider_id FROM embedding_space WHERE id=$1`, f.spaceA).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	threshold := 0.0
	config := RetrievalConfig{TopK: 5, Reranker: &retrieval.RerankOptions{ProviderID: provider, Model: "reranker", TopN: 20, Fallback: true, MinScore: &threshold}}
	type outcome struct {
		config KnowledgeConfig
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			value, err := f.service.PutConfig(f.ctx, actor, base.ID, 0, config)
			results <- outcome{value, err}
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		result := <-results
		if result.err == nil && result.config.Revision == 1 {
			successes++
		} else if errors.Is(result.err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	saved, err := f.service.GetConfig(f.ctx, actor, base.ID)
	if err != nil || saved.Retrieval == nil || saved.Retrieval.Reranker == nil || saved.Retrieval.TopK != 5 || saved.Retrieval.Reranker.MinScore == nil || *saved.Retrieval.Reranker.MinScore != 0 || !saved.Retrieval.Reranker.Fallback {
		t.Fatalf("round trip config=%+v err=%v", saved, err)
	}
	if err := f.service.SetMember(f.ctx, actor, base.ID, f.other, Admin); err != nil {
		t.Fatal(err)
	}
	disabled, err := f.service.PutConfig(f.ctx, f.actor(f.other), base.ID, 1, RetrievalConfig{TopK: 8})
	if err != nil || disabled.Revision != 2 || disabled.Retrieval == nil || disabled.Retrieval.Reranker != nil {
		t.Fatalf("disable=%+v err=%v", disabled, err)
	}
	var revisions, audits int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM kb_retrieval_config_revision WHERE tenant_id=$1 AND kb_id=$2`, f.tenantA, base.ID).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("history count=%d err=%v", revisions, err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND object_id=$2 AND action='kb.config.update' AND result='allow' AND details->>'retrieval_revision' IN ('1','2')`, f.tenantA, base.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	current, err := f.service.Get(f.ctx, actor, base.ID)
	if err != nil || current.ConfigRevision != base.ConfigRevision || current.EmbeddingSpaceID != base.EmbeddingSpaceID {
		t.Fatalf("retrieval update changed ingest binding: %+v err=%v", current, err)
	}
	// The application cannot rewrite history, and RLS hides it from other tenants.
	for _, tenantID := range []int64{f.tenantA, f.tenantB} {
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenantID)); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM kb_retrieval_config_revision WHERE kb_id=$1`, base.ID).Scan(&count); err != nil || (tenantID == f.tenantA && count != 2) || (tenantID == f.tenantB && count != 0) {
			t.Fatalf("RLS count=%d err=%v", count, err)
		}
		_, err = tx.Exec(f.ctx, `UPDATE kb_retrieval_config_revision SET top_k=30 WHERE kb_id=$1`, base.ID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("history mutable: %v", err)
		}
		if err := tx.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigPermissionsAndInvalidSettings(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	owner := f.actor(f.owner)
	config := RetrievalConfig{TopK: 8}
	for _, role := range []Role{Viewer, Editor} {
		if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, role); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.GetConfig(f.ctx, f.actor(f.other), base.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s read: %v", role, err)
		}
		if _, err := f.service.PutConfig(f.ctx, f.actor(f.other), base.ID, 0, config); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s write: %v", role, err)
		}
	}
	for _, actor := range []Actor{f.actor(f.third), {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if _, err := f.service.GetConfig(f.ctx, actor, base.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("hidden read: %v", err)
		}
		if _, err := f.service.PutConfig(f.ctx, actor, base.ID, 0, config); !errors.Is(err, ErrNotFound) {
			t.Fatalf("hidden write: %v", err)
		}
	}
	if err := f.service.SetMember(f.ctx, owner, base.ID, f.other, Admin); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RemoveMember(f.ctx, owner, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PutConfig(f.ctx, f.actor(f.other), base.ID, 0, config); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked admin: %v", err)
	}
	var foreignProvider, externalProvider int64
	if err := f.admin.QueryRow(f.ctx, `SELECT provider_id FROM embedding_space WHERE id=$1`, f.spaceB).Scan(&foreignProvider); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES ($1,'external','https://example.invalid','external') RETURNING id`, f.tenantA).Scan(&externalProvider); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider int64
		want     error
	}{{foreignProvider, ErrConfigProviderNotFound}, {externalProvider, models.ErrEgressDenied}} {
		config.Reranker = &retrieval.RerankOptions{ProviderID: tc.provider, Model: "model", TopN: 30}
		if _, err := f.service.PutConfig(f.ctx, owner, base.ID, 0, config); !errors.Is(err, tc.want) {
			t.Fatalf("provider setting: %v", err)
		}
	}
	for _, bad := range []RetrievalConfig{{TopK: 0}, {TopK: 31}, {TopK: 8, Reranker: &retrieval.RerankOptions{ProviderID: 1, Model: " ", TopN: 30}}, {TopK: 8, Reranker: &retrieval.RerankOptions{ProviderID: 1, Model: "model", TopN: 21}}} {
		if _, err := f.service.PutConfig(f.ctx, owner, base.ID, 0, bad); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid settings accepted: %v", err)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		bad := RetrievalConfig{TopK: 8, Reranker: &retrieval.RerankOptions{ProviderID: 1, Model: "model", TopN: 30, MinScore: &value}}
		if _, err := f.service.PutConfig(f.ctx, owner, base.ID, 0, bad); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid threshold accepted: %v", err)
		}
	}
	if err := f.service.Archive(f.ctx, owner, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PutConfig(f.ctx, owner, base.ID, 0, RetrievalConfig{TopK: 8}); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived update: %v", err)
	}
	unchanged, err := f.service.GetConfig(f.ctx, owner, base.ID)
	if err != nil || unchanged.Revision != 0 || unchanged.Retrieval != nil {
		t.Fatalf("rejected writes persisted: %+v err=%v", unchanged, err)
	}
}

func TestConfigAuditFailureRollsBackRevision(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	// Fail only this tenant's configuration audit; never alter another test's writes.
	name := fmt.Sprintf("config_audit_failure_%d", f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='kb.config.update' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)
	if _, err := f.admin.Exec(f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.service.PutConfig(f.ctx, f.actor(f.owner), base.ID, 0, RetrievalConfig{TopK: 8}); err == nil {
		t.Fatal("audit failure returned success")
	}
	config, err := f.service.GetConfig(f.ctx, f.actor(f.owner), base.ID)
	if err != nil || config.Revision != 0 || config.Retrieval != nil {
		t.Fatalf("revision survived rollback: %+v err=%v", config, err)
	}
}

func TestConfigRevocationWinsWhileUpdateWaits(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, Admin); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, base.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.service.PutConfig(f.ctx, f.actor(f.other), base.ID, 0, RetrievalConfig{TopK: 8})
		result <- err
	}()
	// Wait for an observed PostgreSQL lock dependency, not a timing assumption.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := tx.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid)))`, int32(f.admin.PgConn().PID())).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("update did not wait for revocation lock: %v", err)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-ticker.C:
		}
	}
	if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked writer committed: %v", err)
	}
	config, err := f.service.GetConfig(f.ctx, f.actor(f.owner), base.ID)
	if err != nil || config.Revision != 0 {
		t.Fatalf("revoked write persisted: %+v err=%v", config, err)
	}
}
