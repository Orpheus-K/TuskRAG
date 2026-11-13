package answer

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

func TestStitchChunksRemovesOverlapAndDuplicateTableHeaders(t *testing.T) {
	textChunks := []sectionChunk{
		{ChunkID: 1, Ordinal: 1, Kind: "text", Content: "第一条规定申请需提前提交。\n\n第二条说明审批主体为部门负责人。"},
		{ChunkID: 2, Ordinal: 2, Kind: "text", Content: "第二条说明审批主体为部门负责人。\n\n第三条说明超时自动退回。"},
		{ChunkID: 3, Ordinal: 3, Kind: "text", Content: "第三条说明超时自动退回。补充说明仅在工作日生效。"},
	}
	gotText := stitchChunks(textChunks)
	wantText := "第一条规定申请需提前提交。\n\n第二条说明审批主体为部门负责人。\n\n第三条说明超时自动退回。补充说明仅在工作日生效。"
	if gotText != wantText {
		t.Fatalf("text stitch mismatch:\ngot:  %q\nwant: %q", gotText, wantText)
	}

	// Non-sentence word coincidences must not be stripped.
	noBoundary := []sectionChunk{
		{ChunkID: 10, Ordinal: 1, Kind: "text", Content: "请核对审批制度"},
		{ChunkID: 11, Ordinal: 2, Kind: "text", Content: "制度要求保留原件。"},
	}
	if got := stitchChunks(noBoundary); got != "请核对审批制度\n\n制度要求保留原件。" {
		t.Fatalf("unexpected overlap strip without boundary: %q", got)
	}

	tableChunks := []sectionChunk{
		{ChunkID: 20, Ordinal: 4, Kind: "table", Content: "| 城市 | 标准 |\n| --- | --- |\n| 北京 | 600 |"},
		{ChunkID: 21, Ordinal: 5, Kind: "table", Content: "| 城市 | 标准 |\n| --- | --- |\n| 上海 | 500 |"},
		{ChunkID: 22, Ordinal: 6, Kind: "table", Content: "| 职级 | 补贴 |\n| --- | --- |\n| P7 | 200 |"},
	}
	gotTable := stitchChunks(tableChunks)
	wantTable := "| 城市 | 标准 |\n| --- | --- |\n| 北京 | 600 |\n| 上海 | 500 |\n\n| 职级 | 补贴 |\n| --- | --- |\n| P7 | 200 |"
	if gotTable != wantTable {
		t.Fatalf("table stitch mismatch:\ngot:  %q\nwant: %q", gotTable, wantTable)
	}
}

func TestAssemblyParentBacktrackAndDisabledGuard(t *testing.T) {
	p1, p2, p3 := int32(1), int32(2), int32(3)
	fullSection := sectionSnapshot{
		SectionID:         100,
		SectionTokenCount: 900,
		AllAllowed:        true,
		Chunks: []sectionChunk{
			{ChunkID: 1, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 1, Kind: "text", Content: "开头条件：仅限正式员工。", Title: "制度", MIME: "application/pdf", HeadingPath: []string{"第一章"}, PageStart: &p1, PageEnd: &p1},
			{ChunkID: 2, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 2, Kind: "text", Content: "报销上限为每晚 600 元。", Title: "制度", MIME: "application/pdf", HeadingPath: []string{"第一章"}, PageStart: &p2, PageEnd: &p2},
			{ChunkID: 3, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 3, Kind: "text", Content: "例外：实习生不适用本条款。", Title: "制度", MIME: "application/pdf", HeadingPath: []string{"第一章"}, PageStart: &p3, PageEnd: &p3},
		},
	}

	service := fixture(nil)
	service.loadSections = func(context.Context, retrieval.Actor, int64, []int64) (map[int64]sectionSnapshot, error) {
		return map[int64]sectionSnapshot{100: fullSection}, nil
	}
	hitOnly := []retrieval.Candidate{
		{ChunkID: 2, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 2, Kind: "text", Content: "报销上限为每晚 600 元。", Title: "制度", MIME: "application/pdf", HeadingPath: []string{"第一章"}, PageStart: &p2, PageEnd: &p2},
	}
	selected, evidence, limited, err := service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, hitOnly)
	if err != nil {
		t.Fatal(err)
	}
	if limited || len(evidence) != 1 || len(selected) != 3 {
		t.Fatalf("expected full section promotion: limited=%t evidence=%+v selected=%d", limited, evidence, len(selected))
	}
	if !slices.Equal(evidence[0].ChunkIDs, []int64{1, 2, 3}) || !slices.Equal(evidence[0].Pages, []int32{1, 3}) ||
		!strings.Contains(evidence[0].Content, "例外：实习生不适用本条款。") {
		t.Fatalf("unexpected promoted evidence: %+v", evidence[0])
	}

	// Disabling parent backtracking leaves only the single hit chunk.
	service.Assembly.ParentBacktrack = false
	selected, evidence, _, err = service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, hitOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(evidence) != 1 || !slices.Equal(evidence[0].ChunkIDs, []int64{2}) {
		t.Fatalf("expected hit-only when parent backtrack disabled: %+v", evidence)
	}

	// If any sibling chunk in the section is disabled (AllAllowed=false), parent backtrack must be suppressed
	// and adjacent hits must not bridge across the disabled gap.
	service.Assembly.ParentBacktrack = true
	partialSection := fullSection
	partialSection.AllAllowed = false
	partialSection.Chunks = []sectionChunk{fullSection.Chunks[0], fullSection.Chunks[2]}
	service.loadSections = func(context.Context, retrieval.Actor, int64, []int64) (map[int64]sectionSnapshot, error) {
		return map[int64]sectionSnapshot{100: partialSection}, nil
	}
	hitsAcrossGap := []retrieval.Candidate{
		{ChunkID: 1, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 1, Kind: "text", Content: fullSection.Chunks[0].Content, Title: "制度"},
		{ChunkID: 3, DocumentID: 10, VersionID: 20, SectionID: 100, Ordinal: 3, Kind: "text", Content: fullSection.Chunks[2].Content, Title: "制度"},
	}
	selected, evidence, _, err = service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, hitsAcrossGap)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || len(evidence) != 2 || strings.Contains(evidence[0].Content+evidence[1].Content, "600 元") {
		t.Fatalf("disabled chunk leaked or bridged: %+v", evidence)
	}
}

