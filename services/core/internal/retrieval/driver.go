package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
)

var (
	ErrNotFound       = errors.New("knowledge base is not visible")
	ErrBindingChanged = errors.New("knowledge base embedding configuration changed")
	ErrInvalidQuery   = errors.New("invalid retrieval query")
)

type Actor struct {
	TenantID int64
	UserID   int64
}

type Binding struct {
	SpaceID    int64
	Revision   int32
	Dimensions int32
	Distance   string
	Normalized bool
}

type Filters struct {
	DocumentTags []string
	MIMEs        []string
	UpdatedAfter *time.Time
}

// Candidate contains an RRF score, not a reranker score.
type Candidate struct {
	RRFRank          int
	RerankScore      *float64
	RerankRank       int
	ChunkID          int64
	DocumentID       int64
	VersionID        int64
	SectionID        int64
	Ordinal          int32  `json:"-"`
	Kind             string `json:"-"`
	TokenCount       int32  `json:"-"`
	MIME             string `json:"-"`
	Title            string
	Content          string
	HeadingPath      []string
	PageStart        *int32
	PageEnd          *int32
	Score            float64
	LexicalRank      int64
	SemanticRank     int64
	LexicalScore     float64
	SemanticDistance float64
}

type Driver struct {
	pool          *pgxpool.Pool
	compatibility *CompatibilityTokenizer
}

func NewDriver(pool *pgxpool.Pool) (*Driver, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &Driver{pool: pool}, nil
}

// Binding reads the active embedding configuration only for a current member.
func (d *Driver) Binding(ctx context.Context, actor Actor, kbID int64) (_ Binding, resultErr error) {
	tx, queries, err := d.begin(ctx, actor, kbID)
	if err != nil {
		return Binding{}, err
	}
	defer rollback(tx, &resultErr)
	row, err := queries.GetSearchBinding(ctx, dbquery.GetSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read retrieval binding: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Binding{}, fmt.Errorf("commit retrieval binding: %w", err)
	}
	return Binding{SpaceID: row.EmbeddingSpaceID, Revision: row.ConfigRevision, Dimensions: row.Dims, Distance: row.Distance, Normalized: row.Normalized}, nil
}

// Recall returns authorized hybrid candidates after rechecking current access.
// The caller supplies a query vector from the bound embedding space.
func (d *Driver) Recall(ctx context.Context, actor Actor, kbID int64, binding Binding, query string, vector []float32, filters Filters, limit int) (_ []Candidate, resultErr error) {
	query, err := normalizeQuery(query)
	if err != nil || binding.SpaceID <= 0 || binding.Revision <= 0 || binding.Dimensions < 64 || binding.Dimensions > 4000 || limit < 1 || limit > 100 {
		return nil, ErrInvalidQuery
	}
	encoded, err := encodeVector(vector, int(binding.Dimensions))
	if err != nil {
		return nil, err
	}
	tags, err := copyFilters(filters.DocumentTags, 256)
	if err != nil {
		return nil, err
	}
	mimes, err := copyFilters(filters.MIMEs, 255)
	if err != nil {
		return nil, err
	}
	var updatedAfter pgtype.Timestamptz
	if filters.UpdatedAfter != nil {
		updatedAfter = pgtype.Timestamptz{Time: filters.UpdatedAfter.UTC(), Valid: true}
	}
	principal := []string{"kb:" + strconv.FormatInt(kbID, 10) + ":all"}
	tx, queries, err := d.begin(ctx, actor, kbID)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	row, err := queries.GetSearchBinding(ctx, dbquery.GetSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read retrieval binding: %w", err)
	}
	if binding != (Binding{SpaceID: row.EmbeddingSpaceID, Revision: row.ConfigRevision, Dimensions: row.Dims, Distance: row.Distance, Normalized: row.Normalized}) {
		return nil, ErrBindingChanged
	}
	if err := indexing.Check(ctx, tx, binding.SpaceID, binding.Dimensions, binding.Distance); err != nil {
		return nil, err
	}
	// pg_search's ephemeral parallel workers reload the Jieba dictionary on every
	// query. Keep native Top-K on the pooled backend to reuse its tokenizer cache.
	if d.compatibility == nil {
		if _, err := tx.Exec(ctx, "SET LOCAL max_parallel_workers_per_gather = 0"); err != nil {
			return nil, fmt.Errorf("configure lexical execution: %w", err)
		}
	}
	for _, setting := range []string{"SET LOCAL hnsw.ef_search = 100", "SET LOCAL hnsw.iterative_scan = relaxed_order"} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			return nil, fmt.Errorf("configure retrieval transaction: %w", err)
		}
	}
	var ranked []dbquery.RecallHybridRow
	if d.compatibility != nil {
		rows, recallErr := queries.RecallCompatibility(ctx, dbquery.RecallCompatibilityParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, SpaceID: binding.SpaceID, PrincipalTags: principal, QueryTokens: d.compatibility.Query(query), DocTags: tags, Mimes: mimes, UpdatedAfter: updatedAfter, Dimensions: binding.Dimensions, Vector: encoded})
		err = recallErr
		for _, r := range rows {
			ranked = append(ranked, dbquery.RecallHybridRow(r))
		}
	} else {
		ranked, err = queries.RecallHybrid(ctx, dbquery.RecallHybridParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID,
			SpaceID: binding.SpaceID, PrincipalTags: principal, Query: query, DocTags: tags, Mimes: mimes,
			UpdatedAfter: updatedAfter, Dimensions: binding.Dimensions, Vector: encoded})
	}
	if err != nil {
		return nil, fmt.Errorf("recall hybrid candidates: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit hybrid recall: %w", err)
	}
	hash := sha256.Sum256([]byte(query))
	return d.recheck(ctx, actor, kbID, binding, principal, tags, mimes, updatedAfter, ranked, limit, hash, len(query))
}

