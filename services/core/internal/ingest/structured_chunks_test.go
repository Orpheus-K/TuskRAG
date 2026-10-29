package ingest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"

	"tuskrag.local/core/internal/markdown"
	"tuskrag.local/core/internal/parser"
)

func structuredRequest(kind, value string) StructuredSplitRequest {
	return StructuredSplitRequest{
		Block:       NormalizedBlock{Kind: kind, Page: 2, Text: &value, BBox: []float64{1, 2, 3, 4}},
		HeadingPath: []string{"管理制度", "差旅"}, MaxTokens: 45, CountTokens: countRunes,
	}
}

func TestStructuredChunksBoundCounterInputs(t *testing.T) {
	first, second := strings.Repeat("a", 40000), strings.Repeat("b", 40000)
	count := func(ctx context.Context, value string) (int, error) {
		if len(value) > maxCountInputBytes {
			return 0, errors.New("counter received too many bytes")
		}
		return 1, ctx.Err()
	}
	request := structuredRequest("list", "- "+first+"\n- "+second)
	request.CountTokens = count
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) != 2 || !strings.Contains(chunks[0].Content, first) || !strings.Contains(chunks[1].Content, second) {
		t.Fatalf("list byte boundary lost items: chunks=%d err=%v", len(chunks), err)
	}
	request = structuredRequest("code", strings.Repeat("x", maxCountInputBytes+1))
	request.CountTokens = count
	if chunk, err := SplitCode(context.Background(), request); !errors.Is(err, ErrStructuredUnitTooLarge) || chunk.Content != "" {
		t.Fatalf("oversized code returned a partial chunk: err=%v", err)
	}
}

func TestSplitListPreservesClauseAndSubitems(t *testing.T) {
	value := "12. 第十二条：报销规定。\n    - 事前提交申请。\n    - 票据需真实完整。\n    - 标准依据出差城市。\n    - 超标须提前审批。\n13. 第十三条：复核。"
	request := structuredRequest("list", value)
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) < 3 {
		t.Fatalf("split list: chunks=%d err=%v", len(chunks), err)
	}
	var nextSubitem, maxCount int
	nextSubitem = 1
	for _, chunk := range chunks {
		if chunk.Kind != "list" || chunk.Page != 2 || chunk.TokenCount > request.MaxTokens ||
			chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || !strings.HasPrefix(chunk.SearchText, "管理制度 › 差旅\n") {
			t.Fatalf("invalid list metadata: %+v", chunk)
		}
		maxCount = max(maxCount, chunk.TokenCount)
		if chunk.FirstItem == 1 {
			if chunk.LastItem != 1 || chunk.FirstSubitem != nextSubitem || chunk.LastSubitem < chunk.FirstSubitem ||
				!strings.HasPrefix(chunk.Markdown, "12. 第十二条：报销规定。") {
				t.Fatalf("clause label or subitem range lost: %+v", chunk)
			}
			list, err := singleList(chunk.Markdown)
			if err != nil || list.ChildCount() != 1 {
				t.Fatalf("invalid split clause: %v", err)
			}
			parent := list.FirstChild()
			nested, ok := parent.FirstChild().NextSibling().(*ast.List)
			if !ok || nested.ChildCount() != chunk.LastSubitem-chunk.FirstSubitem+1 {
				t.Fatalf("nested items lost: %+v", chunk)
			}
			nextSubitem = chunk.LastSubitem + 1
		} else if chunk.FirstItem != 2 || chunk.LastItem != 2 || chunk.FirstSubitem != 0 || !strings.HasPrefix(chunk.Markdown, "13. ") {
			t.Fatalf("next clause changed: %+v", chunk)
		}
	}
	if nextSubitem != 5 {
		t.Fatalf("subitems lost or reordered: next=%d", nextSubitem)
	}
	baseline := "管理制度 › 差旅\n" + value
	t.Logf("unsplit=%d limit=%d chunks=%d max=%d subitems=%d/4", utf8.RuneCountInString(baseline), request.MaxTokens, len(chunks), maxCount, nextSubitem-1)
	request.Block.BBox[0] = 99
	request.HeadingPath[0] = "修改"
	if chunks[0].BBox[0] != 1 || chunks[0].HeadingPath[0] != "管理制度" {
		t.Fatal("list chunk aliases input metadata")
	}
}

