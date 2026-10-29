package ingest

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func countRunes(ctx context.Context, input string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return utf8.RuneCountInString(input), nil
}

func sampleTable() TableSplitRequest {
	return TableSplitRequest{
		Block: NormalizedBlock{Kind: "table", Page: 2, Cells: [][]string{
			{"城市", "上限"}, {"北京", "600"}, {"上海", "450"}, {"成都", "350"}, {"深圳", "700"},
		}},
		HeadingPath: []string{"差旅制度", "住宿标准"},
		MIME:        "text/csv", HeaderRow: 1, MaxTokens: 100, CountTokens: countRunes,
	}
}

func TestSplitTableRejectsExcessiveOutputWithoutPartialChunks(t *testing.T) {
	rows := make([][]string, maxPlanChunks+2)
	rows[0] = []string{"header"}
	for index := 1; index < len(rows); index++ {
		rows[index] = []string{"x"}
	}
	count := func(ctx context.Context, value string) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if strings.Count(value, "| x |") > 1 {
			return 2, nil
		}
		return 1, nil
	}
	request := TableSplitRequest{Block: NormalizedBlock{Kind: "table", Page: 1, Cells: rows},
		MIME: "text/markdown", HeaderRow: 1, MaxTokens: 1, CountTokens: count}
	request.Block.Cells = rows[:len(rows)-1]
	allowed, err := SplitTable(context.Background(), request)
	if err != nil || len(allowed) != maxPlanChunks {
		t.Fatalf("table at output limit failed: count=%d err=%v", len(allowed), err)
	}
	request.Block.Cells = rows
	chunks, err := SplitTable(context.Background(), request)
	if !errors.Is(err, ErrNormalizedLimit) || chunks != nil {
		t.Fatalf("excessive table output returned partial chunks: count=%d err=%v", len(chunks), err)
	}
}

func TestSplitTableBoundsCounterInputs(t *testing.T) {
	first, second := strings.Repeat("a", 40000), strings.Repeat("b", 40000)
	count := func(ctx context.Context, value string) (int, error) {
		if len(value) > maxCountInputBytes {
			return 0, errors.New("counter received too many bytes")
		}
		return 1, ctx.Err()
	}
	request := TableSplitRequest{Block: NormalizedBlock{Kind: "table", Page: 1, Cells: [][]string{{"header"}, {first}, {second}}},
		MIME: "text/markdown", HeaderRow: 1, MaxTokens: 800, CountTokens: count}
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 2 || chunks[0].Cells[1][0] != first || chunks[1].Cells[1][0] != second {
		t.Fatalf("table byte boundary lost rows: chunks=%d err=%v", len(chunks), err)
	}
}

func TestSplitTableGolden(t *testing.T) {
	request := sampleTable()
	baseline := request
	baseline.MaxTokens = 800
	whole, err := SplitTable(context.Background(), baseline)
	if err != nil || len(whole) != 1 {
		t.Fatalf("whole table: %+v err=%v", whole, err)
	}
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("split table: %+v err=%v", chunks, err)
	}
	var rows [][]string
	wantStart := 2
	maxCount := 0
	for _, chunk := range chunks {
		if chunk.Page != 2 || chunk.FirstRow != wantStart || chunk.LastRow < chunk.FirstRow ||
			!reflect.DeepEqual(chunk.HeadingPath, request.HeadingPath) || !reflect.DeepEqual(chunk.Cells[0], request.Block.Cells[0]) {
			t.Fatalf("lost table location or header: %+v", chunk)
		}
		if chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || chunk.TokenCount > request.MaxTokens ||
			!strings.HasPrefix(chunk.SearchText, "差旅制度 › 住宿标准\n"+chunk.Markdown) {
			t.Fatalf("search text or token count is incorrect: %+v", chunk)
		}
		for _, row := range chunk.Cells[1:] {
			if !strings.Contains(chunk.SearchText, "城市: "+row[0]+"\n上限: "+row[1]) {
				t.Fatalf("column expansion missing: %+v", chunk)
			}
			rows = append(rows, row)
		}
		wantStart = chunk.LastRow + 1
		maxCount = max(maxCount, chunk.TokenCount)
	}
	if !reflect.DeepEqual(rows, request.Block.Cells[1:]) || wantStart != len(request.Block.Cells)+1 {
		t.Fatalf("data rows changed: %+v", rows)
	}
	t.Logf("unsplit_tokens=%d limit=%d chunks=%d max_chunk_tokens=%d preserved_rows=%d/%d", whole[0].TokenCount, request.MaxTokens, len(chunks), maxCount, len(rows), len(request.Block.Cells)-1)
	request.Block.Cells[0][0] = "修改"
	request.HeadingPath[0] = "修改"
	if chunks[0].Cells[0][0] != "城市" || chunks[0].HeadingPath[0] != "差旅制度" {
		t.Fatal("chunk aliases mutable input")
	}
}

