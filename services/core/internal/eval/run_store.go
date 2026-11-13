package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/retrieval"
)

var (
	ErrRunNotFound            = errors.New("evaluation run not visible")
	ErrRunIdempotencyConflict = errors.New("evaluation run idempotency key reused with different content")
)

type RunRequest struct {
	Set    SetBinding `json:"set"`
	Mode   string     `json:"mode"`
	Config RunConfig  `json:"config"`
}

type StoredRun struct {
	ID           int64      `json:"id"`
	JobID        int64      `json:"job_id"`
	SetDigest    string     `json:"set_digest"`
	ConfigDigest string     `json:"config_digest"`
	Request      RunRequest `json:"request"`
	CreatedAt    time.Time  `json:"created_at"`
}

type RunJobPayload struct {
	RunID int64 `json:"run_id"`
}

type RunStore struct {
	sets  *SetStore
	queue *jobs.Store
}

func NewRunStore(pool *pgxpool.Pool, queue *jobs.Store) (*RunStore, error) {
	if !queue.UsesPool(pool) {
		return nil, errors.New("evaluation runs and queue must share a database pool")
	}
	sets, err := NewSetStore(pool)
	if err != nil {
		return nil, err
	}
	return &RunStore{sets: sets, queue: queue}, nil
}

// Create atomically records one immutable run and one job. No model calls occur
// here. The transport must supply server-selected model metadata and scope.
func (s *RunStore) Create(ctx context.Context, actor retrieval.Actor, kbID int64, key string, request RunRequest) (_ StoredRun, resultErr error) {
	if err := validateRunRequest(actor, kbID, key, request); err != nil {
		return StoredRun{}, err
	}
	configuration, err := json.Marshal(request.Config)
	if err != nil {
		return StoredRun{}, err
	}
	if len(configuration) > 1024*1024 {
		return StoredRun{}, ErrInvalidCase
	}
	configHash := sha256.Sum256(configuration)
	encoded, err := json.Marshal(request)
	if err != nil {
		return StoredRun{}, err
	}
	requestHash := sha256.Sum256(encoded)
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return StoredRun{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	set, err := loadRunSet(ctx, q, actor, kbID, request.Set)
	if err != nil {
		return StoredRun{}, err
	}
	if err := runAccess(ctx, q, actor, kbID, request, set.Definition, true); err != nil {
		return StoredRun{}, err
	}
	operation := fmt.Sprintf("eval.run.create/%d", kbID)
	_, err = q.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, RequestHash: requestHash[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		prior, err := q.FindIdempotency(ctx, dbquery.FindIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key})
		if err != nil {
			return StoredRun{}, fmt.Errorf("read evaluation run retry: %w", err)
		}
		if !bytes.Equal(prior.RequestHash, requestHash[:]) {
			return StoredRun{}, ErrRunIdempotencyConflict
		}
		if !prior.ObjectID.Valid {
			return StoredRun{}, errors.New("incomplete evaluation run retry")
		}
		row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: prior.ObjectID.Int64})
		if err != nil {
			return StoredRun{}, fmt.Errorf("read original evaluation run: %w", err)
		}
		stored, err := decodeStoredRun(row)
		if err != nil {
			return StoredRun{}, err
		}
		if stored.SetDigest != set.Digest || stored.ConfigDigest != hex.EncodeToString(configHash[:]) || stored.Request.Set != request.Set || stored.Request.Mode != request.Mode {
			return StoredRun{}, errors.New("evaluation run retry snapshot mismatch")
		}
		if err := tx.Commit(ctx); err != nil {
			return StoredRun{}, err
		}
		return stored, nil
	}
	if err != nil {
		return StoredRun{}, fmt.Errorf("reserve evaluation run retry: %w", err)
	}
	return s.insertRun(ctx, tx, q, actor, kbID, key, operation, request, set, configuration, hex.EncodeToString(configHash[:]))
}

