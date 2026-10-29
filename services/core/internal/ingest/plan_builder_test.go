package ingest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"tuskrag.local/core/internal/markdown"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/tokenizer"
)

func planRequest(document NormalizedDocument) PlanBuildRequest {
	return PlanBuildRequest{
		Document: document, MIME: "text/markdown", EmbeddingSpaceID: 7,
		EmbeddingProfile: EmbeddingProfile{ProviderID: 9, Model: "test", Dimensions: 64, Normalized: true, Distance: "cosine"},
		ConfigRevision:   1, ParentMaxTokens: 1000, ChildTargetMin: 1, ChildTargetMax: 200,
		ChildHardMax: 400, OverlapMax: 20, CountTokens: countRunes,
	}
}

func buildTestDocument(t *testing.T, pages ...parser.Page) NormalizedDocument {
	t.Helper()
	document, err := AssembleMarkdown(context.Background(), parsedPages(pages...))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestBuildPlanPreservesStructureAndParentPages(t *testing.T) {
	list, code := "- 申请。\n- 审核。", "return amount"
	document := buildTestDocument(t,
		parser.Page{Number: 1, Blocks: []parser.Block{heading("制度", 1), paragraph("总则。")}},
		parser.Page{Number: 2, Blocks: []parser.Block{heading("差旅", 2), paragraph("先申请。"),
			{Type: "table", Cells: [][]string{{"城市", "限额"}, {"北京", "600"}}}, {Type: "list", Text: &list}, {Type: "code", Text: &code}}},
	)
	request := planRequest(document)
	request.TableHeaders = map[BlockPosition]int{{Section: 2, Block: 2}: 1}
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sections) != 2 || plan.Sections[0].ParentIndex != -1 || plan.Sections[1].ParentIndex != 0 ||
		plan.Sections[0].PageStart != 1 || plan.Sections[0].PageEnd != 2 || plan.Sections[1].PageStart != 2 {
		t.Fatalf("parent relationship or page range changed: %+v", plan.Sections)
	}
	var kinds []string
	for _, chunk := range plan.Chunks {
		kinds = append(kinds, chunk.Kind)
		section := plan.Sections[chunk.SectionIndex]
		prefix := strings.Join(section.HeadingPath, " › ") + "\n"
		if !strings.HasPrefix(chunk.SearchText, prefix+chunk.Content) || chunk.TokenCount != int32(utf8.RuneCountInString(chunk.SearchText)) {
			t.Fatalf("chunk content, context or token count changed: %+v", chunk)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"text", "text", "table", "list", "code"}) ||
		!strings.Contains(plan.Chunks[2].Content, "北京") || !strings.Contains(plan.Chunks[2].Content, "600") ||
		plan.Chunks[3].Content != list || plan.Chunks[4].Content != code {
		t.Fatalf("source blocks lost or reordered: %+v", plan.Chunks)
	}
	document.Sections[0].HeadingPath[0] = "改变"
	if plan.Sections[0].HeadingPath[0] != "制度" {
		t.Fatal("plan aliases source heading metadata")
	}
}

func TestBuildPlanOverlapsAcrossParentPiecesAndKeepsChildHeading(t *testing.T) {
	document := buildTestDocument(t,
		parser.Page{Number: 1, Blocks: []parser.Block{heading("差旅", 1), paragraph("申请需要事先提交。"), paragraph("住宿标准按出差城市执行。")}},
		parser.Page{Number: 2, Blocks: []parser.Block{paragraph("超出费用须提前审批。"), paragraph("票据应真实完整。"), heading("细则", 2), paragraph("保存凭证。")}},
	)
	request := planRequest(document)
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 24, 80, 100
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sections) != 4 || plan.Sections[0].BodyMarkdown != "" || plan.Sections[1].ParentIndex != 0 ||
		plan.Sections[2].ParentIndex != 0 || plan.Sections[3].ParentIndex != 0 || plan.Sections[3].Heading != "细则" ||
		plan.Sections[1].PageStart != 1 || plan.Sections[2].PageStart != 1 || plan.Sections[2].PageEnd != 2 {
		t.Fatalf("pseudo-sections lost hierarchy or cross-page evidence: %+v", plan.Sections)
	}
	if len(plan.Chunks) != 3 || plan.Chunks[1].SectionIndex != 2 ||
		!strings.HasPrefix(plan.Chunks[1].Content, "住宿标准按出差城市执行。\n\n超出费用须提前审批。") ||
		plan.Chunks[1].PageStart != 1 || plan.Chunks[1].PageEnd != 2 || strings.Contains(plan.Chunks[2].Content, "票据应真实完整") {
		t.Fatalf("overlap crossed the wrong boundary or lost its page: %+v", plan.Chunks)
	}
}

