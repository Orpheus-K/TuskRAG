package ingest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"tuskrag.local/core/internal/markdown"
)

var ErrTableHeaderUnconfirmed = errors.New("table header must be explicitly confirmed")

// BlockPosition uses one-based original section and block positions.
type BlockPosition struct {
	Section int
	Block   int
}

type PlanBuildRequest struct {
	Document         NormalizedDocument
	MIME             string
	EmbeddingSpaceID int64
	EmbeddingProfile EmbeddingProfile
	ConfigRevision   int32
	ParentMaxTokens  int
	ChildTargetMin   int
	ChildTargetMax   int
	ChildHardMax     int
	OverlapMax       int
	CountTokens      TokenCounter
	TableHeaders     map[BlockPosition]int
}

// BuildPlan links normalized sections to bounded parent pieces and child
// chunks. It does not infer table headers or split oversized code blocks.
func BuildPlan(ctx context.Context, request PlanBuildRequest) (PreparedPlan, error) {
	if err := ctx.Err(); err != nil {
		return PreparedPlan{}, err
	}
	if request.EmbeddingSpaceID <= 0 || !validEmbeddingProfile(request.EmbeddingProfile) || request.ConfigRevision <= 0 ||
		request.ParentMaxTokens <= 0 || int64(request.ParentMaxTokens) > math.MaxInt32 || request.ChildTargetMin <= 0 ||
		request.ChildTargetMax < request.ChildTargetMin || request.ChildHardMax < request.ChildTargetMax || request.ChildHardMax > maxTextChunkTokens ||
		request.OverlapMax < 0 || request.OverlapMax > maxTextOverlapTokens || request.OverlapMax > request.ChildHardMax || request.CountTokens == nil {
		return PreparedPlan{}, ErrInvalidChunkConfig
	}
	if len(request.Document.Sections) == 0 || len(request.Document.Sections) > maxPlanSections {
		return PreparedPlan{}, ErrInvalidPlan
	}
	switch request.MIME {
	case "text/plain", "text/markdown", "text/csv", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
	default:
		return PreparedPlan{}, ErrUnsupportedChunkMIME
	}
	for position := range request.TableHeaders {
		if position.Section < 1 || position.Section > len(request.Document.Sections) || position.Block < 1 ||
			position.Block > len(request.Document.Sections[position.Section-1].Blocks) ||
			request.Document.Sections[position.Section-1].Blocks[position.Block-1].Kind != "table" {
			return PreparedPlan{}, ErrInvalidChunkConfig
		}
	}
	plan := PreparedPlan{EmbeddingSpaceID: request.EmbeddingSpaceID, EmbeddingProfile: request.EmbeddingProfile, ConfigRevision: request.ConfigRevision}
	originalToPlan := make([]int, len(request.Document.Sections))
	for index, section := range request.Document.Sections {
		if err := ctx.Err(); err != nil {
			return PreparedPlan{}, err
		}
		if section.Ordinal != index+1 || section.ParentIndex < -1 || section.ParentIndex >= index ||
			section.PageStart > math.MaxInt32 || section.PageEnd > math.MaxInt32 {
			return PreparedPlan{}, ErrInvalidParsedDocument
		}
		parent := -1
		if section.ParentIndex >= 0 {
			parent = originalToPlan[section.ParentIndex]
		}
		expanded, positions, boundaries, err := expandSectionBlocks(ctx, request, section)
		if err != nil {
			return PreparedPlan{}, err
		}
		pieces, err := splitPlanParents(ctx, request, expanded, boundaries)
		if err != nil {
			return PreparedPlan{}, fmt.Errorf("split parent section %d: %w", section.Ordinal, err)
		}
		if len(pieces) > 1 && section.Level > 0 {
			anchor := makePreparedSection(section, parent, section.PageStart, section.PageEnd, "", 0)
			originalToPlan[index] = len(plan.Sections)
			if err := addPlanSection(&plan, anchor); err != nil {
				return PreparedPlan{}, err
			}
			parent = originalToPlan[index]
		} else {
			originalToPlan[index] = len(plan.Sections)
		}
		blockOffset := 0
		var previous *planOverlap
		for _, piece := range pieces {
			pieceParent := parent
			if section.Level == 0 {
				pieceParent = -1
			}
			start, end := piece.PageStart, piece.PageEnd
			if len(pieces) == 1 {
				start, end = section.PageStart, section.PageEnd
			}
			prepared := makePreparedSection(section, pieceParent, start, end, piece.BodyMarkdown, piece.TokenCount)
			sectionIndex := len(plan.Sections)
			if err := addPlanSection(&plan, prepared); err != nil {
				return PreparedPlan{}, err
			}
			previous, err = appendPieceChunks(ctx, &plan, request, sectionIndex, blockOffset, piece, positions, previous)
			if err != nil {
				return PreparedPlan{}, err
			}
			blockOffset += len(piece.Blocks)
		}
	}
	if _, err := validatePreparedPlan(ctx, plan); err != nil {
		return PreparedPlan{}, err
	}
	return plan, nil
}