func (s *RunStore) insertRun(ctx context.Context, tx pgx.Tx, q *dbquery.Queries, actor retrieval.Actor, kbID int64, key, operation string, request RunRequest, set StoredSet, configuration []byte, configDigest string) (StoredRun, error) {
	if err := checkRunConfiguration(ctx, q, actor, kbID, *request.Config.Configuration); err != nil {
		return StoredRun{}, err
	}
	id, err := q.NextEvalRunID(ctx)
	if err != nil {
		return StoredRun{}, err
	}
	payload, err := json.Marshal(RunJobPayload{RunID: id})
	if err != nil {
		return StoredRun{}, err
	}
	jobID, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{Kind: jobs.EvalRun, Payload: payload, MaxAttempts: 3})
	if err != nil {
		return StoredRun{}, err
	}
	row, err := q.InsertEvalRun(ctx, dbquery.InsertEvalRunParams{ID: id, TenantID: actor.TenantID, KbID: kbID, JobID: jobID, SetID: request.Set.SetID, SetRevision: request.Set.Revision, SetDigest: set.Digest, Mode: request.Mode, Configuration: configuration, ConfigDigest: configDigest, UserID: actor.UserID})
	if err != nil {
		return StoredRun{}, fmt.Errorf("save evaluation run: %w", err)
	}
	if err := q.AuditEvalRun(ctx, dbquery.AuditEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id, JobID: jobID, ConfigDigest: row.ConfigDigest, UserID: actor.UserID}); err != nil {
		return StoredRun{}, err
	}
	count, err := q.CompleteIdempotency(ctx, dbquery.CompleteIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, ObjectID: pgtype.Int8{Int64: id, Valid: true}})
	if err != nil {
		return StoredRun{}, err
	}
	if count != 1 {
		return StoredRun{}, errors.New("evaluation run retry reservation lost")
	}
	stored, err := decodeStoredRun(row)
	if err != nil {
		return StoredRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredRun{}, fmt.Errorf("commit evaluation run: %w", err)
	}
	return stored, nil
}

// Get rechecks current target and scope visibility, even for historical runs.
func (s *RunStore) Get(ctx context.Context, actor retrieval.Actor, kbID, id int64) (_ StoredRun, resultErr error) {
	if id <= 0 {
		return StoredRun{}, ErrInvalidCase
	}
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return StoredRun{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return StoredRun{}, err
	}
	row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredRun{}, ErrRunNotFound
	}
	if err != nil {
		return StoredRun{}, err
	}
	stored, err := decodeStoredRun(row)
	if err != nil {
		return StoredRun{}, err
	}
	set, err := loadRunSet(ctx, q, actor, kbID, stored.Request.Set)
	if err != nil {
		return StoredRun{}, err
	}
	if stored.SetDigest != set.Digest {
		return StoredRun{}, errors.New("evaluation run set digest mismatch")
	}
	if err := runAccess(ctx, q, actor, kbID, stored.Request, set.Definition, false); err != nil {
		return StoredRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredRun{}, err
	}
	return stored, nil
}

func loadRunSet(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, binding SetBinding) (StoredSet, error) {
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return StoredSet{}, err
	}
	row, err := q.GetEvalSetRevision(ctx, dbquery.GetEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: binding.SetID, Revision: binding.Revision})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredSet{}, ErrSetNotFound
	}
	if err != nil {
		return StoredSet{}, err
	}
	set := StoredSet{ID: row.SetID, Revision: row.Revision, Digest: row.Digest, CreatedAt: row.CreatedAt.Time}
	if err := json.Unmarshal(row.Definition, &set.Definition); err != nil {
		return StoredSet{}, err
	}
	return set, validateRunSet(set, binding)
}

func runAccess(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, request RunRequest, definition SetDefinition, writing bool) error {
	documents := slices.Clone(request.Config.AllowedDocumentIDs)
	slices.Sort(documents)
	for _, item := range definition.Cases {
		for _, expected := range item.Expected {
			if _, ok := slices.BinarySearch(documents, expected.DocumentID); !ok {
				return ErrInvalidCase
			}
		}
	}
	// Lock the complete scope in document order before taking KB/member locks.
	if err := checkReportAccess(ctx, q, actor, kbID, documents, nil, nil, writing); err != nil {
		return setAccessError(err)
	}
	return checkSetAccess(ctx, q, actor, kbID, definition, writing)
}

