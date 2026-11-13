package jobs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
)

type fixture struct {
	ctx      context.Context
	admin    *pgx.Conn
	pool     *pgxpool.Pool
	store    *Store
	tenant   int64
	name     string
	sequence int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	appConfig, err := pgx.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(adminConfig.Database, "tuskrag_test_") || adminConfig.Database != appConfig.Database ||
		adminConfig.Host != appConfig.Host || adminConfig.Port != appConfig.Port ||
		(adminConfig.Host != "127.0.0.1" && adminConfig.Host != "localhost" && adminConfig.Host != "::1") {
		t.Fatal("tests require the same loopback tuskrag_test_ database for both roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		if closeErr := admin.Close(context.Background()); closeErr != nil {
			t.Errorf("close admin connection: %v", closeErr)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	store, err := NewStore(pool, RetryPolicy{Initial: time.Hour, Maximum: 4 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, admin: admin, pool: pool, store: store, name: "jobs-test-" + hex.EncodeToString(nonce[:])}
	f.tenant = f.newTenant(t)
	return f
}

func (f *fixture) newTenant(t *testing.T) int64 {
	t.Helper()
	var tenant int64
	f.sequence++
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, fmt.Sprintf("%s-%d", f.name, f.sequence)).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"app_user", "job"} {
			if _, err := f.admin.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id = $1", tenant); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := f.admin.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenant); err != nil {
			t.Errorf("clean tenant: %v", err)
		}
	})
	return tenant
}

func request(key string, attempts int32) Request {
	return Request{Kind: Ingest, DedupeKey: &key, Payload: json.RawMessage(`{"doc":1}`), MaxAttempts: attempts}
}

func TestIngestCancellationRequiresDomainEffects(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("needs-effects", 3))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CancelWithEffects(f.ctx, f.tenant, id, nil); !errors.Is(err, ErrNeedsEffects) {
		t.Fatalf("ingest job cancelled without synchronizing document version: %v", err)
	}
	var state string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id=$1`, id).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("ingest job changed: state=%s err=%v", state, err)
	}
}

func TestConcurrentRecoveryAppliesEffectsOnce(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("concurrent-recovery", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Claim(f.ctx, f.tenant, "expired-owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	start, done := make(chan struct{}), make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := f.store.ClaimWithRecovery(f.ctx, f.tenant, "replacement", time.Minute, func(ctx context.Context, tx EffectsTx, job ExpiredJob) error {
				if job.ID != id {
					return errors.New("wrong exhausted job")
				}
				_, err := tx.Exec(ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'once')`, f.tenant)
				return err
			})
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; !errors.Is(err, ErrNoJob) {
			t.Fatalf("recovery claim: %v", err)
		}
	}
	var state string
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT status,(SELECT count(*) FROM app_user WHERE tenant_id=$2 AND display_name='once') FROM job WHERE id=$1`, id, f.tenant).Scan(&state, &count); err != nil || state != "dead" || count != 1 {
		t.Fatalf("concurrent recovery: state=%s effects=%d err=%v", state, count, err)
	}
}

func TestDeduplicationAndTransactionalEnqueue(t *testing.T) {
	f := setup(t)
	otherTenant := f.newTenant(t)
	req := request("same-key", 3)
	id, err := f.store.Enqueue(f.ctx, f.tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.store.Enqueue(f.ctx, f.tenant, req)
	if err != nil || again != id {
		t.Fatalf("same request should reuse job: id=%d err=%v", again, err)
	}
	req.Payload = json.RawMessage(`{"doc":2}`)
	if _, err := f.store.Enqueue(f.ctx, f.tenant, req); !errors.Is(err, ErrDedupeConflict) {
		t.Fatalf("different payload should conflict: %v", err)
	}
	otherID, err := f.store.Enqueue(f.ctx, otherTenant, request("same-key", 3))
	if err != nil || otherID == id {
		t.Fatalf("deduplication must be tenant-scoped: id=%d err=%v", otherID, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback transaction: %v", err)
		}
	}()
	if _, err := dbquery.New(tx).SetTenant(f.ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.EnqueueTx(f.ctx, tx, otherTenant, request("wrong-tenant", 3)); err == nil {
		t.Fatal("transaction tenant mismatch accepted")
	}
	rolledBackID, err := f.store.EnqueueTx(f.ctx, tx, f.tenant, request("rolled-back", 3))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE id = $1`, rolledBackID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back enqueue persisted: count=%d err=%v", count, err)
	}
}

