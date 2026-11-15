package retrieval

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/secrets"
)

type TokenCounter func(context.Context, string) (int, error)

type Request struct {
	KnowledgeBaseID       int64
	Query                 string
	Filters               Filters
	Limit                 int
	Rerank                *RerankOptions
	RerankEnabled         *bool // nil inherits the saved setting; false explicitly disables it.
	Debug                 bool
	ExpectedConfiguration *ConfigurationSnapshot
}

type RerankOptions struct {
	ProviderID int64    `json:"provider_id" minimum:"1"`
	Model      string   `json:"model" minLength:"1" maxLength:"200"`
	TopN       int      `json:"top_n,omitempty" enum:"20,30,50" default:"30"`
	Fallback   bool     `json:"fallback,omitempty"`
	MinScore   *float64 `json:"min_score,omitempty"`
}

type Trace struct {
	RetrievalRevision int32              `json:"retrieval_revision"`
	Candidates        []Candidate        `json:"candidates"`
	TimingsMS         map[string]float64 `json:"timings_ms"`
	Degraded          []string           `json:"degraded"`
	Filters           Filters            `json:"filters"`
}

type Result struct {
	RetrievalRevision int32
	Candidates        []Candidate
	Degraded          []string
	Trace             *Trace
}

type Service struct {
	driver  *Driver
	pool    *pgxpool.Pool
	catalog *models.Catalog
	count   TokenCounter
	slots   chan struct{}
}

type LexicalDriverStatus struct {
	Driver   string   `json:"driver"`
	Degraded []string `json:"degraded,omitempty"`
}

func NewService(pool *pgxpool.Pool, vault *secrets.Vault, count TokenCounter) (*Service, error) {
	if count == nil {
		return nil, errors.New("query token counter is required")
	}
	driver, err := NewDriver(pool)
	if err != nil {
		return nil, err
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		return nil, err
	}
	return &Service{driver: driver, pool: pool, catalog: catalog, count: count, slots: make(chan struct{}, 8)}, nil
}

// NewServiceWithMode selects the lexical driver ("auto", "pg_search", or "compat")
// and returns the active driver status and any capability degradations.
func NewServiceWithMode(ctx context.Context, pool *pgxpool.Pool, vault *secrets.Vault, count TokenCounter, mode string) (*Service, LexicalDriverStatus, error) {
	service, err := NewService(pool, vault, count)
	if err != nil {
		return nil, LexicalDriverStatus{}, err
	}
	switch strings.TrimSpace(mode) {
	case "", "auto":
		hasPGSearch, err := indexing.DetectPGSearch(ctx, pool)
		if err != nil {
			return nil, LexicalDriverStatus{}, err
		}
		if hasPGSearch {
			return service, LexicalDriverStatus{Driver: "pg_search"}, nil
		}
		compat, err := NewCompatibilityTokenizer()
		if err != nil {
			return nil, LexicalDriverStatus{}, fmt.Errorf("init compatible lexical tokenizer: %w", err)
		}
		service.driver.compatibility = compat
		return service, LexicalDriverStatus{
			Driver:   "compat",
			Degraded: []string{"bm25_idf_disabled", "using_tsvector_gse"},
		}, nil
	case "pg_search":
		hasPGSearch, err := indexing.DetectPGSearch(ctx, pool)
		if err != nil {
			return nil, LexicalDriverStatus{}, err
		}
		if !hasPGSearch {
			return nil, LexicalDriverStatus{}, errors.New("pg_search extension is required when TUSKRAG_LEXICAL_DRIVER=pg_search")
		}
		return service, LexicalDriverStatus{Driver: "pg_search"}, nil
	case "compat":
		compat, err := NewCompatibilityTokenizer()
		if err != nil {
			return nil, LexicalDriverStatus{}, fmt.Errorf("init compatible lexical tokenizer: %w", err)
		}
		service.driver.compatibility = compat
		return service, LexicalDriverStatus{
			Driver:   "compat",
			Degraded: []string{"bm25_idf_disabled", "using_tsvector_gse"},
		}, nil
	default:
		return nil, LexicalDriverStatus{}, fmt.Errorf("unsupported lexical driver mode %q", mode)
	}
}

// Search rechecks all evidence after model calls, including debug candidates.
func (s *Service) ExactMatch(ctx context.Context, actor Actor, kbID int64, pattern string, useRegex bool, limit int) ([]Candidate, error) {
	if s == nil || s.driver == nil {
		return nil, errors.New("retrieval service unavailable")
	}
	return s.driver.ExactMatch(ctx, actor, kbID, pattern, useRegex, limit)
}

