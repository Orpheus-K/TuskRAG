package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
)

var ErrVersionSuperseded = errors.New("a newer document version is already current")

type PublicationStage struct{ queue *jobs.Store }

func NewPublicationStage(queue *jobs.Store) (*PublicationStage, error) {
	if queue == nil {
		return nil, errors.New("job store is required")
	}
	return &PublicationStage{queue: queue}, nil
}

// Prepare validates the plan outside the transaction and returns effects for
// jobs.Store.Complete, so publication and job completion commit together.
func (s *PublicationStage) Prepare(ctx context.Context, job jobs.ClaimedJob, plan PreparedPlan, staged StagedPlan) (func(context.Context, jobs.EffectsTx) error, error) {
	if s == nil || s.queue == nil || ctx == nil {
		return nil, errors.New("initialized publication stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return nil, fmt.Errorf("invalid publication job: %w", err)
	}
	if err := validateEmbeddingPlan(ctx, plan, staged); err != nil {
		return nil, err
	}
	expected, err := snapshotPublication(ctx, plan, staged)
	if err != nil {
		return nil, err
	}
	job.Payload = bytes.Clone(job.Payload)
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		return s.publish(ctx, tx, job, ids, staged.Identity, expected)
	}, nil
}

func (s *PublicationStage) publish(ctx context.Context, tx jobs.EffectsTx, job jobs.ClaimedJob, ids documentIDs, identity PlanIdentity, expected publicationSnapshot) error {
	if tx == nil {
		return errors.New("publication transaction is required")
	}
	if err := s.queue.CheckCompletion(tx, job.Lease); err != nil {
		return err
	}
	if err := db.CheckVersionGuards(ctx, tx); err != nil {
		return err
	}
	queries := dbquery.New(tx)
	if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
		return err
	}
	current, err := queries.LockPublicationDocument(ctx, dbquery.LockPublicationDocumentParams{
		TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errTarget
	}
	if err != nil {
		return fmt.Errorf("lock publication document: %w", err)
	}
	previous := int64(0)
	if current.Valid {
		previous = current.Int64
		if previous >= ids.VersionID {
			return ErrVersionSuperseded
		}
	}
	_, status, actor, err := lockVectorPlan(ctx, queries, job, ids, identity)
	if err != nil {
		return err
	}
	if status != "indexing" || actor <= 0 {
		return ErrPlanConflict
	}
	if err := indexing.Check(ctx, tx, identity.EmbeddingSpaceID, identity.EmbeddingProfile.Dimensions, identity.EmbeddingProfile.Distance); err != nil {
		return fmt.Errorf("check publication indexes: %w", err)
	}
	if err := verifyPublicationRows(ctx, queries, job.Lease.TenantID, ids, identity, expected); err != nil {
		return err
	}
	if err := checkPreviousVersion(ctx, queries, job.Lease.TenantID, ids, previous, identity.EmbeddingSpaceID); err != nil {
		return err
	}
	if previous > 0 {
		if err := deactivatePrevious(ctx, queries, job.Lease.TenantID, ids, previous); err != nil {
			return err
		}
	}
	if err := activatePublication(ctx, queries, job.Lease.TenantID, ids, previous, identity); err != nil {
		return err
	}
	if previous > 0 {
		if err := s.supersede(ctx, tx, queries, job.Lease.TenantID, ids, previous); err != nil {
			return err
		}
	}
	if err := queries.AuditDocumentAction(ctx, dbquery.AuditDocumentActionParams{TenantID: job.Lease.TenantID,
		UserID: actor, Action: "document.publish", DocumentID: ids.DocumentID, Result: "allow"}); err != nil {
		return fmt.Errorf("audit publication: %w", err)
	}
	return nil
}

func checkPreviousVersion(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, previous, spaceID int64) error {
	if previous > 0 {
		status, err := queries.LockPreviousVersion(ctx, dbquery.LockPreviousVersionParams{
			TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlanCorrupt
		}
		if err != nil {
			return fmt.Errorf("lock previous document version: %w", err)
		}
		if status != "ready" && status != "partial" {
			return ErrPlanCorrupt
		}
	}
	consistent, err := queries.PublicationCurrentConsistent(ctx, dbquery.PublicationCurrentConsistentParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous, SpaceID: spaceID,
	})
	if err != nil {
		return fmt.Errorf("check current document version: %w", err)
	}
	if !consistent.Valid || !consistent.Bool {
		return ErrPlanCorrupt
	}
	return nil
}

func deactivatePrevious(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, previous int64) error {
	if _, err := queries.DeactivatePreviousVectors(ctx, dbquery.DeactivatePreviousVectorsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous,
	}); err != nil {
		return fmt.Errorf("deactivate previous vectors: %w", err)
	}
	if _, err := queries.DeactivatePreviousChunks(ctx, dbquery.DeactivatePreviousChunksParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous,
	}); err != nil {
		return fmt.Errorf("deactivate previous chunks: %w", err)
	}
	return nil
}

func (s *PublicationStage) supersede(ctx context.Context, tx jobs.EffectsTx, queries *dbquery.Queries, tenantID int64, ids documentIDs, previous int64) error {
	updated, err := queries.SupersedePreviousVersion(ctx, dbquery.SupersedePreviousVersionParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous,
	})
	if err != nil {
		return fmt.Errorf("supersede previous version: %w", err)
	}
	if updated != 1 {
		return ErrPlanCorrupt
	}
	return enqueueVersionPurge(ctx, tx, s.queue, tenantID, ids, previous)
}

func enqueueVersionPurge(ctx context.Context, tx jobs.EffectsTx, queue *jobs.Store, tenantID int64, ids documentIDs, version int64) error {
	payload, err := json.Marshal(documentIDs{KBID: ids.KBID, DocumentID: ids.DocumentID, VersionID: version})
	if err != nil {
		return fmt.Errorf("encode cleanup job: %w", err)
	}
	key := fmt.Sprintf("version:%d", version)
	if _, err := queue.EnqueueEffects(ctx, tx, tenantID, jobs.Request{Kind: jobs.Purge, DedupeKey: &key, Payload: payload, MaxAttempts: 3}); err != nil {
		return fmt.Errorf("enqueue superseded version cleanup: %w", err)
	}
	return nil
}

func activatePublication(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, previous int64, identity PlanIdentity) error {
	chunks, err := queries.ActivatePublicationChunks(ctx, dbquery.ActivatePublicationChunksParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return fmt.Errorf("activate publication chunks: %w", err)
	}
	if chunks != int64(identity.ChunkCount) {
		return ErrPlanCorrupt
	}
	vectors, err := queries.ActivatePublicationVectors(ctx, dbquery.ActivatePublicationVectorsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID, SpaceID: identity.EmbeddingSpaceID,
	})
	if err != nil {
		return fmt.Errorf("activate publication vectors: %w", err)
	}
	if vectors != chunks {
		return ErrPlanCorrupt
	}
	updated, err := queries.MarkPublicationReady(ctx, dbquery.MarkPublicationReadyParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return fmt.Errorf("mark publication ready: %w", err)
	}
	if updated != 1 {
		return ErrPlanCorrupt
	}
	updated, err = queries.SetPublicationCurrent(ctx, dbquery.SetPublicationCurrentParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, PreviousVersionID: previous,
		VersionID: pgtype.Int8{Int64: ids.VersionID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("set current document version: %w", err)
	}
	if updated != 1 {
		return ErrPlanCorrupt
	}
	return nil
}