func TestSplitListGroupsItemsWithoutChangingOrder(t *testing.T) {
	request := structuredRequest("list", "100. 申请。\n101. 审核。\n102. 批准。\n103. 归档。")
	request.MaxTokens = 27
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("split ordered items: chunks=%d err=%v", len(chunks), err)
	}
	var reconstructed []string
	next := 1
	for _, chunk := range chunks {
		if chunk.FirstItem != next || chunk.LastItem < next || chunk.FirstSubitem != 0 || chunk.LastSubitem != 0 {
			t.Fatalf("invalid item range: %+v", chunk)
		}
		list, err := singleList(chunk.Markdown)
		if err != nil || list.Start != 99+chunk.FirstItem || list.ChildCount() != chunk.LastItem-chunk.FirstItem+1 {
			t.Fatalf("ordered list numbering changed: %+v err=%v", chunk, err)
		}
		reconstructed = append(reconstructed, chunk.Markdown)
		next = chunk.LastItem + 1
	}
	if strings.Join(reconstructed, "\n") != *request.Block.Text || next != 5 {
		t.Fatal("list items changed or were reordered")
	}
}

func TestSplitListPreservesNestedOrderedNumbers(t *testing.T) {
	request := structuredRequest("list", "12. 报销细则。\n\n    100. 第一项内容。\n    101. 第二项内容。\n    102. 第三项内容。")
	request.MaxTokens = 40
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) != 3 {
		t.Fatalf("split nested ordered list: chunks=%d err=%v", len(chunks), err)
	}
	for index, chunk := range chunks {
		root, err := singleList(chunk.Markdown)
		if err != nil || root.ChildCount() != 1 || root.Start != 12 {
			t.Fatalf("root numbering changed: %+v err=%v", chunk, err)
		}
		nested, ok := root.FirstChild().FirstChild().NextSibling().(*ast.List)
		if !ok || nested.Start != 100+index || nested.ChildCount() != 1 || chunk.FirstSubitem != index+1 || chunk.LastSubitem != index+1 {
			t.Fatalf("nested numbering changed: %+v", chunk)
		}
	}
}

func TestParsedMarkdownStructuredChunks(t *testing.T) {
	source := "# 差旅制度\n\n12. 报销细则。\n\n    100. 第一项内容。\n    101. 第二项内容。\n    102. 第三项内容。\n\n```\nreturn \"<data>\"\n```\n"
	parsed := parseTextFixture(t, []byte(source), "text/markdown")
	normalized, err := AssembleMarkdown(context.Background(), parsed)
	if err != nil || len(normalized.Sections) != 1 || len(normalized.Sections[0].Blocks) != 2 {
		t.Fatalf("assemble structured document: %+v err=%v", normalized, err)
	}
	checkNormalizedRoundTrip(t, parsed, normalized)
	section := normalized.Sections[0]
	request := StructuredSplitRequest{Block: section.Blocks[0], HeadingPath: section.HeadingPath, MaxTokens: 33, CountTokens: countRunes}
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) != 3 {
		t.Fatalf("split parsed list: chunks=%d err=%v", len(chunks), err)
	}
	for index, chunk := range chunks {
		if chunk.FirstItem != 1 || chunk.FirstSubitem != index+1 || chunk.LastSubitem != index+1 || chunk.Page != 1 ||
			chunk.TokenCount > request.MaxTokens || !reflect.DeepEqual(chunk.HeadingPath, section.HeadingPath) {
			t.Fatalf("parsed list location or subitems changed: %+v", chunk)
		}
	}
	request.Block = section.Blocks[1]
	code, err := SplitCode(context.Background(), request)
	if err != nil || section.Blocks[1].Text == nil || code.Content != *section.Blocks[1].Text || code.Page != 1 {
		t.Fatalf("parsed code changed: %+v err=%v", code, err)
	}
}

