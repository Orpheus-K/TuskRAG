package ingest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"tuskrag.local/core/internal/parser"
)

func sampleParentSection(t *testing.T) Section {
	t.Helper()
	document := parsedPages(
		parser.Page{Number: 1, Blocks: []parser.Block{heading("差旅", 1), paragraph("申请需要事先提交。"), paragraph("住宿标准按出差城市执行。")}},
		parser.Page{Number: 2, Blocks: []parser.Block{paragraph("超出费用须提前审批。"), paragraph("票据应真实完整。")}},
	)
	normalized, err := AssembleMarkdown(context.Background(), document)
	if err != nil || len(normalized.Sections) != 1 {
		t.Fatalf("assemble parent sample: %+v err=%v", normalized, err)
	}
	return normalized.Sections[0]
}

func TestSplitParentSectionPreservesBlocksAndLocations(t *testing.T) {
	section := sampleParentSection(t)
	request := ParentSplitRequest{Section: section, MaxTokens: 24, CountTokens: countRunes}
	pieces, err := SplitParentSection(context.Background(), request)
	if err != nil || len(pieces) != 2 {
		t.Fatalf("split parent: pieces=%d err=%v", len(pieces), err)
	}
	var blocks []NormalizedBlock
	var bodies []string
	for index, piece := range pieces {
		if piece.SectionOrdinal != section.Ordinal || piece.Sequence != index+1 || !reflect.DeepEqual(piece.HeadingPath, section.HeadingPath) ||
			piece.TokenCount != utf8.RuneCountInString(piece.BodyMarkdown) || piece.TokenCount > request.MaxTokens {
			t.Fatalf("invalid parent piece: %+v", piece)
		}
		blocks = append(blocks, piece.Blocks...)
		bodies = append(bodies, piece.BodyMarkdown)
	}
	if !reflect.DeepEqual(blocks, section.Blocks) || strings.Join(bodies, "\n\n") != section.BodyMarkdown ||
		pieces[0].PageStart != 1 || pieces[0].PageEnd != 1 || pieces[1].PageStart != 2 || pieces[1].PageEnd != 2 {
		t.Fatalf("parent split lost content or location: %+v", pieces)
	}
	maxTokens := max(pieces[0].TokenCount, pieces[1].TokenCount)
	t.Logf("unsplit=%d limit=%d pieces=%d max=%d blocks=%d/%d", utf8.RuneCountInString(section.BodyMarkdown), request.MaxTokens, len(pieces), maxTokens, len(blocks), len(section.Blocks))
	section.HeadingPath[0] = "修改"
	*section.Blocks[0].Text = "修改"
	if pieces[0].HeadingPath[0] != "差旅" || *pieces[0].Blocks[0].Text != "申请需要事先提交。" {
		t.Fatal("parent pieces alias mutable input")
	}
}

