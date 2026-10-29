package ingest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"tuskrag.local/core/internal/markdown"
	"tuskrag.local/core/internal/parser"
)

var (
	ErrInvalidParsedDocument     = errors.New("invalid parsed document")
	ErrUnsupportedParsedDocument = errors.New("unsupported parsed document structure")
	ErrNormalizedLimit           = errors.New("normalized document limit exceeded")
)

const (
	maxNormalizedBytes  = 64 * 1024 * 1024
	maxNormalizedBlocks = 100000
	maxNormalizedCells  = 1000000
	maxNormalizedField  = 1000000
)

type NormalizedDocument struct {
	Markdown string
	Sections []Section
}

// Section headings are untrusted source text; BodyMarkdown is an escaped representation.
type Section struct {
	ParentIndex  int
	Ordinal      int
	Level        int
	Heading      string
	HeadingBBox  []float64
	HeadingPath  []string
	PageStart    int64
	PageEnd      int64
	BodyMarkdown string
	Blocks       []NormalizedBlock
}

// Text and Cells retain untrusted source content; Markdown is the escaped representation.
type NormalizedBlock struct {
	Kind     string
	Page     int64
	Text     *string
	Cells    [][]string
	BBox     []float64
	Markdown string
}

type boundedMarkdown struct{ strings.Builder }

func (b *boundedMarkdown) append(value string) error {
	if len(value) > maxNormalizedBytes-b.Len() {
		return ErrNormalizedLimit
	}
	b.WriteString(value)
	return nil
}

type assembler struct {
	ctx         context.Context
	full        boundedMarkdown
	sections    []Section
	stack       []int
	preamble    int
	blocks      int
	cells       int
	sourceBytes int
}

// AssembleMarkdown retains each block's source location and constructs a heading tree.
func AssembleMarkdown(ctx context.Context, document parser.Document) (NormalizedDocument, error) {
	if err := ctx.Err(); err != nil {
		return NormalizedDocument{}, err
	}
	if document.SchemaVersion != parser.ExpectedSchemaVersion || strings.TrimSpace(document.Meta.ParserProfile) == "" || len(document.Pages) == 0 || len(document.Pages) > 2000 {
		return NormalizedDocument{}, ErrInvalidParsedDocument
	}
	if len(document.Outline) != 0 {
		return NormalizedDocument{}, ErrUnsupportedParsedDocument
	}
	a := assembler{ctx: ctx, preamble: -1}
	var previousPage int64
	for _, page := range document.Pages {
		if page.Number <= previousPage || !validDimension(page.Width) || !validDimension(page.Height) {
			return NormalizedDocument{}, ErrInvalidParsedDocument
		}
		previousPage = page.Number
		for _, block := range page.Blocks {
			if err := ctx.Err(); err != nil {
				return NormalizedDocument{}, err
			}
			a.blocks++
			if a.blocks > maxNormalizedBlocks {
				return NormalizedDocument{}, ErrNormalizedLimit
			}
			if err := a.accept(page.Number, block); err != nil {
				return NormalizedDocument{}, err
			}
		}
	}
	if len(a.sections) == 0 {
		return NormalizedDocument{}, ErrInvalidParsedDocument
	}
	for index := range a.sections {
		if err := ctx.Err(); err != nil {
			return NormalizedDocument{}, err
		}
		parts := make([]string, len(a.sections[index].Blocks))
		for blockIndex, block := range a.sections[index].Blocks {
			parts[blockIndex] = block.Markdown
		}
		a.sections[index].BodyMarkdown = strings.Join(parts, "\n\n")
	}
	return NormalizedDocument{Markdown: a.full.String(), Sections: a.sections}, nil
}

func validDimension(value *float64) bool {
	return value == nil || (*value > 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0))
}

func (a *assembler) accept(page int64, block parser.Block) error {
	if !validBBox(block.BBox) {
		return ErrInvalidParsedDocument
	}
	if block.Type == "heading" {
		return a.heading(page, block)
	}
	if block.Level != nil {
		return ErrInvalidParsedDocument
	}
	segment, err := a.block(page, block)
	if err != nil {
		return err
	}
	if err := a.appendFull(segment.Markdown); err != nil {
		return err
	}
	section := a.activeSection(page)
	a.sections[section].Blocks = append(a.sections[section].Blocks, segment)
	a.extendPages(page)
	return nil
}

func validBBox(box []float64) bool {
	if len(box) != 0 && len(box) != 4 {
		return false
	}
	for _, coordinate := range box {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return false
		}
	}
	return true
}

func (a *assembler) heading(page int64, block parser.Block) error {
	if block.Text == nil || block.Level == nil || len(block.Cells) != 0 || *block.Level < 1 || *block.Level > 9 || strings.TrimSpace(*block.Text) == "" {
		return ErrInvalidParsedDocument
	}
	if *block.Level > 6 {
		return ErrUnsupportedParsedDocument
	}
	if err := a.consume(*block.Text); err != nil {
		return err
	}
	for len(a.stack) > 0 && a.sections[a.stack[len(a.stack)-1]].Level >= *block.Level {
		a.stack = a.stack[:len(a.stack)-1]
	}
	parent := -1
	var path []string
	if len(a.stack) > 0 {
		parent = a.stack[len(a.stack)-1]
		path = slices.Clone(a.sections[parent].HeadingPath)
	}
	path = append(path, *block.Text)
	if err := a.appendFull(strings.Repeat("#", *block.Level) + " " + markdown.EscapeInline(*block.Text)); err != nil {
		return err
	}
	section := Section{ParentIndex: parent, Ordinal: len(a.sections) + 1, Level: *block.Level,
		Heading: *block.Text, HeadingBBox: slices.Clone(block.BBox), HeadingPath: path, PageStart: page, PageEnd: page}
	a.sections = append(a.sections, section)
	a.stack = append(a.stack, len(a.sections)-1)
	a.extendPages(page)
	return nil
}