// Large tables and paragraphs are separated before parent sections are built.
// Positions retain the original block identity and table header confirmations.
func expandSectionBlocks(ctx context.Context, request PlanBuildRequest, section Section) (Section, []BlockPosition, []bool, error) {
	if err := validateParentSection(ctx, ParentSplitRequest{Section: section, MaxTokens: request.ParentMaxTokens, CountTokens: request.CountTokens}); err != nil {
		return Section{}, nil, nil, err
	}
	expanded := section
	expanded.Blocks = nil
	var positions []BlockPosition
	var boundaries []bool
	var body boundedMarkdown
	previousSplit := false
	add := func(block NormalizedBlock, position BlockPosition, split bool) error {
		if len(expanded.Blocks) >= maxNormalizedBlocks {
			return ErrNormalizedLimit
		}
		if len(expanded.Blocks) > 0 {
			if err := body.append("\n\n"); err != nil {
				return err
			}
		}
		if err := body.append(block.Markdown); err != nil {
			return err
		}
		expanded.Blocks = append(expanded.Blocks, block)
		positions = append(positions, position)
		boundaries = append(boundaries, split || previousSplit)
		previousSplit = split
		return nil
	}
	var tableParts int
	for index, block := range section.Blocks {
		if err := ctx.Err(); err != nil {
			return Section{}, nil, nil, err
		}
		position := BlockPosition{Section: section.Ordinal, Block: index + 1}
		if block.Kind == "paragraph" || block.Kind == "list" {
			large, err := exceedsParentLimit(ctx, request, block.Markdown)
			if err != nil {
				return Section{}, nil, nil, err
			}
			if large {
				if block.Kind == "paragraph" {
					chunks, err := splitParentParagraph(ctx, request, block)
					if err != nil {
						return Section{}, nil, nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
					}
					for _, chunk := range chunks {
						part := block
						value := chunk.Text
						part.Text, part.Markdown, part.BBox = &value, chunk.Markdown, slices.Clone(block.BBox)
						if err := add(part, position, true); err != nil {
							return Section{}, nil, nil, err
						}
					}
				} else {
					chunks, err := SplitList(ctx, StructuredSplitRequest{Block: block, MaxTokens: min(request.ParentMaxTokens, request.ChildHardMax),
						CountTokens: request.CountTokens, countMarkdown: true})
					if err != nil {
						return Section{}, nil, nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
					}
					for _, chunk := range chunks {
						part := block
						value := chunk.Markdown
						part.Text, part.Markdown, part.BBox = &value, value, slices.Clone(block.BBox)
						if err := add(part, position, false); err != nil {
							return Section{}, nil, nil, err
						}
					}
				}
				continue
			}
		}
		if block.Kind != "table" {
			if err := add(block, position, false); err != nil {
				return Section{}, nil, nil, err
			}
			continue
		}
		header, confirmed := request.TableHeaders[position]
		if !confirmed {
			return Section{}, nil, nil, fmt.Errorf("section %d block %d: %w", position.Section, position.Block, ErrTableHeaderUnconfirmed)
		}
		chunks, err := SplitTable(ctx, TableSplitRequest{Block: block, HeadingPath: section.HeadingPath, MIME: request.MIME,
			HeaderRow: header, MaxTokens: min(request.ChildHardMax, request.ParentMaxTokens), CountTokens: request.CountTokens})
		if err != nil {
			return Section{}, nil, nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
		}
		for _, chunk := range chunks {
			tableParts++
			if tableParts > maxPlanChunks {
				return Section{}, nil, nil, ErrPlanLimit
			}
			part := block
			part.BBox = slices.Clone(block.BBox)
			part.Cells, part.Markdown = chunk.Cells, chunk.Markdown
			if err := add(part, position, false); err != nil {
				return Section{}, nil, nil, err
			}
		}
	}
	expanded.BodyMarkdown = body.String()
	return expanded, positions, boundaries, nil
}

