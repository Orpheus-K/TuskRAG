package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"tuskrag.local/core/internal/parser"
)

func parseTextFixture(t *testing.T, source []byte, mime string) parser.Document {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "assemble-input-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := file.Write(source); err != nil {
		t.Fatal(err)
	}
	document, err := nativePipelineParser(t).Parse(context.Background(), parser.ParseInput{File: file, MIME: mime, Limits: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestAssembleGoldenRoundTrip(t *testing.T) {
	var cases []struct {
		File     string `json:"file"`
		MIME     string `json:"mime"`
		Sections []struct {
			Parent  int      `json:"parent"`
			Level   int      `json:"level"`
			Heading string   `json:"heading"`
			Path    []string `json:"path"`
			Blocks  int      `json:"blocks"`
		} `json:"sections"`
	}
	data, err := os.ReadFile("testdata/assemble-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		t.Run(item.File, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("../parser/testdata/native-text", item.File))
			if err != nil {
				t.Fatal(err)
			}
			document := parseTextFixture(t, source, item.MIME)
			normalized, err := AssembleMarkdown(context.Background(), document)
			if err != nil || len(normalized.Sections) != len(item.Sections) {
				t.Fatalf("assemble: %+v err=%v", normalized, err)
			}
			for index, expected := range item.Sections {
				actual := normalized.Sections[index]
				if actual.ParentIndex != expected.Parent || actual.Ordinal != index+1 || actual.Level != expected.Level ||
					actual.Heading != expected.Heading || !reflect.DeepEqual(actual.HeadingPath, expected.Path) ||
					len(actual.Blocks) != expected.Blocks || actual.PageStart != 1 || actual.PageEnd != 1 {
					t.Fatalf("section %d: got %+v want %+v", index, actual, expected)
				}
			}
			roundTrip := parseTextFixture(t, []byte(normalized.Markdown), "text/markdown")
			if !reflect.DeepEqual(document.Pages[0].Blocks, roundTrip.Pages[0].Blocks) {
				t.Fatalf("round trip changed blocks:\noriginal=%+v\nroundtrip=%+v\nmarkdown=%s", document.Pages[0].Blocks, roundTrip.Pages[0].Blocks, normalized.Markdown)
			}
		})
	}
}

func heading(value string, level int) parser.Block {
	return parser.Block{Type: "heading", Level: &level, Text: &value}
}

func paragraph(value string) parser.Block {
	return parser.Block{Type: "paragraph", Text: &value}
}

func parsedPages(pages ...parser.Page) parser.Document {
	return parser.Document{SchemaVersion: parser.ExpectedSchemaVersion, Pages: pages, Meta: parser.Metadata{ParserProfile: "test"}}
}

func TestAssembleSectionTreeAndPageRanges(t *testing.T) {
	document := parsedPages(
		parser.Page{Number: 1, Blocks: []parser.Block{paragraph("前言"), heading("第一章", 1), paragraph("正文"), heading("三级", 3)}},
		parser.Page{Number: 2, Blocks: []parser.Block{paragraph("跨页内容"), heading("二级", 2), paragraph("其他内容"), heading("第二章", 1)}},
	)
	normalized, err := AssembleMarkdown(context.Background(), document)
	if err != nil || len(normalized.Sections) != 5 {
		t.Fatalf("assemble section tree: %+v err=%v", normalized, err)
	}
	wantParents := []int{-1, -1, 1, 1, -1}
	wantStarts := []int64{1, 1, 1, 2, 2}
	wantEnds := []int64{1, 2, 2, 2, 2}
	for index, section := range normalized.Sections {
		if section.ParentIndex != wantParents[index] || section.PageStart != wantStarts[index] || section.PageEnd != wantEnds[index] {
			t.Fatalf("section %d has wrong parent or pages: %+v", index, section)
		}
	}
	if !reflect.DeepEqual(normalized.Sections[2].HeadingPath, []string{"第一章", "三级"}) ||
		normalized.Sections[1].BodyMarkdown != "正文" || normalized.Sections[2].BodyMarkdown != "跨页内容" {
		t.Fatalf("heading path or direct body changed: %+v", normalized.Sections)
	}
}

func TestAssembleEscapesUntrustedTextAndPreservesCells(t *testing.T) {
	list := "- [链接](https://example.invalid)\n  - 子项"
	document := parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{
		heading("标题 <标记>", 1), paragraph("<script>alert(1)</script> & 正文"),
		{Type: "table", Cells: [][]string{{"列", "值"}, {"A|B", "第一行\n第二行"}}, BBox: []float64{1, 2, 3, 4}},
		{Type: "list", Text: &list},
	}})
	normalized, err := AssembleMarkdown(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	var html bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.Table)).Convert([]byte(normalized.Markdown), &html); err != nil {
		t.Fatal(err)
	}
	rendered := html.String()
	if strings.Contains(rendered, "<script>") || strings.Contains(rendered, "<a ") ||
		!strings.Contains(rendered, "&lt;script&gt;") || !strings.Contains(rendered, "A|B") {
		t.Fatalf("untrusted text changed meaning: %q", rendered)
	}
	parsed := parseTextFixture(t, []byte(normalized.Markdown), "text/markdown")
	if len(parsed.Pages[0].Blocks) != 4 || !reflect.DeepEqual(parsed.Pages[0].Blocks[2].Cells, document.Pages[0].Blocks[2].Cells) {
		t.Fatalf("table content changed during Markdown round trip: %+v", parsed.Pages[0].Blocks)
	}
	segment := normalized.Sections[0].Blocks[1]
	document.Pages[0].Blocks[2].Cells[1][0] = "修改"
	document.Pages[0].Blocks[2].BBox[0] = 99
	if segment.Cells[1][0] != "A|B" || segment.BBox[0] != 1 {
		t.Fatalf("normalized block aliases mutable input: %+v", segment)
	}
}

