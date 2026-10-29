package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
)

var ErrPlanCorrupt = errors.New("staged ingest plan is incomplete or inconsistent")

type StagedChunk struct {
	ID      int64
	Ordinal int
}

type PlanIdentity struct {
	Digest           [sha256.Size]byte
	ContentDigest    [sha256.Size]byte
	EmbeddingSpaceID int64
	EmbeddingProfile EmbeddingProfile
	ConfigRevision   int32
	SectionCount     int32
	ChunkCount       int32
}

type StagedPlan struct {
	Identity PlanIdentity
	Chunks   []StagedChunk
	Reused   bool
}

type PlanStage struct{ queue *jobs.Store }

func NewPlanStage(queue *jobs.Store) (*PlanStage, error) {
	if queue == nil {
		return nil, errors.New("job store is required")
	}
	return &PlanStage{queue: queue}, nil
}

// Stage atomically writes non-current sections and chunks, without publishing
// a version or completing its job. Replays must use the identical plan.
func (s *PlanStage) Stage(ctx context.Context, job jobs.ClaimedJob, plan PreparedPlan) (StagedPlan, error) {
	if s == nil || s.queue == nil || ctx == nil {
		return StagedPlan{}, errors.New("initialized plan stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return StagedPlan{}, fmt.Errorf("invalid plan job: %w", err)
	}
	hashes, err := validatePreparedPlan(ctx, plan)
	if err != nil {
		return StagedPlan{}, err
	}
	identity := PlanIdentity{Digest: hashes.Digest, ContentDigest: hashes.Content, EmbeddingSpaceID: plan.EmbeddingSpaceID, EmbeddingProfile: plan.EmbeddingProfile, ConfigRevision: plan.ConfigRevision,
		SectionCount: int32(len(plan.Sections)), ChunkCount: int32(len(plan.Chunks))}
	var sectionIDs, chunkIDs []int64
	var result StagedPlan
	err = s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		if _, err := checkPlanTarget(ctx, queries, job.Lease.TenantID, ids, plan); err != nil {
			return err
		}
		stored, found, err := readStagedPlan(ctx, queries, job.Lease.TenantID, ids, plan, hashes.Digest)
		if err != nil {
			return err
		}
		if found {
			result = stored
			return nil
		}
		if err := ensureNoPlanRows(ctx, queries, job.Lease.TenantID, ids); err != nil {
			return err
		}
		sectionIDs, err = queries.ReserveSectionIDs(ctx, int32(len(plan.Sections)))
		if err != nil {
			return fmt.Errorf("reserve section identifiers: %w", err)
		}
		chunkIDs, err = queries.ReserveChunkIDs(ctx, int32(len(plan.Chunks)))
		if err != nil {
			return fmt.Errorf("reserve chunk identifiers: %w", err)
		}
		if !validReservedIDs(sectionIDs, len(plan.Sections)) || !validReservedIDs(chunkIDs, len(plan.Chunks)) {
			return ErrPlanCorrupt
		}
		return nil
	})
	if err != nil {
		return StagedPlan{}, err
	}
	if result.Reused {
		result.Identity = identity
		return result, nil
	}
	sections, err := encodePlanSections(ctx, plan.Sections, sectionIDs)
	if err != nil {
		return StagedPlan{}, err
	}
	chunks, err := encodePlanChunks(ctx, plan.Chunks, sectionIDs, chunkIDs)
	if err != nil {
		return StagedPlan{}, err
	}
	err = s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		if _, err := checkPlanTarget(ctx, queries, job.Lease.TenantID, ids, plan); err != nil {
			return err
		}
		stored, found, err := readStagedPlan(ctx, queries, job.Lease.TenantID, ids, plan, hashes.Digest)
		if err != nil {
			return err
		}
		if found {
			result = stored
			return nil
		}
		if err := ensureNoPlanRows(ctx, queries, job.Lease.TenantID, ids); err != nil {
			return err
		}
		count, err := queries.InsertPlanSections(ctx, dbquery.InsertPlanSectionsParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID, Items: sections,
		})
		if err != nil {
			return safePlanDBError("insert staged sections", err)
		}
		if count != int64(len(plan.Sections)) {
			return ErrPlanCorrupt
		}
		count, err = queries.InsertPlanChunks(ctx, dbquery.InsertPlanChunksParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID, Items: chunks,
		})
		if err != nil {
			return safePlanDBError("insert staged chunks", err)
		}
		if count != int64(len(plan.Chunks)) {
			return ErrPlanCorrupt
		}
		if err := queries.InsertIngestPlan(ctx, dbquery.InsertIngestPlanParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
			EmbeddingSpaceID: plan.EmbeddingSpaceID, ConfigRevision: plan.ConfigRevision, PlanSha256: hashes.Digest[:],
			SectionCount: int32(len(plan.Sections)), ChunkCount: int32(len(plan.Chunks)),
		}); err != nil {
			return safePlanDBError("insert ingest plan", err)
		}
		result.Chunks = make([]StagedChunk, len(chunkIDs))
		for index, id := range chunkIDs {
			result.Chunks[index] = StagedChunk{ID: id, Ordinal: index + 1}
		}
		return nil
	})
	if err != nil {
		return StagedPlan{}, err
	}
	result.Identity = identity
	return result, nil
}

