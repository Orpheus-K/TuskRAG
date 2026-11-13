package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

var ErrJobNotRetryable = errors.New("job is not eligible for retry")

type JobSummary struct {
	ID              int64
	Kind            string
	KnowledgeBaseID int64
	DocumentID      int64
	VersionID       int64
	Status          string
	Attempts        int32
	MaxAttempts     int32
	FailureCode     *string
	Retryable       bool
}

type PurgeJobSummary struct {
	ID              int64
	KnowledgeBaseID int64
	DocumentID      int64
	VersionID       int64
	DocumentTitle   *string
	DocumentDeleted bool
	Status          string
	Attempts        int32
	MaxAttempts     int32
	FailureCode     *string
	Retryable       bool
	CreatedAt       time.Time
	FinishedAt      *time.Time
}

type PurgeJobPage struct {
	Items      []PurgeJobSummary
	NextBefore *int64
}

type documentJobIDs struct {
	KBID       int64 `json:"kb_id"`
	DocumentID int64 `json:"document_id"`
	VersionID  int64 `json:"version_id"`
}

type controlledJob struct {
	row                dbquery.LockJobForControlRow
	ids                documentJobIDs
	version            dbquery.LockDocumentVersionForRetryRow
	purgeVersion       dbquery.LockDocumentVersionForPurgeRetryRow
	purgeVersionExists bool
	docDeleted         bool
	latestJobID        int64
	knowledgeStatus    string
}

// GetJob returns the status of an ingest or purge job visible to an editor.
func (s *DocumentService) GetJob(ctx context.Context, actor Actor, jobID int64) (_ JobSummary, resultErr error) {
	if jobID <= 0 {
		return JobSummary{}, ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return JobSummary{}, err
	}
	defer rollback(tx, &resultErr)
	job, err := lockControlledJob(ctx, tx, queries, actor, jobID, "job.read")
	if err != nil {
		return JobSummary{}, err
	}
	result := JobSummary{
		ID: job.row.ID, Kind: job.row.Kind, KnowledgeBaseID: job.ids.KBID, DocumentID: job.ids.DocumentID, VersionID: job.ids.VersionID,
		Status: job.row.Status, Attempts: job.row.Attempts, MaxAttempts: job.row.MaxAttempts, Retryable: job.retryable(),
	}
	if job.row.LastError.Valid {
		code := job.row.LastError.String
		result.FailureCode = &code
	}
	if err := tx.Commit(ctx); err != nil {
		return JobSummary{}, fmt.Errorf("commit job read: %w", err)
	}
	return result, nil
}

// ListPurgeJobs lists purge jobs for a knowledge base visible to editors and admins.
func (s *DocumentService) ListPurgeJobs(ctx context.Context, actor Actor, kbID, beforeID int64, limit int32) (_ PurgeJobPage, resultErr error) {
	if kbID <= 0 || beforeID < 0 || limit < 1 || limit > 50 {
		return PurgeJobPage{}, ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return PurgeJobPage{}, err
	}
	defer rollback(tx, &resultErr)
	role, kbStatus, err := lockDocumentAccess(ctx, queries, actor, kbID)
	if err == nil && role != Owner && role != Admin && role != Editor {
		err = ErrForbidden
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		return PurgeJobPage{}, deny(ctx, tx, queries, actor, kbID, 0, "", "job.purge.list", err)
	}
	if err != nil {
		return PurgeJobPage{}, err
	}
	rows, err := queries.ListKBJobsForPurge(ctx, dbquery.ListKBJobsForPurgeParams{
		TenantID:   actor.TenantID,
		KbID:       kbID,
		BeforeID:   beforeID,
		FetchLimit: limit + 1,
	})
	if err != nil {
		return PurgeJobPage{}, fmt.Errorf("list purge jobs: %w", err)
	}
	page := PurgeJobPage{Items: make([]PurgeJobSummary, 0, min(len(rows), int(limit)))}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next := rows[len(rows)-1].ID
		page.NextBefore = &next
	}
	for _, row := range rows {
		ids, err := decodeDocumentJob(row.Payload)
		if err != nil || ids.KBID != kbID || !row.DedupeKey.Valid || row.DedupeKey.String != fmt.Sprintf("version:%d", ids.VersionID) {
			return PurgeJobPage{}, errors.New("purge job payload does not match knowledge base")
		}
		isLatest := row.IsLatest.Valid && row.IsLatest.Bool
		item := PurgeJobSummary{
			ID:              row.ID,
			KnowledgeBaseID: ids.KBID,
			DocumentID:      ids.DocumentID,
			VersionID:       ids.VersionID,
			DocumentDeleted: row.DocumentDeleted,
			Status:          row.Status,
			Attempts:        row.Attempts,
			MaxAttempts:     row.MaxAttempts,
			Retryable: isPurgeJobRetryable(
				kbStatus, row.Status, isLatest, row.LastError,
				row.VersionRowID.Valid, row.VersionIsCurrent, row.VersionStatus, row.DocumentDeleted,
			),
			CreatedAt: row.CreatedAt.Time,
		}
		if row.DocumentTitle.Valid {
			title := row.DocumentTitle.String
			item.DocumentTitle = &title
		}
		if row.LastError.Valid {
			code := row.LastError.String
			item.FailureCode = &code
		}
		if row.FinishedAt.Valid {
			finished := row.FinishedAt.Time
			item.FinishedAt = &finished
		}
		page.Items = append(page.Items, item)
	}
	if err := tx.Commit(ctx); err != nil {
		return PurgeJobPage{}, fmt.Errorf("commit purge job list: %w", err)
	}
	return page, nil
}