func TestAssemblySimilarityDedupAndBudgetFallback(t *testing.T) {
	counter, err := tokenizer.NewBGEM3(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	service := fixture(nil)
	service.CountTokens = counter.Count

	// Near-duplicate (>0.95 trigram Dice) boilerplate is deduplicated; distinct city prices remain.
	boilerplateA := strings.Repeat("差旅住宿报销必须附带电子发票与出差审批单，逾期三十天不再受理。", 3)
	boilerplateB := boilerplateA + " "
	boilerplateC := strings.TrimSuffix(boilerplateA, "。") + "！"
	candidates := []retrieval.Candidate{
		{ChunkID: 1, DocumentID: 1, VersionID: 1, SectionID: 10, Ordinal: 1, Kind: "text", Content: boilerplateA, Title: "规则"},
		{ChunkID: 2, DocumentID: 2, VersionID: 1, SectionID: 20, Ordinal: 1, Kind: "text", Content: boilerplateB, Title: "规则副本"},
		{ChunkID: 3, DocumentID: 3, VersionID: 1, SectionID: 30, Ordinal: 1, Kind: "text", Content: boilerplateC, Title: "规则修订"},
		{ChunkID: 4, DocumentID: 4, VersionID: 1, SectionID: 40, Ordinal: 1, Kind: "text", Content: "| 城市 | 标准 |\n| --- | --- |\n| 北京 | 600 元 |", Title: "标准表"},
		{ChunkID: 5, DocumentID: 5, VersionID: 1, SectionID: 50, Ordinal: 1, Kind: "text", Content: "| 城市 | 标准 |\n| --- | --- |\n| 上海 | 500 元 |", Title: "标准表2"},
	}
	_, evidence, limited, err := service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if limited || len(evidence) != 3 || evidence[0].ID != "c_1" || evidence[1].ID != "c_4" || evidence[2].ID != "c_5" {
		t.Fatalf("unexpected dedup result: limited=%t evidence=%+v", limited, evidence)
	}

	// Budget fallback: full section exceeds remaining budget, so fallback uses hit chunk instead of failing/skipping.
	service.loadSections = func(context.Context, retrieval.Actor, int64, []int64) (map[int64]sectionSnapshot, error) {
		return map[int64]sectionSnapshot{
			60: {
				SectionID: 60, SectionTokenCount: 200, AllAllowed: true,
				Chunks: []sectionChunk{{ChunkID: 601, DocumentID: 6, VersionID: 1, SectionID: 60, Ordinal: 1, Kind: "text", Content: strings.Repeat("规范说明", 20), Title: "A"}},
			},
			70: {
				SectionID: 70, SectionTokenCount: 300, AllAllowed: true,
				Chunks: []sectionChunk{
					{ChunkID: 701, DocumentID: 7, VersionID: 1, SectionID: 70, Ordinal: 1, Kind: "text", Content: strings.Repeat("扩展背景材料内容", 35), Title: "B"},
					{ChunkID: 702, DocumentID: 7, VersionID: 1, SectionID: 70, Ordinal: 2, Kind: "text", Content: "关键条款为五天。", Title: "B"},
				},
			},
			80: {
				SectionID: 80, SectionTokenCount: 300, AllAllowed: true,
				Chunks: []sectionChunk{
					{ChunkID: 801, DocumentID: 8, VersionID: 1, SectionID: 80, Ordinal: 1, Kind: "text", Content: strings.Repeat("尾部低分超限段落", 40), Title: "C"},
				},
			},
		}, nil
	}
	// Budget allows section 60 + hit chunk 702 (~55 + ~35 tokens), not full section 70 or tail 80.
	service.Assembly = AssemblyOptions{ParentBacktrack: true, ContextBudgetTokens: 130}
	budgetHits := []retrieval.Candidate{
		{ChunkID: 601, DocumentID: 6, VersionID: 1, SectionID: 60, Ordinal: 1},
		{ChunkID: 702, DocumentID: 7, VersionID: 1, SectionID: 70, Ordinal: 2},
		{ChunkID: 801, DocumentID: 8, VersionID: 1, SectionID: 80, Ordinal: 1},
	}
	selected, evidence, limited, err := service.assemble(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, 1, budgetHits)
	if err != nil {
		t.Fatal(err)
	}
	if !limited || len(evidence) != 2 || len(selected) != 2 || !slices.Equal(evidence[1].ChunkIDs, []int64{702}) {
		t.Fatalf("expected fallback to hit chunk then tail cutoff: limited=%t evidence=%+v", limited, evidence)
	}
}

func TestStreamInheritsKBTopKAndMarksContextBudget(t *testing.T) {
	seenLimit := -1
	service := fixture(generatorFunc(func(_ context.Context, m []models.ChatMessage, emit func(string) error) (models.ChatResult, error) {
		if err := emit("依据规定 [1]。"); err != nil {
			return models.ChatResult{}, err
		}
		return models.ChatResult{Finish: "stop"}, nil
	}))
	service.Search = searchFunc(func(_ context.Context, _ retrieval.Actor, req retrieval.Request) (retrieval.Result, error) {
		seenLimit = req.Limit
		return retrieval.Result{
			Candidates: []retrieval.Candidate{
				{ChunkID: 1, DocumentID: 2, VersionID: 3, SectionID: 4, Ordinal: 1, Content: "短正文。", Title: "制度"},
				{ChunkID: 2, DocumentID: 2, VersionID: 3, SectionID: 5, Ordinal: 1, Content: strings.Repeat("超长尾段", 500), Title: "制度"},
			},
		}, nil
	})
	service.Assembly = AssemblyOptions{ParentBacktrack: true, ContextBudgetTokens: 80}
	var gotRetrieval *RetrievalEvent
	if err := service.Stream(context.Background(), retrieval.Actor{TenantID: 1, UserID: 1}, Request{KnowledgeBaseID: 1, ModelID: 1, Question: "规定？"}, func(e Event) error {
		if e.Retrieval != nil {
			gotRetrieval = e.Retrieval
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seenLimit != 0 || gotRetrieval == nil || !slices.Contains(gotRetrieval.Degraded, "context_budget") {
		t.Fatalf("limit=%d retrieval=%+v", seenLimit, gotRetrieval)
	}
}

func TestStoredEvidenceBackwardCompatibility(t *testing.T) {
	var legacy storedEvidence
	if err := json.Unmarshal([]byte(`{"n":1,"chunk_id":42,"document_id":2,"version_id":3,"section_id":4}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(legacy.effectiveChunkIDs(), []int64{42}) {
		t.Fatalf("legacy chunk ids: %+v", legacy.effectiveChunkIDs())
	}
	var multi storedEvidence
	if err := json.Unmarshal([]byte(`{"n":1,"chunk_id":42,"chunk_ids":[42,43,44],"document_id":2,"version_id":3,"section_id":4}`), &multi); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(multi.effectiveChunkIDs(), []int64{42, 43, 44}) {
		t.Fatalf("multi chunk ids: %+v", multi.effectiveChunkIDs())
	}
}
