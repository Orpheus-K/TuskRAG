package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/storage"
)

var (
	ErrPurgeUnsafe      = errors.New("document version is not safe to purge")
	ErrPurgeJobMismatch = errors.New("claimed purge job does not match stored job")
)

type PurgeStage struct {
	queue *jobs.Store
	files storage.Store
}

var _ jobs.Handler = (*PurgeStage)(nil)

func NewPurgeStage(queue *jobs.Store, files storage.Store) (*PurgeStage, error) {
	if queue == nil || files == nil {
		return nil, errors.New("job store and file store are required")
	}
	return &PurgeStage{queue: queue, files: files}, nil
}

// Prepare fences any ingest of a deleted document, marks its version purging,
// and removes its immutable original. Superseded versions use the same cleanup.
// Database rows are removed when the purge job completes. File deletion is idempotent.
func (s *PurgeStage) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if s == nil || s.queue == nil || s.files == nil || ctx == nil {
		return nil, errors.New("initialized purge stage and context are required")
	}
	if job.Kind != jobs.Purge {
		return nil, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: ErrPurgeJobMismatch}
	}
	ids, err := parseDocumentIDs(job.Payload)
	if err != nil {
		return nil, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	}
	key := storage.Key{TenantID: job.Lease.TenantID, KBID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
	path, err := key.Path()
	if err != nil {
		return nil, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	}
	job.Payload = bytes.Clone(job.Payload)
	err = s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := db.CheckVersionGuards(ctx, tx); err != nil {
			return err
		}
		queries := dbquery.New(tx)
		match, err := queries.ClaimedPurgeJobMatches(ctx, dbquery.ClaimedPurgeJobMatchesParams{
			TenantID: job.Lease.TenantID, JobID: job.Lease.JobID, Payload: job.Payload,
		})
		if err != nil {
			return fmt.Errorf("verify claimed purge job: %w", err)
		}
		if !match {
			return ErrPurgeJobMismatch
		}
		deleted, err := queries.ReadPurgeDocumentDeleted(ctx, dbquery.ReadPurgeDocumentDeletedParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPurgeUnsafe
		}
		if err != nil {
			return fmt.Errorf("read purge document: %w", err)
		}
		if deleted {
			key := fmt.Sprintf("document-version:%d", ids.VersionID)
			if _, err := s.queue.CancelActiveIngestEffects(ctx, tx, job.Lease.TenantID, key, job.Payload); err != nil {
				return fmt.Errorf("cancel deleted document ingest: %w", err)
			}
		}
		if _, err := checkPurgeTarget(ctx, queries, job, ids, path, deleted); err != nil {
			return err
		}
		count, err := queries.MarkVersionPurging(ctx, dbquery.MarkVersionPurgingParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return fmt.Errorf("mark version purging: %w", err)
		}
		if count != 1 {
			return ErrPurgeUnsafe
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrPurgeUnsafe) || errors.Is(err, ErrPurgeJobMismatch) {
			return nil, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
		}
		return nil, err
	}
	if err := s.files.Delete(ctx, key); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &jobs.ProcessingFailure{Code: jobs.StorageUnavailable, Cause: fmt.Errorf("delete version original: %w", err)}
	}
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		return s.complete(ctx, tx, job, ids, path)
	}, nil
}

func checkPurgeTarget(ctx context.Context, queries *dbquery.Queries, job jobs.ClaimedJob, ids documentIDs, path string, allowDeleted bool) (string, error) {
	match, err := queries.ClaimedPurgeJobMatches(ctx, dbquery.ClaimedPurgeJobMatchesParams{
		TenantID: job.Lease.TenantID, JobID: job.Lease.JobID, Payload: job.Payload,
	})
	if err != nil {
		return "", fmt.Errorf("verify claimed purge job: %w", err)
	}
	if !match {
		return "", ErrPurgeJobMismatch
	}
	document, err := queries.LockPurgeDocument(ctx, dbquery.LockPurgeDocumentParams{
		TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPurgeUnsafe
	}
	if err != nil {
		return "", fmt.Errorf("lock purge document: %w", err)
	}
	if allowDeleted && !document.Deleted {
		return "", ErrPurgeUnsafe
	}
	if document.CurrentVersionID.Valid && document.CurrentVersionID.Int64 == ids.VersionID {
		return "", ErrPurgeUnsafe
	}
	version, err := queries.LockPurgeVersion(ctx, dbquery.LockPurgeVersionParams{
		TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPurgeUnsafe
	}
	if err != nil {
		return "", fmt.Errorf("lock purge version: %w", err)
	}
	if (version.Status != "superseded" && version.Status != "purging" && (!allowDeleted || !document.Deleted)) || version.FileKey != path {
		return "", ErrPurgeUnsafe
	}
	hasCurrent, err := queries.PurgeHasCurrentRows(ctx, dbquery.PurgeHasCurrentRowsParams{
		TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return "", fmt.Errorf("check purge visibility: %w", err)
	}
	if !hasCurrent.Valid || hasCurrent.Bool {
		return "", ErrPurgeUnsafe
	}
	return version.Status, nil
}

func (s *PurgeStage) complete(ctx context.Context, tx jobs.EffectsTx, job jobs.ClaimedJob, ids documentIDs, path string) error {
	if err := s.queue.CheckCompletion(tx, job.Lease); err != nil {
		return err
	}
	if err := db.CheckVersionGuards(ctx, tx); err != nil {
		return err
	}
	queries := dbquery.New(tx)
	status, err := checkPurgeTarget(ctx, queries, job, ids, path, false)
	if err != nil {
		return err
	}
	if status != "purging" {
		return ErrPurgeUnsafe
	}
	tenant := job.Lease.TenantID
	if err := queries.DeletePurgePlan(ctx, dbquery.DeletePurgePlanParams{TenantID: tenant, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}); err != nil {
		return fmt.Errorf("delete purge plan: %w", err)
	}
	if err := queries.DeletePurgeChunks(ctx, dbquery.DeletePurgeChunksParams{TenantID: tenant, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}); err != nil {
		return fmt.Errorf("delete purge chunks: %w", err)
	}
	if err := queries.DeletePurgeSections(ctx, dbquery.DeletePurgeSectionsParams{TenantID: tenant, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}); err != nil {
		return fmt.Errorf("delete purge sections: %w", err)
	}
	if err := queries.DeletePurgeArtifact(ctx, dbquery.DeletePurgeArtifactParams{TenantID: tenant, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}); err != nil {
		return fmt.Errorf("delete purge artifact: %w", err)
	}
	count, err := queries.DeletePurgeVersion(ctx, dbquery.DeletePurgeVersionParams{TenantID: tenant, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID})
	if err != nil {
		return fmt.Errorf("delete purge version: %w", err)
	}
	if count != 1 {
		return ErrPurgeUnsafe
	}
	if err := queries.AuditPurgedVersion(ctx, dbquery.AuditPurgedVersionParams{TenantID: tenant, VersionID: ids.VersionID}); err != nil {
		return fmt.Errorf("audit purged version: %w", err)
	}
	return nil
}