func TestBuildPlanDoesNotOverlapAcrossStructuredBlocks(t *testing.T) {
	list := "- 审核。"
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{
		heading("标题", 1), paragraph("第一句。"), {Type: "list", Text: &list}, paragraph("第二句。"),
	}})
	request := planRequest(document)
	request.ParentMaxTokens = 8
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Chunks) != 3 || plan.Chunks[2].Content != "第二句。" {
		t.Fatalf("text overlap crossed a structured block: %+v", plan.Chunks)
	}
}

func TestBuildPlanPreamblePiecesRemainRoots(t *testing.T) {
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{paragraph("第一句。"), paragraph("第二句。")}})
	request := planRequest(document)
	request.ParentMaxTokens = 5
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sections) != 2 || plan.Sections[0].ParentIndex != -1 || plan.Sections[1].ParentIndex != -1 ||
		plan.Sections[0].Heading != "" || plan.Sections[1].Heading != "" || plan.Chunks[1].Content != "第一句。\n\n第二句。" {
		t.Fatalf("preamble sections or overlap changed: %+v", plan)
	}
}

func TestBuildPlanBGEM3ReferenceAndOverlap(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	section := sampleParentSection(t)
	request := planRequest(NormalizedDocument{Sections: []Section{section}})
	request.CountTokens = counter.Count
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 20, 80, 100
	baseline, err := counter.Count(ctx, "差旅\n"+section.BodyMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if baseline != 33 || len(plan.Sections) != 3 || len(plan.Chunks) != 2 ||
		plan.Sections[1].TokenCount != 16 || plan.Sections[2].TokenCount != 16 ||
		plan.Chunks[0].TokenCount != 19 || plan.Chunks[1].TokenCount != 27 ||
		!strings.HasPrefix(plan.Chunks[1].Content, "住宿标准按出差城市执行。\n\n超出费用须提前审批。") {
		t.Fatalf("plan differs from fixed reference: baseline=%d sections=%+v chunks=%+v", baseline, plan.Sections, plan.Chunks)
	}
	t.Logf("unsplit search=%d; parent tokens=[%d %d]; child search tokens=[%d %d]; cross-page overlap=1 sentence",
		baseline, plan.Sections[1].TokenCount, plan.Sections[2].TokenCount, plan.Chunks[0].TokenCount, plan.Chunks[1].TokenCount)
}

func TestBuildPlanSplitsLongTableAcrossParents(t *testing.T) {
	rows := [][]string{{"城市", "上限", "用途"}, {"北京", "600", "住宿"}, {"上海", "450", "住宿"},
		{"成都", "350", "住宿"}, {"深圳", "700", "住宿"}}
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{{Type: "table", Cells: rows}}})
	request := planRequest(document)
	request.MIME, request.ParentMaxTokens = "text/csv", 90
	request.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 1}: 1}
	baseline, err := countRunes(context.Background(), document.Sections[0].BodyMarkdown)
	if err != nil || baseline <= request.ParentMaxTokens {
		t.Fatalf("fixture does not exceed the parent limit: %d %v", baseline, err)
	}
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sections) < 2 || len(plan.Chunks) < 2 {
		t.Fatalf("large table was not split: %+v", plan)
	}
	var combined strings.Builder
	for _, section := range plan.Sections {
		if section.TokenCount > int32(request.ParentMaxTokens) {
			t.Fatalf("parent limit exceeded: %+v", section)
		}
	}
	for _, chunk := range plan.Chunks {
		if chunk.Kind != "table" || chunk.TokenCount > int32(request.ParentMaxTokens) || !strings.Contains(chunk.Content, "城市") ||
			!strings.Contains(chunk.Content, "上限") || !strings.Contains(chunk.SearchText, "城市: ") {
			t.Fatalf("split table lost header, expansion, or limits: %+v", chunk)
		}
		combined.WriteString(chunk.Content)
	}
	for _, row := range rows[1:] {
		if strings.Count(combined.String(), row[0]) != 1 {
			t.Fatalf("table data rows were duplicated or lost: %s", combined.String())
		}
	}
	t.Logf("unsplit parent=%d; limit=%d; parents=%d; table chunks=%d; data rows=%d/%d",
		baseline, request.ParentMaxTokens, len(plan.Sections), len(plan.Chunks), len(rows)-1, len(rows)-1)
}

