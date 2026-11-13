package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

type StoredAnswerReport struct {
	Digest    string       `json:"digest"`
	Source    string       `json:"source"`
	CreatedAt time.Time    `json:"created_at"`
	Report    AnswerReport `json:"report"`
}

type AnswerReportListItem struct {
	Digest         string               `json:"digest"`
	Source         string               `json:"source"`
	CreatedAt      time.Time            `json:"created_at"`
	DatasetVersion string               `json:"dataset_version"`
	ModelLabel     string               `json:"model_label"`
	ModelKind      string               `json:"model_kind"`
	PromptVersion  string               `json:"prompt_version"`
	Assembly       AnswerAssemblyReport `json:"assembly"`
	Judge          *AnswerJudgeConfig   `json:"judge,omitempty"`
	Summary        AnswerSummary        `json:"summary"`
}

type AnswerReportPage struct {
	Items      []AnswerReportListItem `json:"items"`
	NextCursor *string                `json:"next_cursor" nullable:"true"`
}

type AnswerRowComparison struct {
	ID                       string `json:"id"`
	Answerable               bool   `json:"answerable"`
	BaselineOutcome          string `json:"baseline_outcome"`
	CurrentOutcome           string `json:"current_outcome"`
	BaselineValidCitation    bool   `json:"baseline_valid_citation"`
	CurrentValidCitation     bool   `json:"current_valid_citation"`
	BaselineSourceIntegrity  bool   `json:"baseline_source_integrity"`
	CurrentSourceIntegrity   bool   `json:"current_source_integrity"`
	BaselineCitationCount    int    `json:"baseline_citation_count"`
	CurrentCitationCount     int    `json:"current_citation_count"`
	BaselineInvalidCitations int    `json:"baseline_invalid_citations"`
	CurrentInvalidCitations  int    `json:"current_invalid_citations"`
}

type AnswerReportComparisonView struct {
	Gate     string                `json:"gate" enum:"passed,regressed"`
	Baseline AnswerReportListItem  `json:"baseline"`
	Current  AnswerReportListItem  `json:"current"`
	Delta    AnswerMetricDelta     `json:"delta"`
	Total    int                   `json:"total"`
	Offset   int32                 `json:"offset"`
	Limit    int32                 `json:"limit"`
	Rows     []AnswerRowComparison `json:"rows"`
}

type AnswerReportStore struct{ pool *pgxpool.Pool }

func NewAnswerReportStore(pool *pgxpool.Pool) (*AnswerReportStore, error) {
	if pool == nil {
		return nil, errors.New("answer report database pool is required")
	}
	return &AnswerReportStore{pool: pool}, nil
}

