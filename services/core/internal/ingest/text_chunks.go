package ingest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"tuskrag.local/core/internal/markdown"
)

var ErrTextUnitTooLarge = errors.New("sentence exceeds chunk limits")

const (
	maxTextChunkTokens    = 800
	maxTextOverlapTokens  = 60
	maxTextChunkSentences = 512
	maxTextSentences      = 100000
)

// Paragraphs must be a contiguous run in one section; other block types form boundaries.
type TextSplitRequest struct {
	Paragraphs  []NormalizedBlock
	HeadingPath []string
	TargetMin   int
	TargetMax   int
	HardMax     int
	OverlapMax  int
	CountTokens TokenCounter
}

// Text and fragments are untrusted source content; Markdown is escaped for display.
type TextChunk struct {
	Text          string
	Markdown      string
	SearchText    string
	TokenCount    int
	OverlapTokens int
	PageStart     int64
	PageEnd       int64
	HeadingPath   []string
	Fragments     []TextFragment
}

// ParagraphIndex is zero-based within the input; -1 denotes overlap from an earlier parent piece.
type TextFragment struct {
	ParagraphIndex int
	Page           int64
	Text           string
	BBox           []float64
	Overlap        bool
}

type sentence struct {
	paragraph int
	page      int64
	text      string
	bbox      []float64
}

type selectedSentence struct {
	sentence sentence
	overlap  bool
}

type textCandidate struct {
	text, markdown, search string
	tokens                 int
}

// SplitText groups paragraphs at sentence boundaries and keeps at most one overlapping sentence.
func SplitText(ctx context.Context, request TextSplitRequest) ([]TextChunk, error) {
	return splitText(ctx, request, nil)
}

func splitText(ctx context.Context, request TextSplitRequest, previous *sentence) ([]TextChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.Paragraphs) == 0 || len(request.Paragraphs) > maxNormalizedBlocks || len(request.HeadingPath) > 6 ||
		request.TargetMin <= 0 || request.TargetMax < request.TargetMin || request.HardMax < request.TargetMax || request.HardMax > maxTextChunkTokens ||
		request.OverlapMax < 0 || request.OverlapMax > maxTextOverlapTokens || request.OverlapMax > request.HardMax || request.CountTokens == nil {
		return nil, ErrInvalidChunkConfig
	}
	validation := assembler{ctx: ctx}
	for _, heading := range request.HeadingPath {
		if strings.TrimSpace(heading) == "" {
			return nil, ErrInvalidChunkConfig
		}
		if err := validation.consume(heading); err != nil {
			return nil, err
		}
	}
	atoms, err := collectSentences(ctx, request.Paragraphs, &validation)
	if err != nil {
		return nil, err
	}
	prefix := strings.Join(request.HeadingPath, " › ")
	var chunks []TextChunk
	for cursor := 0; cursor < len(atoms); {
		selected := make([]selectedSentence, 0, maxTextChunkSentences)
		overlapTokens := 0
		if previous != nil && request.OverlapMax > 0 {
			count, err := checkedTokenCount(ctx, request.CountTokens, previous.text)
			if err != nil {
				return nil, err
			}
			if count <= request.OverlapMax {
				selected = append(selected, selectedSentence{sentence: *previous, overlap: true})
				overlapTokens = count
			}
		}
		start := cursor
		var current textCandidate
		for cursor < len(atoms) && len(selected) < maxTextChunkSentences {
			selected = append(selected, selectedSentence{sentence: atoms[cursor]})
			candidate, fits, err := evaluateTextGroup(ctx, request, prefix, selected)
			if err != nil {
				return nil, err
			}
			if !fits {
				selected = selected[:len(selected)-1]
				if cursor == start && len(selected) > 0 && selected[0].overlap {
					selected = selected[:0]
					overlapTokens = 0
					continue
				}
				if cursor == start {
					return nil, ErrTextUnitTooLarge
				}
				break
			}
			if cursor > start && candidate.tokens > request.TargetMax && current.tokens >= request.TargetMin {
				selected = selected[:len(selected)-1]
				break
			}
			current = candidate
			cursor++
			if current.tokens >= request.TargetMax {
				break
			}
		}
		if cursor == start {
			return nil, ErrTextUnitTooLarge
		}
		chunk := TextChunk{Text: current.text, Markdown: current.markdown, SearchText: current.search,
			TokenCount: current.tokens, OverlapTokens: overlapTokens, PageStart: selected[0].sentence.page,
			PageEnd: selected[len(selected)-1].sentence.page, HeadingPath: slices.Clone(request.HeadingPath)}
		for _, item := range selected {
			chunk.Fragments = append(chunk.Fragments, TextFragment{ParagraphIndex: item.sentence.paragraph,
				Page: item.sentence.page, Text: item.sentence.text, BBox: slices.Clone(item.sentence.bbox), Overlap: item.overlap})
		}
		chunks = append(chunks, chunk)
		previous = &atoms[cursor-1]
	}
	return chunks, nil
}

