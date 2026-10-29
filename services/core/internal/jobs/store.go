package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
)

var (
	ErrNoJob              = errors.New("no eligible job")
	ErrNotFound           = errors.New("job not found or not cancellable")
	ErrLeaseLost          = errors.New("job lease is no longer valid")
	ErrDedupeConflict     = errors.New("deduplication key has different job parameters")
	ErrNeedsEffects       = errors.New("job state change requires domain effects")
	ErrCompletionRequired = errors.New("matching job completion transaction is required")
	ErrFailureRequired    = errors.New("matching job failure transaction is required")
)

type Kind string

const (
	Ingest     Kind = "ingest"
	Reembed    Kind = "reembed"
	Purge      Kind = "purge"
	EvalRun    Kind = "eval_run"
	ACLRefresh Kind = "acl_refresh"
)

type FailureCode string

const (
	ModelUnavailable     FailureCode = "model_unavailable"
	StorageUnavailable   FailureCode = "storage_unavailable"
	IndexUnavailable     FailureCode = "index_unavailable"
	ModelRejected        FailureCode = "model_rejected"
	ModelInvalid         FailureCode = "model_invalid_response"
	ConfigurationChanged FailureCode = "configuration_changed"
	VersionSuperseded    FailureCode = "version_superseded"
	UnsupportedEmbedding FailureCode = "unsupported_embedding"
	TokenizerMismatch    FailureCode = "tokenizer_mismatch"
	EgressDenied         FailureCode = "egress_denied"
	QualityReview        FailureCode = "quality_review"
	TableHeaderRequired  FailureCode = "table_header_required"
	Timeout              FailureCode = "timeout"
	ParseError           FailureCode = "parse_error"
	Unsupported          FailureCode = "unsupported_format"
	Encrypted            FailureCode = "encrypted"
	LimitExceeded        FailureCode = "limit_exceeded"
	FileMissing          FailureCode = "file_missing"
	FileCorrupt          FailureCode = "file_corrupt"
	LeaseExpired         FailureCode = "lease_expired"
	Internal             FailureCode = "internal"
)

type Status string

const (
	Queued Status = "queued"
	Failed Status = "failed"
	Dead   Status = "dead"
)

type RetryPolicy struct {
	Initial time.Duration
	Maximum time.Duration
}

type Request struct {
	Kind        Kind
	DedupeKey   *string
	Payload     json.RawMessage
	MaxAttempts int32
}

type Lease struct {
	JobID      int64
	TenantID   int64
	Owner      string
	Generation int64
	ExpiresAt  time.Time
	Attempt    int32
}

type ClaimedJob struct {
	Lease   Lease
	Kind    Kind
	Payload json.RawMessage
}

type ExpiredJob struct {
	ID       int64
	TenantID int64
	Kind     Kind
	Payload  json.RawMessage
}

// ExpiredEffects synchronizes domain state for an exhausted lease inside the job transaction.
type ExpiredEffects func(context.Context, EffectsTx, ExpiredJob) error

// EffectsTx exposes database operations without allowing a callback to commit independently.
type EffectsTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type effectsTx struct {
	tx              pgx.Tx
	completionStore *Store
	completionLease Lease
	failureStore    *Store
	failureLease    Lease
	failureStatus   Status
}

func (t effectsTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.tx.Exec(ctx, sql, args...)
}

func (t effectsTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.tx.Query(ctx, sql, args...)
}

