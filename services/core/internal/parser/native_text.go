package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"tuskrag.local/core/internal/markdown"
)

const (
	NativeTextProfile     = "tuskrag-core/native-txt-v1"
	NativeMarkdownProfile = "tuskrag-core/native-markdown-v3;goldmark=1.8.6"

	nativeInputLimit = 8 * 1024 * 1024
	nativeLineLimit  = 65536
	nativeDepthLimit = 64
	nativeNodeLimit  = 200000
	nativeLineCount  = 50000
)

var textHeading = regexp.MustCompile(`^第[一二三四五六七八九十百千万〇零0-9]{1,12}([章节])(?:[ \t]*[^。！？!?；;]+)?$`)

type nativeLimits struct {
	input, output, rows, columns, cells, field, blocks int64
}

func parseNativeLimits(raw json.RawMessage) (nativeLimits, error) {
	defaults := map[string]int64{
		"max_input_bytes": MaxInputBytes, "max_output_bytes": MaxOutputBytes,
		"max_pages": 2000,
		"max_rows":  100000, "max_columns": 1024, "max_cells": 1000000,
		"max_field_chars": 1000000, "max_blocks": 100000,
		"max_archive_entries": 10000, "max_uncompressed_bytes": 1024 * 1024 * 1024,
		"max_compression_ratio": 100, "max_xml_nodes": 2000000,
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return nativeLimits{}, errors.New("invalid native parser limits")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSON(decoder, 0); err != nil {
		return nativeLimits{}, errors.New("invalid native parser limits")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nativeLimits{}, errors.New("invalid native parser limits")
	}
	var overrides map[string]json.RawMessage
	if err := json.Unmarshal(raw, &overrides); err != nil || overrides == nil {
		return nativeLimits{}, errors.New("invalid native parser limits")
	}
	for name, value := range overrides {
		maximum, known := defaults[name]
		encoded := bytes.TrimSpace(value)
		if !known || len(encoded) == 0 || encoded[0] < '0' || encoded[0] > '9' {
			return nativeLimits{}, errors.New("invalid native parser limits")
		}
		amount, err := strconv.ParseInt(string(encoded), 10, 64)
		if err != nil || amount <= 0 || amount > maximum {
			return nativeLimits{}, errors.New("invalid native parser limits")
		}
		defaults[name] = amount
	}
	return nativeLimits{
		input: min(defaults["max_input_bytes"], nativeInputLimit), output: defaults["max_output_bytes"],
		rows: defaults["max_rows"], columns: defaults["max_columns"], cells: defaults["max_cells"],
		field: defaults["max_field_chars"], blocks: defaults["max_blocks"],
	}, nil
}

// ParseNativeText implements the isolated native-text subprocess. Callers must
// execute it in the parser sandbox when processing untrusted documents.
func ParseNativeText(ctx context.Context, input ParseInput, validator *Validator) (Document, error) {
	if ctx == nil || input.File == nil || validator == nil || validator.schema == nil {
		return Document{}, errors.New("native parser context, input and validator are required")
	}
	if input.MIME != "text/markdown" && input.MIME != "text/plain" {
		return Document{}, ErrUnsupportedFormat
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	limits, err := parseNativeLimits(input.Limits)
	if err != nil {
		return Document{}, err
	}
	info, err := input.File.Stat()
	if err != nil {
		return Document{}, fmt.Errorf("stat native text input: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return Document{}, ErrParse
	}
	if info.Size() > limits.input {
		return Document{}, ErrLimitExceeded
	}
	if _, err := input.File.Seek(0, io.SeekStart); err != nil {
		return Document{}, fmt.Errorf("seek native text input: %w", err)
	}
	source, err := io.ReadAll(io.LimitReader(input.File, limits.input+1))
	if err != nil {
		return Document{}, fmt.Errorf("read native text input: %w", err)
	}
	if int64(len(source)) > limits.input {
		return Document{}, ErrLimitExceeded
	}
	if int64(len(source)) != info.Size() || !utf8.Valid(source) {
		return Document{}, ErrParse
	}
	source = bytes.TrimPrefix(source, []byte("\xef\xbb\xbf"))
	source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	source = bytes.ReplaceAll(source, []byte("\r"), []byte("\n"))
	if err := checkNativeSource(ctx, source, limits, input.MIME == "text/markdown", nativeLineLimit); err != nil {
		return Document{}, err
	}
	state := nativeTextState{ctx: ctx, limits: limits}
	profile := NativeTextProfile
	if input.MIME == "text/markdown" {
		profile = NativeMarkdownProfile
		if err := state.markdown(source); err != nil {
			return Document{}, err
		}
	} else if err := state.plain(source); err != nil {
		return Document{}, err
	}
	if len(state.blocks) == 0 {
		return Document{}, ErrParse
	}
	document := Document{
		SchemaVersion: ExpectedSchemaVersion,
		Pages:         []Page{{Number: 1, Blocks: state.blocks, Signals: Signals{CharCount: state.characters}}},
		Meta:          Metadata{ParserProfile: profile},
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return Document{}, fmt.Errorf("encode native text: %w", err)
	}
	if int64(output.Len()) > min(limits.output, info.Size()*20) {
		return Document{}, ErrLimitExceeded
	}
	return validator.Validate(output.Bytes(), info.Size())
}

func checkNativeSource(ctx context.Context, source []byte, limits nativeLimits, markdown bool, lineLimit int) error {
	if len(bytes.TrimSpace(source)) == 0 || bytes.HasPrefix(source, []byte("PK\x03\x04")) || bytes.HasPrefix(source, []byte("%PDF-")) {
		return ErrParse
	}
	var lines, markers int64
	for start := 0; start < len(source); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += start
		}
		line := source[start:end]
		lines++
		if lines > min(limits.rows, nativeLineCount) || len(line) > lineLimit {
			return ErrLimitExceeded
		}
		trimmed := bytes.TrimLeft(line, " \t")
		if markdown && bytes.HasPrefix(trimmed, []byte("[^")) {
			if end := bytes.Index(trimmed, []byte("]:")); end >= 2 {
				return ErrUnsupportedFormat
			}
		}
		leading := 0
		for _, character := range line {
			if character != ' ' && character != '\t' && character != '>' {
				break
			}
			leading++
		}
		if leading > 256 {
			return ErrLimitExceeded
		}
		for _, character := range line {
			if (character < 0x20 && character != '\t' && character != '\r') || character == 0x7f {
				return ErrParse
			}
			if markdown {
				switch character {
				case '[', ']', '*', '_', '~', '!', '|', '<', '>', '`':
					markers++
					if markers > nativeNodeLimit/2 {
						return ErrLimitExceeded
					}
				}
			}
		}
		start = end + 1
	}
	return nil
}

type nativeTextState struct {
	ctx        context.Context
	limits     nativeLimits
	blocks     []Block
	rows       int64
	cells      int64
	characters int64
}

func (s *nativeTextState) add(block Block) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if int64(len(s.blocks)) >= s.limits.blocks {
		return ErrLimitExceeded
	}
	count := func(value string, escaped bool) error {
		if int64(utf8.RuneCountInString(value)) > s.limits.field {
			return ErrLimitExceeded
		}
		if escaped {
			var err error
			value, err = markdown.DecodeInline(value)
			if err != nil {
				return err
			}
		}
		for _, character := range value {
			if !unicode.IsSpace(character) {
				s.characters++
			}
		}
		return nil
	}
	if block.Text != nil {
		if err := count(*block.Text, block.Type == "list"); err != nil {
			return err
		}
	}
	for _, row := range block.Cells {
		s.rows++
		s.cells += int64(len(row))
		if s.rows > s.limits.rows || int64(len(row)) > s.limits.columns || s.cells > s.limits.cells {
			return ErrLimitExceeded
		}
		for _, cell := range row {
			if err := count(cell, false); err != nil {
				return err
			}
		}
	}
	s.blocks = append(s.blocks, block)
	return nil
}

