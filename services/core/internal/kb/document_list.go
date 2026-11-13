package kb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
)

type DocumentSummary struct {
	ID               int64
	Title            string
	MIME             string
	CurrentVersionID *int64
	LatestVersionID  int64
	LatestStatus     string
	LatestJobID      *int64
	Retryable        bool
	ByteSize         int64
	FailureCode      *string
	LatestCreatedAt  time.Time
}

type DocumentPage struct {
	Items     []DocumentSummary
	NextAfter *int64
}

type DocumentFilter struct {
	Query  string
	Tags   []string
	Status string
}

// ListDocuments returns only documents visible to the actor in the knowledge base.
func (s *Service) ListDocuments(ctx context.Context, actor Actor, kbID, after int64, limit int32) (DocumentPage, error) {
	return s.ListDocumentsFiltered(ctx, actor, kbID, after, limit, DocumentFilter{})
}

// ListDocumentsFiltered returns visible documents in the knowledge base matching optional filters.
func (s *Service) ListDocumentsFiltered(ctx context.Context, actor Actor, kbID, after int64, limit int32, filter DocumentFilter) (_ DocumentPage, resultErr error) {
	if kbID <= 0 || after < 0 || limit < 1 || limit > 100 {
		return DocumentPage{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return DocumentPage{}, err
	}
	defer rollback(tx, &resultErr)
	role, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return DocumentPage{}, deny(ctx, tx, queries, actor, kbID, 0, "", "document.list", ErrNotFound)
	}
	if err != nil {
		return DocumentPage{}, fmt.Errorf("lock document membership: %w", err)
	}
	tags := filter.Tags
	if tags == nil {
		tags = []string{}
	}
	rows, err := queries.ListVisibleDocumentsFiltered(ctx, dbquery.ListVisibleDocumentsFilteredParams{
		TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, AfterID: after, FetchLimit: limit + 1,
		Query: filter.Query, Tags: tags, Status: filter.Status,
	})
	if err != nil {
		return DocumentPage{}, fmt.Errorf("list documents: %w", err)
	}
	page := DocumentPage{Items: make([]DocumentSummary, 0, min(len(rows), int(limit)))}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next := rows[len(rows)-1].ID
		page.NextAfter = &next
	}
	for _, row := range rows {
		if !row.LatestCreatedAt.Valid {
			return DocumentPage{}, errors.New("document has no latest version")
		}
		if row.CurrentVersionID.Valid && row.CurrentStatus != "ready" && row.CurrentStatus != "partial" {
			return DocumentPage{}, errors.New("document current version is not searchable")
		}
		if row.LatestJobID > 0 && !row.LatestJobMatches {
			return DocumentPage{}, errors.New("latest job does not match document version")
		}
		item := DocumentSummary{ID: row.ID, Title: row.Title, MIME: row.Mime, LatestVersionID: row.LatestVersionID,
			LatestStatus: row.LatestStatus, ByteSize: row.ByteSize, LatestCreatedAt: row.LatestCreatedAt.Time}
		if row.CurrentVersionID.Valid {
			value := row.CurrentVersionID.Int64
			item.CurrentVersionID = &value
		}
		if row.FailureCode.Valid {
			value := row.FailureCode.String
			item.FailureCode = &value
		}
		if (Role(role) == Owner || Role(role) == Admin || Role(role) == Editor) && row.LatestJobID > 0 {
			value := row.LatestJobID
			item.LatestJobID = &value
			item.Retryable = row.KbStatus == "active" && row.LatestStatus == "failed" && row.LatestJobStatus == "dead" &&
				row.FailureCode.Valid && row.LatestJobError.Valid && row.FailureCode.String == row.LatestJobError.String &&
				isManuallyRecoverable(row.LatestJobError.String)
		}
		page.Items = append(page.Items, item)
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentPage{}, fmt.Errorf("commit document listing: %w", err)
	}
	return page, nil
}
