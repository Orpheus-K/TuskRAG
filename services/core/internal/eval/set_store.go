package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

var (
	ErrSetNotFound            = errors.New("evaluation set or expected location not visible")
	ErrSetForbidden           = errors.New("evaluation set requires knowledge base admin")
	ErrSetArchived            = errors.New("archived knowledge base cannot save evaluation sets")
	ErrSetConflict            = errors.New("evaluation set revision changed")
	ErrSetIdempotencyConflict = errors.New("evaluation set idempotency key reused with different content")
)

type StoredSet struct {
	ID         int64         `json:"id"`
	Revision   int32         `json:"revision"`
	Digest     string        `json:"digest"`
	CreatedAt  time.Time     `json:"created_at"`
	Definition SetDefinition `json:"definition"`
}

type SetStore struct{ pool *pgxpool.Pool }

func NewSetStore(pool *pgxpool.Pool) (*SetStore, error) {
	if pool == nil {
		return nil, errors.New("evaluation set database pool is required")
	}
	return &SetStore{pool: pool}, nil
}

func (s *SetStore) Create(ctx context.Context, actor retrieval.Actor, kbID int64, key string, definition SetDefinition) (StoredSet, error) {
	if len(key) > 128 {
		return StoredSet{}, ErrInvalidCase
	}
	for _, c := range key {
		if c < 0x21 || c > 0x7e {
			return StoredSet{}, ErrInvalidCase
		}
	}
	return s.save(ctx, actor, kbID, 0, 0, key, definition)
}

// Update appends a revision using compare-and-swap; it never modifies history.
func (s *SetStore) Update(ctx context.Context, actor retrieval.Actor, kbID, id int64, expected int32, definition SetDefinition) (StoredSet, error) {
	if id <= 0 || expected <= 0 {
		return StoredSet{}, ErrInvalidCase
	}
	return s.save(ctx, actor, kbID, id, expected, "", definition)
}

func (s *SetStore) save(ctx context.Context, actor retrieval.Actor, kbID, id int64, expected int32, key string, definition SetDefinition) (_ StoredSet, resultErr error) {
	if id < 0 || expected < 0 || expected == math.MaxInt32 || (id == 0) != (expected == 0) {
		return StoredSet{}, ErrInvalidCase
	}
	if err := ValidateSet(definition); err != nil {
		return StoredSet{}, err
	}
	data, err := json.Marshal(definition)
	if err != nil {
		return StoredSet{}, fmt.Errorf("encode evaluation set: %w", err)
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredSet{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return StoredSet{}, err
	}
	if err := checkSetAccess(ctx, q, actor, kbID, definition, true); err != nil {
		return StoredSet{}, err
	}
	operation := fmt.Sprintf("eval.set.create/%d", kbID)
	if key != "" {
		_, err := q.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, RequestHash: hash[:]})
		if errors.Is(err, pgx.ErrNoRows) {
			prior, err := q.FindIdempotency(ctx, dbquery.FindIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key})
			if err != nil {
				return StoredSet{}, fmt.Errorf("read evaluation set retry: %w", err)
			}
			if !bytes.Equal(prior.RequestHash, hash[:]) {
				return StoredSet{}, ErrSetIdempotencyConflict
			}
			if !prior.ObjectID.Valid {
				return StoredSet{}, errors.New("incomplete evaluation set idempotency record")
			}
			row, err := q.GetEvalSetRevision(ctx, dbquery.GetEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: prior.ObjectID.Int64, Revision: 1})
			if err != nil {
				return StoredSet{}, fmt.Errorf("read original evaluation set: %w", err)
			}
			if row.Digest != digest {
				return StoredSet{}, errors.New("evaluation set retry digest mismatch")
			}
			if err := tx.Commit(ctx); err != nil {
				return StoredSet{}, fmt.Errorf("commit evaluation set retry: %w", err)
			}
			return StoredSet{ID: row.SetID, Revision: 1, Digest: digest, CreatedAt: row.CreatedAt.Time, Definition: definition}, nil
		}
		if err != nil {
			return StoredSet{}, fmt.Errorf("reserve evaluation set retry: %w", err)
		}
	}
	revision := int32(1)
	if id == 0 {
		id, err = q.CreateEvalSet(ctx, dbquery.CreateEvalSetParams{TenantID: actor.TenantID, KbID: kbID})
	} else {
		_, err = q.GetEvalSetRevision(ctx, dbquery.GetEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredSet{}, ErrSetNotFound
		}
		if err != nil {
			return StoredSet{}, fmt.Errorf("find evaluation set: %w", err)
		}
		revision, err = q.AdvanceEvalSet(ctx, dbquery.AdvanceEvalSetParams{TenantID: actor.TenantID, KbID: kbID, SetID: id, Expected: expected})
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredSet{}, ErrSetConflict
		}
	}
	if err != nil {
		return StoredSet{}, fmt.Errorf("save evaluation set head: %w", err)
	}
	row, err := q.InsertEvalSetRevision(ctx, dbquery.InsertEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: id, Revision: revision, Definition: data, Digest: digest, UserID: actor.UserID})
	if err != nil {
		return StoredSet{}, fmt.Errorf("save evaluation set revision: %w", err)
	}
	if err := q.AuditEvalSetRevision(ctx, dbquery.AuditEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: id, Revision: revision, Digest: digest, UserID: actor.UserID}); err != nil {
		return StoredSet{}, fmt.Errorf("audit evaluation set: %w", err)
	}
	if key != "" {
		count, err := q.CompleteIdempotency(ctx, dbquery.CompleteIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, ObjectID: pgtype.Int8{Int64: id, Valid: true}})
		if err != nil {
			return StoredSet{}, fmt.Errorf("complete evaluation set retry: %w", err)
		}
		if count != 1 {
			return StoredSet{}, errors.New("evaluation set idempotency reservation lost")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredSet{}, fmt.Errorf("commit evaluation set save: %w", err)
	}
	return StoredSet{ID: id, Revision: revision, Digest: digest, CreatedAt: row.CreatedAt.Time, Definition: definition}, nil
}