func textBlock(kind, value string, level *int) Block {
	return Block{Type: kind, Text: &value, Level: level}
}

func (s *nativeTextState) plain(source []byte) error {
	text := strings.ReplaceAll(strings.ReplaceAll(string(source), "\r\n", "\n"), "\r", "\n")
	var paragraph []string
	flush := func() error {
		if len(paragraph) == 0 {
			return nil
		}
		value := strings.Join(paragraph, "\n")
		paragraph = nil
		return s.add(textBlock("paragraph", value, nil))
	}
	for _, line := range strings.Split(text, "\n") {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if match := textHeading.FindStringSubmatch(trimmed); match != nil && utf8.RuneCountInString(trimmed) <= 120 {
			if err := flush(); err != nil {
				return err
			}
			level := 1
			if match[1] == "节" {
				level = 2
			}
			if err := s.add(textBlock("heading", trimmed, &level)); err != nil {
				return err
			}
			continue
		}
		paragraph = append(paragraph, line)
	}
	return flush()
}

func (s *nativeTextState) markdown(source []byte) error {
	root := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	if err := s.checkTree(root); err != nil {
		return err
	}
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		switch current := node.(type) {
		case *ast.Heading:
			value, err := s.inlineText(current, source)
			if err != nil {
				return err
			}
			if strings.TrimSpace(value) == "" {
				return ErrParse
			}
			level := current.Level
			if err := s.add(textBlock("heading", value, &level)); err != nil {
				return err
			}
		case *ast.Paragraph:
			value, err := s.inlineText(current, source)
			if err != nil {
				return err
			}
			if err := s.add(textBlock("paragraph", value, nil)); err != nil {
				return err
			}
		case *extast.Table:
			cells, err := s.table(current, source)
			if err != nil {
				return err
			}
			if err := s.add(Block{Type: "table", Cells: cells}); err != nil {
				return err
			}
		case *ast.List:
			value, err := s.renderList(current, source, "", 0)
			if err != nil {
				return err
			}
			if err := s.add(textBlock("list", value, nil)); err != nil {
				return err
			}
		case *ast.FencedCodeBlock:
			if current.Info != nil {
				return ErrUnsupportedFormat
			}
			value, err := s.codeText(current, source)
			if err != nil {
				return err
			}
			if err := s.add(textBlock("code", value, nil)); err != nil {
				return err
			}
		case *ast.CodeBlock:
			value, err := s.codeText(current, source)
			if err != nil {
				return err
			}
			if err := s.add(textBlock("code", value, nil)); err != nil {
				return err
			}
		default:
			return ErrUnsupportedFormat
		}
	}
	return nil
}