func TestBuildPlanBGEM3LongTableReference(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	rows := [][]string{{"城市", "上限", "用途"}, {"北京", "600", "住宿"}, {"上海", "450", "住宿"},
		{"成都", "350", "住宿"}, {"深圳", "700", "住宿"}}
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{{Type: "table", Cells: rows}}})
	request := planRequest(document)
	request.MIME, request.ParentMaxTokens, request.CountTokens = "text/csv", 70, counter.Count
	request.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 1}: 1}
	baseline, err := counter.Count(ctx, document.Sections[0].BodyMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, request)
	if err != nil || baseline != 79 || len(plan.Sections) != 4 || len(plan.Chunks) != 4 {
		t.Fatalf("large table plan differs from reference: baseline=%d plan=%+v err=%v", baseline, plan, err)
	}
	for index, chunk := range plan.Chunks {
		if plan.Sections[index].TokenCount != 40 || chunk.TokenCount != 54 || !strings.Contains(chunk.Content, rows[index+1][0]) {
			t.Fatalf("large table fragment differs from reference: section=%+v chunk=%+v", plan.Sections[index], chunk)
		}
	}
	t.Logf("unsplit parent=%d; limit=70; parent tokens=[40 40 40 40]; search tokens=[54 54 54 54]; rows=4/4", baseline)
}

func TestBuildPlanKeepsOriginalPositionsAfterTableExpansion(t *testing.T) {
	rows := [][]string{{"城市", "上限", "用途"}, {"北京", "600", "住宿"}, {"上海", "450", "住宿"},
		{"成都", "350", "住宿"}, {"深圳", "700", "住宿"}}
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{
		{Type: "table", Cells: rows}, paragraph("附表。"), {Type: "table", Cells: [][]string{{"类别", "金额"}, {"餐费", "100"}}},
	}})
	request := planRequest(document)
	request.MIME, request.ParentMaxTokens = "text/csv", 90
	request.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 1}: 1}
	if plan, err := BuildPlan(context.Background(), request); !errors.Is(err, ErrTableHeaderUnconfirmed) ||
		!strings.Contains(err.Error(), "block 3") || len(plan.Chunks) != 0 {
		t.Fatalf("later original table position not checked: %+v %v", plan, err)
	}
	request.TableHeaders[BlockPosition{Section: 1, Block: 3}] = 1
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	for _, chunk := range plan.Chunks {
		content.WriteString(chunk.Content)
	}
	value := content.String()
	first, middle, last := strings.Index(value, "北京"), strings.Index(value, "附表。"), strings.Index(value, "餐费")
	if first < 0 || middle <= first || last <= middle {
		t.Fatalf("table expansion changed block order: %s", value)
	}
}

func TestBuildPlanSplitsLongParagraphWithoutInventingParagraphBoundaries(t *testing.T) {
	value := "甲。乙。丙。丁。戊。己。"
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{
		heading("章", 1), paragraph("前句。"), paragraph(value), paragraph("后句。"),
	}})
	request := planRequest(document)
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 6, 15, 20
	plan, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sections) != 5 || len(plan.Chunks) != 4 {
		t.Fatalf("long paragraph was not split into parent pieces: %+v", plan)
	}
	var body strings.Builder
	for _, section := range plan.Sections[2:4] {
		if section.TokenCount > int32(request.ParentMaxTokens) || section.ParentIndex != 0 {
			t.Fatalf("invalid parent piece: %+v", section)
		}
		body.WriteString(section.BodyMarkdown)
	}
	if body.String() != value || plan.Chunks[1].Content != "前句。\n\n甲。乙。丙。" ||
		plan.Chunks[2].Content != "丙。丁。戊。己。" || plan.Chunks[3].Content != "己。\n\n后句。" {
		t.Fatalf("paragraph content or overlap changed: sections=%+v chunks=%+v", plan.Sections, plan.Chunks)
	}
}