func exceedsParentLimit(ctx context.Context, request PlanBuildRequest, body string) (bool, error) {
	if len(body) > maxCountInputBytes {
		return true, nil
	}
	count, err := checkedTokenCount(ctx, request.CountTokens, body)
	return count > request.ParentMaxTokens, err
}

func splitParentParagraph(ctx context.Context, request PlanBuildRequest, block NormalizedBlock) ([]TextChunk, error) {
	limit := min(request.ParentMaxTokens, request.ChildHardMax)
	target := min(request.ChildTargetMax, limit)
	countBody := func(ctx context.Context, value string) (int, error) {
		escaped := markdown.EscapeInline(value)
		if len(escaped) > maxCountInputBytes {
			return 0, errCountInputTooLarge
		}
		return request.CountTokens(ctx, escaped)
	}
	return SplitText(ctx, TextSplitRequest{Paragraphs: []NormalizedBlock{block}, TargetMin: min(request.ChildTargetMin, target),
		TargetMax: target, HardMax: limit, CountTokens: countBody})
}

func splitPlanParents(ctx context.Context, request PlanBuildRequest, section Section, boundaries []bool) ([]ParentPiece, error) {
	if len(section.Blocks) == 0 {
		return SplitParentSection(ctx, ParentSplitRequest{Section: section, MaxTokens: request.ParentMaxTokens, CountTokens: request.CountTokens})
	}
	var pieces []ParentPiece
	start := 0
	for end := 1; end <= len(section.Blocks); end++ {
		if end < len(section.Blocks) && !boundaries[end] {
			continue
		}
		part := section
		part.Blocks = section.Blocks[start:end]
		if start > 0 {
			part.PageStart = part.Blocks[0].Page
		}
		var body boundedMarkdown
		for index, block := range part.Blocks {
			if index > 0 {
				if err := body.append("\n\n"); err != nil {
					return nil, err
				}
			}
			if err := body.append(block.Markdown); err != nil {
				return nil, err
			}
		}
		part.BodyMarkdown = body.String()
		group, err := SplitParentSection(ctx, ParentSplitRequest{Section: part, MaxTokens: request.ParentMaxTokens, CountTokens: request.CountTokens})
		if err != nil {
			return nil, err
		}
		for _, piece := range group {
			piece.Sequence = len(pieces) + 1
			pieces = append(pieces, piece)
		}
		start = end
	}
	return pieces, nil
}

func makePreparedSection(section Section, parent int, start, end int64, body string, tokens int) PreparedSection {
	return PreparedSection{ParentIndex: parent, Level: int16(section.Level), Heading: section.Heading, HeadingPath: slices.Clone(section.HeadingPath),
		PageStart: int32(start), PageEnd: int32(end), BodyMarkdown: body, TokenCount: int32(tokens)}
}

func addPlanSection(plan *PreparedPlan, section PreparedSection) error {
	if len(plan.Sections) >= maxPlanSections {
		return ErrPlanLimit
	}
	plan.Sections = append(plan.Sections, section)
	return nil
}

func addPlanChunk(plan *PreparedPlan, chunk PreparedChunk) error {
	if len(plan.Chunks) >= maxPlanChunks {
		return ErrPlanLimit
	}
	plan.Chunks = append(plan.Chunks, chunk)
	return nil
}

type planOverlap struct {
	sentence sentence
	origin   BlockPosition
}