func TestSplitTablePreservesWholeTableAndMIMEBehavior(t *testing.T) {
	request := sampleTable()
	request.MaxTokens = 800
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 1 || !reflect.DeepEqual(chunks[0].Cells, request.Block.Cells) {
		t.Fatalf("small table should remain whole: %+v err=%v", chunks, err)
	}
	if !strings.Contains(chunks[0].SearchText, "城市: 北京") || strings.Contains(chunks[0].Markdown, "城市: 北京") {
		t.Fatalf("spreadsheet expansion must only affect search input: %+v", chunks[0])
	}
	request.MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	chunks, err = SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 1 || chunks[0].SearchText != "差旅制度 › 住宿标准\n"+chunks[0].Markdown {
		t.Fatalf("DOCX table must not add spreadsheet expansion: %+v err=%v", chunks, err)
	}
}

func TestSplitTableDoesNotInventBlankColumnNames(t *testing.T) {
	request := sampleTable()
	request.MaxTokens = 800
	request.Block.Cells = [][]string{{"", "上限"}, {"北京", "600"}}
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 1 || !strings.Contains(chunks[0].SearchText, "\n北京\n上限: 600") || strings.Contains(chunks[0].SearchText, ": 北京") {
		t.Fatalf("blank column name was fabricated: %+v err=%v", chunks, err)
	}
}

func TestSplitTableChecksActualCandidateWithoutMonotonicAssumption(t *testing.T) {
	request := sampleTable()
	request.Block.Cells = request.Block.Cells[:3]
	request.MaxTokens = 50
	request.CountTokens = func(_ context.Context, text string) (int, error) {
		if strings.Contains(text, "北京") && strings.Contains(text, "上海") {
			return 51, nil
		}
		return 30, nil
	}
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 2 || chunks[0].TokenCount != 30 || chunks[1].TokenCount != 30 {
		t.Fatalf("tokenizer result was not respected: %+v err=%v", chunks, err)
	}
}

func TestSplitTableMarkdownPreservesCells(t *testing.T) {
	request := sampleTable()
	request.MIME = "text/markdown"
	request.MaxTokens = 800
	request.Block.Cells = [][]string{{" 列|名 ", "值"}, {"A|B", "第一行\n第二行"}, {"反斜线\\", "&amp;"}}
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("table with escaped cells: %+v err=%v", chunks, err)
	}
	document := parseTextFixture(t, []byte(chunks[0].Markdown), "text/markdown")
	if len(document.Pages[0].Blocks) != 1 || !reflect.DeepEqual(document.Pages[0].Blocks[0].Cells, request.Block.Cells) {
		t.Fatalf("table cells changed after Markdown round trip: %+v", document.Pages[0].Blocks)
	}
}

func TestSplitTableGeneratedRowCoverage(t *testing.T) {
	random := rand.New(rand.NewSource(20261028))
	for attempt := 0; attempt < 100; attempt++ {
		request := sampleTable()
		request.Block.Cells = [][]string{{"项目", "内容"}}
		for row := 0; row < random.Intn(30)+1; row++ {
			request.Block.Cells = append(request.Block.Cells, []string{strings.Repeat("甲", random.Intn(8)+1), strings.Repeat("乙", random.Intn(8)+1)})
		}
		request.MaxTokens = random.Intn(111) + 110
		chunks, err := SplitTable(context.Background(), request)
		if err != nil {
			t.Fatalf("generated table %d: %v", attempt, err)
		}
		var rows [][]string
		for _, chunk := range chunks {
			if chunk.TokenCount > request.MaxTokens || chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) ||
				!reflect.DeepEqual(chunk.Cells[0], request.Block.Cells[0]) {
				t.Fatalf("generated chunk violated limits or header: %+v", chunk)
			}
			rows = append(rows, chunk.Cells[1:]...)
		}
		if !reflect.DeepEqual(rows, request.Block.Cells[1:]) {
			t.Fatalf("generated table %d lost or reordered rows", attempt)
		}
	}
}

func TestSplitTableHeaderOnlyAndBoundedRowGroups(t *testing.T) {
	request := sampleTable()
	request.Block.Cells = request.Block.Cells[:1]
	request.MaxTokens = 800
	chunks, err := SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 1 || chunks[0].FirstRow != 1 || chunks[0].LastRow != 1 || len(chunks[0].Cells) != 1 {
		t.Fatalf("header-only table: %+v err=%v", chunks, err)
	}
	request.Block.Cells = [][]string{{"列"}}
	for index := 0; index < 1025; index++ {
		request.Block.Cells = append(request.Block.Cells, []string{"值"})
	}
	request.CountTokens = func(context.Context, string) (int, error) { return 1, nil }
	chunks, err = SplitTable(context.Background(), request)
	if err != nil || len(chunks) != 3 || chunks[0].FirstRow != 2 || chunks[0].LastRow != 513 ||
		chunks[1].FirstRow != 514 || chunks[1].LastRow != 1025 || chunks[2].FirstRow != 1026 || chunks[2].LastRow != 1026 {
		t.Fatalf("row group bound or positions: %+v err=%v", chunks, err)
	}
}