func TestBuildPlanSplitsLongListAtItems(t *testing.T) {
	value := "12. 第十二条：报销规定。\n    - 事前提交申请。\n    - 票据需真实完整。\n    - 标准依据出差城市。\n    - 超标须提前审批。\n13. 第十三条：复核。"
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{
		heading("制度", 1), {Type: "list", Text: &value},
	}})
	request := planRequest(document)
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 30, 80, 100
	plan, err := BuildPlan(context.Background(), request)
	if err != nil || len(plan.Sections) < 3 || len(plan.Chunks) < 3 {
		t.Fatalf("long list did not split: %+v err=%v", plan, err)
	}
	var content strings.Builder
	for _, section := range plan.Sections[1:] {
		if section.TokenCount > int32(request.ParentMaxTokens) || section.ParentIndex != 0 {
			t.Fatalf("invalid list parent: %+v", section)
		}
	}
	for _, chunk := range plan.Chunks {
		if chunk.Kind != "list" || !strings.HasPrefix(chunk.SearchText, "制度\n") {
			t.Fatalf("invalid list child: %+v", chunk)
		}
		content.WriteString(chunk.Content)
	}
	text := content.String()
	previous := -1
	for _, item := range []string{"事前提交申请", "票据需真实完整", "标准依据出差城市", "超标须提前审批", "第十三条"} {
		position := strings.Index(text, item)
		if position <= previous || strings.Count(text, item) != 1 {
			t.Fatalf("list items lost or reordered: %s", text)
		}
		previous = position
	}
}

func TestBuildPlanBGEM3LongListReference(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	value := "12. 第十二条：报销规定。\n    - 事前提交申请。\n    - 票据需真实完整。\n    - 标准依据出差城市。\n    - 超标须提前审批。\n13. 第十三条：复核。"
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{heading("差旅", 1), {Type: "list", Text: &value}}})
	request := planRequest(document)
	request.CountTokens = counter.Count
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 25, 80, 100
	baseline, err := counter.Count(ctx, document.Sections[0].BodyMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, request)
	if err != nil || baseline != 48 || len(plan.Sections) != 5 || len(plan.Chunks) != 4 {
		t.Fatalf("long list differs from reference: baseline=%d plan=%+v err=%v", baseline, plan, err)
	}
	wantParents, wantSearch := []int32{25, 18, 19, 10}, []int32{28, 21, 22, 13}
	for index, chunk := range plan.Chunks {
		if plan.Sections[index+1].TokenCount != wantParents[index] || chunk.TokenCount != wantSearch[index] {
			t.Fatalf("list fragment counts differ from reference: section=%+v chunk=%+v", plan.Sections[index+1], chunk)
		}
	}
	t.Logf("unsplit parent=%d; limit=25; parent tokens=%v; search tokens=%v; subitems=4/4", baseline, wantParents, wantSearch)
}

func TestBuildPlanLongListPreservesLiteralMarkup(t *testing.T) {
	value := "- 保存字面量 \\*示例\\* 与 \\<script\\>。\n- 申请须提前提交。\n- 票据需要完整保存。"
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{{Type: "list", Text: &value}}})
	request := planRequest(document)
	request.ParentMaxTokens = 45
	plan, err := BuildPlan(context.Background(), request)
	if err != nil || len(plan.Chunks) < 2 {
		t.Fatalf("split literal list: %+v err=%v", plan, err)
	}
	if !strings.Contains(plan.Chunks[0].Content, "*示例*") || !strings.Contains(plan.Chunks[0].Content, "<script>") ||
		strings.Contains(plan.Sections[0].BodyMarkdown, "<script>") {
		t.Fatalf("literal markup changed meaning: %+v", plan)
	}
}

func TestBuildPlanBGEM3LongParagraphReference(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	parts := []string{"申请需要事先提交。", "住宿标准按出差城市执行。", "超出费用须提前审批。", "票据应真实完整。"}
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{heading("差旅", 1), paragraph(strings.Join(parts, ""))}})
	request := planRequest(document)
	request.CountTokens = counter.Count
	request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax = 15, 80, 100
	baseline, err := counter.Count(ctx, document.Sections[0].BodyMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, request)
	if err != nil || baseline != 27 || len(plan.Sections) != 3 || len(plan.Chunks) != 2 ||
		plan.Sections[1].TokenCount != 15 || plan.Sections[2].TokenCount != 15 ||
		plan.Chunks[0].TokenCount != 18 || plan.Chunks[1].TokenCount != 25 ||
		plan.Chunks[1].Content != strings.Join(parts[1:], "") {
		t.Fatalf("long paragraph differs from reference: baseline=%d plan=%+v err=%v", baseline, plan, err)
	}
	t.Logf("unsplit parent=%d; limit=15; parent tokens=[15 15]; search tokens=[18 25]; sentences=4/4", baseline)
}

