package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func nativeTextInput(t *testing.T, content []byte, mime, limits string) ParseInput {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "native-text-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	return ParseInput{File: file, MIME: mime, Limits: json.RawMessage(limits)}
}

type nativeTextTestParser struct{ validator *Validator }

func (p nativeTextTestParser) Parse(ctx context.Context, input ParseInput) (Document, error) {
	return ParseNativeText(ctx, input, p.validator)
}

func nativeTextParser(t *testing.T) nativeTextTestParser {
	t.Helper()
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return nativeTextTestParser{validator: validator}
}

func TestParserRequiresSandboxForNativeText(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewParser(nil, validator); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("parser accepted missing sandbox: %v", err)
	}
}

func TestNativeTextGolden(t *testing.T) {
	var cases []struct {
		File   string  `json:"file"`
		MIME   string  `json:"mime"`
		Blocks []Block `json:"blocks"`
	}
	data, err := os.ReadFile("testdata/native-text/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		t.Run(item.File, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata/native-text", item.File))
			if err != nil {
				t.Fatal(err)
			}
			document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, source, item.MIME, `{}`))
			if err != nil || len(document.Pages) != 1 {
				t.Fatalf("parse native document: document=%+v err=%v", document, err)
			}
			if document.Pages[0].Number != 1 || !reflect.DeepEqual(document.Pages[0].Blocks, item.Blocks) {
				t.Fatalf("unexpected structure:\n got %+v\nwant %+v", document.Pages[0].Blocks, item.Blocks)
			}
			var characters int64
			for _, block := range item.Blocks {
				if block.Text != nil {
					for _, character := range *block.Text {
						if !unicode.IsSpace(character) {
							characters++
						}
					}
				}
				for _, row := range block.Cells {
					for _, cell := range row {
						for _, character := range cell {
							if !unicode.IsSpace(character) {
								characters++
							}
						}
					}
				}
			}
			if document.Pages[0].Signals.CharCount != characters {
				t.Fatalf("character signal: got %d, want %d", document.Pages[0].Signals.CharCount, characters)
			}
		})
	}
}

func TestNativeTextRejectsUnsupportedContent(t *testing.T) {
	for name, source := range map[string]string{
		"image":          "# 标题\n\n![图片](image.png)\n",
		"html":           "# 标题\n\n<span>内容</span>\n",
		"blockquote":     "# 标题\n\n> 引用内容\n",
		"code language":  "# 标题\n\n```go\nvalue := 1\n```\n",
		"extra cell":     "| 甲 | 乙 |\n| --- | --- |\n| 一 | 二 | 未显示 |\n",
		"nested content": "- 列表项\n\n  > 引用内容\n",
		"footnote":       "注释[^1]。\n\n[^1]: 说明内容\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", `{}`))
			if !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("unsupported content was not rejected: %v", err)
			}
		})
	}
}