func (a *assembler) activeSection(page int64) int {
	if len(a.stack) > 0 {
		return a.stack[len(a.stack)-1]
	}
	if a.preamble < 0 {
		a.preamble = len(a.sections)
		a.sections = append(a.sections, Section{ParentIndex: -1, Ordinal: len(a.sections) + 1, PageStart: page, PageEnd: page})
	}
	a.sections[a.preamble].PageEnd = page
	return a.preamble
}

func (a *assembler) extendPages(page int64) {
	for _, index := range a.stack {
		a.sections[index].PageEnd = page
	}
}

func (a *assembler) appendFull(value string) error {
	if a.full.Len() > 0 {
		if err := a.full.append("\n\n"); err != nil {
			return err
		}
	}
	return a.full.append(value)
}

func (a *assembler) consume(value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return ErrInvalidParsedDocument
	}
	for _, character := range value {
		if (character < 0x20 && character != '\n' && character != '\r' && character != '\t') || character == 0x7f {
			return ErrInvalidParsedDocument
		}
	}
	if utf8.RuneCountInString(value) > maxNormalizedField || len(value) > maxNormalizedBytes-a.sourceBytes {
		return ErrNormalizedLimit
	}
	a.sourceBytes += len(value)
	return nil
}

func (a *assembler) block(page int64, block parser.Block) (NormalizedBlock, error) {
	segment := NormalizedBlock{Kind: block.Type, Page: page, BBox: slices.Clone(block.BBox)}
	switch block.Type {
	case "paragraph", "list", "code":
		if block.Text == nil || len(block.Cells) != 0 {
			return NormalizedBlock{}, ErrInvalidParsedDocument
		}
		if err := a.consume(*block.Text); err != nil {
			return NormalizedBlock{}, err
		}
		value := *block.Text
		segment.Text = &value
		switch block.Type {
		case "paragraph":
			if strings.TrimSpace(value) == "" {
				return NormalizedBlock{}, ErrInvalidParsedDocument
			}
			segment.Markdown = markdown.EscapeInline(value)
		case "list":
			canonical, err := parser.CanonicalMarkdownList(a.ctx, value)
			if err != nil {
				if errors.Is(err, parser.ErrLimitExceeded) {
					return NormalizedBlock{}, fmt.Errorf("%w: list: %w", ErrNormalizedLimit, err)
				}
				if errors.Is(err, parser.ErrUnsupportedFormat) || errors.Is(err, parser.ErrParse) {
					return NormalizedBlock{}, fmt.Errorf("%w: list: %w", ErrUnsupportedParsedDocument, err)
				}
				return NormalizedBlock{}, fmt.Errorf("normalize list: %w", err)
			}
			segment.Markdown = canonical
		case "code":
			segment.Markdown = fencedCode(value)
		}
	case "table":
		if block.Text != nil || len(block.Cells) == 0 || len(block.Cells[0]) == 0 {
			return NormalizedBlock{}, ErrInvalidParsedDocument
		}
		var err error
		segment.Markdown, segment.Cells, err = a.table(block.Cells)
		if err != nil {
			return NormalizedBlock{}, err
		}
	default:
		return NormalizedBlock{}, ErrUnsupportedParsedDocument
	}
	return segment, nil
}

func (a *assembler) table(cells [][]string) (string, [][]string, error) {
	if err := a.validateTable(cells); err != nil {
		return "", nil, err
	}
	columns := len(cells[0])
	var output boundedMarkdown
	copyRows := make([][]string, 0, len(cells))
	for rowIndex, row := range cells {
		if err := a.ctx.Err(); err != nil {
			return "", nil, err
		}
		if rowIndex > 0 {
			if err := output.append("\n"); err != nil {
				return "", nil, err
			}
		}
		line, err := tableLine(row)
		if err != nil {
			return "", nil, err
		}
		if err := output.append(line); err != nil {
			return "", nil, err
		}
		copyRows = append(copyRows, slices.Clone(row))
		if rowIndex == 0 {
			if err := output.append("\n" + tableDivider(columns)); err != nil {
				return "", nil, err
			}
		}
	}
	return output.String(), copyRows, nil
}

func (a *assembler) validateTable(cells [][]string) error {
	if len(cells) == 0 || len(cells[0]) == 0 {
		return ErrInvalidParsedDocument
	}
	columns := len(cells[0])
	if columns > 1024 || len(cells) > 100000 || len(cells) > (maxNormalizedCells-a.cells)/columns {
		return ErrNormalizedLimit
	}
	for _, row := range cells {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		if len(row) != columns {
			return ErrUnsupportedParsedDocument
		}
		a.cells += len(row)
		for _, value := range row {
			if err := a.consume(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func tableLine(row []string) (string, error) {
	var output boundedMarkdown
	if err := output.append("|"); err != nil {
		return "", err
	}
	for _, value := range row {
		if err := output.append(" " + markdown.EscapeInline(value) + " |"); err != nil {
			return "", err
		}
	}
	return output.String(), nil
}

func tableDivider(columns int) string { return "|" + strings.Repeat(" --- |", columns) }

func fencedCode(value string) string {
	backticks, tildes := longestRun(value, '`'), longestRun(value, '~')
	marker, length := byte('`'), backticks
	if tildes < backticks {
		marker, length = '~', tildes
	}
	fence := strings.Repeat(string(marker), max(3, length+1))
	if !strings.HasSuffix(value, "\n") {
		value += "\n"
	}
	return fence + "\n" + value + fence
}

func longestRun(value string, marker byte) int {
	var longest, current int
	for index := 0; index < len(value); index++ {
		if value[index] == marker {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}
