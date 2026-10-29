package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type handlerFunc func(context.Context, ClaimedJob) (func(context.Context, EffectsTx) error, error)

func (f handlerFunc) Prepare(ctx context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
	return f(ctx, job)
}

type runningWorker struct {
	done          chan struct{}
	err           error
	expectFailure bool
}

func startWorker(t *testing.T, f *fixture, handler Handler, ttl time.Duration, onExpired ...ExpiredEffects) *runningWorker {
	t.Helper()
	config := WorkerConfig{TenantID: f.tenant, Owner: "worker-test", LeaseTTL: ttl, Poll: time.Minute}
	if len(onExpired) != 0 {
		config.OnExpired = onExpired[0]
	}
	worker, err := NewWorker(f.store, handler, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	running := &runningWorker{done: make(chan struct{})}
	go func() { running.err = worker.Run(ctx); close(running.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-running.done:
			if running.err != nil && !errors.Is(running.err, context.Canceled) && !running.expectFailure {
				t.Errorf("worker stopped: %v", running.err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return running
}

func TestWorkerRecoversExhaustedLease(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-exhausted", 1))
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.store.Claim(f.ctx, f.tenant, "previous-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	handler := handlerFunc(func(context.Context, ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		return nil, errors.New("exhausted job should not be claimed")
	})
	worker := startWorker(t, f, handler, 3*time.Second, func(ctx context.Context, tx EffectsTx, expired ExpiredJob) error {
		if expired.ID != id || expired.TenantID != f.tenant || expired.Kind != Ingest {
			return errors.New("incorrect expired job")
		}
		_, err := tx.Exec(ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'expired-effect')`, f.tenant)
		return err
	})
	waitJob(t, f, worker, id, "dead")
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id=$1 AND display_name='expired-effect'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovery effect count=%d err=%v", count, err)
	}
	if err := f.store.Complete(f.ctx, job.Lease, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired worker completed: %v", err)
	}
}

func TestWorkerStopsInFlightJobWhenListenerDisconnects(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-listener-loss", 2))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ClaimedJob, 1)
	cancelled := make(chan struct{})
	handler := handlerFunc(func(ctx context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		started <- job
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	worker := startWorker(t, f, handler, 30*time.Second)
	if job := waitStarted(t, worker, started); job.Lease.JobID != id {
		t.Fatal("wrong job claimed")
	}
	var pid int
	if err := f.admin.QueryRow(f.ctx, `SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1`, fmt.Sprintf("tuskrag-worker-%d", f.tenant)).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := f.admin.QueryRow(f.ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate isolated test listener: terminated=%t err=%v", terminated, err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler continued after listener loss")
	}
	select {
	case <-worker.done:
		worker.expectFailure = true
		if worker.err == nil || errors.Is(worker.err, context.Canceled) {
			t.Fatalf("listener failure was hidden: %v", worker.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after listener loss")
	}
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id = $1`, id).Scan(&status); err != nil || status != "running" {
		t.Fatalf("interrupted job status=%q err=%v", status, err)
	}
}

func waitJob(t *testing.T, f *fixture, worker *runningWorker, id int64, status string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var actual string
		if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id = $1`, id).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual == status {
			return
		}
		select {
		case <-worker.done:
			t.Fatalf("worker stopped before job reached %s: %v", status, worker.err)
		case <-deadline.C:
			t.Fatalf("job status: wanted %s, got %s", status, actual)
		case <-ticker.C:
		}
	}
}

func waitStarted(t *testing.T, worker *runningWorker, started <-chan ClaimedJob) ClaimedJob {
	t.Helper()
	select {
	case job := <-started:
		return job
	case <-worker.done:
		t.Fatalf("worker stopped before starting: %v", worker.err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start the job")
	}
	return ClaimedJob{}
}

func TestWorkerCompletesEffectsAndRecordsFailure(t *testing.T) {
	f := setup(t)
	first, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-success", 2))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ClaimedJob, 2)
	handler := handlerFunc(func(_ context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		started <- job
		if job.Lease.JobID != first {
			return nil, &ProcessingFailure{Code: ModelUnavailable, Cause: errors.New("sensitive input")}
		}
		return func(ctx context.Context, tx EffectsTx) error {
			_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'worker-effect')`, f.tenant)
			return err
		}, nil
	})
	worker := startWorker(t, f, handler, 3*time.Second)
	if job := waitStarted(t, worker, started); job.Lease.JobID != first {
		t.Fatalf("wrong job: %+v", job.Lease)
	}
	waitJob(t, f, worker, first, "succeeded")
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id = $1 AND display_name = 'worker-effect'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("worker effects count=%d err=%v", count, err)
	}
	second, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-retry", 2))
	if err != nil {
		t.Fatal(err)
	}
	if job := waitStarted(t, worker, started); job.Lease.JobID != second {
		t.Fatalf("wrong retry job: %+v", job.Lease)
	}
	waitJob(t, f, worker, second, "queued")
	var failure string
	var delayed bool
	if err := f.admin.QueryRow(f.ctx, `SELECT last_error, run_after > clock_timestamp() FROM job WHERE id = $1`, second).Scan(&failure, &delayed); err != nil || failure != string(ModelUnavailable) || !delayed {
		t.Fatalf("failure code=%q delayed=%t err=%v", failure, delayed, err)
	}
}