// RetryJob records a new ingest or purge attempt without changing the failed job's history.
func (s *DocumentService) RetryJob(ctx context.Context, actor Actor, jobID int64) (_ int64, resultErr error) {
	if jobID <= 0 {
		return 0, ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer rollback(tx, &resultErr)
	job, err := lockControlledJob(ctx, tx, queries, actor, jobID, "job.retry")
	if err != nil {
		return 0, err
	}
	if job.knowledgeStatus != "active" {
		return 0, ErrArchived
	}
	if !job.retryable() {
		return 0, ErrJobNotRetryable
	}
	if job.row.Kind == string(jobs.Ingest) {
		updated, err := queries.QueueFailedDocumentVersion(ctx, dbquery.QueueFailedDocumentVersionParams{
			TenantID: actor.TenantID, KbID: job.ids.KBID, DocumentID: job.ids.DocumentID, VersionID: job.ids.VersionID,
			FailureCode: job.row.LastError,
		})
		if err != nil {
			return 0, fmt.Errorf("queue failed document version: %w", err)
		}
		if updated != 1 {
			return 0, errors.New("failed document version was not queued")
		}
	}
	key := job.row.DedupeKey.String
	newID, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{
		Kind: jobs.Kind(job.row.Kind), DedupeKey: &key, Payload: job.row.Payload, MaxAttempts: job.row.MaxAttempts,
	})
	if err != nil {
		return 0, fmt.Errorf("enqueue document retry: %w", err)
	}
	if newID <= job.row.ID {
		return 0, errors.New("document retry did not create a new job")
	}
	if err := queries.AuditJobRetry(ctx, dbquery.AuditJobRetryParams{TenantID: actor.TenantID, UserID: actor.UserID, JobID: newID}); err != nil {
		return 0, fmt.Errorf("audit job retry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return newID, nil
}

func lockControlledJob(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, jobID int64, action string) (controlledJob, error) {
	row, err := queries.LockJobForControl(ctx, dbquery.LockJobForControlParams{TenantID: actor.TenantID, JobID: jobID})
	if errors.Is(err, pgx.ErrNoRows) {
		return controlledJob{}, deny(ctx, tx, queries, actor, 0, 0, "", action, ErrNotFound)
	}
	if err != nil {
		return controlledJob{}, fmt.Errorf("lock job: %w", err)
	}
	ids, err := decodeDocumentJob(row.Payload)
	if err != nil || !row.DedupeKey.Valid {
		return controlledJob{}, ErrNotFound
	}
	switch row.Kind {
	case string(jobs.Ingest):
		if row.DedupeKey.String != fmt.Sprintf("document-version:%d", ids.VersionID) {
			return controlledJob{}, ErrNotFound
		}
		return lockControlledIngestJob(ctx, tx, queries, actor, row, ids, action)
	case string(jobs.Purge):
		if row.DedupeKey.String != fmt.Sprintf("version:%d", ids.VersionID) {
			return controlledJob{}, ErrNotFound
		}
		return lockControlledPurgeJob(ctx, tx, queries, actor, row, ids, action)
	default:
		return controlledJob{}, ErrNotFound
	}
}

func lockControlledIngestJob(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, row dbquery.LockJobForControlRow, ids documentJobIDs, action string) (controlledJob, error) {
	_, err := queries.LockEditableDocument(ctx, dbquery.LockEditableDocumentParams{
		TenantID: actor.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, UserID: actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return controlledJob{}, deny(ctx, tx, queries, actor, ids.KBID, 0, "", action, ErrNotFound)
	}
	if err != nil {
		return controlledJob{}, fmt.Errorf("lock job document: %w", err)
	}
	role, status, err := lockDocumentAccess(ctx, queries, actor, ids.KBID)
	if err == nil && role != Owner && role != Admin && role != Editor {
		err = ErrForbidden
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		return controlledJob{}, deny(ctx, tx, queries, actor, ids.KBID, 0, "", action, err)
	}
	if err != nil {
		return controlledJob{}, err
	}
	version, err := queries.LockDocumentVersionForRetry(ctx, dbquery.LockDocumentVersionForRetryParams{
		TenantID: actor.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return controlledJob{}, ErrNotFound
	}
	if err != nil {
		return controlledJob{}, fmt.Errorf("lock job document version: %w", err)
	}
	latest, err := queries.LatestIngestJob(ctx, dbquery.LatestIngestJobParams{TenantID: actor.TenantID, DedupeKey: row.DedupeKey})
	if err != nil {
		return controlledJob{}, fmt.Errorf("read latest ingest job: %w", err)
	}
	return controlledJob{row: row, ids: ids, version: version, latestJobID: latest, knowledgeStatus: status}, nil
}

func lockControlledPurgeJob(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, row dbquery.LockJobForControlRow, ids documentJobIDs, action string) (controlledJob, error) {
	docDeleted, err := queries.LockDocumentForPurgeControl(ctx, dbquery.LockDocumentForPurgeControlParams{
		TenantID: actor.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, UserID: actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return controlledJob{}, deny(ctx, tx, queries, actor, ids.KBID, 0, "", action, ErrNotFound)
	}
	if err != nil {
		return controlledJob{}, fmt.Errorf("lock purge job document: %w", err)
	}
	role, status, err := lockDocumentAccess(ctx, queries, actor, ids.KBID)
	if err == nil && role != Owner && role != Admin && role != Editor {
		err = ErrForbidden
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		return controlledJob{}, deny(ctx, tx, queries, actor, ids.KBID, 0, "", action, err)
	}
	if err != nil {
		return controlledJob{}, err
	}
	purgeVersion, err := queries.LockDocumentVersionForPurgeRetry(ctx, dbquery.LockDocumentVersionForPurgeRetryParams{
		TenantID: actor.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	versionExists := true
	if errors.Is(err, pgx.ErrNoRows) {
		versionExists = false
		err = nil
	}
	if err != nil {
		return controlledJob{}, fmt.Errorf("lock purge job document version: %w", err)
	}
	latest, err := queries.LatestPurgeJob(ctx, dbquery.LatestPurgeJobParams{TenantID: actor.TenantID, DedupeKey: row.DedupeKey})
	if err != nil {
		return controlledJob{}, fmt.Errorf("read latest purge job: %w", err)
	}
	return controlledJob{
		row: row, ids: ids, purgeVersion: purgeVersion, purgeVersionExists: versionExists,
		docDeleted: docDeleted, latestJobID: latest, knowledgeStatus: status,
	}, nil
}

func (job controlledJob) retryable() bool {
	if job.row.Kind == string(jobs.Purge) {
		return isPurgeJobRetryable(
			job.knowledgeStatus, job.row.Status, job.latestJobID == job.row.ID, job.row.LastError,
			job.purgeVersionExists, job.purgeVersion.IsCurrent, job.purgeVersion.Status, job.docDeleted,
		)
	}
	if job.knowledgeStatus != "active" || job.row.Status != string(jobs.Dead) || job.latestJobID != job.row.ID ||
		!job.version.IsLatest || job.version.Status != "failed" || !job.row.LastError.Valid ||
		!job.version.FailureCode.Valid || job.version.FailureCode.String != job.row.LastError.String {
		return false
	}
	return isManuallyRecoverable(job.row.LastError.String)
}

func isPurgeJobRetryable(
	kbStatus, jobStatus string,
	isLatest bool,
	lastError pgtype.Text,
	versionExists, versionIsCurrent bool,
	versionStatus string,
	docDeleted bool,
) bool {
	if kbStatus != "active" || jobStatus != string(jobs.Dead) || !isLatest ||
		!versionExists || versionIsCurrent || !lastError.Valid {
		return false
	}
	if versionStatus != "purging" && versionStatus != "superseded" && !docDeleted {
		return false
	}
	return isManuallyRecoverable(lastError.String)
}

func isManuallyRecoverable(code string) bool {
	switch jobs.FailureCode(code) {
	case jobs.ModelUnavailable, jobs.StorageUnavailable, jobs.IndexUnavailable, jobs.Timeout, jobs.LeaseExpired:
		return true
	default:
		return false
	}
}

func decodeDocumentJob(payload []byte) (documentJobIDs, error) {
	if len(payload) > 4096 {
		return documentJobIDs{}, errors.New("invalid job payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var ids documentJobIDs
	if decoder.Decode(&ids) != nil || decoder.Decode(&struct{}{}) != io.EOF || ids.KBID <= 0 || ids.DocumentID <= 0 || ids.VersionID <= 0 {
		return documentJobIDs{}, errors.New("invalid job payload")
	}
	return ids, nil
}
