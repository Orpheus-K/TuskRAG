package kb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/retrieval"
)

type evalSearchFunc func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)

func (f evalSearchFunc) Search(ctx context.Context, actor retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
	return f(ctx, actor, request)
}

type evalWorkerFixture struct {
	f       *fixture
	store   *eval.RunStore
	queue   *jobs.Store
	run     eval.StoredRun
	request eval.RunRequest
	result  retrieval.Result
}

func workerEvalFixture(t *testing.T) evalWorkerFixture {
	t.Helper()
	f, _, request := runFixture(t)
	queue, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Millisecond, Maximum: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store, err := eval.NewRunStore(f.pool, queue)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(f.ctx, request.Config.Actor, request.Config.KnowledgeBaseID, "worker", request)
	if err != nil {
		t.Fatal(err)
	}
	var candidate retrieval.Candidate
	if err := f.admin.QueryRow(f.ctx, `SELECT id,document_id,section_id FROM chunk WHERE tenant_id=$1 ORDER BY id LIMIT 1`, f.tenantA).Scan(&candidate.ChunkID, &candidate.DocumentID, &candidate.SectionID); err != nil {
		t.Fatal(err)
	}
	start, end := int32(3), int32(5)
	candidate.PageStart, candidate.PageEnd, candidate.LexicalRank, candidate.SemanticRank = &start, &end, 1, 1
	result := retrieval.Result{Candidates: []retrieval.Candidate{candidate}, Trace: &retrieval.Trace{Candidates: []retrieval.Candidate{candidate}}}
	return evalWorkerFixture{f: f, store: store, queue: queue, run: run, request: request, result: result}
}