func (s *Service) Search(ctx context.Context, actor Actor, request Request) (Result, error) {
	if s == nil || s.driver == nil || s.pool == nil || s.catalog == nil || s.count == nil || ctx == nil ||
		actor.TenantID <= 0 || actor.UserID <= 0 || request.KnowledgeBaseID <= 0 || request.Limit < 0 || request.Limit > 30 {
		return Result{}, ErrInvalidQuery
	}
	query, err := normalizeQuery(request.Query)
	if err != nil {
		return Result{}, err
	}
	configuration, err := s.driver.searchConfig(ctx, actor, request.KnowledgeBaseID)
	if err != nil {
		return Result{}, err
	}
	if request.ExpectedConfiguration != nil && *request.ExpectedConfiguration != configuration.snapshot() {
		return Result{}, ErrConfigurationChanged
	}
	binding, revision := configuration.Binding, configuration.Revision
	request, err = resolveSearchOptions(request, configuration.Limit, configuration.Reranker)
	if err != nil {
		return Result{}, err
	}
	if request.Rerank != nil {
		options := *request.Rerank
		if options.TopN == 0 {
			options.TopN = 30
		}
		if options.ProviderID <= 0 || strings.TrimSpace(options.Model) == "" || len(options.Model) > 200 || (options.TopN != 20 && options.TopN != 30 && options.TopN != 50) {
			return Result{}, ErrInvalidQuery
		}
		if options.MinScore != nil && (math.IsNaN(*options.MinScore) || math.IsInf(*options.MinScore, 0)) {
			return Result{}, ErrInvalidQuery
		}
		request.Rerank = &options
	}
	timings := map[string]float64{}
	tags, err := copyFilters(request.Filters.DocumentTags, 256)
	if err != nil {
		return Result{}, err
	}
	mimes, err := copyFilters(request.Filters.MIMEs, 255)
	if err != nil {
		return Result{}, err
	}
	request.Filters.DocumentTags, request.Filters.MIMEs = tags, mimes
	if request.Filters.UpdatedAfter != nil {
		value := request.Filters.UpdatedAfter.UTC()
		request.Filters.UpdatedAfter = &value
	}
	if binding.Distance != "cosine" {
		return Result{}, indexing.ErrUnsupportedDistance
	}
	tokens, err := s.count(ctx, query)
	if err != nil {
		return Result{}, fmt.Errorf("count search query: %w", err)
	}
	if tokens <= 0 || tokens > 800 {
		return Result{}, ErrInvalidQuery
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	modelBinding := models.EmbeddingBinding{TenantID: actor.TenantID, KnowledgeBaseID: request.KnowledgeBaseID,
		EmbeddingSpaceID: binding.SpaceID, ConfigRevision: binding.Revision}
	resolver := authorizedScope{driver: s.driver, actor: actor, binding: binding, modelBinding: modelBinding}
	auditor, err := models.NewEmbeddingDenialAuditor(s.pool, actor.TenantID, actor.UserID)
	if err != nil {
		return Result{}, err
	}
	limits := models.ClientLimits{MaxBatch: 1, MaxRequestBytes: 64 * 1024, MaxResponseBytes: 2 * 1024 * 1024,
		Timeout: 10 * time.Second, Concurrency: 1}
	client, space, err := s.catalog.OpenEmbeddingClient(ctx, modelBinding, limits, resolver, auditor)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	if space.ID != binding.SpaceID || space.Dimensions != binding.Dimensions || space.Normalized != binding.Normalized || space.Distance != binding.Distance {
		return Result{}, ErrBindingChanged
	}
	started := time.Now()
	result, err := client.Embed(ctx, models.EgressScope{TenantID: actor.TenantID, KnowledgeBaseID: request.KnowledgeBaseID}, []string{query})
	timings["embedding"] = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		return Result{}, err
	}
	if result.Usage == nil {
		return Result{}, models.ErrTokenizerUsageMissing
	}
	if result.Usage.PromptTokens != tokens {
		return Result{}, models.ErrTokenizerMismatch
	}
	if len(result.Vectors) != 1 {
		return Result{}, models.ErrInvalidResponse
	}
	started = time.Now()
	candidates, err := s.driver.Recall(ctx, actor, request.KnowledgeBaseID, binding, query, result.Vectors[0], request.Filters, 100)
	timings["recall_rrf"] = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		return Result{}, err
	}
	degraded := []string{"rerank"}
	scores := map[int64]float64{}
	if request.Rerank != nil && len(candidates) > 0 {
		started = time.Now()
		reranker, openErr := s.catalog.OpenRerankClient(ctx, modelBinding, request.Rerank.ProviderID, models.RerankConfig{Model: request.Rerank.Model, Timeout: 10 * time.Second}, resolver, auditor)
		if openErr != nil {
			return Result{}, openErr
		}
		defer reranker.Close()
		documents := make([]string, min(request.Rerank.TopN, len(candidates)))
		for i := range documents {
			documents[i] = strings.Join(candidates[i].HeadingPath, " › ") + "\n" + candidates[i].Content
		}
		ranked, rankErr := reranker.Rerank(ctx, models.EgressScope{TenantID: actor.TenantID, KnowledgeBaseID: request.KnowledgeBaseID}, query, documents)
		timings["rerank"] = float64(time.Since(started).Microseconds()) / 1000
		if rankErr != nil {
			var upstream *models.UpstreamError
			recoverable := errors.Is(rankErr, context.DeadlineExceeded) || (errors.As(rankErr, &upstream) && upstream.Retryable)
			if !request.Rerank.Fallback || ctx.Err() != nil || !recoverable {
				return Result{}, rankErr
			}
		} else {
			degraded = []string{}
			for _, item := range ranked {
				scores[candidates[item.Index].ChunkID] = item.Score
			}
		}
	} else if request.Rerank != nil {
		degraded = []string{}
	}
	// Network calls cannot hold a transaction; repeat the publication and ACL fence.
	started = time.Now()
	rows := make([]dbquery.RecallHybridRow, len(candidates))
	for i, c := range candidates {
		rows[i] = dbquery.RecallHybridRow{ChunkID: c.ChunkID, Score: c.Score, LexicalRank: c.LexicalRank, SemanticRank: c.SemanticRank, LexicalScore: c.LexicalScore, SemanticDistance: c.SemanticDistance}
	}
	var after pgtype.Timestamptz
	if request.Filters.UpdatedAfter != nil {
		after = pgtype.Timestamptz{Time: *request.Filters.UpdatedAfter, Valid: true}
	}
	candidates, err = s.driver.recheck(ctx, actor, request.KnowledgeBaseID, binding, []string{"kb:" + strconv.FormatInt(request.KnowledgeBaseID, 10) + ":all"}, tags, mimes, after, rows, 100, sha256.Sum256([]byte(query)), len(query))
	if err != nil {
		return Result{}, err
	}
	timings["recheck"] = float64(time.Since(started).Microseconds()) / 1000
	for i := range candidates {
		candidates[i].RRFRank = i + 1
		if score, ok := scores[candidates[i].ChunkID]; ok {
			candidates[i].RerankScore = &score
		}
	}
	final := append([]Candidate{}, candidates...)
	if len(degraded) == 0 {
		final = final[:0]
		for _, c := range candidates {
			if c.RerankScore != nil {
				final = append(final, c)
			}
		}
		sort.SliceStable(final, func(i, j int) bool { return *final[i].RerankScore > *final[j].RerankScore })
		for i := range final {
			final[i].RerankRank = i + 1
			final[i].Score = *final[i].RerankScore
		}
		for i := range candidates {
			for _, c := range final {
				if candidates[i].ChunkID == c.ChunkID {
					candidates[i].RerankRank = c.RerankRank
				}
			}
		}
		if request.Rerank.MinScore != nil {
			filtered := final[:0]
			for _, c := range final {
				if c.Score >= *request.Rerank.MinScore {
					filtered = append(filtered, c)
				}
			}
			final = filtered
		}
	}
	if len(final) > request.Limit {
		final = final[:request.Limit]
	}
	output := Result{Candidates: final, Degraded: degraded, RetrievalRevision: revision}
	if request.Debug {
		output.Trace = &Trace{Candidates: candidates, TimingsMS: timings, Degraded: degraded, Filters: request.Filters, RetrievalRevision: revision}
	}
	return output, nil
}