func appendPieceChunks(ctx context.Context, plan *PreparedPlan, request PlanBuildRequest, sectionIndex, blockOffset int, piece ParentPiece, positions []BlockPosition, previous *planOverlap) (*planOverlap, error) {
	for index := 0; index < len(piece.Blocks); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		block := piece.Blocks[index]
		position := positions[blockOffset+index]
		switch block.Kind {
		case "paragraph":
			end := index + 1
			for end < len(piece.Blocks) && piece.Blocks[end].Kind == "paragraph" {
				end++
			}
			var prior *sentence
			if previous != nil {
				value := previous.sentence
				value.paragraph = -1
				if previous.origin == position {
					value.paragraph = 0
				}
				prior = &value
			}
			chunks, err := splitText(ctx, TextSplitRequest{Paragraphs: piece.Blocks[index:end], HeadingPath: piece.HeadingPath,
				TargetMin: request.ChildTargetMin, TargetMax: request.ChildTargetMax, HardMax: request.ChildHardMax,
				OverlapMax: request.OverlapMax, CountTokens: request.CountTokens}, prior)
			if err != nil {
				return nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
			}
			for _, chunk := range chunks {
				if chunk.PageStart < int64(plan.Sections[sectionIndex].PageStart) {
					plan.Sections[sectionIndex].PageStart = int32(chunk.PageStart)
				}
				if err := addPlanChunk(plan, PreparedChunk{SectionIndex: sectionIndex, Kind: "text", Content: chunk.Text, SearchText: chunk.SearchText,
					PageStart: int32(chunk.PageStart), PageEnd: int32(chunk.PageEnd), TokenCount: int32(chunk.TokenCount)}); err != nil {
					return nil, err
				}
			}
			last := chunks[len(chunks)-1].Fragments
			fragment := last[len(last)-1]
			previous = &planOverlap{sentence: sentence{page: fragment.Page, text: fragment.Text, bbox: fragment.BBox},
				origin: positions[blockOffset+end-1]}
			index = end
			continue
		case "table":
			previous = nil
			header, confirmed := request.TableHeaders[position]
			if !confirmed {
				return nil, fmt.Errorf("section %d block %d: %w", position.Section, position.Block, ErrTableHeaderUnconfirmed)
			}
			chunks, err := SplitTable(ctx, TableSplitRequest{Block: block, HeadingPath: piece.HeadingPath, MIME: request.MIME,
				HeaderRow: header, MaxTokens: request.ChildHardMax, CountTokens: request.CountTokens})
			if err != nil {
				return nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
			}
			for _, chunk := range chunks {
				if err := addPlanChunk(plan, PreparedChunk{SectionIndex: sectionIndex, Kind: "table", Content: chunk.Markdown, SearchText: chunk.SearchText,
					PageStart: int32(chunk.Page), PageEnd: int32(chunk.Page), TokenCount: int32(chunk.TokenCount)}); err != nil {
					return nil, err
				}
			}
		case "list", "code":
			previous = nil
			structured := StructuredSplitRequest{Block: block, HeadingPath: piece.HeadingPath, MaxTokens: request.ChildHardMax, CountTokens: request.CountTokens}
			var chunks []StructuredChunk
			if block.Kind == "list" {
				var err error
				chunks, err = SplitList(ctx, structured)
				if err != nil {
					return nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
				}
			} else {
				chunk, err := SplitCode(ctx, structured)
				if err != nil {
					return nil, fmt.Errorf("split section %d block %d: %w", position.Section, position.Block, err)
				}
				chunks = []StructuredChunk{chunk}
			}
			for _, chunk := range chunks {
				if err := addPlanChunk(plan, PreparedChunk{SectionIndex: sectionIndex, Kind: chunk.Kind, Content: chunk.Content, SearchText: chunk.SearchText,
					PageStart: int32(chunk.Page), PageEnd: int32(chunk.Page), TokenCount: int32(chunk.TokenCount)}); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("section %d block %d: %w", position.Section, position.Block, ErrUnsupportedParsedDocument)
		}
		index++
	}
	return previous, nil
}