func TestWorkerRecordsFailureFromCompletionEffects(t *testing.T) {
	f := setup(t)
	first, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-completion-failure", 2))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-after-failure", 2))
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerFunc(func(_ context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		if job.Lease.JobID != first {
			return func(ctx context.Context, tx EffectsTx) error {
				_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'after-completion-failure')`, f.tenant)
				return err
			}, nil
		}
		return func(ctx context.Context, tx EffectsTx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'rolled-back-completion')`, f.tenant); err != nil {
				return err
			}
			return &ProcessingFailure{Code: VersionSuperseded, Cause: errors.New("late version"), Apply: func(ctx context.Context, tx EffectsTx, status Status) error {
				if err := f.store.CheckFailure(tx, job.Lease, status); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'committed-failure')`, f.tenant)
				return err
			}}
		}, nil
	})
	worker := startWorker(t, f, handler, 3*time.Second)
	waitJob(t, f, worker, first, "failed")
	waitJob(t, f, worker, second, "succeeded")
	var rolledBack, failed, continued int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		count(*) FILTER (WHERE display_name='rolled-back-completion'),
		count(*) FILTER (WHERE display_name='committed-failure'),
		count(*) FILTER (WHERE display_name='after-completion-failure')
		FROM app_user WHERE tenant_id=$1`, f.tenant).Scan(&rolledBack, &failed, &continued); err != nil || rolledBack != 0 || failed != 1 || continued != 1 {
		t.Fatalf("completion failure effects rolledBack=%d failed=%d continued=%d err=%v", rolledBack, failed, continued, err)
	}
}

func TestWorkerSchedulesTenantsAndLeavesUnsupportedJobs(t *testing.T) {
	f := setup(t)
	other := f.newTenant(t)
	unsupported, err := f.store.Enqueue(f.ctx, f.tenant, Request{Kind: Reembed, Payload: json.RawMessage(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := f.store.Enqueue(f.ctx, f.tenant, Request{Kind: EvalRun, Payload: json.RawMessage(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET status='running', attempts=1, lease_generation=1,
		lease_owner='old-worker', lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, expired); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 4)
	for _, tenant := range []int64{f.tenant, f.tenant, other, other} {
		id, err := f.store.Enqueue(f.ctx, tenant, Request{Kind: Ingest, Payload: json.RawMessage(`{}`), MaxAttempts: 2})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	order := make(chan int64, len(ids))
	handler := handlerFunc(func(_ context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		order <- job.Lease.TenantID
		return nil, nil
	})
	worker, err := NewWorker(f.store, handler, WorkerConfig{TenantIDs: []int64{f.tenant, other}, Kinds: []Kind{Ingest},
		Owner: "multi-test", LeaseTTL: 3 * time.Second, Poll: 25 * time.Millisecond})
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
				t.Errorf("multi-tenant worker stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("multi-tenant worker did not stop")
		}
	})
	want := []int64{f.tenant, other, f.tenant, other}
	for index, expected := range want {
		select {
		case tenant := <-order:
			if tenant != expected {
				t.Fatalf("tenant scheduling at %d: got=%d want=%d", index, tenant, expected)
			}
		case err := <-done:
			t.Fatalf("multi-tenant worker stopped: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("multi-tenant worker did not process queued jobs")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var completed int
		if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE id=ANY($1) AND status='succeeded'`, ids).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("multi-tenant jobs completed=%d", completed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var queuedStatus, expiredStatus string
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT status FROM job WHERE id=$1), (SELECT status FROM job WHERE id=$2)`, unsupported, expired).Scan(&queuedStatus, &expiredStatus); err != nil || queuedStatus != "queued" || expiredStatus != "running" {
		t.Fatalf("unsupported jobs changed: queued=%s expired=%s err=%v", queuedStatus, expiredStatus, err)
	}
}

func TestWorkerVerifiesCancellationNotifications(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-cancel", 2))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ClaimedJob, 1)
	cancelled := make(chan struct{})
	handler := handlerFunc(func(ctx context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		started <- job
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	worker := startWorker(t, f, handler, 30*time.Second)
	job := waitStarted(t, worker, started)
	if job.Lease.JobID != id {
		t.Fatal("wrong job claimed")
	}
	if _, err := f.admin.Exec(f.ctx, `SELECT pg_notify('tuskrag_job_cancelled', $1)`, fmt.Sprint(id)); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var renewed bool
		if err := f.admin.QueryRow(f.ctx, `SELECT lease_until > $2 FROM job WHERE id = $1`, id, job.Lease.ExpiresAt).Scan(&renewed); err != nil {
			t.Fatal(err)
		}
		if renewed {
			break
		}
		select {
		case <-cancelled:
			t.Fatal("forged notification cancelled a valid job")
		case <-worker.done:
			t.Fatalf("worker stopped: %v", worker.err)
		case <-deadline.C:
			t.Fatal("worker did not verify notification")
		case <-ticker.C:
		}
	}
	if err := f.store.CancelWithEffects(f.ctx, f.tenant, id, func(context.Context, EffectsTx, Kind) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-worker.done:
		t.Fatalf("worker stopped before cancellation: %v", worker.err)
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach handler")
	}
	waitJob(t, f, worker, id, "cancelled")
}

func TestWorkerRenewsAndStopsWhenLeaseIsReclaimed(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-reclaim", 3))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ClaimedJob, 1)
	cancelled := make(chan struct{})
	handler := handlerFunc(func(ctx context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		started <- job
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	worker := startWorker(t, f, handler, 900*time.Millisecond)
	job := waitStarted(t, worker, started)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var renewed bool
		if err := f.admin.QueryRow(f.ctx, `SELECT lease_until > $2 FROM job WHERE id = $1`, id, job.Lease.ExpiresAt).Scan(&renewed); err != nil {
			t.Fatal(err)
		}
		if renewed {
			break
		}
		select {
		case <-worker.done:
			t.Fatalf("worker stopped before renewal: %v", worker.err)
		case <-deadline.C:
			t.Fatal("worker did not renew lease")
		case <-ticker.C:
		}
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.store.Claim(f.ctx, f.tenant, "replacement", time.Minute)
	if err != nil || replacement.Lease.JobID != id || replacement.Lease.Generation <= job.Lease.Generation {
		t.Fatalf("replacement claim: %+v err=%v", replacement.Lease, err)
	}
	select {
	case <-cancelled:
	case <-worker.done:
		t.Fatalf("worker stopped before stale handler cancelled: %v", worker.err)
	case <-time.After(3 * time.Second):
		t.Fatal("stale handler continued after lease loss")
	}
	if err := f.store.Complete(f.ctx, replacement.Lease, nil); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f, worker, id, "succeeded")
}

func TestWorkerProcessCrashAndRecovery(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("worker-crash", 2))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestWorkerProcessChild$")
	cmd.Env = append(os.Environ(), "TUSKRAG_TEST_WORKER_CHILD=1", "TUSKRAG_TEST_WORKER_TENANT="+strconv.FormatInt(f.tenant, 10))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("worker child did not claim job: %v %s", err, stderr.String())
	}
	var old Lease
	if err := json.Unmarshal(line, &old); err != nil || old.JobID != id {
		t.Fatalf("invalid child lease: %s err=%v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("worker child was expected to be killed")
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	started := make(chan ClaimedJob, 1)
	handler := handlerFunc(func(_ context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		started <- job
		return func(ctx context.Context, tx EffectsTx) error {
			_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'recovered-effect')`, f.tenant)
			return err
		}, nil
	})
	worker := startWorker(t, f, handler, 3*time.Second)
	recovered := waitStarted(t, worker, started)
	if recovered.Lease.JobID != id || recovered.Lease.Generation <= old.Generation || recovered.Lease.Attempt != 2 {
		t.Fatalf("invalid recovered lease: %+v", recovered.Lease)
	}
	waitJob(t, f, worker, id, "succeeded")
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id = $1 AND display_name = 'recovered-effect'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovered effects count=%d err=%v", count, err)
	}
}

func TestWorkerProcessChild(t *testing.T) {
	if os.Getenv("TUSKRAG_TEST_WORKER_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	tenant, err := strconv.ParseInt(os.Getenv("TUSKRAG_TEST_WORKER_TENANT"), 10, 64)
	if err != nil || tenant <= 0 {
		t.Fatal("invalid test tenant")
	}
	config, err := pgxpool.ParseConfig(os.Getenv("TUSKRAG_TEST_APP_URL"))
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "tuskrag_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("child requires a loopback tuskrag_test_ database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewStore(pool, RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerFunc(func(ctx context.Context, job ClaimedJob) (func(context.Context, EffectsTx) error, error) {
		if err := json.NewEncoder(os.Stdout).Encode(job.Lease); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	worker, err := NewWorker(store, handler, WorkerConfig{TenantID: tenant, Owner: "worker-child", LeaseTTL: 5 * time.Second, Poll: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Fatal("worker child should have been killed")
}