func validReservedIDs(ids []int64, expected int) bool {
	if len(ids) != expected {
		return false
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func checkPlanTarget(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, plan PreparedPlan) (dbquery.LockPlanTargetRow, error) {
	row, err := queries.LockPlanTarget(ctx, dbquery.LockPlanTargetParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbquery.LockPlanTargetRow{}, errTarget
	}
	if err != nil {
		return dbquery.LockPlanTargetRow{}, fmt.Errorf("lock plan target: %w", err)
	}
	if row.Status != "embedding" && row.Status != "indexing" {
		return dbquery.LockPlanTargetRow{}, errTarget
	}
	if len(row.ContentSha256) != sha256.Size || !bytes.Equal(row.ContentSha256, row.ParsedSha256) {
		return dbquery.LockPlanTargetRow{}, errCorruptOriginal
	}
	if row.EmbeddingSpaceID != plan.EmbeddingSpaceID || row.ConfigRevision != plan.ConfigRevision || row.ProviderID != plan.EmbeddingProfile.ProviderID ||
		row.Model != plan.EmbeddingProfile.Model || row.Dims != plan.EmbeddingProfile.Dimensions ||
		row.Normalized != plan.EmbeddingProfile.Normalized || row.Distance != plan.EmbeddingProfile.Distance {
		return dbquery.LockPlanTargetRow{}, ErrPlanConflict
	}
	return row, nil
}

func readStagedPlan(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, plan PreparedPlan, digest [sha256.Size]byte) (StagedPlan, bool, error) {
	row, err := queries.LoadIngestPlan(ctx, dbquery.LoadIngestPlanParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return StagedPlan{}, false, nil
	}
	if err != nil {
		return StagedPlan{}, false, fmt.Errorf("load ingest plan: %w", err)
	}
	if row.EmbeddingSpaceID != plan.EmbeddingSpaceID || row.ConfigRevision != plan.ConfigRevision || !bytes.Equal(row.PlanSha256, digest[:]) ||
		row.SectionCount != int32(len(plan.Sections)) || row.ChunkCount != int32(len(plan.Chunks)) {
		return StagedPlan{}, false, ErrPlanConflict
	}
	sections, refs, err := planRows(ctx, queries, tenantID, ids)
	if err != nil {
		return StagedPlan{}, false, err
	}
	if sections != int64(row.SectionCount) || len(refs) != int(row.ChunkCount) {
		return StagedPlan{}, false, ErrPlanCorrupt
	}
	for index, ref := range refs {
		if ref.ID <= 0 || ref.Ordinal != int32(index+1) || ref.IsCurrent || ref.Disabled || len(ref.AccessTags) != 1 || ref.AccessTags[0] != fmt.Sprintf("kb:%d:all", ids.KBID) {
			return StagedPlan{}, false, ErrPlanCorrupt
		}
	}
	result := StagedPlan{Chunks: make([]StagedChunk, len(refs)), Reused: true}
	for index, ref := range refs {
		result.Chunks[index] = StagedChunk{ID: ref.ID, Ordinal: int(ref.Ordinal)}
	}
	return result, true, nil
}

func planRows(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs) (int64, []dbquery.ListPlanChunkIDsRow, error) {
	sections, err := queries.CountPlanSections(ctx, dbquery.CountPlanSectionsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("count staged sections: %w", err)
	}
	chunks, err := queries.ListPlanChunkIDs(ctx, dbquery.ListPlanChunkIDsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("list staged chunks: %w", err)
	}
	return sections, chunks, nil
}

func ensureNoPlanRows(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs) error {
	sections, chunks, err := planRows(ctx, queries, tenantID, ids)
	if err != nil {
		return err
	}
	if sections != 0 || len(chunks) != 0 {
		return ErrPlanCorrupt
	}
	return nil
}

type stagedDBError struct {
	operation string
	code      string
	cause     error
}

func (e *stagedDBError) Error() string { return e.operation + ": PostgreSQL error " + e.code }
func (e *stagedDBError) Unwrap() error { return e.cause }

func safePlanDBError(operation string, err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return &stagedDBError{operation: operation, code: databaseError.Code, cause: err}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

type sectionPlanRow struct {
	ID          int64    `json:"id"`
	ParentID    *int64   `json:"parent_id"`
	Ordinal     int      `json:"ordinal"`
	Level       int16    `json:"level"`
	Heading     string   `json:"heading"`
	HeadingPath []string `json:"heading_path"`
	PageStart   int32    `json:"page_start"`
	PageEnd     int32    `json:"page_end"`
	Body        string   `json:"body_md"`
	TokenCount  int32    `json:"token_count"`
}

type chunkPlanRow struct {
	ID         int64  `json:"id"`
	SectionID  int64  `json:"section_id"`
	Ordinal    int    `json:"ordinal"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	SearchText string `json:"search_text"`
	PageStart  int32  `json:"page_start"`
	PageEnd    int32  `json:"page_end"`
	TokenCount int32  `json:"token_count"`
}

func encodePlanSections(ctx context.Context, sections []PreparedSection, ids []int64) ([]byte, error) {
	return encodePlanRows(ctx, len(sections), func(index int) any {
		section := sections[index]
		var parent *int64
		if section.ParentIndex >= 0 {
			value := ids[section.ParentIndex]
			parent = &value
		}
		path := section.HeadingPath
		if path == nil {
			path = []string{}
		}
		return sectionPlanRow{ID: ids[index], ParentID: parent, Ordinal: index + 1, Level: section.Level,
			Heading: section.Heading, HeadingPath: path, PageStart: section.PageStart, PageEnd: section.PageEnd,
			Body: section.BodyMarkdown, TokenCount: section.TokenCount}
	})
}

func encodePlanChunks(ctx context.Context, chunks []PreparedChunk, sectionIDs, chunkIDs []int64) ([]byte, error) {
	return encodePlanRows(ctx, len(chunks), func(index int) any {
		chunk := chunks[index]
		return chunkPlanRow{ID: chunkIDs[index], SectionID: sectionIDs[chunk.SectionIndex], Ordinal: index + 1,
			Kind: chunk.Kind, Content: chunk.Content, SearchText: chunk.SearchText, PageStart: chunk.PageStart,
			PageEnd: chunk.PageEnd, TokenCount: chunk.TokenCount}
	})
}

func encodePlanRows(ctx context.Context, count int, row func(int) any) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(row(index))
		if err != nil {
			return nil, fmt.Errorf("encode ingest plan: %w", err)
		}
		if len(encoded) > maxPlanJSONBytes-output.Len()-2 {
			return nil, ErrPlanLimit
		}
		if index > 0 {
			output.WriteByte(',')
		}
		_, _ = output.Write(encoded)
	}
	output.WriteByte(']')
	return output.Bytes(), nil
}
