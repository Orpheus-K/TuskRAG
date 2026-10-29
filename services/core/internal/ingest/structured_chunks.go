package ingest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"tuskrag.local/core/internal/markdown"
	"tuskrag.local/core/internal/parser"
)

var ErrStructuredUnitTooLarge = errors.New("list item or code block exceeds chunk limits")

const maxListGroupItems = 512

type StructuredSplitRequest struct {
	Block       NormalizedBlock
	HeadingPath []string
	MaxTokens   int
	CountTokens TokenCounter
	// Parent fragments are counted as Markdown; child search counts are recomputed.
	countMarkdown bool
}

// List item positions are one-based. Subitem positions are set only when an
// oversized top-level item is split at its direct nested items.
type StructuredChunk struct {
	Kind         string
	Content      string
	Markdown     string
	SearchText   string
	TokenCount   int
	Page         int64
	BBox         []float64
	HeadingPath  []string
	FirstItem    int
	LastItem     int
	FirstSubitem int
	LastSubitem  int
}

// SplitCode preserves a complete code block and rejects blocks over the limit.
func SplitCode(ctx context.Context, request StructuredSplitRequest) (StructuredChunk, error) {
	if err := validateStructuredRequest(ctx, request, "code"); err != nil {
		return StructuredChunk{}, err
	}
	value := *request.Block.Text
	if value == "" {
		return StructuredChunk{}, ErrInvalidParsedDocument
	}
	chunk, fits, err := structuredCandidate(ctx, request, value, fencedCode(value))
	if err != nil {
		return StructuredChunk{}, err
	}
	if !fits {
		return StructuredChunk{}, ErrStructuredUnitTooLarge
	}
	return chunk, nil
}

// SplitList preserves top-level items; a long item may split at its direct
// nested list items while repeating the enclosing item text and marker.
func SplitList(ctx context.Context, request StructuredSplitRequest) ([]StructuredChunk, error) {
	if err := validateStructuredRequest(ctx, request, "list"); err != nil {
		return nil, err
	}
	canonical, err := parser.CanonicalMarkdownList(ctx, *request.Block.Text)
	if err != nil {
		switch {
		case errors.Is(err, parser.ErrLimitExceeded):
			return nil, fmt.Errorf("%w: list: %w", ErrNormalizedLimit, err)
		case errors.Is(err, parser.ErrUnsupportedFormat), errors.Is(err, parser.ErrParse):
			return nil, fmt.Errorf("%w: list: %w", ErrUnsupportedParsedDocument, err)
		default:
			return nil, fmt.Errorf("normalize list: %w", err)
		}
	}
	root, err := singleList(canonical)
	if err != nil {
		return nil, err
	}
	items, err := listItems(ctx, canonical, 0, root.ChildCount())
	if err != nil {
		return nil, err
	}
	var chunks []StructuredChunk
	var group []string
	var current StructuredChunk
	start := 0
	finish := func() {
		current.FirstItem = start + 1
		current.LastItem = start + len(group)
		chunks = append(chunks, current)
		group = nil
	}
	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(group) == maxListGroupItems {
			finish()
		}
		candidateItems := append(group, item)
		candidate, fits, err := listCandidate(ctx, request, candidateItems)
		if err != nil {
			return nil, err
		}
		if !fits && len(group) > 0 {
			finish()
			candidateItems = []string{item}
			candidate, fits, err = listCandidate(ctx, request, candidateItems)
			if err != nil {
				return nil, err
			}
		}
		if !fits {
			fragments, err := splitNestedListItem(ctx, request, item, index+1)
			if err != nil {
				return nil, err
			}
			chunks = append(chunks, fragments...)
			continue
		}
		if len(group) == 0 {
			start = index
		}
		group = candidateItems
		current = candidate
	}
	if len(group) > 0 {
		finish()
	}
	return chunks, nil
}

func validateStructuredRequest(ctx context.Context, request StructuredSplitRequest, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Block.Kind != kind || request.Block.Text == nil || len(request.Block.Cells) != 0 || request.Block.Page <= 0 ||
		!validBBox(request.Block.BBox) || len(request.HeadingPath) > 6 || request.MaxTokens <= 0 ||
		request.MaxTokens > maxTextChunkTokens || request.CountTokens == nil {
		return ErrInvalidChunkConfig
	}
	validation := assembler{ctx: ctx}
	for _, heading := range request.HeadingPath {
		if strings.TrimSpace(heading) == "" {
			return ErrInvalidChunkConfig
		}
		if err := validation.consume(heading); err != nil {
			return err
		}
	}
	return validation.consume(*request.Block.Text)
}

