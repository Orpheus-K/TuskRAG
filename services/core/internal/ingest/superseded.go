package ingest

import (
	"context"
	"fmt"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

// A late version cannot become current. Its failure transaction marks it
// superseded and schedules the same safe cleanup as an older current version.
func (h *IngestHandler) supersededFailure(job jobs.ClaimedJob, ids documentIDs, cause error) error {
	return &jobs.ProcessingFailure{Code: jobs.VersionSuperseded, Cause: cause, Apply: func(ctx context.Context, tx jobs.EffectsTx, status jobs.Status) error {
		if err := h.queue.CheckFailure(tx, job.Lease, status); err != nil {
			return err
		}
		if status != jobs.Failed {
			return ErrPlanConflict
		}
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		current, err := queries.LockPublicationDocument(ctx, dbquery.LockPublicationDocumentParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID,
		})
		if err != nil {
			return fmt.Errorf("lock superseded document: %w", err)
		}
		if !current.Valid || current.Int64 <= ids.VersionID {
			return ErrPlanConflict
		}
		version, err := queries.LockIngestVersion(ctx, lockParams(job.Lease.TenantID, ids))
		if err != nil {
			return fmt.Errorf("lock superseded version: %w", err)
		}
		if version.Status != "indexing" {
			return ErrPlanConflict
		}
		hasCurrent, err := queries.PurgeHasCurrentRows(ctx, dbquery.PurgeHasCurrentRowsParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return fmt.Errorf("check superseded visibility: %w", err)
		}
		if !hasCurrent.Valid || hasCurrent.Bool {
			return ErrPlanCorrupt
		}
		count, err := queries.SupersedePendingVersion(ctx, dbquery.SupersedePendingVersionParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return fmt.Errorf("supersede pending version: %w", err)
		}
		if count != 1 {
			return ErrPlanConflict
		}
		return enqueueVersionPurge(ctx, tx, h.queue, job.Lease.TenantID, ids, ids.VersionID)
	}}
}
