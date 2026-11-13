package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
)

// The corpus crosses ListRebuildDocuments' 64-document page boundary.
// Distinct fixed vectors isolate rebuild correctness from model quality and
// avoid equidistant Top-50 truncation when the corpus has more than 50 chunks.
func TestRebuildMultiDocumentWorker(t *testing.T) {
	for _, scenario := range []string{"success", "failed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			publicationDatabase(t)
			f, job, target, stage, fake, plan := prepareRebuildEmbedding(t)
			// This exercises 65 before/after queries, not a latency budget.
			corpusCtx, corpusCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer corpusCancel()
			f.ctx = corpusCtx
			allowed := map[int64]bool{}
			queries := []string{"申请"}
			fake.vectors = map[string][]float32{}
			for i, chunk := range plan.Chunks {
				fake.vectors[chunk.SearchText] = testVector(1, float32(i+1)/50)
			}
			for i := range 64 {
				marker := fmt.Sprintf("rebuildmarker%03d", i)
				content := marker + " 申请需要提交登记材料。"
				document := insertEvaluationDocument(t, f, plan.EmbeddingSpaceID, eval.Document{Key: marker, Title: marker, Content: content}, false, testVector(1, 0))
				allowed[document] = true
				queries = append(queries, marker)
				fake.documents[content] = 0
				fake.vectors[content] = testVector(1, float32(i+3)/50)
				if i == 63 && scenario == "failed" {
					fake.mismatch = content
				}
			}
			for content, vector := range fake.vectors {
				encoded, err := encodeHalfVector(vector, true)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector v SET embedding=$1::halfvec FROM chunk c WHERE v.chunk_id=c.id AND v.tenant_id=$2 AND v.space_id=$3 AND c.search_text=$4`, encoded, f.tenant, plan.EmbeddingSpaceID, content); err != nil {
					t.Fatal(err)
				}
			}
			var firstDocument int64
			if err := f.admin.QueryRow(f.ctx, `SELECT min(id) FROM document WHERE tenant_id=$1`, f.tenant).Scan(&firstDocument); err != nil {
				t.Fatal(err)
			}
			allowed[firstDocument] = true
			foreignActor, foreignKB, foreignDocument := setupForeignSearchFixture(t, f)
			for _, space := range []int64{plan.EmbeddingSpaceID, target} {
				if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
					t.Fatal(err)
				}
			}
			driver, err := retrieval.NewDriver(f.pool)
			if err != nil {
				t.Fatal(err)
			}
			actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
			binding, err := driver.Binding(f.ctx, actor, f.kbID)
			if err != nil {
				t.Fatal(err)
			}
			before := make([][]retrieval.Candidate, len(queries))
			for i, query := range queries {
				before[i], err = driver.Recall(f.ctx, actor, f.kbID, binding, query, testVector(1, 0), retrieval.Filters{}, 8)
				if err != nil || len(before[i]) == 0 {
					t.Fatalf("baseline query %d: %v", i, err)
				}
				for _, c := range before[i] {
					if !allowed[c.DocumentID] {
						t.Fatalf("unauthorized baseline evidence %d", c.DocumentID)
					}
				}
			}
			fake.entered, fake.release = make(chan struct{}, 1), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(fake.release) })
			if _, err := f.admin.Exec(f.ctx, `UPDATE job SET status='queued',attempts=0,lease_owner=NULL,lease_until=NULL WHERE id=$1`, job.Lease.JobID); err != nil {
				t.Fatal(err)
			}
			worker, err := jobs.NewWorker(f.queue, stage, jobs.WorkerConfig{TenantID: f.tenant, Kinds: []jobs.Kind{jobs.Reembed}, Owner: "multidocument", LeaseTTL: time.Minute, Poll: 10 * time.Millisecond, OnExpired: stage.Expire})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			joined := false
			t.Cleanup(func() {
				if joined {
					return
				}
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("worker did not stop")
				}
			})
			select {
			case <-fake.entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			duringBinding, err := driver.Binding(f.ctx, actor, f.kbID)
			if err != nil || duringBinding != binding {
				t.Fatalf("early switch %+v %v", duringBinding, err)
			}
			during, err := driver.Recall(f.ctx, actor, f.kbID, binding, queries[0], testVector(1, 0), retrieval.Filters{}, 8)
			if err != nil || !reflect.DeepEqual(before[0], during) {
				t.Fatalf("in-progress recall changed: %v", err)
			}
			if scenario == "cancelled" {
				if err := stage.rebuild.Cancel(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, job.Lease.JobID); err != nil {
					t.Fatal(err)
				}
			}
			release.Do(func() { close(fake.release) })
			want := map[string]string{"success": "succeeded", "failed": "failed", "cancelled": "cancelled"}[scenario]
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var status string
				if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id=$1`, job.Lease.JobID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status == "succeeded" || status == "failed" || status == "dead" || status == "cancelled" {
					if status != want {
						t.Fatalf("terminal=%s want=%s", status, want)
					}
					break
				}
				select {
				case <-tick.C:
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				}
			}
			// Join the worker before inspecting late writes, including cancellation.
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			joined = true
			var pendingClear bool
			var vectors, originals int
			if err := f.admin.QueryRow(f.ctx, `SELECT pending_space_id IS NULL,(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$2),(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$3) FROM knowledge_base WHERE id=$4`, f.tenant, target, plan.EmbeddingSpaceID, f.kbID).Scan(&pendingClear, &vectors, &originals); err != nil {
				t.Fatal(err)
			}
			expectedVectors := map[string]int{"success": 66, "failed": 65, "cancelled": 0}[scenario]
			if !pendingClear || vectors != expectedVectors || originals != 66 {
				t.Fatalf("pendingClear=%t target=%d old=%d", pendingClear, vectors, originals)
			}
			afterBinding, err := driver.Binding(f.ctx, actor, f.kbID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				if afterBinding.SpaceID != target || afterBinding.Revision != binding.Revision+1 {
					t.Fatalf("switch %+v", afterBinding)
				}
			} else if afterBinding != binding {
				t.Fatalf("non-success switched %+v", afterBinding)
			}
			for i, query := range queries {
				after, err := driver.Recall(f.ctx, actor, f.kbID, afterBinding, query, testVector(1, 0), retrieval.Filters{}, 8)
				if err != nil || !reflect.DeepEqual(before[i], after) {
					t.Fatalf("query %d changed: before=%+v after=%+v err=%v", i, before[i], after, err)
				}
				for _, c := range after {
					if !allowed[c.DocumentID] || c.DocumentID == foreignDocument {
						t.Fatalf("unauthorized evidence %d", c.DocumentID)
					}
				}
			}
			if _, err := driver.Binding(f.ctx, foreignActor, f.kbID); err == nil {
				t.Fatal("foreign actor accessed rebuilt KB")
			}
			if _, err := driver.Binding(f.ctx, actor, foreignKB); err == nil {
				t.Fatal("owner accessed foreign KB")
			}
			if scenario == "success" {
				if _, err := driver.Recall(f.ctx, actor, f.kbID, binding, queries[0], testVector(1, 0), retrieval.Filters{}, 8); !errors.Is(err, retrieval.ErrBindingChanged) {
					t.Fatalf("stale binding %v", err)
				}
			}
			t.Logf("documents=65 chunks=66 queries=%d target_vectors=%d terminal=%s exact_recall_equal=true unauthorized=0", len(queries), vectors, want)
		})
	}
}
