package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

var ErrContentLimit = errors.New("document content exceeds read limits")

const (
	maxReadChunks   = 10000
	maxHeadingBytes = 8192
	maxPathBytes    = 16384
	maxReadBytes    = 1024 * 1024
)

type SectionReference struct {
	ID          int64
	ParentID    *int64
	Ordinal     int32
	Level       int16
	Heading     string
	HeadingPath []string
	PageStart   *int32
	PageEnd     *int32
}

type DocumentContents struct {
	KbID       int64
	DocumentID int64
	VersionID  int64
	Title      string
	Sections   []SectionReference
	NextAfter  *int32
}

type SectionContent struct {
	KbID       int64
	DocumentID int64
	VersionID  int64
	Title      string
	Section    SectionReference
	Markdown   string
	TokenCount int32
}

type NeighborChunk struct {
	ID         int64
	Ordinal    int32
	Kind       string
	Content    string
	PageStart  *int32
	PageEnd    *int32
	TokenCount int32
	IsTarget   bool
}

type ChunkNeighbors struct {
	KbID       int64
	DocumentID int64
	VersionID  int64
	Title      string
	Section    SectionReference
	Chunks     []NeighborChunk
}

type readableDocument struct {
	ID, KbID, VersionID int64
	Title, Mime         string
}

// DocumentContents reads a page of the currently published document tree.
func (s *Service) DocumentContents(ctx context.Context, actor Actor, documentID int64, after, limit int32) (_ DocumentContents, resultErr error) {
	if documentID <= 0 || after < 0 || limit < 1 || limit > 50 {
		return DocumentContents{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return DocumentContents{}, err
	}
	defer rollback(tx, &resultErr)
	doc, err := lockReadableDocument(ctx, queries, actor, documentID)
	if errors.Is(err, ErrNotFound) {
		return DocumentContents{}, denyContentRead(ctx, tx, queries, actor, "document.toc", "document", documentID)
	}
	if err != nil {
		return DocumentContents{}, fmt.Errorf("lock readable document: %w", err)
	}
	if _, err := lockReadableChunks(ctx, queries, actor.TenantID, doc); errors.Is(err, ErrNotFound) {
		return DocumentContents{}, denyContentRead(ctx, tx, queries, actor, "document.toc", "document", documentID)
	} else if err != nil {
		return DocumentContents{}, err
	}
	rows, err := queries.ListReadableSections(ctx, dbquery.ListReadableSectionsParams{
		TenantID: actor.TenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, AfterOrdinal: after, FetchLimit: limit + 1,
	})
	if err != nil {
		return DocumentContents{}, fmt.Errorf("list document sections: %w", err)
	}
	result := DocumentContents{KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, Title: doc.Title, Sections: make([]SectionReference, 0, min(len(rows), int(limit)))}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1].Ordinal
		result.NextAfter = &last
	}
	total := len(doc.Title)
	for _, row := range rows {
		ref, err := makeSectionReference(row.ID, row.ParentSectionID, row.Ordinal, row.Level, row.Heading, row.HeadingPath,
			row.HeadingBytes, row.PathBytes, row.PageStart, row.PageEnd, doc.Mime == "application/pdf")
		if err != nil {
			return DocumentContents{}, err
		}
		total += int(row.HeadingBytes + row.PathBytes)
		if total > maxReadBytes {
			return DocumentContents{}, ErrContentLimit
		}
		result.Sections = append(result.Sections, ref)
	}
	if err := auditContentRead(ctx, queries, actor, "document.toc", "document", documentID, "allow"); err != nil {
		return DocumentContents{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentContents{}, fmt.Errorf("commit document contents: %w", err)
	}
	return result, nil
}

