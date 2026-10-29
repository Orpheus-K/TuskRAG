package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

var ErrDeleteVersionLimit = errors.New("document has too many versions to delete in one transaction")

const maxDeleteVersions = 10000

// Delete hides a document and enqueues cleanup atomically. Its original files
// and version rows are removed asynchronously by fenced purge jobs.
func (s *DocumentService) Delete(ctx context.Context, actor Actor, kbID, documentID int64) (resultErr error) {
	if kbID <= 0 || documentID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := db.CheckVersionGuards(ctx, tx); err != nil {
		return err
	}
	// Publication, upload and deletion all lock the document before the KB.
	_, err = queries.LockEditableDocument(ctx, dbquery.LockEditableDocumentParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID, UserID: actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.delete", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock document for deletion: %w", err)
	}
	if err := lockEditor(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrArchived) {
			return deny(ctx, tx, queries, actor, kbID, 0, "", "document.delete", err)
		}
		return err
	}
	versions, err := queries.ListDocumentVersionsForDeletion(ctx, dbquery.ListDocumentVersionsForDeletionParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID,
	})
	if err != nil {
		return fmt.Errorf("list document versions for deletion: %w", err)
	}
	if len(versions) > maxDeleteVersions {
		return ErrDeleteVersionLimit
	}
	for _, version := range versions {
		if !version.ActivePurgeValid.Valid || !version.ActivePurgeValid.Bool {
			return errors.New("active purge job does not match document version")
		}
	}
	if _, err := queries.DeactivateDeletedDocumentVectors(ctx, dbquery.DeactivateDeletedDocumentVectorsParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID,
	}); err != nil {
		return fmt.Errorf("hide document vectors: %w", err)
	}
	if _, err := queries.DeactivateDeletedDocumentChunks(ctx, dbquery.DeactivateDeletedDocumentChunksParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID,
	}); err != nil {
		return fmt.Errorf("hide document chunks: %w", err)
	}
	updated, err := queries.MarkDocumentDeleted(ctx, dbquery.MarkDocumentDeletedParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID,
	})
	if err != nil {
		return fmt.Errorf("mark document deleted: %w", err)
	}
	if updated != 1 {
		return errors.New("document was not marked deleted")
	}
	for _, version := range versions {
		if version.HasActivePurge {
			continue
		}
		payload, err := ingestPayload(kbID, documentID, version.ID)
		if err != nil {
			return fmt.Errorf("encode document purge: %w", err)
		}
		key := fmt.Sprintf("version:%d", version.ID)
		if _, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{
			Kind: jobs.Purge, DedupeKey: &key, Payload: payload, MaxAttempts: 3,
		}); err != nil {
			return fmt.Errorf("enqueue document purge: %w", err)
		}
	}
	if err := auditDocument(ctx, queries, actor, documentID, "document.delete"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return nil
}