func TestStructuredChunksPreserveLiteralContentAndSafeMarkdown(t *testing.T) {
	request := structuredRequest("list", "- 保存字面量 \\*示例\\* 与 \\<script\\>。")
	request.MaxTokens = 200
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) != 1 || !strings.Contains(chunks[0].Content, "*示例*") || !strings.Contains(chunks[0].Content, "<script>") {
		t.Fatalf("list content changed: %+v err=%v", chunks, err)
	}
	var rendered bytes.Buffer
	if err := goldmark.Convert([]byte(chunks[0].Markdown), &rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), "<script>") || !strings.Contains(rendered.String(), "&lt;script&gt;") || strings.Contains(rendered.String(), "<em>") {
		t.Fatalf("list display interpreted untrusted content: %q", rendered.String())
	}

	value := "```\n<script>\n~~~\n"
	code := structuredRequest("code", value)
	code.MaxTokens = 200
	chunk, err := SplitCode(context.Background(), code)
	if err != nil || chunk.Content != value || chunk.SearchText != "管理制度 › 差旅\n"+value || chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) {
		t.Fatalf("code content changed: %+v err=%v", chunk, err)
	}
	rendered.Reset()
	if err := goldmark.Convert([]byte(chunk.Markdown), &rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), "<script>") || !strings.Contains(rendered.String(), "&lt;script&gt;") {
		t.Fatalf("code display interpreted untrusted content: %q", rendered.String())
	}
}