func (t effectsTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

type Store struct {
	pool  *pgxpool.Pool
	retry RetryPolicy
}

func NewStore(pool *pgxpool.Pool, retry RetryPolicy) (*Store, error) {
	if pool == nil || retry.Initial <= 0 || retry.Maximum < retry.Initial {
		return nil, errors.New("database pool and valid retry policy are required")
	}
	return &Store{pool: pool, retry: retry}, nil
}

// UsesPool reports whether this store is bound to the supplied database pool.
func (s *Store) UsesPool(pool *pgxpool.Pool) bool {
	return s != nil && pool != nil && s.pool == pool
}

// CheckCompletion ensures an effect is executed only by this store's Complete
// transaction for the same lease, not by a checkpoint or an unrelated job.
func (s *Store) CheckCompletion(tx EffectsTx, lease Lease) error {
	completed, ok := tx.(effectsTx)
	if !ok || s == nil || completed.completionStore != s || !sameLease(completed.completionLease, lease) {
		return ErrCompletionRequired
	}
	return nil
}

// CheckFailure binds domain failure effects to the planned failure of one lease.
func (s *Store) CheckFailure(tx EffectsTx, lease Lease, status Status) error {
	failed, ok := tx.(effectsTx)
	if !ok || s == nil || failed.failureStore != s || !sameLease(failed.failureLease, lease) || failed.failureStatus != status {
		return ErrFailureRequired
	}
	return nil
}

func sameLease(left, right Lease) bool {
	return left.JobID == right.JobID && left.TenantID == right.TenantID && left.Owner == right.Owner &&
		left.Generation == right.Generation && left.Attempt == right.Attempt
}

func (s *Store) begin(ctx context.Context, tenantID int64) (pgx.Tx, *dbquery.Queries, error) {
	if tenantID <= 0 {
		return nil, nil, errors.New("tenant ID must be positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin job transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		result := fmt.Errorf("set job tenant: %w", err)
		rollback(tx, &result)
		return nil, nil, result
	}
	return tx, queries, nil
}

func rollback(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback job transaction: %w", err))
	}
}

func (s *Store) Enqueue(ctx context.Context, tenantID int64, request Request) (_ int64, err error) {
	tx, _, err := s.begin(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	defer rollback(tx, &err)
	id, err := s.EnqueueTx(ctx, tx, tenantID, request)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit job enqueue: %w", err)
	}
	return id, nil
}

// EnqueueTx adds a job to an existing tenant-scoped transaction.
func (s *Store) EnqueueTx(ctx context.Context, tx pgx.Tx, tenantID int64, request Request) (int64, error) {
	return s.enqueue(ctx, tx, tenantID, request)
}

// EnqueueEffects adds a job within a fenced job-effects transaction.
func (s *Store) EnqueueEffects(ctx context.Context, tx EffectsTx, tenantID int64, request Request) (int64, error) {
	return s.enqueue(ctx, tx, tenantID, request)
}

func (s *Store) enqueue(ctx context.Context, tx EffectsTx, tenantID int64, request Request) (int64, error) {
	if tx == nil || tenantID <= 0 || !validKind(request.Kind) || request.MaxAttempts <= 0 || !json.Valid(request.Payload) {
		return 0, errors.New("invalid job request")
	}
	key := pgtype.Text{}
	if request.DedupeKey != nil {
		if strings.TrimSpace(*request.DedupeKey) == "" {
			return 0, errors.New("deduplication key must not be empty")
		}
		key = pgtype.Text{String: *request.DedupeKey, Valid: true}
	}
	queries := dbquery.New(tx)
	currentTenant, err := queries.CurrentTenant(ctx)
	if err != nil {
		return 0, fmt.Errorf("read transaction tenant: %w", err)
	}
	if currentTenant != tenantID {
		return 0, errors.New("job tenant does not match transaction tenant")
	}
	id, err := queries.EnqueueJob(ctx, dbquery.EnqueueJobParams{
		TenantID: tenantID, Kind: string(request.Kind), DedupeKey: key,
		Payload: request.Payload, MaxAttempts: request.MaxAttempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrDedupeConflict
	}
	if err != nil {
		return 0, fmt.Errorf("enqueue job: %w", err)
	}
	if err := queries.NotifyJobReady(ctx, tenantID); err != nil {
		return 0, fmt.Errorf("notify job readiness: %w", err)
	}
	return id, nil
}

func (s *Store) Claim(ctx context.Context, tenantID int64, owner string, ttl time.Duration) (_ ClaimedJob, err error) {
	return s.ClaimWithRecovery(ctx, tenantID, owner, ttl, nil)
}

// ClaimWithRecovery atomically resolves exhausted jobs before claiming new work.
func (s *Store) ClaimWithRecovery(ctx context.Context, tenantID int64, owner string, ttl time.Duration, recover ExpiredEffects) (_ ClaimedJob, err error) {
	return s.ClaimKindsWithRecovery(ctx, tenantID, owner, ttl, []Kind{Ingest, Reembed, Purge, EvalRun, ACLRefresh}, recover)
}

// ClaimKindsWithRecovery leaves jobs of unsupported kinds untouched.
func (s *Store) ClaimKindsWithRecovery(ctx context.Context, tenantID int64, owner string, ttl time.Duration, kinds []Kind, recover ExpiredEffects) (_ ClaimedJob, err error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 {
		return ClaimedJob{}, errors.New("owner and positive lease duration are required")
	}
	selected, err := validateKinds(kinds)
	if err != nil {
		return ClaimedJob{}, err
	}
	for range 16 {
		found, err := s.recoverExpired(ctx, tenantID, selected, recover)
		if err != nil {
			return ClaimedJob{}, err
		}
		if !found {
			break
		}
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return ClaimedJob{}, err
	}
	defer rollback(tx, &err)
	row, err := queries.ClaimJob(ctx, dbquery.ClaimJobParams{
		TenantID: tenantID, Kinds: selected, Owner: text(owner), LeaseSeconds: ttl.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return ClaimedJob{}, fmt.Errorf("commit expired job cleanup: %w", err)
		}
		return ClaimedJob{}, ErrNoJob
	}
	if err != nil {
		return ClaimedJob{}, fmt.Errorf("claim job: %w", err)
	}
	if !row.LeaseUntil.Valid || !validKind(Kind(row.Kind)) || !json.Valid(row.Payload) {
		return ClaimedJob{}, errors.New("claimed job has invalid stored data")
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedJob{}, fmt.Errorf("commit job claim: %w", err)
	}
	return ClaimedJob{
		Lease: Lease{JobID: row.ID, TenantID: row.TenantID, Owner: owner,
			Generation: row.LeaseGeneration, ExpiresAt: row.LeaseUntil.Time, Attempt: row.Attempts},
		Kind: Kind(row.Kind), Payload: row.Payload,
	}, nil
}

func (s *Store) recoverExpired(ctx context.Context, tenantID int64, kinds []string, recover ExpiredEffects) (_ bool, resultErr error) {
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return false, err
	}
	defer rollback(tx, &resultErr)
	row, err := queries.LockExpiredJob(ctx, dbquery.LockExpiredJobParams{TenantID: tenantID, Kinds: kinds})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock exhausted job: %w", err)
	}
	if row.ID <= 0 || row.TenantID != tenantID || !validKind(Kind(row.Kind)) || !json.Valid(row.Payload) {
		return false, errors.New("exhausted job has invalid stored data")
	}
	if recover == nil {
		return false, ErrNeedsEffects
	}
	if err := recover(ctx, effectsTx{tx: tx}, ExpiredJob{ID: row.ID, TenantID: row.TenantID, Kind: Kind(row.Kind), Payload: row.Payload}); err != nil {
		return false, fmt.Errorf("recover exhausted job: %w", err)
	}
	if _, err := queries.DeadLetterExpiredJob(ctx, dbquery.DeadLetterExpiredJobParams{TenantID: tenantID, JobID: row.ID}); errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("exhausted job changed during recovery")
	} else if err != nil {
		return false, fmt.Errorf("dead-letter exhausted job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit exhausted job recovery: %w", err)
	}
	return true, nil
}