func TestConcurrentClaimsAndStaleCompletion(t *testing.T) {
	f := setup(t)
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("lease", 3)); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		job ClaimedJob
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for i := 0; i < 2; i++ {
		go func(owner string) {
			<-start
			job, err := f.store.Claim(f.ctx, f.tenant, owner, time.Minute)
			results <- claimResult{job: job, err: err}
		}(fmt.Sprintf("worker-%d", i))
	}
	close(start)
	var first Lease
	claimed, empty := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			claimed++
			first = result.job.Lease
		case errors.Is(result.err, ErrNoJob):
			empty++
		default:
			t.Fatal(result.err)
		}
	}
	if claimed != 1 || empty != 1 {
		t.Fatalf("competing claims: claimed=%d empty=%d", claimed, empty)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, first.JobID); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Claim(f.ctx, f.tenant, "replacement", time.Minute)
	if err != nil || second.Lease.JobID != first.JobID || second.Lease.Generation <= first.Generation || second.Lease.Attempt != 2 {
		t.Fatalf("reclaim: %+v err=%v", second.Lease, err)
	}
	forged := second.Lease
	forged.Attempt++
	if _, err := f.store.Renew(f.ctx, forged, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("incorrect attempt renewed a lease: %v", err)
	}
	called := false
	err = f.store.Complete(f.ctx, first, func(context.Context, EffectsTx) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrLeaseLost) || called {
		t.Fatalf("stale worker could apply effects: called=%t err=%v", called, err)
	}
	if err := f.store.Complete(f.ctx, second.Lease, func(ctx context.Context, tx EffectsTx) error {
		_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'committed')`, f.tenant)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Complete(f.ctx, second.Lease, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("duplicate completion was accepted: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id = $1`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("effects count=%d err=%v", count, err)
	}
}

func TestExpiryDuringEffectsRollsBack(t *testing.T) {
	f := setup(t)
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("expire-in-tx", 2)); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.Complete(f.ctx, job.Lease, func(ctx context.Context, tx EffectsTx) error {
		if _, err := tx.Exec(ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, job.Lease.JobID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'rolled-back')`, f.tenant)
		return err
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease should reject commit: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id = $1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired effects persisted: count=%d err=%v", count, err)
	}
	applyErr := errors.New("effect failed")
	err = f.store.Complete(f.ctx, job.Lease, func(ctx context.Context, tx EffectsTx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'failed')`, f.tenant); err != nil {
			return err
		}
		return applyErr
	})
	if !errors.Is(err, applyErr) {
		t.Fatalf("effect failure was lost: %v", err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id = $1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed effects persisted: count=%d err=%v", count, err)
	}
	if err := f.store.Complete(f.ctx, job.Lease, nil); err != nil {
		t.Fatalf("job did not remain retryable after transaction rollback: %v", err)
	}
}