func TestNativeTextRejectsMalformedAndLimits(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":      {},
		"whitespace": []byte(" \n\n"),
		"utf8":       {0xff, 0xfe},
		"nul":        []byte("visible\x00hidden"),
		"zip":        []byte("PK\x03\x04 fake"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, data, "text/plain", `{}`))
			if !errors.Is(err, ErrParse) {
				t.Fatalf("malformed content was not rejected: %v", err)
			}
		})
	}
	for name, limits := range map[string]string{
		"input":   `{"max_input_bytes":10}`,
		"output":  `{"max_output_bytes":10}`,
		"rows":    `{"max_rows":1}`,
		"columns": `{"max_columns":1}`,
		"cells":   `{"max_cells":2}`,
		"field":   `{"max_field_chars":1}`,
		"blocks":  `{"max_blocks":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			source := "# 标题\n\n| 甲 | 乙 |\n| --- | --- |\n| 一 | 二 |\n"
			_, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", limits))
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("limit was not enforced: %v", err)
			}
		})
	}
	for _, limits := range []string{`null`, `[]`, `{"unknown":1}`, `{"max_rows":0}`, `{"max_rows":1.5}`, `{"max_rows":"1"}`, `{"max_rows":100001}`, `{"max_rows":1,"max_rows":2}`} {
		_, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte("some content"), "text/plain", limits))
		if err == nil {
			t.Fatalf("invalid limits were accepted: %s", limits)
		}
	}
}

func TestNativeTextRespectsCancellationAndHardBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := nativeTextParser(t).Parse(ctx, nativeTextInput(t, []byte("content"), "text/plain", `{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled input: %v", err)
	}
	input := nativeTextInput(t, nil, "text/plain", `{}`)
	if err := input.File.Truncate(8*1024*1024 + 1); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTextParser(t).Parse(context.Background(), input); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("native input hard limit not enforced: %v", err)
	}
	input = nativeTextInput(t, []byte(strings.Repeat("x", 65537)), "text/markdown", `{}`)
	if _, err := nativeTextParser(t).Parse(context.Background(), input); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("line bound not enforced: %v", err)
	}
	input = nativeTextInput(t, []byte(strings.Repeat("[]", 25001)+"\n"+strings.Repeat("[]", 25001)), "text/markdown", `{}`)
	if _, err := nativeTextParser(t).Parse(context.Background(), input); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("markup complexity bound not enforced: %v", err)
	}
	input = nativeTextInput(t, []byte("value\n"), "text/csv", `{}`)
	if _, err := nativeTextParser(t).Parse(context.Background(), input); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("native parser accepted a non-native format: %v", err)
	}
}

func TestNativeTextNormalizesLineEndingsWithoutLosingParagraphs(t *testing.T) {
	content := []byte("\xef\xbb\xbf第一章 标题\r正文\r\r第二节 结束\r尾巴")
	parser := nativeTextParser(t)
	document, err := parser.Parse(context.Background(), nativeTextInput(t, content, "text/plain", `{}`))
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 4 {
		t.Fatalf("CR-only text: document=%+v err=%v", document, err)
	}
	blocks := document.Pages[0].Blocks
	if blocks[0].Text == nil || *blocks[0].Text != "第一章 标题" || blocks[3].Text == nil || *blocks[3].Text != "尾巴" {
		t.Fatalf("unexpected CR-only structure: %+v", blocks)
	}
	_, err = parser.Parse(context.Background(), nativeTextInput(t, content, "text/plain", `{"max_rows":2}`))
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("CR-only line count bypassed: %v", err)
	}
}

func TestNativeMarkdownListKeepsHierarchyWhenRendered(t *testing.T) {
	source := "# 标题\n\n100. 申请\n     - 发票\n     - 订单\n"
	document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", `{}`))
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 2 || document.Pages[0].Blocks[1].Text == nil {
		t.Fatalf("parse nested list: %+v err=%v", document, err)
	}
	value := []byte(*document.Pages[0].Blocks[1].Text)
	root := goldmark.DefaultParser().Parse(text.NewReader(value))
	list, ok := root.FirstChild().(*ast.List)
	if !ok || !list.IsOrdered() || list.Start != 100 || list.FirstChild() == nil {
		t.Fatalf("ordered list was not preserved: %q", value)
	}
	item := list.FirstChild()
	if item.FirstChild() == nil || item.FirstChild().NextSibling() == nil {
		t.Fatalf("nested list was flattened: %q", value)
	}
	if nested, ok := item.FirstChild().NextSibling().(*ast.List); !ok || nested.IsOrdered() || nested.ChildCount() != 2 {
		t.Fatalf("nested items were not preserved: %q", value)
	}
}