func validateKinds(kinds []Kind) ([]string, error) {
	if len(kinds) == 0 || len(kinds) > 5 {
		return nil, errors.New("one or more supported job kinds are required")
	}
	result := make([]string, 0, len(kinds))
	seen := make(map[Kind]struct{}, len(kinds))
	for _, kind := range kinds {
		if !validKind(kind) {
			return nil, errors.New("unsupported job kind")
		}
		if _, exists := seen[kind]; exists {
			return nil, errors.New("duplicate job kind")
		}
		seen[kind] = struct{}{}
		result = append(result, string(kind))
	}
	return result, nil
}

func (s *Store) Renew(ctx context.Context, lease Lease, ttl time.Duration) (_ time.Time, err error) {
	if err := validateLease(lease); err != nil || ttl <= 0 {
		return time.Time{}, errors.New("valid lease and positive duration are required")
	}
	tx, queries, err := s.begin(ctx, lease.TenantID)
	if err != nil {
		return time.Time{}, err
	}
	defer rollback(tx, &err)
	expiresAt, err := queries.RenewLease(ctx, dbquery.RenewLeaseParams{
		TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner),
		Generation: lease.Generation, Attempt: lease.Attempt, LeaseSeconds: ttl.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrLeaseLost
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("renew job lease: %w", err)
	}
	if !expiresAt.Valid {
		return time.Time{}, errors.New("renewed lease has no expiration")
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, fmt.Errorf("commit lease renewal: %w", err)
	}
	return expiresAt.Time, nil
}

