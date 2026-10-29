package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

var ErrJobNotRetryable = errors.New("job is not eligible for retry")

type JobSummary struct {
	ID              int64
	KnowledgeBaseID int64
	DocumentID      int64
	VersionID       int64
	Status          string
	Attempts        int32
	MaxAttempts     int32
	FailureCode     *string
	Retryable       bool
}

type documentJobIDs struct {
	KBID       int64 `json:"kb_id"`
	DocumentID int64 `json:"document_id"`
	VersionID  int64 `json:"version_id"`
}

type controlledJob struct {
	row             dbquery.LockJobForControlRow
	ids             documentJobIDs
	version         dbquery.LockDocumentVersionForRetryRow
	latestJobID     int64
	knowledgeStatus string
}

// GetJob returns the status of an ingest job visible to an editor.
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
		ID: job.row.ID, KnowledgeBaseID: job.ids.KBID, DocumentID: job.ids.DocumentID, VersionID: job.ids.VersionID,
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

// RetryJob records a new ingest attempt without changing the failed job's history.
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
	key := job.row.DedupeKey.String
	newID, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{
		Kind: jobs.Ingest, DedupeKey: &key, Payload: job.row.Payload, MaxAttempts: job.row.MaxAttempts,
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
	if row.Kind != string(jobs.Ingest) || err != nil || !row.DedupeKey.Valid || row.DedupeKey.String != fmt.Sprintf("document-version:%d", ids.VersionID) {
		return controlledJob{}, ErrNotFound
	}
	_, err = queries.LockEditableDocument(ctx, dbquery.LockEditableDocumentParams{
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

func (job controlledJob) retryable() bool {
	if job.knowledgeStatus != "active" || job.row.Status != string(jobs.Dead) || job.latestJobID != job.row.ID ||
		!job.version.IsLatest || job.version.Status != "failed" || !job.row.LastError.Valid ||
		!job.version.FailureCode.Valid || job.version.FailureCode.String != job.row.LastError.String {
		return false
	}
	return isManuallyRecoverable(job.row.LastError.String)
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