// Revision zero selects the current head snapshot; explicit revisions allow
// reproducible runs. Both paths recheck current target visibility.
func (s *SetStore) Get(ctx context.Context, actor retrieval.Actor, kbID, id int64, revision int32) (_ StoredSet, resultErr error) {
	if id <= 0 || revision < 0 {
		return StoredSet{}, ErrInvalidCase
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredSet{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return StoredSet{}, err
	}
	row, err := q.GetEvalSetRevision(ctx, dbquery.GetEvalSetRevisionParams{TenantID: actor.TenantID, KbID: kbID, SetID: id, Revision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredSet{}, ErrSetNotFound
	}
	if err != nil {
		return StoredSet{}, fmt.Errorf("read evaluation set: %w", err)
	}
	var definition SetDefinition
	err = json.Unmarshal(row.Definition, &definition)
	if err != nil {
		return StoredSet{}, fmt.Errorf("stored evaluation set is corrupt: %w", err)
	}
	if err := ValidateSet(definition); err != nil {
		return StoredSet{}, fmt.Errorf("stored evaluation set is corrupt: %w", err)
	}
	data, err := json.Marshal(definition)
	if err != nil {
		return StoredSet{}, fmt.Errorf("encode stored evaluation set: %w", err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != row.Digest {
		return StoredSet{}, errors.New("stored evaluation set digest mismatch")
	}
	if err := checkSetAccess(ctx, q, actor, kbID, definition, false); err != nil {
		return StoredSet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredSet{}, fmt.Errorf("commit evaluation set read: %w", err)
	}
	return StoredSet{ID: id, Revision: row.Revision, Digest: row.Digest, CreatedAt: row.CreatedAt.Time, Definition: definition}, nil
}

func (s *SetStore) begin(ctx context.Context, actor retrieval.Actor, kbID int64) (pgx.Tx, *dbquery.Queries, error) {
	if actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 {
		return nil, nil, ErrInvalidCase
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin evaluation set: %w", err)
	}
	q := dbquery.New(tx)
	if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
		result := fmt.Errorf("set evaluation tenant: %w", err)
		finishEvaluationTransaction(tx, &result)
		return nil, nil, result
	}
	return tx, q, nil
}

func checkSetAccess(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, definition SetDefinition, writing bool) error {
	documents := map[int64]bool{}
	unique := map[ExpectedLocation]bool{}
	locations := make([]ExpectedLocation, 0)
	for _, item := range definition.Cases {
		for _, location := range item.Expected {
			documents[location.DocumentID] = true
			if !unique[location] {
				unique[location] = true
				locations = append(locations, location)
			}
		}
	}
	docs := make([]int64, 0, len(documents))
	for id := range documents {
		docs = append(docs, id)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i] < docs[j] })
	// Reuse the report lock order and current membership/archive checks, without
	// report chunks; authored targets are checked independently below.
	if err := setAccessError(checkReportAccess(ctx, q, actor, kbID, docs, nil, nil, writing)); err != nil {
		return err
	}
	data, err := json.Marshal(locations)
	if err != nil {
		return fmt.Errorf("encode evaluation locations: %w", err)
	}
	count, err := q.CountVisibleEvalSetLocations(ctx, dbquery.CountVisibleEvalSetLocationsParams{TenantID: actor.TenantID, KbID: kbID, Locations: data})
	if err != nil {
		return fmt.Errorf("recheck evaluation locations: %w", err)
	}
	if count != int64(len(locations)) {
		return ErrSetNotFound
	}
	return nil
}

func setAccessError(err error) error {
	switch {
	case errors.Is(err, ErrReportNotFound):
		return ErrSetNotFound
	case errors.Is(err, ErrReportForbidden):
		return ErrSetForbidden
	case errors.Is(err, ErrReportArchived):
		return ErrSetArchived
	default:
		return err
	}
}