func structuredCandidate(ctx context.Context, request StructuredSplitRequest, content, display string) (StructuredChunk, bool, error) {
	var search chunkBuffer
	if len(display) > maxTableChunkBytes {
		return StructuredChunk{}, false, nil
	}
	if len(request.HeadingPath) > 0 {
		if err := search.append(strings.Join(request.HeadingPath, " › ") + "\n"); err != nil {
			return StructuredChunk{}, false, nil
		}
	}
	if err := search.append(content); err != nil {
		return StructuredChunk{}, false, nil
	}
	countInput := search.String()
	if request.countMarkdown {
		countInput = display
	}
	if len(countInput) > maxCountInputBytes {
		return StructuredChunk{}, false, nil
	}
	count, err := checkedTokenCount(ctx, request.CountTokens, countInput)
	if err != nil {
		return StructuredChunk{}, false, err
	}
	if count > request.MaxTokens {
		return StructuredChunk{}, false, nil
	}
	return StructuredChunk{Kind: request.Block.Kind, Content: content, Markdown: display, SearchText: search.String(),
		TokenCount: count, Page: request.Block.Page, BBox: slices.Clone(request.Block.BBox), HeadingPath: slices.Clone(request.HeadingPath)}, true, nil
}

func listCandidate(ctx context.Context, request StructuredSplitRequest, items []string) (StructuredChunk, bool, error) {
	display := strings.Join(items, "\n")
	if len(display) > maxTableChunkBytes {
		return StructuredChunk{}, false, nil
	}
	content, err := markdown.DecodeInline(display)
	if err != nil {
		return StructuredChunk{}, false, fmt.Errorf("decode list content: %w", err)
	}
	return structuredCandidate(ctx, request, content, display)
}

func singleList(value string) (*ast.List, error) {
	root := goldmark.DefaultParser().Parse(text.NewReader([]byte(value)))
	list, ok := root.FirstChild().(*ast.List)
	if !ok || list.NextSibling() != nil || list.ChildCount() == 0 {
		return nil, ErrUnsupportedParsedDocument
	}
	return list, nil
}

// CanonicalMarkdownList emits one marker per item on a line at its nesting indent.
func listItems(ctx context.Context, value string, indent, expected int) ([]string, error) {
	var starts []int
	prefix := strings.Repeat(" ", indent)
	lines := 0
	for offset := 0; offset < len(value); {
		if lines%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		lines++
		end := strings.IndexByte(value[offset:], '\n')
		if end < 0 {
			end = len(value)
		} else {
			end += offset
		}
		line := value[offset:end]
		if len(line) > indent && strings.HasPrefix(line, prefix) && listMarkerWidth(line[indent:]) > 0 {
			starts = append(starts, offset)
		}
		if end == len(value) {
			break
		}
		offset = end + 1
	}
	if len(starts) != expected || len(starts) == 0 || starts[0] != 0 || len(starts) > maxNormalizedBlocks {
		return nil, ErrUnsupportedParsedDocument
	}
	items := make([]string, len(starts))
	for index, start := range starts {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		end := len(value)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		items[index] = strings.TrimSuffix(value[start:end], "\n")
	}
	return items, nil
}

func listMarkerWidth(line string) int {
	if strings.HasPrefix(line, "- ") {
		return 2
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index == 0 || index+1 >= len(line) || line[index:index+2] != ". " {
		return 0
	}
	return index + 2
}

func splitNestedListItem(ctx context.Context, request StructuredSplitRequest, item string, itemIndex int) ([]StructuredChunk, error) {
	root, err := singleList(item)
	if err != nil || root.ChildCount() != 1 {
		return nil, ErrStructuredUnitTooLarge
	}
	parent := root.FirstChild()
	first := parent.FirstChild()
	if first == nil || first.NextSibling() == nil || first.NextSibling().NextSibling() != nil {
		return nil, ErrStructuredUnitTooLarge
	}
	switch first.(type) {
	case *ast.TextBlock, *ast.Paragraph:
	default:
		return nil, ErrStructuredUnitTooLarge
	}
	nested, ok := first.NextSibling().(*ast.List)
	if !ok || nested.ChildCount() == 0 {
		return nil, ErrStructuredUnitTooLarge
	}
	end := strings.IndexByte(item, '\n')
	if end < 0 {
		return nil, ErrStructuredUnitTooLarge
	}
	header := item[:end]
	indent := listMarkerWidth(header)
	if indent == 0 {
		return nil, ErrUnsupportedParsedDocument
	}
	rest := item[end+1:]
	if strings.HasPrefix(rest, "\n") {
		header += "\n"
		rest = rest[1:]
	}
	children, err := listItems(ctx, rest, indent, nested.ChildCount())
	if err != nil {
		return nil, err
	}
	var chunks []StructuredChunk
	var group []string
	var current StructuredChunk
	start := 0
	finish := func() {
		current.FirstItem, current.LastItem = itemIndex, itemIndex
		current.FirstSubitem, current.LastSubitem = start+1, start+len(group)
		chunks = append(chunks, current)
		group = nil
	}
	for index, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(group) == maxListGroupItems {
			finish()
		}
		candidateItems := append(group, child)
		candidate, fits, err := listCandidate(ctx, request, append([]string{header}, candidateItems...))
		if err != nil {
			return nil, err
		}
		if !fits && len(group) > 0 {
			finish()
			candidateItems = []string{child}
			candidate, fits, err = listCandidate(ctx, request, []string{header, child})
			if err != nil {
				return nil, err
			}
		}
		if !fits {
			return nil, ErrStructuredUnitTooLarge
		}
		if len(group) == 0 {
			start = index
		}
		group = candidateItems
		current = candidate
	}
	if len(group) > 0 {
		finish()
	}
	return chunks, nil
}