// Complete runs database-only effects and marks the job complete in one short transaction.
func (s *Store) Complete(ctx context.Context, lease Lease, apply func(context.Context, EffectsTx) error) (err error) {
	if err := validateLease(lease); err != nil {
		return err
	}
	tx, queries, err := s.begin(ctx, lease.TenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &err)
	if _, err := queries.LockLease(ctx, dbquery.LockLeaseParams{
		TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner), Generation: lease.Generation, Attempt: lease.Attempt,
	}); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock job lease: %w", err)
	}
	if apply != nil {
		if err := apply(ctx, effectsTx{tx: tx, completionStore: s, completionLease: lease}); err != nil {
			return fmt.Errorf("apply job effects: %w", err)
		}
	}
	if _, err := queries.CompleteJob(ctx, dbquery.CompleteJobParams{
		TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner), Generation: lease.Generation, Attempt: lease.Attempt,
	}); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job completion: %w", err)
	}
	return nil
}

// WithLease commits a short database-only checkpoint while the lease remains current.
func (s *Store) WithLease(ctx context.Context, lease Lease, apply func(context.Context, EffectsTx) error) (err error) {
	if err := validateLease(lease); err != nil || apply == nil {
		return errors.New("valid lease and checkpoint effects are required")
	}
	tx, queries, err := s.begin(ctx, lease.TenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &err)
	params := dbquery.LockLeaseParams{TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner), Generation: lease.Generation, Attempt: lease.Attempt}
	if _, err := queries.LockLease(ctx, params); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock checkpoint lease: %w", err)
	}
	if err := apply(ctx, effectsTx{tx: tx}); err != nil {
		return fmt.Errorf("apply job checkpoint: %w", err)
	}
	if _, err := queries.LockLease(ctx, params); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return fmt.Errorf("verify checkpoint lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job checkpoint: %w", err)
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, lease Lease, code FailureCode) (_ Status, err error) {
	return s.fail(ctx, lease, code, nil)
}

// FailureEffects must perform only short database operations in the supplied transaction.
type FailureEffects func(context.Context, EffectsTx, Status) error

// FailWithEffects records failure and its database-only state changes atomically.
func (s *Store) FailWithEffects(ctx context.Context, lease Lease, code FailureCode, apply FailureEffects) (_ Status, err error) {
	if apply == nil {
		return "", errors.New("failure effects are required")
	}
	return s.fail(ctx, lease, code, apply)
}

