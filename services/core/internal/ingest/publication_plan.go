package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	"tuskrag.local/core/internal/dbquery"
)

type publicationSection struct {
	parentOrdinal int32
	level         int16
	heading       string
	headingPath   []string
	pageStart     int32
	pageEnd       int32
	tokenCount    int32
	bodyHash      [sha256.Size]byte
}

type publicationChunk struct {
	id             int64
	sectionOrdinal int32
	kind           string
	pageStart      int32
	pageEnd        int32
	tokenCount     int32
	contentHash    [sha256.Size]byte
	searchHash     [sha256.Size]byte
}

type publicationSnapshot struct {
	sections []publicationSection
	chunks   []publicationChunk
}

func snapshotPublication(ctx context.Context, plan PreparedPlan, staged StagedPlan) (publicationSnapshot, error) {
	snapshot := publicationSnapshot{sections: make([]publicationSection, len(plan.Sections)), chunks: make([]publicationChunk, len(plan.Chunks))}
	for index, section := range plan.Sections {
		if err := ctx.Err(); err != nil {
			return publicationSnapshot{}, err
		}
		snapshot.sections[index] = publicationSection{
			parentOrdinal: int32(section.ParentIndex + 1), level: section.Level, heading: section.Heading,
			headingPath: slices.Clone(section.HeadingPath), pageStart: section.PageStart, pageEnd: section.PageEnd,
			tokenCount: section.TokenCount, bodyHash: sha256.Sum256([]byte(section.BodyMarkdown)),
		}
	}
	for index, chunk := range plan.Chunks {
		if err := ctx.Err(); err != nil {
			return publicationSnapshot{}, err
		}
		snapshot.chunks[index] = publicationChunk{
			id: staged.Chunks[index].ID, sectionOrdinal: int32(chunk.SectionIndex + 1), kind: chunk.Kind,
			pageStart: chunk.PageStart, pageEnd: chunk.PageEnd, tokenCount: chunk.TokenCount,
			contentHash: sha256.Sum256([]byte(chunk.Content)), searchHash: sha256.Sum256([]byte(chunk.SearchText)),
		}
	}
	return snapshot, nil
}

func verifyPublicationRows(ctx context.Context, queries *dbquery.Queries, tenantID int64, ids documentIDs, identity PlanIdentity, expected publicationSnapshot) error {
	sections, err := queries.LockPublicationSections(ctx, dbquery.LockPublicationSectionsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return fmt.Errorf("lock publication sections: %w", err)
	}
	if len(sections) != len(expected.sections) {
		return ErrPlanCorrupt
	}
	for index, row := range sections {
		want := expected.sections[index]
		if row.ID <= 0 || row.Ordinal != int32(index+1) || row.ParentOrdinal != want.parentOrdinal || row.Level != want.level ||
			row.Heading != want.heading || !slices.Equal(row.HeadingPath, want.headingPath) || !row.PageStart.Valid || row.PageStart.Int32 != want.pageStart ||
			!row.PageEnd.Valid || row.PageEnd.Int32 != want.pageEnd || row.TokenCount != want.tokenCount || !bytes.Equal(row.BodyHash, want.bodyHash[:]) {
			return ErrPlanCorrupt
		}
	}
	chunks, err := queries.LockPublicationChunks(ctx, dbquery.LockPublicationChunksParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
	})
	if err != nil {
		return fmt.Errorf("lock publication chunks: %w", err)
	}
	if len(chunks) != len(expected.chunks) {
		return ErrPlanCorrupt
	}
	tag := fmt.Sprintf("kb:%d:all", ids.KBID)
	chunkIDs := make(map[int64]struct{}, len(chunks))
	for index, row := range chunks {
		want := expected.chunks[index]
		if row.ID != want.id || row.Ordinal != int32(index+1) || row.SectionOrdinal != want.sectionOrdinal || row.Kind != want.kind ||
			row.TokenCount != want.tokenCount || !row.PageStart.Valid || row.PageStart.Int32 != want.pageStart || !row.PageEnd.Valid || row.PageEnd.Int32 != want.pageEnd ||
			row.IsCurrent || row.Disabled || len(row.AccessTags) != 1 || row.AccessTags[0] != tag || !row.MetadataCurrent ||
			!bytes.Equal(row.ContentHash, want.contentHash[:]) || !bytes.Equal(row.SearchHash, want.searchHash[:]) {
			return ErrPlanCorrupt
		}
		chunkIDs[row.ID] = struct{}{}
	}
	vectors, err := queries.LockPublicationVectors(ctx, dbquery.LockPublicationVectorsParams{
		TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		SpaceID: identity.EmbeddingSpaceID, Normalized: identity.EmbeddingProfile.Normalized,
	})
	if err != nil {
		return fmt.Errorf("lock publication vectors: %w", err)
	}
	if len(vectors) != len(chunks) {
		return ErrVectorsIncomplete
	}
	for _, row := range vectors {
		if _, ok := chunkIDs[row.ChunkID]; !ok || row.IsCurrent || !row.NormValid || len(row.AccessTags) != 1 || row.AccessTags[0] != tag {
			return ErrPlanCorrupt
		}
		delete(chunkIDs, row.ChunkID)
	}
	if len(chunkIDs) != 0 {
		return ErrVectorsIncomplete
	}
	return nil
}
