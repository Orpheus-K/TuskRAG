package ingest

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/tokenizer"
)

func TestEmbeddingStageFromNativeMarkdown(t *testing.T) {
	f := setup(t)
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "synthetic.md", MIME: "text/markdown", Content: strings.NewReader("# 制度\n\n申请。\n\n复核。\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "embedding-document-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := makeStage(t, f, nativePipelineParser(t)).Parse(f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil {
		t.Fatal(err)
	}
	var space int64
	var revision int32
	var profile EmbeddingProfile
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id, k.config_revision, e.provider_id, e.model, e.dims, e.normalized, e.distance
		FROM knowledge_base k JOIN embedding_space e ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id
		WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&space, &revision, &profile.ProviderID, &profile.Model,
		&profile.Dimensions, &profile.Normalized, &profile.Distance); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(f.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	request := planRequest(normalized)
	request.EmbeddingSpaceID, request.EmbeddingProfile, request.ConfigRevision = space, profile, revision
	request.ChildTargetMax, request.ChildHardMax, request.OverlapMax, request.CountTokens = 6, 80, 0, counter.Count
	plan, err := BuildPlan(f.ctx, request)
	if err != nil || len(plan.Chunks) != 2 || plan.Chunks[0].Content != "申请。" || plan.Chunks[1].Content != "复核。" {
		t.Fatalf("build parsed document plan: %+v err=%v", plan, err)
	}
	planStage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := planStage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeEmbeddingService{t: t, counter: counter, documents: map[string]int{plan.Chunks[0].SearchText: 0, plan.Chunks[1].SearchText: 0}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, profile.ProviderID); err != nil {
		t.Fatal(err)
	}
	limits := models.ClientLimits{MaxBatch: 1, MaxRequestBytes: 1024 * 1024, MaxResponseBytes: 1024 * 1024, Timeout: 10 * time.Second, Concurrency: 1}
	stage, err := NewEmbeddingStage(f.queue, f.pool, nil, counter.Count, limits, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); err != nil {
		t.Fatal(err)
	}
	var status string
	var vectorCount, currentCount int
	var noCurrent bool
	if err := f.admin.QueryRow(f.ctx, `SELECT v.status, d.current_version_id IS NULL,
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current)
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &noCurrent, &vectorCount, &currentCount); err != nil ||
		status != "indexing" || !noCurrent || vectorCount != 2 || currentCount != 0 ||
		fake.callsFor(plan.Chunks[0].SearchText) != 1 || fake.callsFor(plan.Chunks[1].SearchText) != 1 {
		t.Fatalf("document embedding state: status=%s vectors=%d current=%d err=%v", status, vectorCount, currentCount, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE chunk_id=$1`, staged.Chunks[0].ID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign tenant saw document vectors: count=%d err=%v", visible, err)
	}
}