func (s *Store) fail(ctx context.Context, lease Lease, code FailureCode, apply FailureEffects) (_ Status, err error) {
	if err := validateLease(lease); err != nil {
		return "", err
	}
	retryable, valid := failurePolicy(code)
	if !valid {
		return "", errors.New("unknown failure code")
	}
	tx, queries, err := s.begin(ctx, lease.TenantID)
	if err != nil {
		return "", err
	}
	defer rollback(tx, &err)
	var planned Status
	if apply != nil {
		next, err := queries.PlanJobFailure(ctx, dbquery.PlanJobFailureParams{
			TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner), Generation: lease.Generation, Attempt: lease.Attempt, Retryable: retryable,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrLeaseLost
		}
		if err != nil {
			return "", fmt.Errorf("lock failure lease: %w", err)
		}
		planned = Status(next)
		if planned != Queued && planned != Failed && planned != Dead {
			return "", fmt.Errorf("unexpected planned failure state %q", next)
		}
		if err := apply(ctx, effectsTx{tx: tx, failureStore: s, failureLease: lease, failureStatus: planned}, planned); err != nil {
			return "", fmt.Errorf("apply job failure effects: %w", err)
		}
	}
	status, err := queries.FailJob(ctx, dbquery.FailJobParams{
		TenantID: lease.TenantID, JobID: lease.JobID, Owner: text(lease.Owner), Generation: lease.Generation, Attempt: lease.Attempt,
		Retryable: retryable, DelaySeconds: s.retryDelay(lease.Attempt).Seconds(), FailureCode: text(string(code)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	if err != nil {
		return "", fmt.Errorf("fail job: %w", err)
	}
	result := Status(status)
	if result != Queued && result != Failed && result != Dead {
		return "", fmt.Errorf("unexpected job failure state %q", status)
	}
	if apply != nil && result != planned {
		return "", errors.New("job failure state changed during transaction")
	}
	if result == Queued {
		if err := queries.NotifyJobReady(ctx, lease.TenantID); err != nil {
			return "", fmt.Errorf("notify job retry: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit job failure: %w", err)
	}
	return result, nil
}

type CancelEffects func(context.Context, EffectsTx, Kind) error

// CancelWithEffects records cancellation and its domain state changes atomically.
func (s *Store) CancelWithEffects(ctx context.Context, tenantID, jobID int64, apply CancelEffects) (err error) {
	if apply == nil {
		return ErrNeedsEffects
	}
	if jobID <= 0 {
		return errors.New("job ID must be positive")
	}
	tx, _, err := s.begin(ctx, tenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &err)
	kind, err := s.CancelTx(ctx, tx, tenantID, jobID)
	if err != nil {
		return err
	}
	if err := apply(ctx, effectsTx{tx: tx}, kind); err != nil {
		return fmt.Errorf("apply job cancellation effects: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job cancellation: %w", err)
	}
	return nil
}

// LockActiveTx locks an active deduplicated job whose payload matches the request.
func (s *Store) LockActiveTx(ctx context.Context, tx pgx.Tx, tenantID int64, kind Kind, key string, payload json.RawMessage) (int64, error) {
	if tx == nil || tenantID <= 0 || !validKind(kind) || strings.TrimSpace(key) == "" || len(payload) > 4096 || !json.Valid(payload) {
		return 0, errors.New("invalid active job request")
	}
	queries := dbquery.New(tx)
	if err := verifyTransactionTenant(ctx, queries, tenantID); err != nil {
		return 0, err
	}
	id, err := queries.LockActiveJob(ctx, dbquery.LockActiveJobParams{
		TenantID: tenantID, Kind: string(kind), DedupeKey: text(key), Payload: payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock active job: %w", err)
	}
	return id, nil
}

// CancelTx invalidates a job's lease and emits a notification upon transaction commit.
// The caller must update any associated domain state in the same transaction.
func (s *Store) CancelTx(ctx context.Context, tx pgx.Tx, tenantID, jobID int64) (Kind, error) {
	if tx == nil || tenantID <= 0 || jobID <= 0 {
		return "", errors.New("valid transaction, tenant and job are required")
	}
	queries := dbquery.New(tx)
	if err := verifyTransactionTenant(ctx, queries, tenantID); err != nil {
		return "", err
	}
	kind, err := queries.CancelJob(ctx, dbquery.CancelJobParams{TenantID: tenantID, JobID: jobID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("cancel job: %w", err)
	}
	if !validKind(Kind(kind)) {
		return "", errors.New("cancelled job has an invalid kind")
	}
	if err := queries.NotifyJobCancelled(ctx, jobID); err != nil {
		return "", fmt.Errorf("notify job cancellation: %w", err)
	}
	return Kind(kind), nil
}

// CancelActiveIngestEffects fences the matching ingest within an existing job
// transaction. The caller must update the associated version in that transaction.
func (s *Store) CancelActiveIngestEffects(ctx context.Context, tx EffectsTx, tenantID int64, key string, payload json.RawMessage) (bool, error) {
	if tx == nil || tenantID <= 0 || strings.TrimSpace(key) == "" || len(payload) > 4096 || !json.Valid(payload) {
		return false, errors.New("invalid active ingest cancellation")
	}
	queries := dbquery.New(tx)
	if err := verifyTransactionTenant(ctx, queries, tenantID); err != nil {
		return false, err
	}
	job, err := queries.LockNamedActiveIngest(ctx, dbquery.LockNamedActiveIngestParams{
		TenantID: tenantID, DedupeKey: text(key), Payload: payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock document ingest for cancellation: %w", err)
	}
	if !job.Matches {
		return false, ErrDedupeConflict
	}
	kind, err := queries.CancelJob(ctx, dbquery.CancelJobParams{TenantID: tenantID, JobID: job.ID})
	if err != nil {
		return false, fmt.Errorf("cancel document ingest: %w", err)
	}
	if Kind(kind) != Ingest {
		return false, errors.New("cancelled job is not an ingest")
	}
	if err := queries.NotifyJobCancelled(ctx, job.ID); err != nil {
		return false, fmt.Errorf("notify ingest cancellation: %w", err)
	}
	return true, nil
}

func verifyTransactionTenant(ctx context.Context, queries *dbquery.Queries, tenantID int64) error {
	current, err := queries.CurrentTenant(ctx)
	if err != nil {
		return fmt.Errorf("read transaction tenant: %w", err)
	}
	if current != tenantID {
		return errors.New("job tenant does not match transaction tenant")
	}
	return nil
}

func (s *Store) retryDelay(attempt int32) time.Duration {
	delay := s.retry.Initial
	for n := int32(1); n < attempt && delay < s.retry.Maximum; n++ {
		if delay > s.retry.Maximum/2 {
			return s.retry.Maximum
		}
		delay *= 2
	}
	return delay
}

func validateLease(lease Lease) error {
	if lease.JobID <= 0 || lease.TenantID <= 0 || strings.TrimSpace(lease.Owner) == "" || lease.Generation <= 0 || lease.Attempt <= 0 {
		return errors.New("invalid job lease")
	}
	return nil
}

func validKind(kind Kind) bool {
	switch kind {
	case Ingest, Reembed, Purge, EvalRun, ACLRefresh:
		return true
	default:
		return false
	}
}

func failurePolicy(code FailureCode) (retryable, valid bool) {
	switch code {
	case ModelUnavailable, StorageUnavailable, IndexUnavailable, Timeout:
		return true, true
	case ParseError, Unsupported, Encrypted, LimitExceeded, FileMissing, FileCorrupt, LeaseExpired, Internal,
		ModelRejected, ModelInvalid, ConfigurationChanged, VersionSuperseded, UnsupportedEmbedding,
		TokenizerMismatch, EgressDenied, QualityReview, TableHeaderRequired:
		return false, true
	default:
		return false, false
	}
}

func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