func TestBuildPlanBoundsCounterInputs(t *testing.T) {
	count := func(ctx context.Context, value string) (int, error) {
		if len(value) > maxCountInputBytes {
			return 0, errors.New("counter received too many bytes")
		}
		return 1, ctx.Err()
	}
	for name, paragraphs := range map[string][]string{
		"multiple blocks":   {strings.Repeat("a", 40000) + ".", strings.Repeat("b", 40000) + "."},
		"single long block": {strings.Repeat(strings.Repeat("a", 125)+". ", 600)},
	} {
		t.Run(name, func(t *testing.T) {
			blocks := make([]parser.Block, len(paragraphs))
			for index, value := range paragraphs {
				blocks[index] = paragraph(value)
			}
			document := buildTestDocument(t, parser.Page{Number: 1, Blocks: blocks})
			request := planRequest(document)
			request.CountTokens = count
			request.ParentMaxTokens, request.ChildTargetMax, request.ChildHardMax, request.OverlapMax = 10, 10, 10, 0
			plan, err := BuildPlan(context.Background(), request)
			if err != nil || len(plan.Sections) < 2 || len(plan.Chunks) < 2 {
				t.Fatalf("bounded counter failed: sections=%d chunks=%d err=%v", len(plan.Sections), len(plan.Chunks), err)
			}
			for _, chunk := range plan.Chunks {
				if len(chunk.SearchText) > maxCountInputBytes {
					t.Fatal("oversized search input returned")
				}
			}
			if len(paragraphs) == 1 {
				var rebuilt strings.Builder
				for _, section := range plan.Sections {
					part, err := markdown.DecodeInline(section.BodyMarkdown)
					if err != nil {
						t.Fatal(err)
					}
					rebuilt.WriteString(part)
				}
				if rebuilt.String() != paragraphs[0] {
					t.Fatal("splitting changed the long paragraph")
				}
			}
		})
	}
}

func FuzzBuildPlanLongParagraph(f *testing.F) {
	f.Add([]byte("差旅申请。住宿 & 标准。"), uint8(20))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, uint8(12))
	f.Fuzz(func(t *testing.T, input []byte, limit uint8) {
		if len(input) > 256 {
			return
		}
		alphabet := []string{"甲", "乙", "A", " ", "&", "<", "*", "'", "😀", "\t"}
		var source strings.Builder
		for offset := 0; offset < len(input); offset += 8 {
			source.WriteString("项")
			for index := offset; index < min(offset+8, len(input)); index++ {
				source.WriteString(alphabet[int(input[index])%len(alphabet)])
			}
			source.WriteString("。")
		}
		if source.Len() == 0 {
			source.WriteString("空项。")
		}
		value := source.String()
		document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{paragraph(value)}})
		request := planRequest(document)
		request.ParentMaxTokens = int(limit%40) + 15
		request.ChildTargetMax, request.ChildHardMax, request.OverlapMax = 60, 80, 0
		plan, err := BuildPlan(context.Background(), request)
		if errors.Is(err, ErrTextUnitTooLarge) {
			if len(plan.Sections) != 0 || len(plan.Chunks) != 0 {
				t.Fatal("oversized sentence returned a partial plan")
			}
			return
		}
		if err != nil {
			t.Fatalf("build paragraph: %v", err)
		}
		var parents, children strings.Builder
		for _, section := range plan.Sections {
			part, err := markdown.DecodeInline(section.BodyMarkdown)
			if err != nil || section.TokenCount > int32(request.ParentMaxTokens) {
				t.Fatalf("invalid parent: %+v %v", section, err)
			}
			parents.WriteString(part)
		}
		for _, chunk := range plan.Chunks {
			if chunk.Kind != "text" || chunk.TokenCount > int32(request.ChildHardMax) {
				t.Fatalf("invalid child: %+v", chunk)
			}
			children.WriteString(chunk.Content)
		}
		if parents.String() != value || children.String() != value {
			t.Fatalf("split paragraph changed source: %q", value)
		}
	})
}

