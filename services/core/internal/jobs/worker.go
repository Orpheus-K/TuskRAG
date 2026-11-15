package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/dbquery"
)

// Handler performs long-running work outside a transaction and returns database-only effects.
// It must return promptly when its context is cancelled.
type Handler interface {
	Prepare(context.Context, ClaimedJob) (func(context.Context, EffectsTx) error, error)
}

// ProcessingFailure classifies an expected job failure without persisting its cause.
type ProcessingFailure struct {
	Code  FailureCode
	Cause error
	Apply FailureEffects
}

func (e *ProcessingFailure) Error() string { return string(e.Code) }
func (e *ProcessingFailure) Unwrap() error { return e.Cause }

type WorkerConfig struct {
	TenantID  int64
	TenantIDs []int64
	Kinds     []Kind
	Owner     string
	LeaseTTL  time.Duration
	Poll      time.Duration
	OnExpired ExpiredEffects
}

type Worker struct {
	store   *Store
	handler Handler
	config  WorkerConfig
}

func NewWorker(store *Store, handler Handler, config WorkerConfig) (*Worker, error) {
	if store == nil || handler == nil || strings.TrimSpace(config.Owner) == "" ||
		config.LeaseTTL < 300*time.Millisecond || config.Poll <= 0 {
		return nil, errors.New("valid store, handler, tenant, owner and worker intervals are required")
	}
	if len(config.TenantIDs) == 0 && config.TenantID > 0 {
		config.TenantIDs = []int64{config.TenantID}
	} else if config.TenantID != 0 {
		return nil, errors.New("specify either one tenant or a tenant list")
	}
	if len(config.TenantIDs) == 0 || len(config.TenantIDs) > 64 {
		return nil, errors.New("worker requires between 1 and 64 tenants")
	}
	seen := make(map[int64]struct{}, len(config.TenantIDs))
	for _, tenantID := range config.TenantIDs {
		if tenantID <= 0 {
			return nil, errors.New("worker tenant IDs must be positive")
		}
		if _, duplicate := seen[tenantID]; duplicate {
			return nil, errors.New("worker tenant IDs must be unique")
		}
		seen[tenantID] = struct{}{}
	}
	config.TenantIDs = slices.Clone(config.TenantIDs)
	if len(config.Kinds) == 0 {
		config.Kinds = []Kind{Ingest, Reembed, Purge, EvalRun, ACLRefresh}
	}
	if _, err := validateKinds(config.Kinds); err != nil {
		return nil, err
	}
	config.Kinds = slices.Clone(config.Kinds)
	return &Worker{store: store, handler: handler, config: config}, nil
}

// Run processes configured tenants fairly until cancelled or an infrastructure error occurs.
func (w *Worker) Run(ctx context.Context) (resultErr error) {
	startupCtx, cancelStartup := w.databaseContext(ctx)
	defer cancelStartup()
	listenerConfig := w.store.pool.Config().ConnConfig.Copy()
	if listenerConfig.RuntimeParams == nil {
		listenerConfig.RuntimeParams = make(map[string]string)
	}
	listenerName := "tuskrag-worker-multi"
	if len(w.config.TenantIDs) == 1 {
		listenerName = "tuskrag-worker-" + strconv.FormatInt(w.config.TenantIDs[0], 10)
	}
	listenerConfig.RuntimeParams["application_name"] = listenerName
	listener, err := pgx.ConnectConfig(startupCtx, listenerConfig)
	if err != nil {
		return fmt.Errorf("connect job listener: %w", err)
	}
	listenerCtx, stopListener := context.WithCancel(ctx)
	listenerDone := make(chan struct{})
	defer func() {
		stopListener()
		<-listenerDone
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := listener.Close(closeCtx); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close job listener: %w", err))
		}
	}()
	queries := dbquery.New(listener)
	if err := queries.ListenJobReady(startupCtx); err != nil {
		close(listenerDone)
		return fmt.Errorf("listen for ready jobs: %w", err)
	}
	if err := queries.ListenJobCancelled(startupCtx); err != nil {
		close(listenerDone)
		return fmt.Errorf("listen for cancelled jobs: %w", err)
	}
	cancelStartup()
	events := make(chan *pgconn.Notification, 32)
	listenerErrors := make(chan error, 1)
	go func() {
		defer close(listenerDone)
		for {
			notification, err := listener.WaitForNotification(listenerCtx)
			if err != nil {
				if listenerCtx.Err() == nil {
					listenerErrors <- err
				}
				return
			}
			select {
			case events <- notification:
			case <-listenerCtx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(w.config.Poll)
	defer ticker.Stop()
	nextTenant := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		claimed := false
		for offset := range len(w.config.TenantIDs) {
			position := (nextTenant + offset) % len(w.config.TenantIDs)
			claimCtx, cancelClaim := w.databaseContext(ctx)
			job, err := w.store.ClaimKindsWithRecovery(claimCtx, w.config.TenantIDs[position], w.config.Owner, w.config.LeaseTTL, w.config.Kinds, w.config.OnExpired)
			cancelClaim()
			if errors.Is(err, ErrNoJob) {
				continue
			}
			if err != nil {
				return fmt.Errorf("claim worker job: %w", err)
			}
			claimed = true
			nextTenant = (position + 1) % len(w.config.TenantIDs)
			if err := w.runClaimed(ctx, job, events, listenerErrors); err != nil &&
				!errors.Is(err, ErrLeaseLost) && (!errors.Is(err, context.Canceled) || ctx.Err() != nil) {
				return err
			}
			break
		}
		if !claimed {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-listenerErrors:
				return fmt.Errorf("job listener failed: %w", err)
			case <-ticker.C:
			case <-events:
			}
		}
	}
}

