package ingest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	ErrInvalidChunkConfig   = errors.New("invalid chunk configuration")
	ErrInvalidTokenCount    = errors.New("invalid token count")
	ErrTableUnitTooLarge    = errors.New("table header or row exceeds chunk limits")
	ErrTableHeaderMissing   = errors.New("table header has no column names")
	ErrUnsupportedChunkMIME = errors.New("unsupported table document format")
	errCountInputTooLarge   = errors.New("chunk counting input exceeds byte limit")
)

const (
	maxTableChunkTokens = 800
	maxTableChunkBytes  = 4 * 1024 * 1024
	maxTableGroupRows   = 512
	maxCountInputBytes  = 64 * 1024
)

// TokenCounter must count the exact input, including special tokens, for the configured embedding model.
type TokenCounter func(context.Context, string) (int, error)

type TableSplitRequest struct {
	Block       NormalizedBlock
	HeadingPath []string
	MIME        string
	HeaderRow   int
	MaxTokens   int
	CountTokens TokenCounter
}

// FirstRow and LastRow are one-based logical table rows; Cells includes the repeated header.
type TableChunk struct {
	Markdown    string
	SearchText  string
	TokenCount  int
	Page        int64
	FirstRow    int
	LastRow     int
	HeadingPath []string
	Cells       [][]string
}

type tableGroup struct {
	lines      []string
	expansions []string
	first      int
	last       int
	candidate  TableChunk
}

