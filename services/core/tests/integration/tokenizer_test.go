package integration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"tuskrag.local/core/internal/ingest"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/tokenizer"
)

func TestBGEM3CounterWithChunkers(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	paragraphs := []string{"差旅申请需事先提交。主管应核验出差目的。票据应真实完整。", "住宿费用按城市标准执行。超出部分应提前审批。"}
	request := ingest.TextSplitRequest{
		Paragraphs:  []ingest.NormalizedBlock{{Kind: "paragraph", Page: 1, Text: &paragraphs[0]}, {Kind: "paragraph", Page: 2, Text: &paragraphs[1]}},
		HeadingPath: []string{"差旅制度", "审批流程"}, TargetMin: 20, TargetMax: 30, HardMax: 40, OverlapMax: 12, CountTokens: counter.Count,
	}
	baseline, err := counter.Count(ctx, strings.Join(request.HeadingPath, " › ")+"\n"+strings.Join(paragraphs, "\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := ingest.SplitText(ctx, request)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("split text: chunks=%d err=%v", len(chunks), err)
	}
	var rebuilt [2]strings.Builder
	maxText := 0
	var textCounts []int
	for _, chunk := range chunks {
		count, err := counter.Count(ctx, chunk.SearchText)
		if err != nil || count != chunk.TokenCount || count > request.HardMax {
			t.Fatalf("text token count=%d metadata=%d err=%v", count, chunk.TokenCount, err)
		}
		maxText = max(maxText, count)
		textCounts = append(textCounts, count)
		for _, fragment := range chunk.Fragments {
			if !fragment.Overlap {
				rebuilt[fragment.ParagraphIndex].WriteString(fragment.Text)
			}
		}
	}
	for index, paragraph := range paragraphs {
		if rebuilt[index].String() != paragraph {
			t.Fatalf("paragraph %d changed", index)
		}
	}
	if !reflect.DeepEqual(textCounts, []int{25, 24, 25, 24}) {
		t.Fatalf("text counts differ from reference tokenizer: %v", textCounts)
	}
	rows := [][]string{{"城市", "上限"}, {"北京", "600"}, {"上海", "450"}, {"成都", "350"}, {"深圳", "700"}}
	table := ingest.TableSplitRequest{Block: ingest.NormalizedBlock{Kind: "table", Page: 2, Cells: rows}, HeadingPath: []string{"差旅制度", "住宿标准"}, MIME: "text/csv", HeaderRow: 1, MaxTokens: 65, CountTokens: counter.Count}
	wholeRequest := table
	wholeRequest.MaxTokens = 800
	whole, err := ingest.SplitTable(ctx, wholeRequest)
	if err != nil || len(whole) != 1 {
		t.Fatalf("whole table: %v", err)
	}
	groups, err := ingest.SplitTable(ctx, table)
	if err != nil || len(groups) < 2 {
		t.Fatalf("split table: groups=%d err=%v", len(groups), err)
	}
	var rebuiltRows [][]string
	maxTable := 0
	var tableCounts []int
	for _, group := range groups {
		count, err := counter.Count(ctx, group.SearchText)
		if err != nil || count != group.TokenCount || count > table.MaxTokens || !reflect.DeepEqual(group.Cells[0], rows[0]) {
			t.Fatalf("table token count=%d metadata=%d err=%v", count, group.TokenCount, err)
		}
		maxTable = max(maxTable, count)
		tableCounts = append(tableCounts, count)
		rebuiltRows = append(rebuiltRows, group.Cells[1:]...)
	}
	if !reflect.DeepEqual(rebuiltRows, rows[1:]) {
		t.Fatal("table rows changed")
	}
	if !reflect.DeepEqual(tableCounts, []int{64, 64}) {
		t.Fatalf("table counts differ from reference tokenizer: %v", tableCounts)
	}
	t.Logf("text: unsplit=%d limit=%d chunks=%d max=%d; table: unsplit=%d limit=%d chunks=%d max=%d rows=%d/%d", baseline, request.HardMax, len(chunks), maxText, whole[0].TokenCount, table.MaxTokens, len(groups), maxTable, len(rebuiltRows), len(rows)-1)
}

func TestBGEM3CounterWithStructuredChunks(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	value := "12. 第十二条：报销规定。\n    - 事前提交申请。\n    - 票据需真实完整。\n    - 标准依据出差城市。\n    - 超标须提前审批。\n13. 第十三条：复核。"
	path := []string{"管理制度", "差旅"}
	request := ingest.StructuredSplitRequest{Block: ingest.NormalizedBlock{Kind: "list", Page: 1, Text: &value},
		HeadingPath: path, MaxTokens: 32, CountTokens: counter.Count}
	baseline, err := counter.Count(ctx, strings.Join(path, " › ")+"\n"+value)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := ingest.SplitList(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var counts []int
	for _, chunk := range chunks {
		counts = append(counts, chunk.TokenCount)
	}
	if baseline != 55 || !reflect.DeepEqual(counts, []int{32, 25, 26, 17}) ||
		chunks[0].FirstSubitem != 1 || chunks[0].LastSubitem != 2 || chunks[1].FirstSubitem != 3 || chunks[2].FirstSubitem != 4 || chunks[3].FirstItem != 2 {
		t.Fatalf("list counts or item coverage differ from reference: baseline=%d counts=%v", baseline, counts)
	}
	code := "def safe():\n    return \"<data>\"\n"
	request.Block = ingest.NormalizedBlock{Kind: "code", Page: 1, Text: &code}
	chunk, err := ingest.SplitCode(ctx, request)
	if err != nil || chunk.Content != code || chunk.TokenCount != 21 {
		t.Fatalf("code count or content differs from reference: count=%d err=%v", chunk.TokenCount, err)
	}
	t.Logf("list: unsplit=%d limit=%d chunks=%v subitems=4/4; code=%d", baseline, request.MaxTokens, counts, chunk.TokenCount)
}

func TestBGEM3CounterWithParentSections(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	heading, level := "差旅", 1
	texts := []string{"申请需要事先提交。", "住宿标准按出差城市执行。", "超出费用须提前审批。", "票据应真实完整。"}
	document := parser.Document{SchemaVersion: parser.ExpectedSchemaVersion, Meta: parser.Metadata{ParserProfile: "test"}, Pages: []parser.Page{
		{Number: 1, Blocks: []parser.Block{{Type: "heading", Text: &heading, Level: &level}, {Type: "paragraph", Text: &texts[0]}, {Type: "paragraph", Text: &texts[1]}}},
		{Number: 2, Blocks: []parser.Block{{Type: "paragraph", Text: &texts[2]}, {Type: "paragraph", Text: &texts[3]}}},
	}}
	normalized, err := ingest.AssembleMarkdown(ctx, document)
	if err != nil || len(normalized.Sections) != 1 {
		t.Fatalf("assemble parent fixture: %v", err)
	}
	section := normalized.Sections[0]
	baseline, err := counter.Count(ctx, section.BodyMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	pieces, err := ingest.SplitParentSection(ctx, ingest.ParentSplitRequest{Section: section, MaxTokens: 20, CountTokens: counter.Count})
	if err != nil || len(pieces) != 2 {
		t.Fatalf("split parent: pieces=%d err=%v", len(pieces), err)
	}
	counts := []int{pieces[0].TokenCount, pieces[1].TokenCount}
	if baseline != 30 || !reflect.DeepEqual(counts, []int{16, 16}) || len(pieces[0].Blocks) != 2 || len(pieces[1].Blocks) != 2 ||
		pieces[0].PageStart != 1 || pieces[0].PageEnd != 1 || pieces[1].PageStart != 2 || pieces[1].PageEnd != 2 ||
		pieces[0].BodyMarkdown+"\n\n"+pieces[1].BodyMarkdown != section.BodyMarkdown {
		t.Fatalf("parent result differs from reference: baseline=%d counts=%v", baseline, counts)
	}
	t.Logf("parent: unsplit=%d limit=20 pieces=%v blocks=4/4", baseline, counts)
}
