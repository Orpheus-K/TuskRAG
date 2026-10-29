package ingest

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
)

var ErrParentUnitTooLarge = errors.New("section block exceeds parent token limit")

const maxParentGroupBlocks = 512

type ParentSplitRequest struct {
	Section     Section
	MaxTokens   int
	CountTokens TokenCounter
}

// ParentPiece identifies a contiguous part of one original section. Empty
// heading-only sections have no blocks and a zero token count.
type ParentPiece struct {
	SectionOrdinal int
	Sequence       int
	HeadingPath    []string
	PageStart      int64
	PageEnd        int64
	BodyMarkdown   string
	TokenCount     int
	Blocks         []NormalizedBlock
}

// SplitParentSection keeps complete blocks in source order. An individual
// block that cannot fit must be split by its structural splitter first.
func SplitParentSection(ctx context.Context, request ParentSplitRequest) ([]ParentPiece, error) {
	if err := validateParentSection(ctx, request); err != nil {
		return nil, err
	}
	section := request.Section
	if len(section.Blocks) == 0 {
		return []ParentPiece{{SectionOrdinal: section.Ordinal, Sequence: 1, HeadingPath: slices.Clone(section.HeadingPath),
			PageStart: section.PageStart, PageEnd: section.PageEnd}}, nil
	}
	var pieces []ParentPiece
	var group []NormalizedBlock
	var body string
	var tokens int
	finish := func() {
		piece := ParentPiece{SectionOrdinal: section.Ordinal, Sequence: len(pieces) + 1, HeadingPath: slices.Clone(section.HeadingPath),
			PageStart: group[0].Page, PageEnd: group[len(group)-1].Page, BodyMarkdown: body, TokenCount: tokens}
		if len(pieces) == 0 && section.PageStart < piece.PageStart {
			piece.PageStart = section.PageStart
		}
		piece.Blocks = make([]NormalizedBlock, len(group))
		for index, block := range group {
			piece.Blocks[index] = cloneNormalizedBlock(block)
		}
		pieces = append(pieces, piece)
		group = nil
	}
	for _, block := range section.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(group) == maxParentGroupBlocks {
			finish()
		}
		candidate, candidateTokens, fits, err := parentCandidate(ctx, request, group, block)
		if err != nil {
			return nil, err
		}
		if !fits && len(group) > 0 {
			finish()
			candidate, candidateTokens, fits, err = parentCandidate(ctx, request, nil, block)
			if err != nil {
				return nil, err
			}
		}
		if !fits {
			return nil, ErrParentUnitTooLarge
		}
		group = append(group, block)
		body, tokens = candidate, candidateTokens
	}
	finish()
	return pieces, nil
}

func validateParentSection(ctx context.Context, request ParentSplitRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	section := request.Section
	if request.CountTokens == nil || request.MaxTokens <= 0 || int64(request.MaxTokens) > math.MaxInt32 ||
		section.Ordinal <= 0 || section.ParentIndex < -1 || section.PageStart <= 0 || section.PageEnd < section.PageStart ||
		len(section.Blocks) > maxNormalizedBlocks || len(section.BodyMarkdown) > maxNormalizedBytes || len(section.HeadingPath) > 6 {
		return ErrInvalidChunkConfig
	}
	if section.Level == 0 {
		if section.Heading != "" || len(section.HeadingPath) != 0 || section.ParentIndex != -1 {
			return ErrInvalidParsedDocument
		}
	} else if section.Level < 0 || section.Level > 6 || strings.TrimSpace(section.Heading) == "" ||
		len(section.HeadingPath) == 0 || section.HeadingPath[len(section.HeadingPath)-1] != section.Heading {
		return ErrInvalidParsedDocument
	}
	validation := assembler{ctx: ctx}
	for _, heading := range section.HeadingPath {
		if strings.TrimSpace(heading) == "" {
			return ErrInvalidParsedDocument
		}
		if err := validation.consume(heading); err != nil {
			return err
		}
	}
	var offset int
	previousPage := section.PageStart
	for index, block := range section.Blocks {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if block.Page < previousPage || block.Page > section.PageEnd || !validBBox(block.BBox) || block.Markdown == "" {
			return ErrInvalidParsedDocument
		}
		switch block.Kind {
		case "paragraph", "list", "code":
			if block.Text == nil || len(block.Cells) != 0 {
				return ErrInvalidParsedDocument
			}
		case "table":
			if block.Text != nil || len(block.Cells) == 0 {
				return ErrInvalidParsedDocument
			}
		default:
			return ErrUnsupportedParsedDocument
		}
		previousPage = block.Page
		if index > 0 {
			if !strings.HasPrefix(section.BodyMarkdown[offset:], "\n\n") {
				return ErrInvalidParsedDocument
			}
			offset += 2
		}
		if !strings.HasPrefix(section.BodyMarkdown[offset:], block.Markdown) {
			return ErrInvalidParsedDocument
		}
		offset += len(block.Markdown)
	}
	if offset != len(section.BodyMarkdown) {
		return ErrInvalidParsedDocument
	}
	return nil
}

func parentCandidate(ctx context.Context, request ParentSplitRequest, current []NormalizedBlock, next NormalizedBlock) (string, int, bool, error) {
	var output boundedMarkdown
	for _, block := range current {
		if output.Len() > 0 {
			if err := output.append("\n\n"); err != nil {
				return "", 0, false, nil
			}
		}
		if err := output.append(block.Markdown); err != nil {
			return "", 0, false, nil
		}
		if output.Len() > maxCountInputBytes {
			return "", 0, false, nil
		}
	}
	if output.Len() > 0 {
		if err := output.append("\n\n"); err != nil {
			return "", 0, false, nil
		}
	}
	if err := output.append(next.Markdown); err != nil {
		return "", 0, false, nil
	}
	if output.Len() > maxCountInputBytes {
		return "", 0, false, nil
	}
	count, err := checkedTokenCount(ctx, request.CountTokens, output.String())
	if err != nil {
		return "", 0, false, err
	}
	if count > request.MaxTokens {
		return "", 0, false, nil
	}
	return output.String(), count, true, nil
}

func cloneNormalizedBlock(block NormalizedBlock) NormalizedBlock {
	copyBlock := block
	copyBlock.BBox = slices.Clone(block.BBox)
	if block.Text != nil {
		value := *block.Text
		copyBlock.Text = &value
	}
	if block.Cells != nil {
		copyBlock.Cells = make([][]string, len(block.Cells))
		for index, row := range block.Cells {
			copyBlock.Cells[index] = slices.Clone(row)
		}
	}
	return copyBlock
}
