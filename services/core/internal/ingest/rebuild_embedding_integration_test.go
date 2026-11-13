package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

func prepareRebuildEmbedding(t *testing.T) (*fixture, jobs.ClaimedJob, int64, *RebuildEmbedding, *fakeEmbeddingService, PreparedPlan) {
	t.Helper()
	f, job, _, plan, staged, original, fake := prepareEmbeddingStage(t)
	vectors, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []VectorInput
	for i := range staged.Chunks {
		inputs = append(inputs, vectorInput(staged, i, testVector(1, 0)))
	}
	if err := vectors.Write(f.ctx, job, staged.Identity, inputs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM embedding_rebuild WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	for _, statement := range []string{`UPDATE document_version SET status='ready' WHERE tenant_id=$1`, `UPDATE document SET current_version_id=(SELECT id FROM document_version WHERE document_id=document.id) WHERE tenant_id=$1`, `UPDATE chunk SET is_current=true WHERE tenant_id=$1`, `UPDATE chunk_vector SET is_current=true WHERE tenant_id=$1`} {
		if _, err := f.admin.Exec(f.ctx, statement, f.tenant); err != nil {
			t.Fatal(err)
		}
	}
	var target int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) SELECT tenant_id,provider_id,model,dims,normalized,distance FROM embedding_space WHERE id=$1 RETURNING id`, staged.Identity.EmbeddingSpaceID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	stage, err := NewRebuildEmbedding(f.queue, f.pool, nil, original.count, original.limits, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.rebuild.Create(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, "embedding", kb.RebuildRequest{TargetSpaceID: target, Revision: staged.Identity.ConfigRevision}); err != nil {
		t.Fatal(err)
	}
	leased, err := f.queue.ClaimKindsWithRecovery(f.ctx, f.tenant, "rebuild-model", time.Minute, []jobs.Kind{jobs.Reembed}, stage.rebuild.Expire)
	if err != nil {
		t.Fatal(err)
	}
	return f, leased, target, stage, fake, plan
}

func TestRebuildEmbeddingRetryAndReplay(t *testing.T) {
	f, job, target, stage, fake, plan := prepareRebuildEmbedding(t)
	fake.failOnce = plan.Chunks[1].SearchText
	err := stage.Run(f.ctx, job)
	var upstream *models.UpstreamError
	if !errors.As(err, &upstream) || !upstream.Retryable {
		t.Fatalf("model error %v", err)
	}
	for range 2 {
		if err := stage.Run(f.ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	var total int
	var old bool
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$2),k.embedding_space_id<>$2 AND k.pending_space_id=$2,j.status FROM knowledge_base k JOIN job j ON j.id=$3 WHERE k.id=$4`, f.tenant, target, job.Lease.JobID, f.kbID).Scan(&total, &old, &status); err != nil || total != len(plan.Chunks) || !old || status != "running" {
		t.Fatalf("state %d %t %s %v", total, old, status, err)
	}
}

func TestRebuildEmbeddingRejectsUsageMismatch(t *testing.T) {
	f, job, target, stage, fake, plan := prepareRebuildEmbedding(t)
	fake.mismatch = plan.Chunks[0].SearchText
	if err := stage.Run(f.ctx, job); !errors.Is(err, models.ErrTokenizerMismatch) {
		t.Fatalf("usage mismatch %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$2`, f.tenant, target).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid batch written %d %v", count, err)
	}
}

func TestRebuildEmbeddingCancelsDuringModelCall(t *testing.T) {
	f, job, target, stage, fake, _ := prepareRebuildEmbedding(t)
	fake.entered = make(chan struct{}, 1)
	fake.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- stage.Run(f.ctx, job) }()
	select {
	case <-fake.entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	err := stage.rebuild.Cancel(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, job.Lease.JobID)
	close(fake.release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late model result %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$2`, f.tenant, target).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled batch written %d %v", count, err)
	}
}

func TestRebuildEmbeddingRechecksDocumentBeforeSend(t *testing.T) {
	f, job, _, stage, fake, plan := prepareRebuildEmbedding(t)
	original := stage.count
	changed := false
	stage.count = func(ctx context.Context, input string) (int, error) {
		count, err := original(ctx, input)
		if err == nil && input == plan.Chunks[0].SearchText && !changed {
			changed = true
			_, err = f.admin.Exec(ctx, `UPDATE document SET deleted_at=clock_timestamp() WHERE tenant_id=$1`, f.tenant)
		}
		return count, err
	}
	if err := stage.Run(f.ctx, job); err == nil || !changed || fake.callsFor(plan.Chunks[0].SearchText) != 0 {
		t.Fatalf("removed document sent: %v changed=%t calls=%d", err, changed, fake.callsFor(plan.Chunks[0].SearchText))
	}
}
