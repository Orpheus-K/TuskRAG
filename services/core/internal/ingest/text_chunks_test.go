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
)

func textParagraph(value string, page int64) NormalizedBlock {
	return NormalizedBlock{Kind: "paragraph", Page: page, Text: &value}
}

func sampleText() TextSplitRequest {
	return TextSplitRequest{
		Paragraphs: []NormalizedBlock{
			textParagraph("差旅申请需事先提交。主管应核验出差目的。票据应真实完整。", 1),
			textParagraph("住宿费用按城市标准执行。超出部分应提前审批。", 2),
		},
		HeadingPath: []string{"差旅制度", "审批流程"},
		TargetMin:   20, TargetMax: 32, HardMax: 45, OverlapMax: 12, CountTokens: countRunes,
	}
}

func TestSplitTextGolden(t *testing.T) {
	request := sampleText()
	chunks, err := SplitText(context.Background(), request)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("split text: %+v err=%v", chunks, err)
	}
	rebuilt := make([]strings.Builder, len(request.Paragraphs))
	var previous string
	var overlaps, novel, maxCount int
	for chunkIndex, chunk := range chunks {
		if chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || chunk.TokenCount > request.HardMax ||
			chunk.SearchText != "差旅制度 › 审批流程\n"+chunk.Text || !reflect.DeepEqual(chunk.HeadingPath, request.HeadingPath) ||
			len(chunk.Fragments) == 0 || chunk.PageStart != chunk.Fragments[0].Page || chunk.PageEnd != chunk.Fragments[len(chunk.Fragments)-1].Page {
			t.Fatalf("chunk metadata or token count is incorrect: %+v", chunk)
		}
		for index, fragment := range chunk.Fragments {
			if fragment.Overlap {
				if chunkIndex == 0 || index != 0 || fragment.Text != previous || chunk.OverlapTokens <= 0 || chunk.OverlapTokens > request.OverlapMax {
					t.Fatalf("invalid overlap: %+v", chunk)
				}
				overlaps++
				continue
			}
			rebuilt[fragment.ParagraphIndex].WriteString(fragment.Text)
			previous = fragment.Text
			novel++
		}
		maxCount = max(maxCount, chunk.TokenCount)
	}
	for index, paragraph := range request.Paragraphs {
		if rebuilt[index].String() != *paragraph.Text {
			t.Fatalf("paragraph %d changed: %q", index, rebuilt[index].String())
		}
	}
	if overlaps == 0 {
		t.Fatal("expected one-sentence overlap")
	}
	baseline := "差旅制度 › 审批流程\n" + *request.Paragraphs[0].Text + "\n\n" + *request.Paragraphs[1].Text
	t.Logf("unsplit_tokens=%d hard_limit=%d chunks=%d max_chunk_tokens=%d preserved_sentences=%d overlaps=%d", utf8.RuneCountInString(baseline), request.HardMax, len(chunks), maxCount, novel, overlaps)
}

func TestSplitTextSentenceBoundaries(t *testing.T) {
	value := "金额 1.2 元。版本 v1.2 稳定。He said \"OK.\" Next!"
	validation := assembler{ctx: context.Background()}
	atoms, err := collectSentences(context.Background(), []NormalizedBlock{textParagraph(value, 1)}, &validation)
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, atom := range atoms {
		actual = append(actual, atom.text)
	}
	want := []string{"金额 1.2 元。", "版本 v1.2 稳定。", "He said \"OK.\" ", "Next!"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("unexpected sentence boundaries: %#v", actual)
	}
}

func TestSplitTextDropsOverlapWhenItWouldExceedHardLimit(t *testing.T) {
	request := TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph("12345。67890。", 1)},
		TargetMin: 1, TargetMax: 6, HardMax: 8, OverlapMax: 6, CountTokens: countRunes}
	chunks, err := SplitText(context.Background(), request)
	if err != nil || len(chunks) != 2 || chunks[1].OverlapTokens != 0 || chunks[1].Fragments[0].Overlap || chunks[1].Text != "67890。" {
		t.Fatalf("overlap should not force a false failure: %+v err=%v", chunks, err)
	}
}

func TestSplitTextOverlapRetainsCrossPageLocation(t *testing.T) {
	request := TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph("甲乙丙。", 1), textParagraph("丁戊己。", 2)},
		TargetMin: 1, TargetMax: 5, HardMax: 20, OverlapMax: 5, CountTokens: countRunes}
	chunks, err := SplitText(context.Background(), request)
	if err != nil || len(chunks) != 2 || chunks[1].PageStart != 1 || chunks[1].PageEnd != 2 ||
		len(chunks[1].Fragments) != 2 || !chunks[1].Fragments[0].Overlap || chunks[1].OverlapTokens != 4 {
		t.Fatalf("cross-page overlap lost location: %+v err=%v", chunks, err)
	}
}

func TestSplitTextEscapesDisplayAndPreservesSource(t *testing.T) {
	request := TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph("输入 <script>alert(1)</script> & 原文。", 3)},
		TargetMin: 1, TargetMax: 100, HardMax: 200, CountTokens: countRunes}
	request.Paragraphs[0].BBox = []float64{1, 2, 3, 4}
	chunks, err := SplitText(context.Background(), request)
	if err != nil || len(chunks) != 1 || chunks[0].Text != *request.Paragraphs[0].Text {
		t.Fatalf("split source text: %+v err=%v", chunks, err)
	}
	var html bytes.Buffer
	if err := goldmark.Convert([]byte(chunks[0].Markdown), &html); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html.String(), "<script>") || !strings.Contains(html.String(), "&lt;script&gt;") {
		t.Fatalf("display interpreted untrusted HTML: %q", html.String())
	}
	request.Paragraphs[0].BBox[0] = 99
	if chunks[0].Fragments[0].BBox[0] != 1 {
		t.Fatal("fragment aliases mutable coordinates")
	}
}