func collectSentences(ctx context.Context, blocks []NormalizedBlock, validation *assembler) ([]sentence, error) {
	var result []sentence
	var previousPage int64
	for index, block := range blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if block.Kind != "paragraph" || block.Text == nil || len(block.Cells) != 0 || block.Page <= 0 || block.Page < previousPage ||
			!validBBox(block.BBox) || strings.TrimSpace(*block.Text) == "" {
			return nil, ErrInvalidParsedDocument
		}
		if err := validation.consume(*block.Text); err != nil {
			return nil, err
		}
		previousPage = block.Page
		value := *block.Text
		for start := 0; start < len(value); {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			end := sentenceEnd(value, start)
			if len(result) >= maxTextSentences {
				return nil, ErrNormalizedLimit
			}
			result = append(result, sentence{paragraph: index, page: block.Page, text: value[start:end], bbox: block.BBox})
			start = end
		}
	}
	return result, nil
}

func sentenceEnd(value string, start int) int {
	for position := start; position < len(value); {
		character, size := utf8.DecodeRuneInString(value[position:])
		position += size
		if !isSentenceBoundary(character, value[position:]) {
			continue
		}
		for position < len(value) {
			next, width := utf8.DecodeRuneInString(value[position:])
			if !isSentenceCloser(next) && !isSentencePunctuation(next) && !unicode.IsSpace(next) {
				break
			}
			position += width
		}
		return position
	}
	return len(value)
}

func isSentenceBoundary(character rune, remaining string) bool {
	if character == '\n' || character == '。' || character == '！' || character == '？' || character == '!' || character == '?' {
		return true
	}
	if character != '.' {
		return false
	}
	for remaining != "" {
		next, width := utf8.DecodeRuneInString(remaining)
		if isSentenceCloser(next) {
			remaining = remaining[width:]
			continue
		}
		return unicode.IsSpace(next)
	}
	return true
}

func isSentenceCloser(character rune) bool {
	return strings.ContainsRune("\"'”’）)】]」』》", character)
}

func isSentencePunctuation(character rune) bool {
	return strings.ContainsRune("。！？!?", character)
}

func evaluateTextGroup(ctx context.Context, request TextSplitRequest, prefix string, selected []selectedSentence) (textCandidate, bool, error) {
	var raw, display, paragraph chunkBuffer
	previousParagraph := -2
	flush := func() error {
		if paragraph.Len() == 0 {
			return nil
		}
		if display.Len() > 0 {
			if err := display.append("\n\n"); err != nil {
				return err
			}
		}
		if err := display.append(markdown.EscapeInline(paragraph.String())); err != nil {
			return err
		}
		paragraph = chunkBuffer{}
		return nil
	}
	for _, item := range selected {
		if previousParagraph != -2 && item.sentence.paragraph != previousParagraph {
			if err := flush(); err != nil {
				return textCandidate{}, false, nil
			}
			if err := raw.append("\n\n"); err != nil {
				return textCandidate{}, false, nil
			}
		}
		if err := raw.append(item.sentence.text); err != nil {
			return textCandidate{}, false, nil
		}
		if err := paragraph.append(item.sentence.text); err != nil {
			return textCandidate{}, false, nil
		}
		previousParagraph = item.sentence.paragraph
	}
	if err := flush(); err != nil {
		return textCandidate{}, false, nil
	}
	var search chunkBuffer
	if prefix != "" {
		if err := search.append(prefix); err != nil {
			return textCandidate{}, false, nil
		}
		if err := search.append("\n"); err != nil {
			return textCandidate{}, false, nil
		}
	}
	if err := search.append(raw.String()); err != nil {
		return textCandidate{}, false, nil
	}
	if search.Len() > maxCountInputBytes {
		return textCandidate{}, false, nil
	}
	count, err := checkedTokenCount(ctx, request.CountTokens, search.String())
	if err != nil {
		if errors.Is(err, errCountInputTooLarge) {
			return textCandidate{}, false, nil
		}
		return textCandidate{}, false, err
	}
	if count > request.HardMax {
		return textCandidate{}, false, nil
	}
	return textCandidate{text: raw.String(), markdown: display.String(), search: search.String(), tokens: count}, true, nil
}