// Import stores an immutable, explicitly imported E6 answer evaluation artifact.
func (s *AnswerReportStore) Import(ctx context.Context, actor retrieval.Actor, kbID int64, report AnswerReport) (_ StoredAnswerReport, resultErr error) {
	docs, err := answerReportDocumentIDs(report)
	if err != nil {
		return StoredAnswerReport{}, err
	}
	data, err := json.Marshal(report)
	if err != nil || len(data) > 8*1024*1024 {
		return StoredAnswerReport{}, ErrReportInvalid
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredAnswerReport{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return StoredAnswerReport{}, err
	}
	if err := checkAnswerReportAccess(ctx, q, actor, kbID, docs, true); err != nil {
		return StoredAnswerReport{}, err
	}
	count, err := q.StoreAnswerEvalReport(ctx, dbquery.StoreAnswerEvalReportParams{
		TenantID: actor.TenantID, KbID: kbID, Digest: digest, Report: data, UserID: actor.UserID,
	})
	if err != nil {
		return StoredAnswerReport{}, fmt.Errorf("store answer evaluation report: %w", err)
	}
	if count == 1 {
		if err := q.AuditAnswerEvalReport(ctx, dbquery.AuditAnswerEvalReportParams{
			TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, Digest: digest,
		}); err != nil {
			return StoredAnswerReport{}, fmt.Errorf("audit answer evaluation report: %w", err)
		}
	}
	stored, err := loadAnswerReport(ctx, q, actor.TenantID, kbID, digest)
	if err != nil {
		return StoredAnswerReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredAnswerReport{}, fmt.Errorf("commit answer evaluation import: %w", err)
	}
	return stored, nil
}

func (s *AnswerReportStore) Get(ctx context.Context, actor retrieval.Actor, kbID int64, digest string) (_ StoredAnswerReport, resultErr error) {
	if !validReportDigest(digest) {
		return StoredAnswerReport{}, ErrReportInvalid
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return StoredAnswerReport{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return StoredAnswerReport{}, err
	}
	stored, err := loadAnswerReport(ctx, q, actor.TenantID, kbID, digest)
	if err != nil {
		return StoredAnswerReport{}, err
	}
	docs, err := answerReportDocumentIDs(stored.Report)
	if err != nil {
		return StoredAnswerReport{}, fmt.Errorf("stored answer report is corrupt: %w", err)
	}
	if err := checkAnswerReportAccess(ctx, q, actor, kbID, docs, false); err != nil {
		return StoredAnswerReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredAnswerReport{}, fmt.Errorf("commit answer evaluation read: %w", err)
	}
	return stored, nil
}

func (s *AnswerReportStore) List(ctx context.Context, actor retrieval.Actor, kbID int64, cursor string, limit int32) (_ AnswerReportPage, resultErr error) {
	if limit < 1 || limit > 10 {
		return AnswerReportPage{}, ErrReportInvalid
	}
	if cursor != "" && !validReportDigest(cursor) {
		return AnswerReportPage{}, ErrReportInvalid
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return AnswerReportPage{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return AnswerReportPage{}, err
	}
	rows, err := q.ListVisibleAnswerEvalReports(ctx, dbquery.ListVisibleAnswerEvalReportsParams{
		TenantID: actor.TenantID, KbID: kbID, Cursor: cursor, PageSize: limit + 1,
	})
	if err != nil {
		return AnswerReportPage{}, fmt.Errorf("list answer evaluation reports: %w", err)
	}
	hasMore := len(rows) > int(limit)
	if hasMore {
		rows = rows[:limit]
	}
	docSet := map[int64]bool{}
	items := make([]AnswerReportListItem, 0, len(rows))
	for _, row := range rows {
		var meta struct {
			DatasetVersion string               `json:"dataset_version"`
			ModelLabel     string               `json:"model_label"`
			ModelKind      string               `json:"model_kind"`
			PromptVersion  string               `json:"prompt_version"`
			Assembly       AnswerAssemblyReport `json:"assembly"`
			Judge          *AnswerJudgeConfig   `json:"judge,omitempty"`
			Summary        AnswerSummary        `json:"summary"`
		}
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			return AnswerReportPage{}, fmt.Errorf("decode answer report metadata: %w", err)
		}
		var docIDs []int64
		if err := json.Unmarshal(row.DocumentIds, &docIDs); err != nil {
			return AnswerReportPage{}, fmt.Errorf("decode answer report documents: %w", err)
		}
		for _, id := range docIDs {
			docSet[id] = true
		}
		items = append(items, AnswerReportListItem{
			Digest: row.Digest, Source: row.Source, CreatedAt: row.CreatedAt.Time,
			DatasetVersion: meta.DatasetVersion, ModelLabel: meta.ModelLabel, ModelKind: meta.ModelKind,
			PromptVersion: meta.PromptVersion, Assembly: meta.Assembly, Judge: meta.Judge, Summary: meta.Summary,
		})
	}
	docs := make([]int64, 0, len(docSet))
	for id := range docSet {
		docs = append(docs, id)
	}
	slices.Sort(docs)
	if err := checkAnswerReportAccess(ctx, q, actor, kbID, docs, false); err != nil {
		return AnswerReportPage{}, err
	}
	var next *string
	if hasMore && len(items) > 0 {
		value := items[len(items)-1].Digest
		next = &value
	}
	if err := tx.Commit(ctx); err != nil {
		return AnswerReportPage{}, fmt.Errorf("commit answer evaluation list: %w", err)
	}
	return AnswerReportPage{Items: items, NextCursor: next}, nil
}

func (s *AnswerReportStore) Compare(ctx context.Context, actor retrieval.Actor, kbID int64, baselineDigest, currentDigest string, offset, limit int32) (_ AnswerReportComparisonView, resultErr error) {
	if offset < 0 || offset > 10000 || limit < 1 || limit > 50 {
		return AnswerReportComparisonView{}, ErrReportInvalid
	}
	if !validReportDigest(baselineDigest) || !validReportDigest(currentDigest) {
		return AnswerReportComparisonView{}, ErrReportInvalid
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return AnswerReportComparisonView{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := reportMembership(ctx, q, actor, kbID); err != nil {
		return AnswerReportComparisonView{}, err
	}
	baseline, err := loadAnswerReport(ctx, q, actor.TenantID, kbID, baselineDigest)
	if err != nil {
		return AnswerReportComparisonView{}, err
	}
	current, err := loadAnswerReport(ctx, q, actor.TenantID, kbID, currentDigest)
	if err != nil {
		return AnswerReportComparisonView{}, err
	}
	docSet := map[int64]bool{}
	for _, report := range []AnswerReport{baseline.Report, current.Report} {
		docs, err := answerReportDocumentIDs(report)
		if err != nil {
			return AnswerReportComparisonView{}, fmt.Errorf("stored answer report is corrupt: %w", err)
		}
		for _, id := range docs {
			docSet[id] = true
		}
	}
	docs := make([]int64, 0, len(docSet))
	for id := range docSet {
		docs = append(docs, id)
	}
	slices.Sort(docs)
	if err := checkAnswerReportAccess(ctx, q, actor, kbID, docs, false); err != nil {
		return AnswerReportComparisonView{}, err
	}
	comparison, err := CompareAnswerReports(baseline.Report, current.Report)
	if err != nil {
		return AnswerReportComparisonView{}, fmt.Errorf("%w: %w", ErrReportInvalid, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AnswerReportComparisonView{}, fmt.Errorf("commit answer evaluation comparison: %w", err)
	}
	currentByID := make(map[string]AnswerRow, len(current.Report.Rows))
	for _, row := range current.Report.Rows {
		currentByID[row.ID] = row
	}
	total := len(baseline.Report.Rows)
	start := min(int(offset), total)
	end := min(start+int(limit), total)
	rows := make([]AnswerRowComparison, 0, end-start)
	for _, baseRow := range baseline.Report.Rows[start:end] {
		curRow := currentByID[baseRow.ID]
		rows = append(rows, AnswerRowComparison{
			ID:                       baseRow.ID,
			Answerable:               baseRow.Answerable,
			BaselineOutcome:          baseRow.Outcome,
			CurrentOutcome:           curRow.Outcome,
			BaselineValidCitation:    baseRow.ValidCitation,
			CurrentValidCitation:     curRow.ValidCitation,
			BaselineSourceIntegrity:  baseRow.SourceIntegrity,
			CurrentSourceIntegrity:   curRow.SourceIntegrity,
			BaselineCitationCount:    baseRow.CitationCount,
			CurrentCitationCount:     curRow.CitationCount,
			BaselineInvalidCitations: baseRow.InvalidCitations,
			CurrentInvalidCitations:  curRow.InvalidCitations,
		})
	}
	return AnswerReportComparisonView{
		Gate:     comparison.Gate,
		Baseline: answerReportListItem(baseline),
		Current:  answerReportListItem(current),
		Delta:    comparison.Delta,
		Total:    total,
		Offset:   offset,
		Limit:    limit,
		Rows:     rows,
	}, nil
}

func (s *AnswerReportStore) begin(ctx context.Context, actor retrieval.Actor, kbID int64) (pgx.Tx, *dbquery.Queries, error) {
	if actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 {
		return nil, nil, ErrReportInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, nil, fmt.Errorf("begin answer evaluation transaction: %w", err)
	}
	q := dbquery.New(tx)
	if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
		result := fmt.Errorf("set answer evaluation tenant: %w", err)
		finishEvaluationTransaction(tx, &result)
		return nil, nil, result
	}
	return tx, q, nil
}

func validReportDigest(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
}

func loadAnswerReport(ctx context.Context, q *dbquery.Queries, tenantID, kbID int64, digest string) (StoredAnswerReport, error) {
	row, err := q.GetAnswerEvalReport(ctx, dbquery.GetAnswerEvalReportParams{TenantID: tenantID, KbID: kbID, Digest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredAnswerReport{}, ErrReportNotFound
	}
	if err != nil {
		return StoredAnswerReport{}, fmt.Errorf("read answer evaluation report: %w", err)
	}
	result := StoredAnswerReport{Digest: row.Digest, Source: row.Source, CreatedAt: row.CreatedAt.Time}
	if err := json.Unmarshal(row.Report, &result.Report); err != nil {
		return StoredAnswerReport{}, fmt.Errorf("decode answer evaluation report: %w", err)
	}
	return result, nil
}

func answerReportListItem(stored StoredAnswerReport) AnswerReportListItem {
	return AnswerReportListItem{
		Digest:         stored.Digest,
		Source:         stored.Source,
		CreatedAt:      stored.CreatedAt,
		DatasetVersion: stored.Report.DatasetVersion,
		ModelLabel:     stored.Report.ModelLabel,
		ModelKind:      stored.Report.ModelKind,
		PromptVersion:  stored.Report.PromptVersion,
		Assembly:       stored.Report.Assembly,
		Judge:          stored.Report.Judge,
		Summary:        stored.Report.Summary,
	}
}

func answerReportDocumentIDs(report AnswerReport) ([]int64, error) {
	if len(report.Rows) > 10000 {
		return nil, ErrReportInvalid
	}
	if err := ValidateAnswerReport(report); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReportInvalid, err)
	}
	documents := map[int64]bool{}
	for _, row := range report.Rows {
		if len(row.EvidenceDocuments) > 100 {
			return nil, ErrReportInvalid
		}
		for _, docID := range row.EvidenceDocuments {
			if docID <= 0 {
				return nil, ErrReportInvalid
			}
			documents[docID] = true
		}
	}
	if len(documents) > 10000 {
		return nil, ErrReportInvalid
	}
	docs := make([]int64, 0, len(documents))
	for id := range documents {
		docs = append(docs, id)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i] < docs[j] })
	return docs, nil
}

func checkAnswerReportAccess(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, kbID int64, docs []int64, writing bool) error {
	locked, err := q.LockEvalDocuments(ctx, dbquery.LockEvalDocumentsParams{TenantID: actor.TenantID, KbID: kbID, DocumentIds: docs})
	if err != nil {
		return fmt.Errorf("lock answer evaluation documents: %w", err)
	}
	status, err := q.LockEvalKnowledgeBase(ctx, dbquery.LockEvalKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("lock answer evaluation knowledge base: %w", err)
	}
	role, err := q.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("lock answer evaluation membership: %w", err)
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
	return nil
}
