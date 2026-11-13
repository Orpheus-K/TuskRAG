package ingest

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
)

func TestRebuildWorkerLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "index_exhausted", "usage_failed", "revoked", "lease_exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "success" {
				publicationDatabase(t)
			}
			f, job, target, stage, fake, plan := prepareRebuildEmbedding(t)
			wantStatus, wantCode, wantAttempts := "failed", "tokenizer_mismatch", 1
			switch scenario {
			case "success":
				if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, target); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantCode = "succeeded", ""
			case "index_exhausted":
				wantStatus, wantCode, wantAttempts = "dead", "index_unavailable", 3
			case "usage_failed":
				fake.mismatch = plan.Chunks[0].SearchText
			case "revoked":
				if _, err := f.admin.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND user_id=$2`, f.tenant, f.user); err != nil {
					t.Fatal(err)
				}
				wantCode = "permission_revoked"
			case "lease_exhausted":
				wantStatus, wantCode, wantAttempts = "dead", "lease_expired", 3
			}
			if scenario == "lease_exhausted" {
				if _, err := f.admin.Exec(f.ctx, `UPDATE job SET attempts=max_attempts,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.Lease.JobID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.admin.Exec(f.ctx, `UPDATE job SET status='queued',attempts=0,lease_owner=NULL,lease_until=NULL WHERE id=$1`, job.Lease.JobID); err != nil {
					t.Fatal(err)
				}
			}
			worker, err := jobs.NewWorker(f.queue, stage, jobs.WorkerConfig{TenantID: f.tenant, Kinds: []jobs.Kind{jobs.Reembed}, Owner: "rebuild-worker", LeaseTTL: time.Minute, Poll: 10 * time.Millisecond, OnExpired: stage.Expire})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			t.Cleanup(func() {
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
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var status, code string
				var attempts int
				var pendingClear, newActive bool
				if err := f.admin.QueryRow(f.ctx, `SELECT j.status,COALESCE(j.last_error,''),j.attempts,k.pending_space_id IS NULL,k.embedding_space_id=$1 FROM job j JOIN knowledge_base k ON k.id=$2 WHERE j.id=$3`, target, f.kbID, job.Lease.JobID).Scan(&status, &code, &attempts, &pendingClear, &newActive); err != nil {
					t.Fatal(err)
				}
				if status == "succeeded" || status == "failed" || status == "dead" {
					if status != wantStatus || code != wantCode || attempts != wantAttempts || !pendingClear || newActive != (scenario == "success") {
						t.Fatalf("terminal %s %s %d pendingClear=%t newActive=%t", status, code, attempts, pendingClear, newActive)
					}
					if (scenario == "revoked" || scenario == "lease_exhausted") && fake.totalCalls() != 0 {
						t.Fatal("unauthorized or exhausted work called model")
					}
					return
				}
				select {
				case <-tick.C:
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				}
			}
		})
	}
}
