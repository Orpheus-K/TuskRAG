package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
)

func TestRebuildVectorsPreserveOldSpaceAndFenceChanges(t *testing.T) {
	f, job, uploaded, _, staged := prepareVectorFixture(t)
	vectors, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	var input []VectorInput
	for i := range staged.Chunks {
		input = append(input, vectorInput(staged, i, testVector(1, 0)))
	}
	if err := vectors.Write(f.ctx, job, staged.Identity, input); err != nil {
		t.Fatal(err)
	}
	// This fixture publishes existing synthetic rows without invoking index DDL.
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET status='ready' WHERE id=$1`, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE id=$2`, uploaded.VersionID, uploaded.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=true WHERE version_id=$1`, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector SET is_current=true WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	var target int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) SELECT tenant_id,provider_id,'replacement',dims,normalized,distance FROM embedding_space WHERE id=$1 RETURNING id`, staged.Identity.EmbeddingSpaceID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	rebuild, err := kb.NewRebuildService(f.pool, f.queue)
	if err != nil {
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
	r, err := rebuild.Create(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, "vectors", kb.RebuildRequest{TargetSpaceID: target, Revision: staged.Identity.ConfigRevision})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := f.queue.ClaimKindsWithRecovery(f.ctx, f.tenant, "rebuild", time.Minute, []jobs.Kind{jobs.Reembed}, rebuild.Expire)
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := rebuild.Target(f.ctx, leased.Lease)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := NewRebuildVectors(f.queue, rebuild)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := stage.Read(f.ctx, leased.Lease, binding, uploaded.DocumentID, 0, 1)
	if err != nil || len(batch.Inputs()) != 1 {
		t.Fatalf("read %+v %v", batch, err)
	}
	values := [][]float32{testVector(3, 4)}
	for range 2 {
		if err := stage.Write(f.ctx, leased.Lease, batch, values); err != nil {
			t.Fatal(err)
		}
	}
	var oldCount, newCount int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE space_id=$2),count(*) FILTER(WHERE space_id=$3) FROM chunk_vector WHERE tenant_id=$1`, f.tenant, staged.Identity.EmbeddingSpaceID, target).Scan(&oldCount, &newCount); err != nil || oldCount != len(staged.Chunks) || newCount != 1 {
		t.Fatalf("vectors %d %d %v", oldCount, newCount, err)
	}
	next, err := stage.Read(f.ctx, leased.Lease, binding, uploaded.DocumentID, batch.LastID(), 1)
	if err != nil || len(next.Inputs()) != 1 || next.LastID() == batch.LastID() {
		t.Fatalf("page %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET search_text=search_text||' changed' WHERE id=$1`, next.LastID()); err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, leased.Lease, next, values); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("stale content %v", err)
	}
	if err := stage.Write(f.ctx, leased.Lease, batch, [][]float32{{0}}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("invalid dimension %v", err)
	}
	if err := rebuild.Cancel(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, r.JobID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, leased.Lease, batch, values); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late write %v", err)
	}
	var oldActive bool
	if err := f.admin.QueryRow(f.ctx, `SELECT embedding_space_id=$2 AND pending_space_id IS NULL AND config_revision=$3 FROM knowledge_base WHERE id=$1`, f.kbID, staged.Identity.EmbeddingSpaceID, staged.Identity.ConfigRevision).Scan(&oldActive); err != nil || !oldActive {
		t.Fatalf("old binding %t %v", oldActive, err)
	}
}