// ReadSection returns one section only while its document version remains current.
func (s *Service) ReadSection(ctx context.Context, actor Actor, sectionID int64) (_ SectionContent, resultErr error) {
	if sectionID <= 0 {
		return SectionContent{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return SectionContent{}, err
	}
	defer rollback(tx, &resultErr)
	documentID, err := queries.GetSectionDocument(ctx, dbquery.GetSectionDocumentParams{TenantID: actor.TenantID, SectionID: sectionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return SectionContent{}, denyContentRead(ctx, tx, queries, actor, "section.read", "section", sectionID)
	}
	if err != nil {
		return SectionContent{}, fmt.Errorf("find section document: %w", err)
	}
	doc, err := lockReadableDocument(ctx, queries, actor, documentID)
	if errors.Is(err, ErrNotFound) {
		return SectionContent{}, denyContentRead(ctx, tx, queries, actor, "section.read", "section", sectionID)
	}
	if err != nil {
		return SectionContent{}, fmt.Errorf("lock section document: %w", err)
	}
	chunks, err := lockReadableChunks(ctx, queries, actor.TenantID, doc)
	if errors.Is(err, ErrNotFound) {
		return SectionContent{}, denyContentRead(ctx, tx, queries, actor, "section.read", "section", sectionID)
	}
	if err != nil {
		return SectionContent{}, err
	}
	row, err := queries.LockReadableSection(ctx, dbquery.LockReadableSectionParams{
		TenantID: actor.TenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, SectionID: sectionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SectionContent{}, denyContentRead(ctx, tx, queries, actor, "section.read", "section", sectionID)
	}
	if err != nil {
		return SectionContent{}, fmt.Errorf("lock readable section: %w", err)
	}
	if row.BodyBytes > maxReadBytes {
		return SectionContent{}, ErrContentLimit
	}
	if int32(len(row.BodyMd)) != row.BodyBytes || (row.BodyBytes > 0 && chunks[row.ID] == 0) {
		return SectionContent{}, errors.New("section content does not match current chunks")
	}
	ref, err := makeSectionReference(row.ID, row.ParentSectionID, row.Ordinal, row.Level, row.Heading, row.HeadingPath,
		row.HeadingBytes, row.PathBytes, row.PageStart, row.PageEnd, doc.Mime == "application/pdf")
	if err != nil {
		return SectionContent{}, err
	}
	if err := auditContentRead(ctx, queries, actor, "section.read", "section", sectionID, "allow"); err != nil {
		return SectionContent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SectionContent{}, fmt.Errorf("commit section read: %w", err)
	}
	return SectionContent{KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, Title: doc.Title, Section: ref, Markdown: row.BodyMd, TokenCount: row.TokenCount}, nil
}

// GetChunkNeighbors returns bounded neighboring chunks for a visible current chunk.
func (s *Service) GetChunkNeighbors(ctx context.Context, actor Actor, chunkID int64, before, after int32) (_ ChunkNeighbors, resultErr error) {
	if chunkID <= 0 || before < 0 || before > 10 || after < 0 || after > 10 {
		return ChunkNeighbors{}, ErrInvalidRequest
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return ChunkNeighbors{}, err
	}
	defer rollback(tx, &resultErr)
	loc, err := queries.GetChunkLocation(ctx, dbquery.GetChunkLocationParams{TenantID: actor.TenantID, ChunkID: chunkID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChunkNeighbors{}, denyContentRead(ctx, tx, queries, actor, "chunk.neighbors", "chunk", chunkID)
	}
	if err != nil {
		return ChunkNeighbors{}, fmt.Errorf("find chunk location: %w", err)
	}
	doc, err := lockReadableDocument(ctx, queries, actor, loc.DocumentID)
	if errors.Is(err, ErrNotFound) || doc.VersionID != loc.VersionID {
		return ChunkNeighbors{}, denyContentRead(ctx, tx, queries, actor, "chunk.neighbors", "chunk", chunkID)
	}
	if err != nil {
		return ChunkNeighbors{}, fmt.Errorf("lock chunk document: %w", err)
	}
	if _, err := lockReadableChunks(ctx, queries, actor.TenantID, doc); errors.Is(err, ErrNotFound) {
		return ChunkNeighbors{}, denyContentRead(ctx, tx, queries, actor, "chunk.neighbors", "chunk", chunkID)
	} else if err != nil {
		return ChunkNeighbors{}, err
	}
	secRow, err := queries.LockReadableSection(ctx, dbquery.LockReadableSectionParams{
		TenantID: actor.TenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, SectionID: loc.SectionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChunkNeighbors{}, denyContentRead(ctx, tx, queries, actor, "chunk.neighbors", "chunk", chunkID)
	}
	if err != nil {
		return ChunkNeighbors{}, fmt.Errorf("lock neighbor section: %w", err)
	}
	ref, err := makeSectionReference(secRow.ID, secRow.ParentSectionID, secRow.Ordinal, secRow.Level, secRow.Heading, secRow.HeadingPath,
		secRow.HeadingBytes, secRow.PathBytes, secRow.PageStart, secRow.PageEnd, doc.Mime == "application/pdf")
	if err != nil {
		return ChunkNeighbors{}, err
	}
	minOrdinal := max(1, loc.Ordinal-before)
	maxOrdinal := loc.Ordinal + after
	rows, err := queries.ListNeighborChunks(ctx, dbquery.ListNeighborChunksParams{
		TenantID: actor.TenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID,
		MinOrdinal: minOrdinal, MaxOrdinal: maxOrdinal,
	})
	if err != nil {
		return ChunkNeighbors{}, fmt.Errorf("list neighbor chunks: %w", err)
	}
	chunks := make([]NeighborChunk, 0, len(rows))
	var totalBytes int
	var foundTarget bool
	for _, row := range rows {
		totalBytes += len(row.Content)
		if totalBytes > maxReadBytes {
			return ChunkNeighbors{}, ErrContentLimit
		}
		item := NeighborChunk{
			ID: row.ID, Ordinal: row.Ordinal, Kind: row.Kind, Content: row.Content,
			TokenCount: row.TokenCount, IsTarget: row.ID == chunkID,
		}
		if item.IsTarget {
			foundTarget = true
		}
		if doc.Mime == "application/pdf" && row.PageStart.Valid && row.PageEnd.Valid {
			ps, pe := row.PageStart.Int32, row.PageEnd.Int32
			item.PageStart, item.PageEnd = &ps, &pe
		}
		chunks = append(chunks, item)
	}
	if !foundTarget {
		return ChunkNeighbors{}, denyContentRead(ctx, tx, queries, actor, "chunk.neighbors", "chunk", chunkID)
	}
	if err := auditContentRead(ctx, queries, actor, "chunk.neighbors", "chunk", chunkID, "allow"); err != nil {
		return ChunkNeighbors{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChunkNeighbors{}, fmt.Errorf("commit neighbor chunks: %w", err)
	}
	return ChunkNeighbors{KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID, Title: doc.Title, Section: ref, Chunks: chunks}, nil
}

func lockReadableDocument(ctx context.Context, queries *dbquery.Queries, actor Actor, documentID int64) (readableDocument, error) {
	row, err := queries.LockReadableDocument(ctx, dbquery.LockReadableDocumentParams{TenantID: actor.TenantID, DocumentID: documentID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return readableDocument{}, ErrNotFound
	}
	if err != nil {
		return readableDocument{}, fmt.Errorf("lock document: %w", err)
	}
	if !row.CurrentVersionID.Valid || row.CurrentVersionID.Int64 <= 0 {
		return readableDocument{}, ErrNotFound
	}
	if _, err := queries.LockReadableKnowledge(ctx, dbquery.LockReadableKnowledgeParams{TenantID: actor.TenantID, KbID: row.KbID}); errors.Is(err, pgx.ErrNoRows) {
		return readableDocument{}, ErrNotFound
	} else if err != nil {
		return readableDocument{}, fmt.Errorf("lock knowledge base: %w", err)
	}
	if _, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: row.KbID, UserID: actor.UserID}); errors.Is(err, pgx.ErrNoRows) {
		return readableDocument{}, ErrNotFound
	} else if err != nil {
		return readableDocument{}, fmt.Errorf("lock knowledge membership: %w", err)
	}
	if _, err := queries.LockReadableVersion(ctx, dbquery.LockReadableVersionParams{
		TenantID: actor.TenantID, KbID: row.KbID, DocumentID: row.ID, VersionID: row.CurrentVersionID.Int64,
	}); errors.Is(err, pgx.ErrNoRows) {
		return readableDocument{}, ErrNotFound
	} else if err != nil {
		return readableDocument{}, fmt.Errorf("lock current version: %w", err)
	}
	return readableDocument{ID: row.ID, KbID: row.KbID, VersionID: row.CurrentVersionID.Int64, Title: row.Title, Mime: row.Mime}, nil
}

func lockReadableChunks(ctx context.Context, queries *dbquery.Queries, tenantID int64, doc readableDocument) (map[int64]int, error) {
	plan, err := queries.GetReadablePlan(ctx, dbquery.GetReadablePlanParams{TenantID: tenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID})
	if err != nil {
		return nil, fmt.Errorf("read document plan: %w", err)
	}
	sectionCount, err := queries.CountReadableSections(ctx, dbquery.CountReadableSectionsParams{TenantID: tenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID})
	if err != nil {
		return nil, fmt.Errorf("count readable document sections: %w", err)
	}
	rows, err := queries.LockReadableChunks(ctx, dbquery.LockReadableChunksParams{TenantID: tenantID, KbID: doc.KbID, DocumentID: doc.ID, VersionID: doc.VersionID})
	if err != nil {
		return nil, fmt.Errorf("lock readable chunks: %w", err)
	}
	if len(rows) > maxReadChunks {
		return nil, ErrContentLimit
	}
	if len(rows) != int(plan.ChunkCount) || sectionCount != int64(plan.SectionCount) {
		return nil, errors.New("published document does not match its ingest plan")
	}
	sections := make(map[int64]int)
	for _, row := range rows {
		if !row.Allowed {
			return nil, ErrNotFound
		}
		sections[row.SectionID]++
	}
	return sections, nil
}

func makeSectionReference(id int64, parent pgtype.Int8, ordinal int32, level int16, heading string, path []string,
	headingBytes, pathBytes int32, start, end pgtype.Int4, physicalPages bool) (SectionReference, error) {
	if headingBytes > maxHeadingBytes || pathBytes > maxPathBytes {
		return SectionReference{}, ErrContentLimit
	}
	pathLength := 0
	for _, part := range path {
		pathLength += len(part)
	}
	if id <= 0 || ordinal <= 0 || level < 0 || level > 9 || headingBytes != int32(len(heading)) || pathBytes != int32(pathLength) ||
		start.Valid != end.Valid || (start.Valid && (start.Int32 <= 0 || end.Int32 < start.Int32)) || (parent.Valid && parent.Int64 <= 0) {
		return SectionReference{}, errors.New("invalid section reference")
	}
	ref := SectionReference{ID: id, Ordinal: ordinal, Level: level, Heading: heading, HeadingPath: append([]string{}, path...)}
	if parent.Valid {
		ref.ParentID = &parent.Int64
	}
	if physicalPages && start.Valid {
		ref.PageStart, ref.PageEnd = &start.Int32, &end.Int32
	}
	return ref, nil
}

func auditContentRead(ctx context.Context, queries *dbquery.Queries, actor Actor, action, objectType string, objectID int64, result string) error {
	if err := queries.AuditContentRead(ctx, dbquery.AuditContentReadParams{
		TenantID: actor.TenantID, UserID: actor.UserID, Action: action, ObjectType: objectType, ObjectID: objectID, Result: result,
	}); err != nil {
		return fmt.Errorf("audit content read: %w", err)
	}
	return nil
}

func denyContentRead(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, action, objectType string, objectID int64) error {
	if err := auditContentRead(ctx, queries, actor, action, objectType, objectID, "deny"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit content denial: %w", err)
	}
	return ErrNotFound
}