func (d *Driver) recheck(ctx context.Context, actor Actor, kbID int64, binding Binding, principal, tags, mimes []string,
	updatedAfter pgtype.Timestamptz, ranked []dbquery.RecallHybridRow, limit int, queryHash [sha256.Size]byte, queryBytes int) (_ []Candidate, resultErr error) {
	tx, queries, err := d.begin(ctx, actor, kbID)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	row, err := queries.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock retrieval binding: %w", err)
	}
	if binding != (Binding{SpaceID: row.EmbeddingSpaceID, Revision: row.ConfigRevision, Dimensions: row.Dims, Distance: row.Distance, Normalized: row.Normalized}) {
		return nil, ErrBindingChanged
	}
	ids := make([]int64, 0, len(ranked))
	for _, item := range ranked {
		if item.ChunkID <= 0 || item.Score <= 0 || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) ||
			item.LexicalRank < 0 || item.LexicalRank > 50 || item.SemanticRank < 0 || item.SemanticRank > 50 ||
			(item.LexicalRank == 0 && item.SemanticRank == 0) || math.IsNaN(item.LexicalScore) ||
			math.IsInf(item.LexicalScore, 0) || math.IsNaN(item.SemanticDistance) || math.IsInf(item.SemanticDistance, 0) {
			return nil, errors.New("retrieval returned an invalid candidate")
		}
		ids = append(ids, item.ChunkID)
	}
	visible, err := queries.RecheckSearchEvidence(ctx, dbquery.RecheckSearchEvidenceParams{
		TenantID: actor.TenantID, KbID: kbID, ChunkIds: ids, PrincipalTags: principal,
		DocTags: tags, Mimes: mimes, UpdatedAfter: updatedAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("recheck retrieval evidence: %w", err)
	}
	byID := make(map[int64]dbquery.RecheckSearchEvidenceRow, len(visible))
	for _, item := range visible {
		byID[item.ID] = item
	}
	result := make([]Candidate, 0, min(limit, len(visible)))
	for _, item := range ranked {
		evidence, ok := byID[item.ChunkID]
		if !ok {
			continue
		}
		candidate := Candidate{ChunkID: item.ChunkID, DocumentID: evidence.DocumentID, VersionID: evidence.VersionID,
			SectionID: evidence.SectionID, Ordinal: evidence.Ordinal, Kind: evidence.Kind, TokenCount: evidence.TokenCount,
			MIME: evidence.Mime, Title: evidence.Title, Content: evidence.Content, HeadingPath: evidence.HeadingPath,
			Score: item.Score, LexicalRank: item.LexicalRank, SemanticRank: item.SemanticRank,
			LexicalScore: item.LexicalScore, SemanticDistance: item.SemanticDistance}
		if evidence.PageStart.Valid != evidence.PageEnd.Valid {
			return nil, errors.New("retrieval evidence has an invalid page range")
		}
		if evidence.PageStart.Valid && evidence.Mime == "application/pdf" {
			candidate.PageStart, candidate.PageEnd = &evidence.PageStart.Int32, &evidence.PageEnd.Int32
		}
		result = append(result, candidate)
		if len(result) == limit {
			break
		}
	}
	if err := queries.AuditSearch(ctx, dbquery.AuditSearchParams{TenantID: actor.TenantID, UserID: actor.UserID, KbID: kbID,
		QueryHash: hex.EncodeToString(queryHash[:]), QueryBytes: int32(queryBytes), EvidenceCount: int32(len(result))}); err != nil {
		return nil, fmt.Errorf("audit retrieval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit retrieval access recheck: %w", err)
	}
	return result, nil
}

func normalizeQuery(query string) (string, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" || len(query) > 4096 || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", ErrInvalidQuery
	}
	return query, nil
}

func (d *Driver) begin(ctx context.Context, actor Actor, kbID int64) (pgx.Tx, *dbquery.Queries, error) {
	if d == nil || d.pool == nil || ctx == nil || actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 {
		return nil, nil, ErrInvalidQuery
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin retrieval transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, actor.TenantID); err != nil {
		result := fmt.Errorf("set retrieval tenant: %w", err)
		rollback(tx, &result)
		return nil, nil, result
	}
	if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`); err != nil {
		result := fmt.Errorf("set retrieval plan mode: %w", err)
		rollback(tx, &result)
		return nil, nil, result
	}
	return tx, queries, nil
}

func rollback(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback retrieval transaction: %w", err))
	}
}

func encodeVector(vector []float32, dimensions int) (string, error) {
	if len(vector) != dimensions {
		return "", ErrInvalidQuery
	}
	encoded := make([]byte, 0, dimensions*8)
	encoded = append(encoded, '[')
	var representable bool
	for index, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || math.Abs(float64(value)) > 65504 {
			return "", ErrInvalidQuery
		}
		if math.Abs(float64(value)) > math.Ldexp(1, -25) {
			representable = true
		}
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded = strconv.AppendFloat(encoded, float64(value), 'g', -1, 32)
	}
	if !representable {
		return "", ErrInvalidQuery
	}
	return string(append(encoded, ']')), nil
}

func copyFilters(values []string, maxLength int) ([]string, error) {
	if len(values) > 64 {
		return nil, ErrInvalidQuery
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || len(value) > maxLength || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return nil, ErrInvalidQuery
		}
		result = append(result, value)
	}
	return result, nil
}