func FuzzBuildPlanLongList(f *testing.F) {
	f.Add([]byte("差旅<项目>\n&\\*"), uint8(30))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, uint8(60))
	f.Fuzz(func(t *testing.T, input []byte, limit uint8) {
		if len(input) > 256 {
			return
		}
		alphabet := []string{"甲", "乙", "A", "-", ".", "\n", "\\", "&", "<", " ", "\t", "😀"}
		var items []string
		for offset := 0; offset < len(input); offset += 8 {
			var item strings.Builder
			item.WriteString("项")
			for index := offset; index < min(offset+8, len(input)); index++ {
				item.WriteString(alphabet[int(input[index])%len(alphabet)])
			}
			items = append(items, "- "+markdown.EscapeInline(item.String()))
		}
		if len(items) == 0 {
			items = append(items, "- 空项")
		}
		value := strings.Join(items, "\n")
		document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{{Type: "list", Text: &value}}})
		request := planRequest(document)
		request.ParentMaxTokens = int(limit%80) + 30
		plan, err := BuildPlan(context.Background(), request)
		if errors.Is(err, ErrStructuredUnitTooLarge) {
			if len(plan.Sections) != 0 || len(plan.Chunks) != 0 {
				t.Fatal("oversized item returned a partial plan")
			}
			return
		}
		if err != nil {
			t.Fatalf("build list: %v", err)
		}
		var parts []string
		for _, chunk := range plan.Chunks {
			if chunk.Kind != "list" || chunk.TokenCount > int32(request.ChildHardMax) {
				t.Fatalf("invalid list child: %+v", chunk)
			}
			parts = append(parts, chunk.Content)
		}
		expected, err := markdown.DecodeInline(document.Sections[0].BodyMarkdown)
		if err != nil || strings.Join(parts, "\n") != expected {
			t.Fatalf("list content changed: %q", value)
		}
	})
}

func TestBuildPlanRejectsIncompleteAndInvalidInputs(t *testing.T) {
	document := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{
		heading("标题", 1), paragraph("正文。"), {Type: "table", Cells: [][]string{{"列"}, {"值"}}},
	}})
	for name, test := range map[string]struct {
		mutate func(*PlanBuildRequest)
		want   error
	}{
		"unconfirmed header":    {func(r *PlanBuildRequest) {}, ErrTableHeaderUnconfirmed},
		"wrong header position": {func(r *PlanBuildRequest) { r.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 1}: 1} }, ErrInvalidChunkConfig},
		"extra header position": {func(r *PlanBuildRequest) { r.TableHeaders = map[BlockPosition]int{{Section: 2, Block: 1}: 1} }, ErrInvalidChunkConfig},
		"unsupported header":    {func(r *PlanBuildRequest) { r.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 2}: 2} }, ErrUnsupportedParsedDocument},
		"oversized parent block": {func(r *PlanBuildRequest) {
			r.ParentMaxTokens = 1
			r.Document = buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{paragraph("正文。")}})
		}, ErrTextUnitTooLarge},
		"unsupported MIME": {func(r *PlanBuildRequest) { r.MIME = "application/x-unsupported" }, ErrUnsupportedChunkMIME},
		"invalid profile":  {func(r *PlanBuildRequest) { r.EmbeddingProfile.Dimensions = 1 }, ErrInvalidChunkConfig},
	} {
		t.Run(name, func(t *testing.T) {
			request := planRequest(document)
			test.mutate(&request)
			plan, err := BuildPlan(context.Background(), request)
			if !errors.Is(err, test.want) || len(plan.Sections) != 0 || len(plan.Chunks) != 0 {
				t.Fatalf("invalid input returned partial plan: %+v, %v", plan, err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if plan, err := BuildPlan(cancelled, planRequest(document)); !errors.Is(err, context.Canceled) || len(plan.Sections) != 0 {
		t.Fatalf("cancellation returned partial plan: %+v, %v", plan, err)
	}
	onlyHeading := buildTestDocument(t, parser.Page{Number: 1, Blocks: []parser.Block{heading("空标题", 1)}})
	if plan, err := BuildPlan(context.Background(), planRequest(onlyHeading)); !errors.Is(err, ErrInvalidPlan) || len(plan.Sections) != 0 {
		t.Fatalf("heading-only document returned partial plan: %+v, %v", plan, err)
	}
}