func TestSplitTableRejectsErrorsWithoutPartialResults(t *testing.T) {
	for name, change := range map[string]func(*TableSplitRequest){
		"nil counter":     func(request *TableSplitRequest) { request.CountTokens = nil },
		"zero limit":      func(request *TableSplitRequest) { request.MaxTokens = 0 },
		"excessive limit": func(request *TableSplitRequest) { request.MaxTokens = 801 },
		"wrong block":     func(request *TableSplitRequest) { request.Block.Kind = "paragraph" },
		"invalid page":    func(request *TableSplitRequest) { request.Block.Page = 0 },
		"missing header":  func(request *TableSplitRequest) { request.HeaderRow = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			request := sampleTable()
			change(&request)
			if chunks, err := SplitTable(context.Background(), request); !errors.Is(err, ErrInvalidChunkConfig) || chunks != nil {
				t.Fatalf("invalid configuration accepted: %+v err=%v", chunks, err)
			}
		})
	}
	request := sampleTable()
	request.MIME = "application/unknown"
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, ErrUnsupportedChunkMIME) {
		t.Fatalf("unknown MIME accepted: %v", err)
	}
	request = sampleTable()
	request.HeaderRow = 2
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, ErrUnsupportedParsedDocument) {
		t.Fatalf("unsupported header position accepted: %v", err)
	}
	request = sampleTable()
	request.Block.Cells[0] = []string{"", " "}
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, ErrTableHeaderMissing) {
		t.Fatalf("empty header accepted: %v", err)
	}
	request = sampleTable()
	request.Block.Cells[2] = []string{"不足"}
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, ErrUnsupportedParsedDocument) {
		t.Fatalf("ragged rows accepted: %v", err)
	}
	request = sampleTable()
	request.CountTokens = func(context.Context, string) (int, error) { return 0, nil }
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, ErrInvalidTokenCount) {
		t.Fatalf("invalid token count accepted: %v", err)
	}
	failure := errors.New("counter unavailable")
	request.CountTokens = func(context.Context, string) (int, error) { return 0, failure }
	if _, err := SplitTable(context.Background(), request); !errors.Is(err, failure) {
		t.Fatalf("counter error was lost: %v", err)
	}
	request = sampleTable()
	request.Block.Cells[2][1] = strings.Repeat("大", 200)
	if chunks, err := SplitTable(context.Background(), request); !errors.Is(err, ErrTableUnitTooLarge) || chunks != nil {
		t.Fatalf("oversized row returned partial chunks: %+v err=%v", chunks, err)
	}
	request = sampleTable()
	request.Block.Cells = [][]string{{"列"}, {"小"}, {strings.Repeat("值", 1000000)}}
	request.CountTokens = func(context.Context, string) (int, error) { return 1, nil }
	if chunks, err := SplitTable(context.Background(), request); !errors.Is(err, ErrTableUnitTooLarge) || chunks != nil {
		t.Fatalf("byte limit returned %d partial chunks, err=%v", len(chunks), err)
	}
	request = sampleTable()
	ctx, cancel := context.WithCancel(context.Background())
	request.CountTokens = func(context.Context, string) (int, error) { cancel(); return 1, nil }
	if chunks, err := SplitTable(ctx, request); !errors.Is(err, context.Canceled) || chunks != nil {
		t.Fatalf("cancellation was ignored: %+v err=%v", chunks, err)
	}
}

func FuzzSplitTableRows(f *testing.F) {
	f.Add([]byte("表格 | 数据\n"), uint16(100))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6}, uint16(180))
	f.Fuzz(func(t *testing.T, data []byte, limit uint16) {
		if len(data) > 512 {
			return
		}
		alphabet := []string{"甲", "乙", "A", "|", "\n", "\\", "&", "<", " ", "\t", "😀"}
		rows := [][]string{{"名称", "值"}}
		for offset := 0; offset < len(data); offset += 12 {
			var value strings.Builder
			for index := offset; index < min(offset+12, len(data)); index++ {
				value.WriteString(alphabet[int(data[index])%len(alphabet)])
			}
			rows = append(rows, []string{"条目", value.String()})
		}
		if len(rows) == 1 {
			rows = append(rows, []string{"条目", "空"})
		}
		request := TableSplitRequest{Block: NormalizedBlock{Kind: "table", Page: 1, Cells: rows},
			MIME: "text/csv", HeaderRow: 1, MaxTokens: int(limit%180) + 80, CountTokens: countRunes}
		chunks, err := SplitTable(context.Background(), request)
		if err != nil {
			if !errors.Is(err, ErrTableUnitTooLarge) || chunks != nil {
				t.Fatalf("unexpected split error: %v", err)
			}
			return
		}
		var got [][]string
		next := 2
		for _, chunk := range chunks {
			if chunk.FirstRow != next || chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || chunk.TokenCount > request.MaxTokens ||
				!reflect.DeepEqual(chunk.Cells[0], rows[0]) {
				t.Fatalf("invalid chunk: %+v", chunk)
			}
			got = append(got, chunk.Cells[1:]...)
			next = chunk.LastRow + 1
		}
		if !reflect.DeepEqual(got, rows[1:]) || next != len(rows)+1 {
			t.Fatal("split table lost or reordered rows")
		}
	})
}