func TestStructuredChunksRejectErrorsWithoutPartialResults(t *testing.T) {
	request := structuredRequest("list", "- 小条目。\n- "+strings.Repeat("长", 100))
	if chunks, err := SplitList(context.Background(), request); !errors.Is(err, ErrStructuredUnitTooLarge) || chunks != nil {
		t.Fatalf("oversized item returned partial chunks: %+v err=%v", chunks, err)
	}
	request = structuredRequest("list", "- 条目。\n\n  补充段落。\n  - 子项一。\n  - 子项二。")
	request.MaxTokens = 20
	if chunks, err := SplitList(context.Background(), request); !errors.Is(err, ErrStructuredUnitTooLarge) || chunks != nil {
		t.Fatalf("unsupported split returned partial chunks: %+v err=%v", chunks, err)
	}
	request = structuredRequest("code", strings.Repeat("代码", 50))
	if _, err := SplitCode(context.Background(), request); !errors.Is(err, ErrStructuredUnitTooLarge) {
		t.Fatalf("oversized code accepted: %v", err)
	}
	request = structuredRequest("list", "- 第一项\n- 第二项")
	failure := errors.New("counter unavailable")
	request.CountTokens = func(context.Context, string) (int, error) { return 0, failure }
	if chunks, err := SplitList(context.Background(), request); !errors.Is(err, failure) || chunks != nil {
		t.Fatalf("counter failure hidden: %+v err=%v", chunks, err)
	}
	request.CountTokens = func(context.Context, string) (int, error) { return 0, nil }
	if chunks, err := SplitList(context.Background(), request); !errors.Is(err, ErrInvalidTokenCount) || chunks != nil {
		t.Fatalf("invalid token count accepted: %+v err=%v", chunks, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request.CountTokens = func(context.Context, string) (int, error) { cancel(); return 1, nil }
	if chunks, err := SplitList(ctx, request); !errors.Is(err, context.Canceled) || chunks != nil {
		t.Fatalf("cancellation ignored: %+v err=%v", chunks, err)
	}
}

func TestStructuredChunksValidateInputs(t *testing.T) {
	for name, change := range map[string]func(*StructuredSplitRequest){
		"counter":  func(r *StructuredSplitRequest) { r.CountTokens = nil },
		"limit":    func(r *StructuredSplitRequest) { r.MaxTokens = 801 },
		"page":     func(r *StructuredSplitRequest) { r.Block.Page = 0 },
		"box":      func(r *StructuredSplitRequest) { r.Block.BBox = []float64{1} },
		"heading":  func(r *StructuredSplitRequest) { r.HeadingPath = []string{" "} },
		"bad type": func(r *StructuredSplitRequest) { r.Block.Kind = "table" },
	} {
		t.Run(name, func(t *testing.T) {
			request := structuredRequest("list", "- 项目")
			change(&request)
			if chunks, err := SplitList(context.Background(), request); !errors.Is(err, ErrInvalidChunkConfig) || chunks != nil {
				t.Fatalf("invalid input accepted: %+v err=%v", chunks, err)
			}
		})
	}
	request := structuredRequest("list", "- 一项\x00隐藏")
	if _, err := SplitList(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("NUL accepted: %v", err)
	}
	request = structuredRequest("list", "段落而非列表")
	if _, err := SplitList(context.Background(), request); !errors.Is(err, ErrUnsupportedParsedDocument) {
		t.Fatalf("non-list accepted: %v", err)
	}
	request = structuredRequest("code", "")
	if _, err := SplitCode(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("empty code accepted: %v", err)
	}
}

func TestListGroupItemLimit(t *testing.T) {
	request := structuredRequest("list", strings.TrimSuffix(strings.Repeat("- 项目\n", 1025), "\n"))
	request.CountTokens = func(context.Context, string) (int, error) { return 1, nil }
	chunks, err := SplitList(context.Background(), request)
	if err != nil || len(chunks) != 3 {
		t.Fatalf("bounded groups: chunks=%d err=%v", len(chunks), err)
	}
	ranges := [][2]int{{1, 512}, {513, 1024}, {1025, 1025}}
	for index, chunk := range chunks {
		if !reflect.DeepEqual([2]int{chunk.FirstItem, chunk.LastItem}, ranges[index]) {
			t.Fatalf("invalid bounded range: %+v", chunk)
		}
	}
}

func FuzzSplitListPreservesItems(f *testing.F) {
	f.Add([]byte("差旅<项目>\n&\\*"), uint8(30))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, uint8(60))
	f.Fuzz(func(t *testing.T, data []byte, limit uint8) {
		if len(data) > 256 {
			return
		}
		alphabet := []string{"甲", "乙", "A", "-", ".", "\n", "\\", "&", "<", " ", "\t", "😀"}
		var items []string
		for offset := 0; offset < len(data); offset += 8 {
			var item strings.Builder
			item.WriteString("项")
			for index := offset; index < min(offset+8, len(data)); index++ {
				item.WriteString(alphabet[int(data[index])%len(alphabet)])
			}
			items = append(items, "- "+markdown.EscapeInline(item.String()))
		}
		if len(items) == 0 {
			items = append(items, "- 空项")
		}
		request := structuredRequest("list", strings.Join(items, "\n"))
		request.MaxTokens = int(limit%80) + 30
		canonical, err := parser.CanonicalMarkdownList(context.Background(), *request.Block.Text)
		if err != nil {
			t.Fatalf("generated list is invalid: %v", err)
		}
		chunks, err := SplitList(context.Background(), request)
		if err != nil {
			if !errors.Is(err, ErrStructuredUnitTooLarge) || chunks != nil {
				t.Fatalf("unexpected list failure: %v", err)
			}
			return
		}
		var parts []string
		next := 1
		for _, chunk := range chunks {
			if chunk.FirstItem != next || chunk.LastItem < next || chunk.FirstSubitem != 0 || chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || chunk.TokenCount > request.MaxTokens {
				t.Fatalf("invalid chunk metadata: %+v", chunk)
			}
			list, err := singleList(chunk.Markdown)
			if err != nil || list.ChildCount() != chunk.LastItem-chunk.FirstItem+1 {
				t.Fatalf("invalid chunk list structure: %v", err)
			}
			parts = append(parts, chunk.Markdown)
			next = chunk.LastItem + 1
		}
		if strings.Join(parts, "\n") != canonical || next != len(items)+1 {
			t.Fatal("list items changed or were reordered")
		}
	})
}