func TestSplitParentSectionCopiesTableCellsAndCoordinates(t *testing.T) {
	document := parsedPages(parser.Page{Number: 1, Blocks: []parser.Block{
		heading("表格", 1), {Type: "table", Cells: [][]string{{"城市", "上限"}, {"北京", "600"}}, BBox: []float64{1, 2, 3, 4}},
	}})
	normalized, err := AssembleMarkdown(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	section := normalized.Sections[0]
	pieces, err := SplitParentSection(context.Background(), ParentSplitRequest{Section: section, MaxTokens: 100, CountTokens: countRunes})
	if err != nil || len(pieces) != 1 || len(pieces[0].Blocks) != 1 {
		t.Fatalf("split table parent: %+v err=%v", pieces, err)
	}
	section.Blocks[0].Cells[0][0] = "修改"
	section.Blocks[0].BBox[0] = 99
	if pieces[0].Blocks[0].Cells[0][0] != "城市" || pieces[0].Blocks[0].BBox[0] != 1 {
		t.Fatal("parent table metadata aliases input")
	}
}

func TestSplitParentSectionKeepsEmptyHeading(t *testing.T) {
	section := Section{ParentIndex: -1, Ordinal: 1, Level: 1, Heading: "标题", HeadingPath: []string{"标题"}, PageStart: 1, PageEnd: 3}
	called := false
	count := func(context.Context, string) (int, error) { called = true; return 1, nil }
	pieces, err := SplitParentSection(context.Background(), ParentSplitRequest{Section: section, MaxTokens: 10, CountTokens: count})
	if err != nil || len(pieces) != 1 || called || pieces[0].BodyMarkdown != "" || pieces[0].TokenCount != 0 || pieces[0].PageStart != 1 || pieces[0].PageEnd != 3 {
		t.Fatalf("heading-only section changed: %+v called=%t err=%v", pieces, called, err)
	}
}

func TestSplitParentSectionRejectsErrorsWithoutPartialResults(t *testing.T) {
	section := sampleParentSection(t)
	request := ParentSplitRequest{Section: section, MaxTokens: 9, CountTokens: countRunes}
	if pieces, err := SplitParentSection(context.Background(), request); !errors.Is(err, ErrParentUnitTooLarge) || pieces != nil {
		t.Fatalf("oversized block returned partial pieces: %+v err=%v", pieces, err)
	}
	failure := errors.New("counter unavailable")
	request.MaxTokens = 100
	request.CountTokens = func(context.Context, string) (int, error) { return 0, failure }
	if pieces, err := SplitParentSection(context.Background(), request); !errors.Is(err, failure) || pieces != nil {
		t.Fatalf("counter failure hidden: %+v err=%v", pieces, err)
	}
	request.CountTokens = func(context.Context, string) (int, error) { return 0, nil }
	if pieces, err := SplitParentSection(context.Background(), request); !errors.Is(err, ErrInvalidTokenCount) || pieces != nil {
		t.Fatalf("invalid count accepted: %+v err=%v", pieces, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request.CountTokens = func(context.Context, string) (int, error) { cancel(); return 1, nil }
	if pieces, err := SplitParentSection(ctx, request); !errors.Is(err, context.Canceled) || pieces != nil {
		t.Fatalf("cancellation ignored: %+v err=%v", pieces, err)
	}
}

func TestSplitParentSectionValidatesStructure(t *testing.T) {
	for name, change := range map[string]func(*ParentSplitRequest){
		"counter": func(r *ParentSplitRequest) { r.CountTokens = nil },
		"limit":   func(r *ParentSplitRequest) { r.MaxTokens = 0 },
		"ordinal": func(r *ParentSplitRequest) { r.Section.Ordinal = 0 },
		"page":    func(r *ParentSplitRequest) { r.Section.PageStart = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			request := ParentSplitRequest{Section: sampleParentSection(t), MaxTokens: 100, CountTokens: countRunes}
			change(&request)
			if pieces, err := SplitParentSection(context.Background(), request); !errors.Is(err, ErrInvalidChunkConfig) || pieces != nil {
				t.Fatalf("invalid configuration accepted: %+v err=%v", pieces, err)
			}
		})
	}
	for name, change := range map[string]func(*Section){
		"heading":    func(s *Section) { s.HeadingPath[0] = "其他" },
		"body":       func(s *Section) { s.BodyMarkdown += "多余内容" },
		"page order": func(s *Section) { s.Blocks[0].Page = 2 },
		"bbox":       func(s *Section) { s.Blocks[0].BBox = []float64{1} },
		"payload":    func(s *Section) { s.Blocks[0].Text = nil },
	} {
		t.Run(name, func(t *testing.T) {
			section := sampleParentSection(t)
			change(&section)
			if pieces, err := SplitParentSection(context.Background(), ParentSplitRequest{Section: section, MaxTokens: 100, CountTokens: countRunes}); !errors.Is(err, ErrInvalidParsedDocument) || pieces != nil {
				t.Fatalf("invalid section accepted: %+v err=%v", pieces, err)
			}
		})
	}
	section := sampleParentSection(t)
	section.Blocks[0].Kind = "figure"
	if pieces, err := SplitParentSection(context.Background(), ParentSplitRequest{Section: section, MaxTokens: 100, CountTokens: countRunes}); !errors.Is(err, ErrUnsupportedParsedDocument) || pieces != nil {
		t.Fatalf("unsupported parent block accepted: %+v err=%v", pieces, err)
	}
}

func TestSplitParentSectionLimitsBlockGroups(t *testing.T) {
	blocks := make([]parser.Block, 1025)
	for index := range blocks {
		blocks[index] = paragraph("正文")
	}
	normalized, err := AssembleMarkdown(context.Background(), parsedPages(parser.Page{Number: 1, Blocks: blocks}))
	if err != nil {
		t.Fatal(err)
	}
	count := func(context.Context, string) (int, error) { return 1, nil }
	pieces, err := SplitParentSection(context.Background(), ParentSplitRequest{Section: normalized.Sections[0], MaxTokens: 10, CountTokens: count})
	if err != nil || len(pieces) != 3 || len(pieces[0].Blocks) != 512 || len(pieces[1].Blocks) != 512 || len(pieces[2].Blocks) != 1 {
		t.Fatalf("block groups not bounded: pieces=%d err=%v", len(pieces), err)
	}
}

func FuzzSplitParentSectionPreservesBlocks(f *testing.F) {
	f.Add([]byte("差旅报销。\n下一段<&"), uint8(40))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, uint8(80))
	f.Fuzz(func(t *testing.T, data []byte, limit uint8) {
		if len(data) > 256 {
			return
		}
		alphabet := []string{"甲", "乙", "A", "。", "！", "\n", "\\", "&", "<", " ", "\t", "😀"}
		blocks := []parser.Block{heading("标题", 1)}
		for offset := 0; offset < len(data); offset += 8 {
			var value strings.Builder
			value.WriteString("项")
			for index := offset; index < min(offset+8, len(data)); index++ {
				value.WriteString(alphabet[int(data[index])%len(alphabet)])
			}
			blocks = append(blocks, paragraph(value.String()))
		}
		if len(blocks) == 1 {
			blocks = append(blocks, paragraph("空项"))
		}
		split := 1 + (len(blocks)-1)/2
		normalized, err := AssembleMarkdown(context.Background(), parsedPages(
			parser.Page{Number: 1, Blocks: blocks[:split]}, parser.Page{Number: 2, Blocks: blocks[split:]},
		))
		if err != nil || len(normalized.Sections) != 1 {
			t.Fatalf("assemble generated section: %v", err)
		}
		section := normalized.Sections[0]
		request := ParentSplitRequest{Section: section, MaxTokens: int(limit%80) + 30, CountTokens: countRunes}
		pieces, err := SplitParentSection(context.Background(), request)
		if err != nil {
			if !errors.Is(err, ErrParentUnitTooLarge) || pieces != nil {
				t.Fatalf("unexpected parent failure: %v", err)
			}
			return
		}
		var bodies []string
		var got []NormalizedBlock
		for index, piece := range pieces {
			if piece.Sequence != index+1 || piece.SectionOrdinal != section.Ordinal || piece.TokenCount != utf8.RuneCountInString(piece.BodyMarkdown) ||
				piece.TokenCount > request.MaxTokens || len(piece.Blocks) == 0 || piece.PageEnd < piece.PageStart {
				t.Fatalf("invalid parent metadata: %+v", piece)
			}
			bodies = append(bodies, piece.BodyMarkdown)
			got = append(got, piece.Blocks...)
		}
		if strings.Join(bodies, "\n\n") != section.BodyMarkdown || !reflect.DeepEqual(got, section.Blocks) {
			t.Fatal("parent split lost or reordered blocks")
		}
	})
}