func TestAssemblePreservesWhitespaceAndCodeFence(t *testing.T) {
	title := " 标题\n后续 "
	body := "\t第一行\n\n末行 "
	code := "开始\n```\n~~~\n结束"
	document := parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{
		heading(title, 1), paragraph(body), {Type: "code", Text: &code},
	}})
	normalized, err := AssembleMarkdown(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := parseTextFixture(t, []byte(normalized.Markdown), "text/markdown")
	blocks := roundTrip.Pages[0].Blocks
	if len(blocks) != 3 || blocks[0].Text == nil || *blocks[0].Text != title || blocks[1].Text == nil || *blocks[1].Text != body ||
		blocks[2].Text == nil || *blocks[2].Text != code+"\n" || normalized.Sections[0].Blocks[1].Text == nil || *normalized.Sections[0].Blocks[1].Text != code {
		t.Fatalf("whitespace or code fence changed content: blocks=%+v markdown=%q", blocks, normalized.Markdown)
	}
}

func TestAssembleAcceptsEscapedListOutputWithinFieldLimit(t *testing.T) {
	content := "# 标题\n\n- " + strings.Repeat("!", 40000) + "\n"
	document := parseTextFixture(t, []byte(content), "text/markdown")
	if _, err := AssembleMarkdown(context.Background(), document); err != nil {
		t.Fatalf("valid list could not be assembled: %v", err)
	}
}

func TestAssembleRejectsInvalidAndUnrepresentableStructures(t *testing.T) {
	missingLevel := "标题"
	listHTML := "- <script>bad</script>"
	invalidNumber := math.NaN()
	for name, item := range map[string]struct {
		document parser.Document
		want     error
	}{
		"empty":           {parsedPages(), ErrInvalidParsedDocument},
		"duplicate page":  {parsedPages(parser.Page{Number: 1}, parser.Page{Number: 1}), ErrInvalidParsedDocument},
		"heading level":   {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{{Type: "heading", Text: &missingLevel}}}), ErrInvalidParsedDocument},
		"deep heading":    {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{heading("标题", 7)}}), ErrUnsupportedParsedDocument},
		"ragged table":    {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{{Type: "table", Cells: [][]string{{"a"}, {"b", "c"}}}}}), ErrUnsupportedParsedDocument},
		"list HTML":       {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{{Type: "list", Text: &listHTML}}}), ErrUnsupportedParsedDocument},
		"empty paragraph": {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{paragraph(" ")}}), ErrInvalidParsedDocument},
		"nul":             {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{paragraph("a\x00b")}}), ErrInvalidParsedDocument},
		"invalid bbox":    {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{{Type: "paragraph", Text: &missingLevel, BBox: []float64{invalidNumber, 0, 1, 1}}}}), ErrInvalidParsedDocument},
		"field limit":     {parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{paragraph(strings.Repeat("a", maxNormalizedField+1))}}), ErrNormalizedLimit},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AssembleMarkdown(context.Background(), item.document); !errors.Is(err, item.want) {
				t.Fatalf("got %v, want %v", err, item.want)
			}
		})
	}
	withOutline := parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{paragraph("文本")}})
	withOutline.Outline = []parser.OutlineItem{{Title: "书签", Level: 1}}
	if _, err := AssembleMarkdown(context.Background(), withOutline); !errors.Is(err, ErrUnsupportedParsedDocument) {
		t.Fatalf("outline was silently discarded: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AssembleMarkdown(ctx, parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{paragraph("文本")}})); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was ignored: %v", err)
	}
}

func FuzzAssembleParagraph(f *testing.F) {
	f.Add([]byte("字面量 <script> &amp; \\*强调\\*\n第二行"))
	f.Add([]byte(" 表格 | 标点\t"))
	f.Fuzz(func(t *testing.T, data []byte) {
		value := strings.Repeat("可见文本 ", 12) + string(data)
		document := parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{heading("标题", 1), paragraph(value)}})
		normalized, err := AssembleMarkdown(context.Background(), document)
		if err != nil {
			return
		}
		roundTrip := parseTextFixture(t, []byte(normalized.Markdown), "text/markdown")
		if len(roundTrip.Pages[0].Blocks) != 2 || roundTrip.Pages[0].Blocks[1].Text == nil || *roundTrip.Pages[0].Blocks[1].Text != value {
			t.Fatalf("paragraph changed during round trip: input=%q output=%+v", value, roundTrip.Pages[0].Blocks)
		}
	})
}