func TestCheckpointAndFailureEffectsAreFenced(t *testing.T) {
	f := setup(t)
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("checkpoint", 2)); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.Claim(f.ctx, f.tenant, "checkpoint-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(name string) func(context.Context, EffectsTx) error {
		return func(ctx context.Context, tx EffectsTx) error {
			_, err := tx.Exec(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, $2)`, f.tenant, name)
			return err
		}
	}
	if err := f.store.WithLease(f.ctx, job.Lease, insert("checkpoint")); err != nil {
		t.Fatal(err)
	}
	expiringEffect := func(ctx context.Context, tx EffectsTx) error {
		if _, err := tx.Exec(ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, job.Lease.JobID); err != nil {
			return err
		}
		return insert("stale-effect")(ctx, tx)
	}
	if err := f.store.WithLease(f.ctx, job.Lease, expiringEffect); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired checkpoint: %v", err)
	}
	_, err = f.store.FailWithEffects(f.ctx, job.Lease, ModelUnavailable, func(ctx context.Context, tx EffectsTx, status Status) error {
		if status != Queued {
			return fmt.Errorf("unexpected planned state %s", status)
		}
		return expiringEffect(ctx, tx)
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired failure effect: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id=$1 AND display_name='stale-effect'`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale effects persisted: count=%d err=%v", count, err)
	}
	status, err := f.store.FailWithEffects(f.ctx, job.Lease, ModelUnavailable, func(ctx context.Context, tx EffectsTx, status Status) error {
		if status != Queued {
			return fmt.Errorf("unexpected planned state %s", status)
		}
		return insert("retry-effect")(ctx, tx)
	})
	if err != nil || status != Queued {
		t.Fatalf("failure transaction: status=%s err=%v", status, err)
	}
	called := false
	_, err = f.store.FailWithEffects(f.ctx, job.Lease, ParseError, func(context.Context, EffectsTx, Status) error { called = true; return nil })
	if !errors.Is(err, ErrLeaseLost) || called {
		t.Fatalf("stale failure effects ran: called=%t err=%v", called, err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM app_user WHERE tenant_id=$1 AND display_name IN ('checkpoint','retry-effect')`, f.tenant).Scan(&count); err != nil || count != 2 {
		t.Fatalf("committed effects missing: count=%d err=%v", count, err)
	}
}

func TestRetryDeadLetterAndCancellation(t *testing.T) {
	f := setup(t)
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("retry", 2)); err != nil {
		t.Fatal(err)
	}
	first, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.store.Fail(f.ctx, first.Lease, ModelUnavailable)
	if err != nil || status != Queued {
		t.Fatalf("retryable failure: state=%s err=%v", status, err)
	}
	var scheduled bool
	if err := f.admin.QueryRow(f.ctx, `SELECT run_after > clock_timestamp() FROM job WHERE id = $1`, first.Lease.JobID).Scan(&scheduled); err != nil || !scheduled {
		t.Fatalf("retry was not delayed: scheduled=%t err=%v", scheduled, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET run_after = clock_timestamp() - interval '1 second' WHERE id = $1`, first.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil || second.Lease.Attempt != 2 {
		t.Fatalf("second attempt: %+v err=%v", second.Lease, err)
	}
	status, err = f.store.Fail(f.ctx, second.Lease, Timeout)
	if err != nil || status != Dead {
		t.Fatalf("exhausted retries: state=%s err=%v", status, err)
	}
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("cancel", 2)); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := f.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(f.ctx, `LISTEN tuskrag_job_cancelled`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CancelWithEffects(f.ctx, f.tenant, job.Lease.JobID, func(context.Context, EffectsTx, Kind) error { return nil }); err != nil {
		t.Fatal(err)
	}
	noticeCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	notice, err := listener.Conn().WaitForNotification(noticeCtx)
	if err != nil || notice.Payload != strconv.FormatInt(job.Lease.JobID, 10) {
		t.Fatalf("cancel notification: notice=%v err=%v", notice, err)
	}
	if _, err := f.store.Renew(f.ctx, job.Lease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("cancelled lease renewed: %v", err)
	}
	if err := f.store.Complete(f.ctx, job.Lease, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("cancelled lease completed: %v", err)
	}
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("parse-failure", 2)); err != nil {
		t.Fatal(err)
	}
	parseJob, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	status, err = f.store.Fail(f.ctx, parseJob.Lease, ParseError)
	if err != nil || status != Failed {
		t.Fatalf("permanent parse failure: state=%s err=%v", status, err)
	}
	if _, err := f.store.Enqueue(f.ctx, f.tenant, request("expired-final-attempt", 1)); err != nil {
		t.Fatal(err)
	}
	expired, err := f.store.Claim(f.ctx, f.tenant, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, expired.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	recovered := false
	if _, err := f.store.ClaimWithRecovery(f.ctx, f.tenant, "replacement", time.Minute, func(_ context.Context, _ EffectsTx, job ExpiredJob) error {
		recovered = job.ID == expired.Lease.JobID
		return nil
	}); !errors.Is(err, ErrNoJob) || !recovered {
		t.Fatalf("exhausted expired job was claimed: %v", err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE id = $1`, expired.Lease.JobID).Scan(&status); err != nil || status != Dead {
		t.Fatalf("expired final attempt: state=%s err=%v", status, err)
	}
}

func TestKilledClaimantCanBeRecovered(t *testing.T) {
	f := setup(t)
	id, err := f.store.Enqueue(f.ctx, f.tenant, request("crashed-process", 2))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestClaimChild$")
	cmd.Env = append(os.Environ(), "TUSKRAG_TEST_CRASH_CHILD=1", "TUSKRAG_TEST_CRASH_TENANT="+strconv.FormatInt(f.tenant, 10))
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
		t.Fatalf("claimant did not report lease: %v %s", err, stderr.String())
	}
	var old Lease
	if err := json.Unmarshal(line, &old); err != nil || old.JobID != id {
		t.Fatalf("invalid child lease: %s err=%v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("claimant was expected to be killed")
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.store.Claim(f.ctx, f.tenant, "replacement", time.Minute)
	if err != nil || recovered.Lease.JobID != id || recovered.Lease.Generation <= old.Generation {
		t.Fatalf("recover killed claimant: %+v err=%v", recovered.Lease, err)
	}
	if err := f.store.Complete(f.ctx, recovered.Lease, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClaimChild(t *testing.T) {
	if os.Getenv("TUSKRAG_TEST_CRASH_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	tenant, err := strconv.ParseInt(os.Getenv("TUSKRAG_TEST_CRASH_TENANT"), 10, 64)
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
	job, err := store.Claim(ctx, tenant, "crash-child", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(job.Lease); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	t.Fatal("claimant should have been killed by the parent")
}

func TestRollbackClosedConnectionAfterCancellation(t *testing.T) {
	f := setup(t)
	for _, cancelled := range []bool{true, false} {
		tx, _, err := f.store.begin(f.ctx, f.tenant)
		if err != nil {
			t.Fatal(err)
		}
		id, err := f.store.EnqueueTx(f.ctx, tx, f.tenant, request(fmt.Sprintf("closed-%t", cancelled), 3))
		if err != nil {
			t.Fatal(err)
		}
		original := errors.New("unrelated failure")
		if cancelled {
			original = fmt.Errorf("claim: %w", context.Canceled)
		}
		if err := tx.Conn().Close(f.ctx); err != nil {
			t.Fatal(err)
		}
		result := original
		rollback(tx, &result)
		if cancelled && result != original {
			t.Fatalf("redundant rollback failure: %v", result)
		}
		if !cancelled && (result == original || !errors.Is(result, original)) {
			t.Fatalf("rollback failure lost: %v", result)
		}
		// puddle destroys closed connections asynchronously after Release.
		releaseCtx, cancelRelease := context.WithTimeout(f.ctx, time.Second)
		tick := time.NewTicker(time.Millisecond)
		for f.pool.Stat().AcquiredConns() != 0 {
			select {
			case <-tick.C:
			case <-releaseCtx.Done():
				tick.Stop()
				cancelRelease()
				t.Fatal("transaction connection not released")
			}
		}
		tick.Stop()
		cancelRelease()
		var count int
		if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE id=$1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("disconnected transaction committed: count=%d err=%v", count, err)
		}
	}
}