func TestNativeMarkdownListPreservesNestedOrderedStart(t *testing.T) {
	source := "12. 报销细则。\n\n    100. 第一项。\n    101. 第二项。\n"
	document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", `{}`))
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 1 || document.Pages[0].Blocks[0].Text == nil {
		t.Fatalf("parse ordered list: %+v err=%v", document, err)
	}
	value := *document.Pages[0].Blocks[0].Text
	oldOutput := strings.Replace(value, "\n\n", "\n", 1)
	oldRoot := goldmark.DefaultParser().Parse(text.NewReader([]byte(oldOutput)))
	oldList, ok := oldRoot.FirstChild().(*ast.List)
	if !ok || oldList.FirstChild() == nil || oldList.FirstChild().FirstChild() == nil || oldList.FirstChild().FirstChild().NextSibling() != nil {
		t.Fatalf("old rendering did not reproduce the flattened nested list: %q", oldOutput)
	}
	canonical, err := CanonicalMarkdownList(context.Background(), value)
	if err != nil || canonical != value {
		t.Fatalf("canonical list changed on round trip: %q err=%v", canonical, err)
	}
	root := goldmark.DefaultParser().Parse(text.NewReader([]byte(value)))
	list, ok := root.FirstChild().(*ast.List)
	if !ok || list.Start != 12 || list.ChildCount() != 1 || list.FirstChild().FirstChild() == nil {
		t.Fatalf("parent list changed: %q", value)
	}
	nested, ok := list.FirstChild().FirstChild().NextSibling().(*ast.List)
	if !ok || nested.Start != 100 || nested.ChildCount() != 2 {
		t.Fatalf("nested numbering changed: %q", value)
	}
	t.Log("nested ordered list retained: old rendering 0/1, corrected rendering 1/1")
}

func TestNativeMarkdownListKeepsLiteralMarkup(t *testing.T) {
	source := "# 标题\n\n- 字面量 \\*强调\\*、\\[链接\\](https://invalid) 和 `<tag>`。\n"
	document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", `{}`))
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 2 || document.Pages[0].Blocks[1].Text == nil {
		t.Fatalf("parse literal list text: %+v err=%v", document, err)
	}
	var rendered bytes.Buffer
	if err := goldmark.Convert([]byte(*document.Pages[0].Blocks[1].Text), &rendered); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	if !strings.Contains(html, "*强调*") || !strings.Contains(html, "[链接](https://invalid)") || !strings.Contains(html, "&lt;tag&gt;") ||
		strings.Contains(html, "<em>") || strings.Contains(html, "<a ") || strings.Contains(html, "raw HTML omitted") {
		t.Fatalf("literal list content changed when rendered: %q", html)
	}
}

func TestNativeMarkdownListKeepsParagraphBoundaries(t *testing.T) {
	source := "# 标题\n\n- 第一段。\n\n  第二段。\n"
	document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, []byte(source), "text/markdown", `{}`))
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 2 || document.Pages[0].Blocks[1].Text == nil {
		t.Fatalf("parse list paragraphs: %+v err=%v", document, err)
	}
	value := []byte(*document.Pages[0].Blocks[1].Text)
	root := goldmark.DefaultParser().Parse(text.NewReader(value))
	list, ok := root.FirstChild().(*ast.List)
	if !ok || list.FirstChild() == nil || list.FirstChild().ChildCount() != 2 {
		t.Fatalf("list paragraphs were merged: %q", value)
	}
}

func FuzzNativeText(f *testing.F) {
	f.Add(true, []byte("# 标题\n\n| 甲 | 乙 |\n| --- | --- |\n| 一 | 二 |\n"))
	f.Add(false, []byte("第一章 总则\n正文。\n"))
	f.Add(true, []byte("# 标题\n\n![图片](image.png)\n"))
	f.Fuzz(func(t *testing.T, markdown bool, content []byte) {
		mime := "text/plain"
		if markdown {
			mime = "text/markdown"
		}
		document, err := nativeTextParser(t).Parse(context.Background(), nativeTextInput(t, content, mime, `{}`))
		if err == nil && (len(document.Pages) != 1 || len(document.Pages[0].Blocks) == 0) {
			t.Fatalf("empty successful parse: %+v", document)
		}
	})
}