type authorizedScope struct {
	driver       *Driver
	actor        Actor
	binding      Binding
	modelBinding models.EmbeddingBinding
}

func (r authorizedScope) ResolveEmbeddingScope(ctx context.Context, binding models.EmbeddingBinding) (_ models.EgressScope, resultErr error) {
	if binding != r.modelBinding {
		return models.EgressScope{}, models.ErrScopeMismatch
	}
	tx, queries, err := r.driver.begin(ctx, r.actor, binding.KnowledgeBaseID)
	if err != nil {
		return models.EgressScope{}, err
	}
	defer rollback(tx, &resultErr)
	current, err := queries.GetSearchBinding(ctx, dbquery.GetSearchBindingParams{TenantID: r.actor.TenantID, KbID: binding.KnowledgeBaseID, UserID: r.actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EgressScope{}, ErrNotFound
	}
	if err != nil {
		return models.EgressScope{}, fmt.Errorf("read authorized search binding: %w", err)
	}
	if r.binding != (Binding{SpaceID: current.EmbeddingSpaceID, Revision: current.ConfigRevision, Dimensions: current.Dims,
		Distance: current.Distance, Normalized: current.Normalized}) {
		return models.EgressScope{}, ErrBindingChanged
	}
	row, err := queries.GetAuthorizedSearchScope(ctx, dbquery.GetAuthorizedSearchScopeParams{
		TenantID: r.actor.TenantID, KbID: binding.KnowledgeBaseID, UserID: r.actor.UserID,
		SpaceID: binding.EmbeddingSpaceID, ConfigRevision: binding.ConfigRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EgressScope{}, ErrBindingChanged
	}
	if err != nil {
		return models.EgressScope{}, fmt.Errorf("read authorized search scope: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return models.EgressScope{}, fmt.Errorf("commit authorized search scope: %w", err)
	}
	return models.EgressScope{TenantID: row.TenantID, KnowledgeBaseID: row.KbID,
		TenantPolicy: models.EgressPolicy(row.EgressPolicyMax), KnowledgeBasePolicy: models.EgressPolicy(row.EgressPolicy)}, nil
}