// CanonicalMarkdownList validates and safely renders one untrusted Markdown list.
func CanonicalMarkdownList(ctx context.Context, value string) (string, error) {
	limits, err := parseNativeLimits(json.RawMessage(`{}`))
	if err != nil {
		return "", err
	}
	if int64(len(value)) > limits.input {
		return "", ErrLimitExceeded
	}
	if !utf8.ValidString(value) {
		return "", ErrParse
	}
	source := []byte(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	if err := checkNativeSource(ctx, source, limits, true, nativeInputLimit); err != nil {
		return "", err
	}
	state := nativeTextState{ctx: ctx, limits: limits}
	root := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	if err := state.checkTree(root); err != nil {
		return "", err
	}
	list, ok := root.FirstChild().(*ast.List)
	if !ok || list.NextSibling() != nil {
		return "", ErrUnsupportedFormat
	}
	return state.renderList(list, source, "", 0)
}

func (s *nativeTextState) checkTree(root ast.Node) error {
	type entry struct {
		node  ast.Node
		depth int
	}
	stack := []entry{{node: root}}
	visited := 0
	for len(stack) > 0 {
		last := len(stack) - 1
		item := stack[last]
		stack = stack[:last]
		visited++
		if visited > nativeNodeLimit || item.depth > nativeDepthLimit {
			return ErrLimitExceeded
		}
		if visited%256 == 0 {
			if err := s.ctx.Err(); err != nil {
				return err
			}
		}
		switch item.node.(type) {
		case *ast.Image, *ast.RawHTML, *ast.HTMLBlock:
			return ErrUnsupportedFormat
		}
		if next := item.node.NextSibling(); next != nil {
			stack = append(stack, entry{node: next, depth: item.depth})
		}
		if child := item.node.FirstChild(); child != nil {
			stack = append(stack, entry{node: child, depth: item.depth + 1})
		}
	}
	return s.ctx.Err()
}

func (s *nativeTextState) inlineText(root ast.Node, source []byte) (string, error) {
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	stack := []ast.Node{}
	if child := root.FirstChild(); child != nil {
		stack = append(stack, child)
	}
	visited := 0
	for len(stack) > 0 {
		visited++
		if visited%256 == 0 {
			if err := s.ctx.Err(); err != nil {
				return "", err
			}
			if int64(output.Len()) > s.limits.field*24 {
				return "", ErrLimitExceeded
			}
		}
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if next := node.NextSibling(); next != nil {
			stack = append(stack, next)
		}
		descend := true
		switch current := node.(type) {
		case *ast.Text:
			if current.IsRaw() {
				goldhtml.DefaultWriter.RawWrite(writer, current.Value(source))
			} else {
				goldhtml.DefaultWriter.Write(writer, current.Value(source))
			}
			if current.SoftLineBreak() || current.HardLineBreak() {
				if err := writer.WriteByte('\n'); err != nil {
					return "", err
				}
			}
		case *ast.String:
			if current.IsRaw() || current.IsCode() {
				goldhtml.DefaultWriter.RawWrite(writer, current.Value)
			} else {
				goldhtml.DefaultWriter.Write(writer, current.Value)
			}
		case *ast.AutoLink:
			goldhtml.DefaultWriter.RawWrite(writer, current.Label(source))
		case *ast.CodeSpan:
			descend = false
			for child := current.FirstChild(); child != nil; child = child.NextSibling() {
				part, ok := child.(*ast.Text)
				if !ok {
					return "", ErrUnsupportedFormat
				}
				value := part.Value(source)
				if bytes.HasSuffix(value, []byte("\n")) {
					value = append(bytes.Clone(value[:len(value)-1]), ' ')
				}
				goldhtml.DefaultWriter.RawWrite(writer, value)
			}
		case *ast.Link, *ast.Emphasis:
		default:
			return "", ErrUnsupportedFormat
		}
		if descend {
			if child := node.FirstChild(); child != nil {
				stack = append(stack, child)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("render inline text: %w", err)
	}
	if int64(output.Len()) > s.limits.field*24 {
		return "", ErrLimitExceeded
	}
	return html.UnescapeString(output.String()), nil
}

func (s *nativeTextState) codeText(node ast.Node, source []byte) (string, error) {
	var output strings.Builder
	for i := 0; i < node.Lines().Len(); i++ {
		if err := s.ctx.Err(); err != nil {
			return "", err
		}
		segment := node.Lines().At(i)
		value := segment.Value(source)
		if int64(output.Len()+len(value)) > s.limits.field*4 {
			return "", ErrLimitExceeded
		}
		output.Write(value)
	}
	return output.String(), nil
}

func (s *nativeTextState) table(table *extast.Table, source []byte) ([][]string, error) {
	var rows [][]string
	columns := len(table.Alignments)
	if columns == 0 || int64(columns) > s.limits.columns {
		return nil, ErrLimitExceeded
	}
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(rows))+s.rows >= s.limits.rows || int64(len(rows)+1)*int64(columns)+s.cells > s.limits.cells {
			return nil, ErrLimitExceeded
		}
		if _, header := row.(*extast.TableHeader); !header {
			if _, normal := row.(*extast.TableRow); !normal || tableColumns(source, row.Pos()) > columns {
				return nil, ErrUnsupportedFormat
			}
		}
		cells := make([]string, 0, columns)
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if _, ok := cell.(*extast.TableCell); !ok {
				return nil, ErrUnsupportedFormat
			}
			value, err := s.inlineText(cell, source)
			if err != nil {
				return nil, err
			}
			cells = append(cells, value)
		}
		if len(cells) != columns {
			return nil, ErrParse
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

func tableColumns(source []byte, position int) int {
	if position < 0 || position >= len(source) {
		return nativeNodeLimit
	}
	line := source[position:]
	if end := bytes.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	line = bytes.TrimSpace(line)
	if len(line) > 0 && line[0] == '|' {
		line = line[1:]
	}
	if len(line) > 0 && line[len(line)-1] == '|' {
		if len(line) > 1 && line[len(line)-2] == '\\' {
			return nativeNodeLimit
		}
		line = line[:len(line)-1]
	}
	columns := 1
	for index, value := range line {
		if value == '|' && (index == 0 || line[index-1] != '\\') {
			columns++
		}
	}
	return columns
}

func (s *nativeTextState) renderList(list *ast.List, source []byte, indent string, depth int) (string, error) {
	if depth > 16 {
		return "", ErrLimitExceeded
	}
	var lines []string
	var size int64
	appendLine := func(value string) error {
		size += int64(len(value)) + 1
		if size > s.limits.field*4+1 {
			return ErrLimitExceeded
		}
		lines = append(lines, value)
		return nil
	}
	index := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		if err := s.ctx.Err(); err != nil {
			return "", err
		}
		if _, ok := item.(*ast.ListItem); !ok {
			return "", ErrUnsupportedFormat
		}
		prefix := "- "
		if list.IsOrdered() {
			prefix = strconv.Itoa(index) + ". "
			index++
		}
		started := false
		for part := item.FirstChild(); part != nil; part = part.NextSibling() {
			switch current := part.(type) {
			case *ast.TextBlock, *ast.Paragraph:
				value, err := s.inlineText(part, source)
				if err != nil {
					return "", err
				}
				value = markdown.EscapeInline(value)
				if !started {
					if err := appendLine(indent + prefix + value); err != nil {
						return "", err
					}
					started = true
				} else {
					if err := appendLine(""); err != nil {
						return "", err
					}
					if err := appendLine(indent + strings.Repeat(" ", len(prefix)) + value); err != nil {
						return "", err
					}
				}
			case *ast.List:
				if !started {
					return "", ErrUnsupportedFormat
				}
				// An ordered list starting after 1 cannot interrupt its parent's paragraph.
				if current.IsOrdered() && current.Start != 1 {
					if err := appendLine(""); err != nil {
						return "", err
					}
				}
				value, err := s.renderList(current, source, indent+strings.Repeat(" ", len(prefix)), depth+1)
				if err != nil {
					return "", err
				}
				if err := appendLine(value); err != nil {
					return "", err
				}
			default:
				return "", ErrUnsupportedFormat
			}
		}
		if !started {
			return "", ErrUnsupportedFormat
		}
	}
	return strings.Join(lines, "\n"), nil
}
