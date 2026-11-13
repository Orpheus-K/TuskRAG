package kb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"time"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

var ErrRebuildInProgress = errors.New("knowledge base already has a pending embedding rebuild")
var ErrRebuildTargetUnsupported = errors.New("embedding rebuild target is unsupported")

type RebuildRequest struct {
	TargetSpaceID int64 `json:"target_space_id" minimum:"1"`
	Revision      int32 `json:"revision" minimum:"1" maximum:"2147483646"`
}
type Rebuild struct {
	JobID          int64      `json:"job_id"`
	SourceSpaceID  int64      `json:"source_space_id"`
	SourceRevision int32      `json:"source_revision"`
	TargetSpaceID  int64      `json:"target_space_id" minimum:"1"`
	Status         string     `json:"status" enum:"queued,running,succeeded,failed,dead,cancelled"`
	Attempts       int32      `json:"attempts"`
	MaxAttempts    int32      `json:"max_attempts"`
	FailureCode    *string    `json:"failure_code"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}
type RebuildService struct {
	knowledge *Service
	queue     *jobs.Store
}

func NewRebuildService(pool *pgxpool.Pool, queue *jobs.Store) (*RebuildService, error) {
	if !queue.UsesPool(pool) {
		return nil, errors.New("rebuild and queue must share a pool")
	}
	service, err := NewService(pool)
	if err != nil {
		return nil, err
	}
	return &RebuildService{knowledge: service, queue: queue}, nil
}

// Create persists intent only. The old binding remains active until fenced
// background completion verifies coverage and atomically switches it.
func (s *RebuildService) Create(ctx context.Context, actor Actor, kbID int64, key string, input RebuildRequest) (_ Rebuild, resultErr error) {
	if kbID <= 0 || key == "" || !validIdempotencyKey(key) || input.TargetSpaceID <= 0 || input.Revision <= 0 || input.Revision == math.MaxInt32 {
		return Rebuild{}, ErrInvalidRequest
	}
	tx, q, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return Rebuild{}, err
	}
	defer rollback(tx, &resultErr)
	base, err := lockConfigAccess(ctx, q, actor, kbID)
	if err != nil {
		return Rebuild{}, err
	}
	if base.Status != "active" {
		return Rebuild{}, ErrArchived
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return Rebuild{}, err
	}
	hash := sha256.Sum256(encoded)
	operation := fmt.Sprintf("kb.rebuild/%d", kbID)
	// The KB lock serializes this operation before any idempotency write.
	prior, err := q.FindIdempotency(ctx, dbquery.FindIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key})
	if err == nil {
		if !bytes.Equal(prior.RequestHash, hash[:]) {
			return Rebuild{}, ErrIdempotencyConflict
		}
		if !prior.ObjectID.Valid {
			return Rebuild{}, errors.New("incomplete rebuild reservation")
		}
		result, err := readRebuild(ctx, q, actor.TenantID, kbID, prior.ObjectID.Int64)
		if err != nil {
			return Rebuild{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Rebuild{}, err
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Rebuild{}, err
	}
	if base.ConfigRevision != input.Revision {
		return Rebuild{}, ErrRevisionConflict
	}
	if base.EmbeddingSpaceID == input.TargetSpaceID {
		return Rebuild{}, ErrInvalidRequest
	}
	target, err := q.RebuildTarget(ctx, dbquery.RebuildTargetParams{TenantID: actor.TenantID, SpaceID: input.TargetSpaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rebuild{}, ErrNotFound
	}
	if err != nil {
		return Rebuild{}, err
	}
	if target.Distance != "cosine" {
		return Rebuild{}, ErrRebuildTargetUnsupported
	}
	tenantPolicy, err := q.TenantEgressPolicy(ctx, actor.TenantID)
	if err != nil {
		return Rebuild{}, err
	}
	within, err := models.PolicyWithin(models.EgressPolicy(tenantPolicy), models.EgressPolicy(base.EgressPolicy))
	if err != nil {
		return Rebuild{}, err
	}
	if !within || (target.Locality == string(models.External) && base.EgressPolicy != string(models.Allow)) {
		return Rebuild{}, deny(ctx, tx, q, actor, kbID, 0, "", "kb.rebuild.create", models.ErrEgressDenied)
	}
	_, err = q.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, RequestHash: hash[:]})
	if err != nil {
		return Rebuild{}, err
	}
	count, err := q.ReserveRebuildSpace(ctx, dbquery.ReserveRebuildSpaceParams{TenantID: actor.TenantID, KbID: kbID, SourceSpaceID: base.EmbeddingSpaceID, SourceRevision: base.ConfigRevision, TargetSpaceID: pgtype.Int8{Int64: target.ID, Valid: true}})
	if err != nil {
		return Rebuild{}, err
	}
	if count != 1 {
		return Rebuild{}, ErrRebuildInProgress
	}
	payload, err := json.Marshal(struct {
		KBID int64 `json:"kb_id"`
	}{kbID})
	if err != nil {
		return Rebuild{}, err
	}
	jobID, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{Kind: jobs.Reembed, Payload: payload, MaxAttempts: 3})
	if err != nil {
		return Rebuild{}, err
	}
	_, err = q.InsertEmbeddingRebuild(ctx, dbquery.InsertEmbeddingRebuildParams{JobID: jobID, TenantID: actor.TenantID, KbID: kbID, SourceSpaceID: base.EmbeddingSpaceID, SourceRevision: base.ConfigRevision, TargetSpaceID: target.ID, TargetProviderID: target.ProviderID, TargetModel: target.Model, TargetDims: target.Dims, TargetNormalized: target.Normalized, TargetDistance: target.Distance, UserID: actor.UserID})
	if err != nil {
		return Rebuild{}, err
	}
	if err := q.AuditEmbeddingRebuild(ctx, dbquery.AuditEmbeddingRebuildParams{TenantID: actor.TenantID, KbID: kbID, JobID: jobID, UserID: actor.UserID, SourceSpaceID: base.EmbeddingSpaceID, TargetSpaceID: target.ID, SourceRevision: base.ConfigRevision}); err != nil {
		return Rebuild{}, err
	}
	count, err = q.CompleteIdempotency(ctx, dbquery.CompleteIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, ObjectID: pgtype.Int8{Int64: jobID, Valid: true}})
	if err != nil {
		return Rebuild{}, err
	}
	if count != 1 {
		return Rebuild{}, errors.New("rebuild reservation lost")
	}
	result, err := readRebuild(ctx, q, actor.TenantID, kbID, jobID)
	if err != nil {
		return Rebuild{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Rebuild{}, err
	}
	return result, nil
}
func (s *RebuildService) Get(ctx context.Context, actor Actor, kbID, jobID int64) (_ Rebuild, resultErr error) {
	if kbID <= 0 || jobID <= 0 {
		return Rebuild{}, ErrInvalidRequest
	}
	tx, q, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return Rebuild{}, err
	}
	defer rollback(tx, &resultErr)
	if _, err := lockConfigAccess(ctx, q, actor, kbID); err != nil {
		return Rebuild{}, err
	}
	result, err := readRebuild(ctx, q, actor.TenantID, kbID, jobID)
	if err != nil {
		return Rebuild{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Rebuild{}, err
	}
	return result, nil
}
func readRebuild(ctx context.Context, q *dbquery.Queries, tenant, kbID, jobID int64) (Rebuild, error) {
	row, err := q.GetEmbeddingRebuild(ctx, dbquery.GetEmbeddingRebuildParams{TenantID: tenant, KbID: kbID, JobID: jobID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rebuild{}, ErrNotFound
	}
	if err != nil {
		return Rebuild{}, err
	}
	result := Rebuild{JobID: row.JobID, SourceSpaceID: row.SourceSpaceID, SourceRevision: row.SourceRevision, TargetSpaceID: row.TargetSpaceID, Status: row.Status, Attempts: row.Attempts, MaxAttempts: row.MaxAttempts, CreatedAt: row.CreatedAt.Time}
	if row.LastError.Valid {
		result.FailureCode = &row.LastError.String
	}
	if row.FinishedAt.Valid {
		result.FinishedAt = &row.FinishedAt.Time
	}
	return result, nil
}
