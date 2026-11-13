package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

var (
	ErrReportInvalid   = errors.New("invalid evaluation report")
	ErrReportNotFound  = errors.New("evaluation report or evidence not visible")
	ErrReportForbidden = errors.New("evaluation report requires knowledge base admin")
	ErrReportArchived  = errors.New("archived knowledge base cannot import reports")
)

type StoredReport struct {
	Digest    string    `json:"digest"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	Report    RunReport `json:"report"`
}

type ReportStore struct{ pool *pgxpool.Pool }

func NewReportStore(pool *pgxpool.Pool) (*ReportStore, error) {
	if pool == nil {
		return nil, errors.New("report database pool is required")
	}
	return &ReportStore{pool: pool}, nil
}

// Import stores an immutable, explicitly imported artifact. Its digest makes
// identical retries idempotent; it does not attest that the server ran the eval.
func (s *ReportStore) Import(ctx context.Context, actor retrieval.Actor, kbID int64, report RunReport) (_ StoredReport, resultErr error) {
	evidence, docs, chunks, err := reportBindings(report)
	if err != nil {
		return StoredReport{}, err
	}
	data, err := json.Marshal(report)
	if err != nil || len(data) > 8*1024*1024 {
		return StoredReport{}, ErrReportInvalid
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredReport{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return StoredReport{}, err
	}
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, evidence, true); err != nil {
		return StoredReport{}, err
	}
	count, err := q.StoreEvalReport(ctx, dbquery.StoreEvalReportParams{TenantID: actor.TenantID, KbID: kbID, Digest: digest, Report: data, UserID: actor.UserID})
	if err != nil {
		return StoredReport{}, fmt.Errorf("store evaluation report: %w", err)
	}
	if count == 1 {
		if err := q.AuditEvalReport(ctx, dbquery.AuditEvalReportParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, Digest: digest}); err != nil {
			return StoredReport{}, fmt.Errorf("audit evaluation report: %w", err)
		}
	}
	stored, err := loadReport(ctx, q, actor.TenantID, kbID, digest)
	if err != nil {
		return StoredReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredReport{}, fmt.Errorf("commit evaluation import: %w", err)
	}
	return stored, nil
}

func (s *ReportStore) Get(ctx context.Context, actor retrieval.Actor, kbID int64, digest string) (_ StoredReport, resultErr error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
		return StoredReport{}, ErrReportInvalid
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredReport{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return StoredReport{}, err
	}
	stored, err := loadReport(ctx, q, actor.TenantID, kbID, digest)
	if err != nil {
		return StoredReport{}, err
	}
	evidence, docs, chunks, err := reportBindings(stored.Report)
	if err != nil {
		return StoredReport{}, fmt.Errorf("stored report is corrupt: %w", err)
	}
	if err := checkReportAccess(ctx, q, actor, kbID, docs, chunks, evidence, false); err != nil {
		return StoredReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredReport{}, fmt.Errorf("commit evaluation read: %w", err)
	}
	return stored, nil
}

// Check before looking up a digest; recheck under locks before returning data.
func reportMembership(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64) error {
	base, err := q.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("read report membership: %w", err)
	}
	if base.Role != "owner" && base.Role != "admin" {
		return ErrReportForbidden
	}
	return nil
}

func (s *ReportStore) begin(ctx context.Context, actor retrieval.Actor, kbID int64) (pgx.Tx, *dbquery.Queries, error) {
	if actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 {
		return nil, nil, ErrReportInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin evaluation report: %w", err)
	}
	q := dbquery.New(tx)
	if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
		result := fmt.Errorf("set evaluation tenant: %w", err)
		finishEvaluationTransaction(tx, &result)
		return nil, nil, result
	}
	return tx, q, nil
}

func finishEvaluationTransaction(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback evaluation transaction: %w", err))
	}
}

func loadReport(ctx context.Context, q *dbquery.Queries, tenantID, kbID int64, digest string) (StoredReport, error) {
	row, err := q.GetEvalReport(ctx, dbquery.GetEvalReportParams{TenantID: tenantID, KbID: kbID, Digest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredReport{}, ErrReportNotFound
	}
	if err != nil {
		return StoredReport{}, fmt.Errorf("read evaluation report: %w", err)
	}
	result := StoredReport{Digest: row.Digest, Source: row.Source, CreatedAt: row.CreatedAt.Time}
	if err := json.Unmarshal(row.Report, &result.Report); err != nil {
		return StoredReport{}, fmt.Errorf("decode evaluation report: %w", err)
	}
	return result, nil
}

func reportBindings(report RunReport) (map[int64]Evidence, []int64, []int64, error) {
	if len(report.Rows) > 10000 {
		return nil, nil, nil, ErrReportInvalid
	}
	if err := ValidateReport(report); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", ErrReportInvalid, err)
	}
	evidence := map[int64]Evidence{}
	documents := map[int64]bool{}
	for _, row := range report.Rows {
		if len(row.Fused) > 100 || len(row.Ranked) > 100 {
			return nil, nil, nil, ErrReportInvalid
		}
		for _, items := range [][]Evidence{row.Fused, row.Ranked} {
			for _, item := range items {
				if previous, ok := evidence[item.ChunkID]; ok && previous != item {
					return nil, nil, nil, ErrReportInvalid
				}
				evidence[item.ChunkID] = item
				documents[item.DocumentID] = true
			}
		}
	}
	if len(evidence) > 10000 {
		return nil, nil, nil, ErrReportInvalid
	}
	docs := make([]int64, 0, len(documents))
	chunks := make([]int64, 0, len(evidence))
	for id := range documents {
		docs = append(docs, id)
	}
	for id := range evidence {
		chunks = append(chunks, id)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i] < docs[j] })
	sort.Slice(chunks, func(i, j int) bool { return chunks[i] < chunks[j] })
	return evidence, docs, chunks, nil
}

func checkReportAccess(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, docs, chunks []int64, expected map[int64]Evidence, writing bool) error {
	locked, err := q.LockEvalDocuments(ctx, dbquery.LockEvalDocumentsParams{TenantID: actor.TenantID, KbID: kbID, DocumentIds: docs})
	if err != nil {
		return fmt.Errorf("lock evaluation documents: %w", err)
	}
	status, err := q.LockEvalKnowledgeBase(ctx, dbquery.LockEvalKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("lock evaluation knowledge base: %w", err)
	}
	role, err := q.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("lock evaluation membership: %w", err)
	}
	if role != "owner" && role != "admin" {
		return ErrReportForbidden
	}
	if writing && status != "active" {
		return ErrReportArchived
	}
	if len(locked) != len(docs) {
		return ErrReportNotFound
	}
	rows, err := q.EvalReportEvidence(ctx, dbquery.EvalReportEvidenceParams{TenantID: actor.TenantID, KbID: kbID, ChunkIds: chunks})
	if err != nil {
		return fmt.Errorf("recheck evaluation evidence: %w", err)
	}
	if len(rows) != len(expected) {
		return ErrReportNotFound
	}
	for _, row := range rows {
		actual := Evidence{ChunkID: row.ID, DocumentID: row.DocumentID, SectionID: row.SectionID, PageStart: row.PageStart.Int32, PageEnd: row.PageEnd.Int32}
		if actual != expected[row.ID] {
			return ErrReportNotFound
		}
	}
	return nil
}
