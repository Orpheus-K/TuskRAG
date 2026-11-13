package kb

import (
	"context"
	"errors"
	"slices"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func startScopeDocument(t *testing.T, f *fixture, kbID int64, restricted bool) int64 {
	t.Helper()
	var id, version int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES($1,$2,'scope fixture','text/plain') RETURNING id`, f.tenantA, kbID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by) VALUES($1,$2,$3,decode(repeat('cd',32),'hex'),1,'scope-fixture-' || $3::bigint::text,'ready',$4) RETURNING id`, f.tenantA, kbID, id, f.owner).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE id=$2`, version, id); err != nil {
		t.Fatal(err)
	}
	if restricted {
		if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestEvalStartServerScopeAndStableRetry(t *testing.T) {
	f, store, request := runFixture(t)
	actor, kbID := request.Config.Actor, request.Config.KnowledgeBaseID
	extra := startScopeDocument(t, f, kbID, false)
	hidden := startScopeDocument(t, f, kbID, true)
	input := eval.StartRequest{SetID: request.Set.SetID, Revision: request.Set.Revision, Mode: "rrf"}
	type outcome struct {
		run eval.StartedRun
		err error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() { run, err := store.Start(f.ctx, actor, kbID, "same", input); done <- outcome{run, err} }()
	}
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.run.ID <= 0 || a.run != b.run {
		t.Fatalf("duplicate start: %+v %+v", a, b)
	}
	stored, err := store.Get(f.ctx, actor, kbID, a.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := stored.Request.Config
	if c.Actor != actor || c.KnowledgeBaseID != kbID || c.ModelKind != "real" || c.ModelLabel == "" || c.Reranker != nil || c.Configuration == nil || c.Configuration.RetrievalRevision != 0 || !slices.Equal(c.AllowedDocumentIDs, []int64{request.Config.AllowedDocumentIDs[0], extra}) || slices.Contains(c.AllowedDocumentIDs, hidden) {
		t.Fatalf("server selection: %+v", c)
	}
	if _, err := f.service.PutConfig(f.ctx, f.actor(f.owner), kbID, 0, RetrievalConfig{TopK: 5}); err != nil {
		t.Fatal(err)
	}
	newDoc := startScopeDocument(t, f, kbID, false)
	retry, err := store.Start(f.ctx, actor, kbID, "same", input)
	if err != nil || retry != a.run {
		t.Fatalf("changed defaults broke retry: %+v %v", retry, err)
	}
	original, err := store.Get(f.ctx, actor, kbID, a.run.ID)
	if err != nil || original.ConfigDigest != stored.ConfigDigest || slices.Contains(original.Request.Config.AllowedDocumentIDs, newDoc) {
		t.Fatalf("retry replaced snapshot: %+v %v", original, err)
	}
	changed := input
	changed.Mode = "vector"
	if _, err := store.Start(f.ctx, actor, kbID, "same", changed); !errors.Is(err, eval.ErrRunIdempotencyConflict) {
		t.Fatalf("different input reused key: %v", err)
	}
	fresh, err := store.Start(f.ctx, actor, kbID, "fresh", input)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.Get(f.ctx, actor, kbID, fresh.ID)
	if err != nil || latest.Request.Config.Configuration.RetrievalRevision != 1 || !slices.Contains(latest.Request.Config.AllowedDocumentIDs, newDoc) {
		t.Fatalf("fresh snapshot: %+v %v", latest, err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 2 {
		t.Fatalf("jobs %d %v", count, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, request.Config.AllowedDocumentIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Start(f.ctx, actor, kbID, "same", input); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("retry bypassed revoked scope: %v", err)
	}
	if _, err := store.Start(f.ctx, actor, kbID, "revoked-new", input); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("new request disclosed revoked target: %v", err)
	}
}

func TestEvalStartSavedRerankerAndAccess(t *testing.T) {
	f, store, request := runFixture(t)
	actor, kbID := request.Config.Actor, request.Config.KnowledgeBaseID
	input := eval.StartRequest{SetID: request.Set.SetID, Revision: 1, Mode: "rerank"}
	if _, err := store.Start(f.ctx, actor, kbID, "rank", input); !errors.Is(err, retrieval.ErrRerankUnavailable) {
		t.Fatalf("unconfigured rerank: %v", err)
	}
	var provider int64
	if err := f.admin.QueryRow(f.ctx, `SELECT provider_id FROM embedding_space WHERE id=$1`, f.spaceA).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	score := 0.4
	if _, err := f.service.PutConfig(f.ctx, f.actor(f.owner), kbID, 0, RetrievalConfig{TopK: 8, Reranker: &retrieval.RerankOptions{ProviderID: provider, Model: "reranker", TopN: 20, Fallback: true, MinScore: &score}}); err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(f.ctx, actor, kbID, "rank", input)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(f.ctx, actor, kbID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	rank := stored.Request.Config.Reranker
	if rank == nil || rank.ProviderID != provider || rank.Model != "reranker" || rank.TopN != 20 || rank.Fallback || rank.MinScore == nil || *rank.MinScore != score {
		t.Fatalf("rank snapshot: %+v", rank)
	}
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), kbID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Start(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, kbID, "viewer", input); !errors.Is(err, eval.ErrSetForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := store.Start(f.ctx, retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}, kbID, "foreign", input); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("tenant: %v", err)
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Start(f.ctx, actor, kbID, "rank", input); !errors.Is(err, eval.ErrSetArchived) {
		t.Fatalf("archived retry: %v", err)
	}
}

func TestEvalStartRevokedWhileWaiting(t *testing.T) {
	f, store, request := runFixture(t)
	kbID := request.Config.KnowledgeBaseID
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), kbID, f.other, Admin); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, kbID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Start(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, kbID, "revoke", eval.StartRequest{SetID: request.Set.SetID, Revision: 1, Mode: "rrf"})
		done <- err
	}()
	waitSetListBlock(t, f, tx, done)
	if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, kbID, f.other); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("revoked start: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked job %d %v", count, err)
	}
}