func (w *Worker) runClaimed(ctx context.Context, job ClaimedJob, events <-chan *pgconn.Notification, listenerErrors <-chan error) error {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.execute(jobCtx, job) }()
	stop := func(cause error) error {
		cancel()
		workerErr := <-done
		if workerErr != nil && !errors.Is(workerErr, context.Canceled) && !errors.Is(workerErr, ErrLeaseLost) {
			return errors.Join(cause, workerErr)
		}
		return cause
	}
	for {
		select {
		case <-ctx.Done():
			return stop(ctx.Err())
		case err := <-listenerErrors:
			return stop(fmt.Errorf("job listener failed: %w", err))
		case result := <-done:
			return result
		case event := <-events:
			if event.Channel != "tuskrag_job_cancelled" || event.Payload != strconv.FormatInt(job.Lease.JobID, 10) {
				continue
			}
			if err := w.renew(ctx, job.Lease); errors.Is(err, ErrLeaseLost) {
				return stop(ErrLeaseLost)
			} else if err != nil {
				return stop(fmt.Errorf("verify job cancellation: %w", err))
			}
		}
	}
}

func (w *Worker) execute(ctx context.Context, job ClaimedJob) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type prepared struct {
		apply func(context.Context, EffectsTx) error
		err   error
	}
	result := make(chan prepared, 1)
	go func() {
		apply, err := w.handler.Prepare(workCtx, job)
		result <- prepared{apply: apply, err: err}
	}()
	ticker := time.NewTicker(w.config.LeaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-result
			return ctx.Err()
		case <-ticker.C:
			if err := w.renew(ctx, job.Lease); err != nil {
				cancel()
				<-result
				return err
			}
		case prepared := <-result:
			if err := ctx.Err(); err != nil {
				return err
			}
			if prepared.err != nil {
				var failure *ProcessingFailure
				if !errors.As(prepared.err, &failure) {
					return fmt.Errorf("prepare job: %w", prepared.err)
				}
				return w.recordFailure(ctx, job, failure)
			}
			if err := w.renew(ctx, job.Lease); err != nil {
				return fmt.Errorf("renew job before completion: %w", err)
			}
			completeCtx, cancelComplete := context.WithTimeout(ctx, min(30*time.Second, w.config.LeaseTTL/2))
			err := w.store.Complete(completeCtx, job.Lease, prepared.apply)
			cancelComplete()
			if err != nil {
				var failure *ProcessingFailure
				if errors.As(err, &failure) {
					return w.recordFailure(ctx, job, failure)
				}
				return fmt.Errorf("complete worker job: %w", err)
			}
			return nil
		}
	}
}

func (w *Worker) recordFailure(ctx context.Context, job ClaimedJob, failure *ProcessingFailure) error {
	if failure == nil {
		return errors.New("job handler returned an empty failure")
	}
	if _, valid := failurePolicy(failure.Code); !valid {
		return errors.New("job handler returned an invalid failure code")
	}
	slog.Error("worker job failed", "job_id", job.Lease.JobID, "kind", job.Kind, "tenant_id", job.Lease.TenantID, "code", failure.Code, "error", failure.Cause)
	failureCtx, cancelFailure := w.databaseContext(ctx)
	defer cancelFailure()
	var err error
	if failure.Apply == nil {
		_, err = w.store.Fail(failureCtx, job.Lease, failure.Code)
	} else {
		_, err = w.store.FailWithEffects(failureCtx, job.Lease, failure.Code, failure.Apply)
	}
	if err != nil {
		return fmt.Errorf("record job failure: %w", err)
	}
	return nil
}

func (w *Worker) databaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, min(5*time.Second, w.config.LeaseTTL/3))
}

func (w *Worker) renew(ctx context.Context, lease Lease) error {
	renewCtx, cancel := w.databaseContext(ctx)
	defer cancel()
	_, err := w.store.Renew(renewCtx, lease, w.config.LeaseTTL)
	return err
}