func (w evalWorkerFixture) prepare(t *testing.T) (jobs.ClaimedJob, func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	handler, err := eval.NewRunHandler(w.store, evalSearchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
		return w.result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := w.queue.ClaimKindsWithRecovery(w.f.ctx, w.f.tenantA, "test-worker", time.Minute, []jobs.Kind{jobs.EvalRun}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := handler.Prepare(w.f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	return job, apply
}

func TestEvalWorkerCompletionFenceAndResultAccess(t *testing.T) {
	w := workerEvalFixture(t)
	job, apply := w.prepare(t)
	if err := w.queue.WithLease(w.f.ctx, job.Lease, apply); !errors.Is(err, jobs.ErrCompletionRequired) {
		t.Fatalf("checkpoint wrote result: %v", err)
	}
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); err != nil {
		t.Fatal(err)
	}
	stored, err := w.store.GetResult(w.f.ctx, w.request.Config.Actor, w.request.Config.KnowledgeBaseID, w.run.ID)
	if err != nil || stored.Source != "executed" || stored.Report.Summary.Cases != 2 || stored.Report.Summary.Metrics.RecallAt20 != 1 {
		t.Fatalf("result: %+v %v", stored, err)
	}
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("duplicate completion: %v", err)
	}
	var status string
	if err := w.f.admin.QueryRow(w.f.ctx, `SELECT status FROM job WHERE id=$1`, job.Lease.JobID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("job %s %v", status, err)
	}
	for _, statement := range []string{"UPDATE eval_run_result SET digest=repeat('a',64)", "DELETE FROM eval_run_result"} {
		tx, err := w.f.pool.Begin(w.f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(w.f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(w.f.tenantA)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(w.f.ctx, statement); err == nil {
			t.Fatal("app can mutate result")
		}
		if err := tx.Rollback(w.f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := w.f.pool.Begin(w.f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(w.f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(w.f.tenantB)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(w.f.ctx, `SELECT count(*) FROM eval_run_result`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("result RLS leak %d %v", count, err)
	}
	if err := tx.Rollback(w.f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1`, w.f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.GetResult(w.f.ctx, w.request.Config.Actor, w.request.Config.KnowledgeBaseID, w.run.ID); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("revoked result readable: %v", err)
	}
}

func TestEvalWorkerRejectsLateAndRevokedResults(t *testing.T) {
	for _, scenario := range []string{"cancel", "reclaim", "membership", "configuration", "returned evidence"} {
		t.Run(scenario, func(t *testing.T) {
			w := workerEvalFixture(t)
			job, apply := w.prepare(t)
			switch scenario {
			case "cancel":
				if err := w.queue.CancelWithEffects(w.f.ctx, w.f.tenantA, job.Lease.JobID, func(context.Context, jobs.EffectsTx, jobs.Kind) error { return nil }); err != nil {
					t.Fatal(err)
				}
			case "reclaim":
				if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE job SET lease_until=now()-interval '1 second' WHERE id=$1`, job.Lease.JobID); err != nil {
					t.Fatal(err)
				}
				claimed, err := w.queue.ClaimKindsWithRecovery(w.f.ctx, w.f.tenantA, "replacement", time.Minute, []jobs.Kind{jobs.EvalRun}, nil)
				if err != nil || claimed.Lease.Generation <= job.Lease.Generation {
					t.Fatalf("reclaim: %+v %v", claimed, err)
				}
			case "membership":
				if _, err := w.f.admin.Exec(w.f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND user_id=$2`, w.f.tenantA, w.f.owner); err != nil {
					t.Fatal(err)
				}
			case "configuration":
				if _, err := w.f.service.PutConfig(w.f.ctx, w.f.actor(w.f.owner), w.request.Config.KnowledgeBaseID, 0, RetrievalConfig{TopK: 5}); err != nil {
					t.Fatal(err)
				}
			case "returned evidence":
				// Keep a live witness for the expected target while invalidating only
				// the chunk actually returned by the evaluator.
				if _, err := w.f.admin.Exec(w.f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end) SELECT tenant_id,kb_id,document_id,version_id,section_id,2,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at,page_start,page_end FROM chunk WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
					t.Fatal(err)
				}
				if _, err := w.f.admin.Exec(w.f.ctx, `UPDATE chunk SET is_current=false,disabled=true WHERE id=$1`, w.result.Candidates[0].ChunkID); err != nil {
					t.Fatal(err)
				}
			}
			err := w.queue.Complete(w.f.ctx, job.Lease, apply)
			if err == nil {
				t.Fatal("late/revoked completion accepted")
			}
			if (scenario == "cancel" || scenario == "reclaim") && !errors.Is(err, jobs.ErrLeaseLost) {
				t.Fatalf("not fenced: %v", err)
			}
			if scenario != "cancel" && scenario != "reclaim" {
				var failure *jobs.ProcessingFailure
				if !errors.As(err, &failure) {
					t.Fatalf("unclassified failure: %v", err)
				}
				status, err := w.queue.Fail(w.f.ctx, job.Lease, failure.Code)
				if err != nil || status != jobs.Failed {
					t.Fatalf("domain rejection not terminal: %s %v", status, err)
				}
			}
			var count int
			if err := w.f.admin.QueryRow(w.f.ctx, `SELECT count(*) FROM eval_run_result WHERE tenant_id=$1`, w.f.tenantA).Scan(&count); err != nil || count != 0 {
				t.Fatalf("result survived rejection %d %v", count, err)
			}
		})
	}
}

func TestEvalWorkerAuditRollback(t *testing.T) {
	w := workerEvalFixture(t)
	job, apply := w.prepare(t)
	name := fmt.Sprintf("result_audit_failure_%d", w.f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='eval.run.complete' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, w.f.tenantA, name, name)
	if _, err := w.f.admin.Exec(w.f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := w.f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if err := w.queue.Complete(w.f.ctx, job.Lease, apply); err == nil {
		t.Fatal("audit failure accepted")
	}
	var count int
	var status string
	if err := w.f.admin.QueryRow(w.f.ctx, `SELECT count(*) FROM eval_run_result WHERE tenant_id=$1`, w.f.tenantA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("result survived rollback %d %v", count, err)
	}
	if err := w.f.admin.QueryRow(w.f.ctx, `SELECT status FROM job WHERE id=$1`, job.Lease.JobID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("job completed despite rollback %s %v", status, err)
	}
}

func TestEvalWorkerRunsAndCancels(t *testing.T) {
	for _, cancelJob := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelJob), func(t *testing.T) {
			w := workerEvalFixture(t)
			started := make(chan struct{}, 1)
			stopped := make(chan struct{}, 1)
			handler, err := eval.NewRunHandler(w.store, evalSearchFunc(func(ctx context.Context, _ retrieval.Actor, _ retrieval.Request) (retrieval.Result, error) {
				if cancelJob {
					started <- struct{}{}
					<-ctx.Done()
					stopped <- struct{}{}
					return retrieval.Result{}, ctx.Err()
				}
				return w.result, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			worker, err := jobs.NewWorker(w.queue, handler, jobs.WorkerConfig{TenantID: w.f.tenantA, Kinds: []jobs.Kind{jobs.EvalRun}, Owner: "actual-worker", LeaseTTL: time.Second, Poll: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(w.f.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			if cancelJob {
				select {
				case <-started:
				case <-w.f.ctx.Done():
					t.Fatal(w.f.ctx.Err())
				}
				if err := w.queue.CancelWithEffects(w.f.ctx, w.f.tenantA, w.run.JobID, func(context.Context, jobs.EffectsTx, jobs.Kind) error { return nil }); err != nil {
					t.Fatal(err)
				}
				select {
				case <-stopped:
				case <-w.f.ctx.Done():
					t.Fatal("cancel notification did not stop evaluation")
				}
			} else {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					var status string
					if err := w.f.admin.QueryRow(w.f.ctx, `SELECT status FROM job WHERE id=$1`, w.run.JobID).Scan(&status); err != nil {
						t.Fatal(err)
					}
					if status == "succeeded" {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("worker stopped: %v", err)
					case <-w.f.ctx.Done():
						t.Fatal(w.f.ctx.Err())
					case <-ticker.C:
					}
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("worker exit: %v", err)
			}
			_, err = w.store.GetResult(w.f.ctx, w.request.Config.Actor, w.request.Config.KnowledgeBaseID, w.run.ID)
			if cancelJob && !errors.Is(err, eval.ErrRunResultUnavailable) || !cancelJob && err != nil {
				t.Fatalf("result after worker: %v", err)
			}
		})
	}
}