func TestSplitTextBoundsAndFailures(t *testing.T) {
	for name, change := range map[string]func(*TextSplitRequest){
		"nil counter":  func(request *TextSplitRequest) { request.CountTokens = nil },
		"target order": func(request *TextSplitRequest) { request.TargetMin = request.TargetMax + 1 },
		"hard limit":   func(request *TextSplitRequest) { request.HardMax = 801 },
		"overlap":      func(request *TextSplitRequest) { request.OverlapMax = 61 },
		"overlap hard": func(request *TextSplitRequest) { request.HardMax = 32; request.OverlapMax = 33 },
	} {
		t.Run(name, func(t *testing.T) {
			request := sampleText()
			change(&request)
			if chunks, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidChunkConfig) || chunks != nil {
				t.Fatalf("invalid configuration accepted: %+v err=%v", chunks, err)
			}
		})
	}
	request := sampleText()
	request.Paragraphs[1].Page = 0
	if _, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("invalid page accepted: %v", err)
	}
	request = sampleText()
	request.Paragraphs[0].Kind = "table"
	if _, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("non-paragraph block accepted: %v", err)
	}
	request = sampleText()
	request.Paragraphs[0].Page, request.Paragraphs[1].Page = 2, 1
	if _, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("out-of-order pages accepted: %v", err)
	}
	request = sampleText()
	request.Paragraphs[0] = textParagraph("内容\x00隐藏", 1)
	if _, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("NUL text accepted: %v", err)
	}
	request = sampleText()
	request.CountTokens = func(context.Context, string) (int, error) { return 0, nil }
	if _, err := SplitText(context.Background(), request); !errors.Is(err, ErrInvalidTokenCount) {
		t.Fatalf("invalid token count accepted: %v", err)
	}
	failure := errors.New("counter unavailable")
	request.CountTokens = func(context.Context, string) (int, error) { return 0, failure }
	if _, err := SplitText(context.Background(), request); !errors.Is(err, failure) {
		t.Fatalf("counter failure was lost: %v", err)
	}
	request = TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph("短句。"+strings.Repeat("长", 100), 1)},
		TargetMin: 1, TargetMax: 2, HardMax: 40, CountTokens: countRunes}
	if chunks, err := SplitText(context.Background(), request); !errors.Is(err, ErrTextUnitTooLarge) || chunks != nil {
		t.Fatalf("oversized sentence returned partial chunks: %+v err=%v", chunks, err)
	}
	request = sampleText()
	ctx, cancel := context.WithCancel(context.Background())
	request.CountTokens = func(context.Context, string) (int, error) { cancel(); return 1, nil }
	if chunks, err := SplitText(ctx, request); !errors.Is(err, context.Canceled) || chunks != nil {
		t.Fatalf("cancellation was ignored: %+v err=%v", chunks, err)
	}
}

func TestSplitTextSentenceGroupBound(t *testing.T) {
	request := TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph(strings.Repeat("甲。", 1025), 1)},
		TargetMin: 2, TargetMax: 3, HardMax: 4, CountTokens: func(context.Context, string) (int, error) { return 1, nil }}
	chunks, err := SplitText(context.Background(), request)
	if err != nil || len(chunks) != 3 || len(chunks[0].Fragments) != 512 || len(chunks[1].Fragments) != 512 || len(chunks[2].Fragments) != 1 {
		t.Fatalf("sentence group limit failed: count=%d err=%v", len(chunks), err)
	}
}

func FuzzSplitTextFragments(f *testing.F) {
	f.Add([]byte("第一个句子。第二句！\n下一段"))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			return
		}
		alphabet := []string{"甲", "乙", "A", "。", "！", "?", ".", " ", "\n", "\t", "<", "&", "😀"}
		var first, second strings.Builder
		first.WriteString("首")
		second.WriteString("次")
		for index, value := range data {
			if index < len(data)/2 {
				first.WriteString(alphabet[int(value)%len(alphabet)])
			} else {
				second.WriteString(alphabet[int(value)%len(alphabet)])
			}
		}
		request := TextSplitRequest{Paragraphs: []NormalizedBlock{textParagraph(first.String(), 1), textParagraph(second.String(), 2)},
			HeadingPath: []string{"章"}, TargetMin: 20, TargetMax: 40, HardMax: 80, OverlapMax: 10, CountTokens: countRunes}
		chunks, err := SplitText(context.Background(), request)
		if err != nil {
			if !errors.Is(err, ErrTextUnitTooLarge) || chunks != nil {
				t.Fatalf("unexpected split failure: %v", err)
			}
			return
		}
		var rebuilt [2]strings.Builder
		var previous string
		for _, chunk := range chunks {
			if chunk.TokenCount != utf8.RuneCountInString(chunk.SearchText) || chunk.TokenCount > request.HardMax || chunk.SearchText != "章\n"+chunk.Text {
				t.Fatalf("invalid text chunk: %+v", chunk)
			}
			for index, fragment := range chunk.Fragments {
				if fragment.Overlap {
					if index != 0 || fragment.Text != previous || chunk.OverlapTokens > request.OverlapMax {
						t.Fatalf("invalid overlap: %+v", chunk)
					}
					continue
				}
				if fragment.ParagraphIndex < 0 || fragment.ParagraphIndex >= len(rebuilt) {
					t.Fatalf("invalid paragraph index: %+v", fragment)
				}
				rebuilt[fragment.ParagraphIndex].WriteString(fragment.Text)
				previous = fragment.Text
			}
		}
		if rebuilt[0].String() != first.String() || rebuilt[1].String() != second.String() {
			t.Fatal("text chunks lost source content")
		}
	})
}