// SplitTable keeps data rows in order and repeats the header in each fragment.
func SplitTable(ctx context.Context, request TableSplitRequest) ([]TableChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Block.Kind != "table" || request.Block.Text != nil || request.Block.Page <= 0 || request.HeaderRow <= 0 || len(request.HeadingPath) > 6 ||
		request.MaxTokens <= 0 || request.MaxTokens > maxTableChunkTokens || request.CountTokens == nil {
		return nil, ErrInvalidChunkConfig
	}
	if request.HeaderRow != 1 {
		return nil, ErrUnsupportedParsedDocument
	}
	spreadsheet, err := spreadsheetMIME(request.MIME)
	if err != nil {
		return nil, err
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
	if err := validation.validateTable(request.Block.Cells); err != nil {
		return nil, err
	}
	cells := request.Block.Cells
	headerPresent := false
	for _, value := range cells[0] {
		if strings.TrimSpace(value) != "" {
			headerPresent = true
			break
		}
	}
	if !headerPresent {
		return nil, ErrTableHeaderMissing
	}
	if !rowWithinChunkBytes(cells[0]) {
		return nil, ErrTableUnitTooLarge
	}
	headerLine, err := tableLine(cells[0])
	if err != nil {
		return nil, err
	}
	header := headerLine + "\n" + tableDivider(len(cells[0]))
	prefix := strings.Join(request.HeadingPath, " › ")
	var chunks []TableChunk
	var outputBytes, outputCells int
	group := tableGroup{}
	finish := func() error {
		chunk := group.candidate
		fragmentBytes := len(chunk.Markdown) + len(chunk.SearchText)
		fragmentCells := len(cells[0]) * (1 + len(group.lines))
		if len(chunks) >= maxPlanChunks || fragmentBytes > maxNormalizedBytes-outputBytes || fragmentCells > maxNormalizedCells-outputCells {
			return ErrNormalizedLimit
		}
		outputBytes += fragmentBytes
		outputCells += fragmentCells
		chunk.Page = request.Block.Page
		chunk.HeadingPath = slices.Clone(request.HeadingPath)
		chunk.FirstRow, chunk.LastRow = group.first+1, group.last+1
		chunk.Cells = make([][]string, 1, len(group.lines)+1)
		chunk.Cells[0] = slices.Clone(cells[0])
		for index := group.first; index <= group.last; index++ {
			chunk.Cells = append(chunk.Cells, slices.Clone(cells[index]))
		}
		chunks = append(chunks, chunk)
		group = tableGroup{}
		return nil
	}
	if len(cells) == 1 {
		candidate, fits, err := evaluateTableGroup(ctx, request, prefix, header, nil, nil)
		if err != nil {
			return nil, err
		}
		if !fits {
			return nil, ErrTableUnitTooLarge
		}
		candidate.Page = request.Block.Page
		candidate.FirstRow, candidate.LastRow = 1, 1
		candidate.HeadingPath = slices.Clone(request.HeadingPath)
		candidate.Cells = [][]string{slices.Clone(cells[0])}
		return []TableChunk{candidate}, nil
	}
	for index := 1; index < len(cells); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(group.lines) == maxTableGroupRows {
			if err := finish(); err != nil {
				return nil, err
			}
		}
		if !rowWithinChunkBytes(cells[index]) {
			return nil, ErrTableUnitTooLarge
		}
		line, err := tableLine(cells[index])
		if err != nil {
			return nil, err
		}
		expansion := ""
		if spreadsheet {
			expansion, err = tableRowExpansion(cells[0], cells[index])
			if err != nil {
				return nil, err
			}
		}
		previous := len(group.lines)
		group.lines = append(group.lines, line)
		if spreadsheet {
			group.expansions = append(group.expansions, expansion)
		}
		candidate, fits, err := evaluateTableGroup(ctx, request, prefix, header, group.lines, group.expansions)
		if err != nil {
			return nil, err
		}
		if !fits && previous > 0 {
			group.lines = group.lines[:previous]
			if spreadsheet {
				group.expansions = group.expansions[:previous]
			}
			if err := finish(); err != nil {
				return nil, err
			}
			group.lines = append(group.lines, line)
			if spreadsheet {
				group.expansions = append(group.expansions, expansion)
			}
			candidate, fits, err = evaluateTableGroup(ctx, request, prefix, header, group.lines, group.expansions)
			if err != nil {
				return nil, err
			}
		}
		if !fits {
			return nil, ErrTableUnitTooLarge
		}
		if len(group.lines) == 1 {
			group.first = index
		}
		group.last = index
		group.candidate = candidate
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func spreadsheetMIME(mime string) (bool, error) {
	switch mime {
	case "text/csv", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return true, nil
	case "text/markdown", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return false, nil
	default:
		return false, ErrUnsupportedChunkMIME
	}
}

func rowWithinChunkBytes(row []string) bool {
	var size int
	for _, value := range row {
		if len(value) > maxTableChunkBytes-size {
			return false
		}
		size += len(value)
	}
	return true
}

func tableRowExpansion(header, row []string) (string, error) {
	var output chunkBuffer
	for index, value := range row {
		if index > 0 {
			if err := output.append("\n"); err != nil {
				return "", err
			}
		}
		parts := []string{value}
		if strings.TrimSpace(header[index]) != "" {
			parts = []string{header[index], ": ", value}
		}
		for _, part := range parts {
			if err := output.append(part); err != nil {
				return "", err
			}
		}
	}
	return output.String(), nil
}

type chunkBuffer struct{ strings.Builder }

func (b *chunkBuffer) append(value string) error {
	if len(value) > maxTableChunkBytes-b.Len() {
		return ErrTableUnitTooLarge
	}
	b.WriteString(value)
	return nil
}

func evaluateTableGroup(ctx context.Context, request TableSplitRequest, prefix, header string, lines, expansions []string) (TableChunk, bool, error) {
	var body chunkBuffer
	if err := body.append(header); err != nil {
		return TableChunk{}, false, nil
	}
	for _, line := range lines {
		if err := body.append("\n"); err != nil {
			return TableChunk{}, false, nil
		}
		if err := body.append(line); err != nil {
			return TableChunk{}, false, nil
		}
	}
	var search chunkBuffer
	if prefix != "" {
		if err := search.append(prefix); err != nil {
			return TableChunk{}, false, nil
		}
		if err := search.append("\n"); err != nil {
			return TableChunk{}, false, nil
		}
	}
	if err := search.append(body.String()); err != nil {
		return TableChunk{}, false, nil
	}
	for _, expansion := range expansions {
		if err := search.append("\n"); err != nil {
			return TableChunk{}, false, nil
		}
		if err := search.append(expansion); err != nil {
			return TableChunk{}, false, nil
		}
	}
	if search.Len() > maxCountInputBytes {
		return TableChunk{}, false, nil
	}
	count, err := checkedTokenCount(ctx, request.CountTokens, search.String())
	if err != nil {
		return TableChunk{}, false, err
	}
	if count > request.MaxTokens {
		return TableChunk{}, false, nil
	}
	return TableChunk{Markdown: body.String(), SearchText: search.String(), TokenCount: count}, true, nil
}

func checkedTokenCount(ctx context.Context, counter TokenCounter, input string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count, err := counter(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("count chunk tokens: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if count <= 0 {
		return 0, ErrInvalidTokenCount
	}
	return count, nil
}