func checkRunConfiguration(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, expected retrieval.ConfigurationSnapshot) error {
	bound, err := q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if err != nil {
		return err
	}
	actual := retrieval.ConfigurationSnapshot{Embedding: retrieval.Binding{SpaceID: bound.EmbeddingSpaceID, Revision: bound.ConfigRevision, Dimensions: bound.Dims, Distance: bound.Distance, Normalized: bound.Normalized}}
	row, err := q.LatestRetrievalConfig(ctx, dbquery.LatestRetrievalConfigParams{TenantID: actor.TenantID, KbID: kbID})
	if err == nil {
		actual.RetrievalRevision = row.Revision
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if actual != expected {
		return retrieval.ErrConfigurationChanged
	}
	return nil
}

func validateRunRequest(actor retrieval.Actor, kbID int64, key string, request RunRequest) error {
	c := request.Config
	if actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 || c.Actor != actor || c.KnowledgeBaseID != kbID || request.Set.SetID <= 0 || request.Set.Revision <= 0 || c.Configuration == nil || len(c.Documents) != 0 || len(c.AllowedDocumentIDs) > 10000 || len(key) == 0 || len(key) > 128 {
		return ErrInvalidCase
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return ErrInvalidCase
		}
	}
	if request.Mode != "lexical" && request.Mode != "vector" && request.Mode != "rrf" && request.Mode != "rerank" {
		return ErrInvalidCase
	}
	if c.ModelKind != "stub" && c.ModelKind != "real" {
		return ErrInvalidCase
	}
	if strings.TrimSpace(c.ModelLabel) == "" || len(c.ModelLabel) > 200 || !utf8.ValidString(c.ModelLabel) || strings.ContainsRune(c.ModelLabel, 0) {
		return ErrInvalidCase
	}
	seen := make(map[int64]bool, len(c.AllowedDocumentIDs))
	for _, id := range c.AllowedDocumentIDs {
		if id <= 0 || seen[id] {
			return ErrInvalidCase
		}
		seen[id] = true
	}
	if request.Mode == "rerank" && c.Reranker == nil {
		return ErrInvalidCase
	}
	if c.Reranker != nil {
		r := c.Reranker
		if r.ProviderID <= 0 || strings.TrimSpace(r.Model) == "" || len(r.Model) > 200 || !utf8.ValidString(r.Model) || strings.ContainsRune(r.Model, 0) || (r.TopN != 0 && r.TopN != 20 && r.TopN != 30 && r.TopN != 50) {
			return ErrInvalidCase
		}
	}
	return nil
}

func decodeStoredRun(row dbquery.EvalRun) (StoredRun, error) {
	stored := StoredRun{ID: row.ID, JobID: row.JobID, SetDigest: row.SetDigest, ConfigDigest: row.ConfigDigest, CreatedAt: row.CreatedAt.Time, Request: RunRequest{Set: SetBinding{SetID: row.SetID, Revision: row.SetRevision}, Mode: row.Mode}}
	if err := json.Unmarshal(row.Configuration, &stored.Request.Config); err != nil {
		return StoredRun{}, err
	}
	if err := validateRunRequest(retrieval.Actor{TenantID: row.TenantID, UserID: row.CreatedBy}, row.KbID, "stored", stored.Request); err != nil {
		return StoredRun{}, fmt.Errorf("invalid stored evaluation run: %w", err)
	}
	encoded, err := json.Marshal(stored.Request.Config)
	if err != nil {
		return StoredRun{}, err
	}
	hash := sha256.Sum256(encoded)
	if hex.EncodeToString(hash[:]) != stored.ConfigDigest {
		return StoredRun{}, errors.New("stored evaluation run configuration digest mismatch")
	}
	return stored, nil
}
