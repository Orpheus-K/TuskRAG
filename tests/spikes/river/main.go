// Command river reproduces a stale completion in an isolated River queue.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

type args struct{}

func (args) Kind() string { return "stale_attempt_spike" }

type outcome struct {
	committed bool
	err       error
}

type worker struct {
	river.WorkerDefaults[args]
	pool       *pgxpool.Pool
	schema     string
	started    chan<- int64
	release    <-chan struct{}
	completion chan<- outcome
	old        bool
}

func (w *worker) Work(ctx context.Context, job *river.Job[args]) error {
	select {
	case w.started <- job.ID:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !w.old {
		return nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		w.completion <- outcome{err: err}
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO "+w.schema+".effects (job_id) VALUES ($1)", job.ID); err == nil {
		_, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	w.completion <- outcome{committed: err == nil, err: err}
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	config, err := pgxpool.ParseConfig(os.Getenv("TUSKRAG_SPIKE_DATABASE_URL"))
	if err != nil {
		return errors.New("invalid isolated test database URL")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "tuskrag_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		return errors.New("S3 requires a loopback tuskrag_test_ database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("open test database: %w", err)
	}
	defer pool.Close()
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	schemaName := "s3_river_" + hex.EncodeToString(nonce[:])
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		return fmt.Errorf("create spike schema: %w", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove spike schema: %w", err))
		}
	}()
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: schemaName})
	if err != nil {
		return fmt.Errorf("configure River migrations: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate River schema: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".effects (job_id bigint PRIMARY KEY)"); err != nil {
		return fmt.Errorf("create effect table: %w", err)
	}

	oldStarted, newStarted := make(chan int64, 1), make(chan int64, 1)
	releaseOld, releaseNew := make(chan struct{}), make(chan struct{})
	completed := make(chan outcome, 1)
	newClient := func(id string, w *worker) (*river.Client[pgx.Tx], error) {
		workers := river.NewWorkers()
		river.AddWorker(workers, w)
		return river.NewClient(driver, &river.Config{
			ID: id, Schema: schemaName, Workers: workers,
			Queues:        map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
			FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 20 * time.Millisecond,
		})
	}
	oldClient, err := newClient("s3-old", &worker{pool: pool, schema: schema, started: oldStarted, release: releaseOld, completion: completed, old: true})
	if err != nil {
		return fmt.Errorf("configure old worker: %w", err)
	}
	if err := oldClient.Start(ctx); err != nil {
		return fmt.Errorf("start old worker: %w", err)
	}
	defer stopClient(oldClient, &resultErr)
	inserted, err := oldClient.Insert(ctx, args{}, nil)
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}
	oldID, err := waitID(ctx, oldStarted)
	if err != nil {
		return fmt.Errorf("old attempt did not start: %w", err)
	}
	if oldID != inserted.Job.ID {
		return fmt.Errorf("old worker started unexpected job %d", oldID)
	}
	res, err := pool.Exec(ctx, "UPDATE "+schema+".river_job SET state = 'available', scheduled_at = now() WHERE id = $1 AND state = 'running' AND attempt = 1", oldID)
	if err != nil {
		return fmt.Errorf("simulate rescue: %w", err)
	}
	if res.RowsAffected() != 1 {
		return fmt.Errorf("simulate rescue: updated %d rows", res.RowsAffected())
	}
	newClientInstance, err := newClient("s3-new", &worker{pool: pool, schema: schema, started: newStarted, release: releaseNew})
	if err != nil {
		return fmt.Errorf("configure new worker: %w", err)
	}
	if err := newClientInstance.Start(ctx); err != nil {
		return fmt.Errorf("start new worker: %w", err)
	}
	defer stopClient(newClientInstance, &resultErr)
	newID, err := waitID(ctx, newStarted)
	if err != nil {
		return fmt.Errorf("new attempt did not start: %w", err)
	}
	if newID != oldID {
		return fmt.Errorf("new worker started unexpected job %d", newID)
	}
	var attempt, effects int
	var state string
	if err := pool.QueryRow(ctx, "SELECT attempt, state FROM "+schema+".river_job WHERE id = $1", oldID).Scan(&attempt, &state); err != nil {
		return err
	}
	if attempt != 2 || state != "running" {
		return fmt.Errorf("expected running attempt 2, got attempt=%d state=%s", attempt, state)
	}
	close(releaseOld)
	var oldOutcome outcome
	select {
	case oldOutcome = <-completed:
	case <-ctx.Done():
		return fmt.Errorf("wait for old completion: %w", ctx.Err())
	}
	if err := pool.QueryRow(ctx, "SELECT attempt, state FROM "+schema+".river_job WHERE id = $1", oldID).Scan(&attempt, &state); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+schema+".effects WHERE job_id = $1", oldID).Scan(&effects); err != nil {
		return err
	}
	close(releaseNew)
	fmt.Printf("old attempt committed=%t; current attempt=%d; state=%s; effects=%d\n", oldOutcome.committed, attempt, state, effects)
	if !oldOutcome.committed || oldOutcome.err != nil || attempt != 2 || state != "completed" || effects != 1 {
		return errors.New("stale completion was not reproduced")
	}
	return nil
}

func waitID(ctx context.Context, started <-chan int64) (int64, error) {
	select {
	case id := <-started:
		return id, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func stopClient(client *river.Client[pgx.Tx], resultErr *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.StopAndCancel(ctx); err != nil {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("stop River client: %w", err))
	}
}
