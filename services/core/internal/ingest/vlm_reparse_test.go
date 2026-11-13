package ingest

import (
	"context"
	"testing"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/parsereport"
)

type stubRenderer struct {
	rendered []int64
}

func (s *stubRenderer) RenderPages(_ context.Context, _ string, pages []int64, _ int) (map[int64][]byte, error) {
	s.rendered = append(s.rendered, pages...)
	out := make(map[int64][]byte, len(pages))
	for _, p := range pages {
		out[p] = []byte("png-page")
	}
	return out, nil
}

type stubVLM struct {
	called []int
}

func (s *stubVLM) ExtractPage(_ context.Context, page int, _ []byte) (string, *models.ChatUsage, error) {
	s.called = append(s.called, page)
	return "# OCR Heading\n\nRecovered text on page.", &models.ChatUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, nil
}

func TestReparseLowScorePagesOnlyTargetsFlaggedPages(t *testing.T) {
	p1 := "Native digital page"
	p2 := "garbled"
	doc := parser.Document{
		SchemaVersion: "1.0.0",
		Pages: []parser.Page{
			{Number: 1, Blocks: []parser.Block{{Type: "paragraph", Text: &p1}}, Signals: parser.Signals{CharCount: 19}},
			{Number: 2, Blocks: []parser.Block{{Type: "paragraph", Text: &p2}}, Signals: parser.Signals{CharCount: 7}},
		},
	}
	quality := QualityReport{
		NeedsReview: true,
		Pages: []PageQuality{
			{Page: 1},
			{Page: 2, Reasons: []parsereport.Reason{parsereport.ImageHeavy}},
		},
	}

	renderer := &stubRenderer{}
	vlm := &stubVLM{}
	res, err := ReparseLowScorePages(context.Background(), "/tmp/test.pdf", doc, quality, renderer, vlm, 10)
	if err != nil {
		t.Fatalf("ReparseLowScorePages failed: %v", err)
	}
	if len(renderer.rendered) != 1 || renderer.rendered[0] != 2 {
		t.Fatalf("expected only page 2 rendered, got %v", renderer.rendered)
	}
	if len(vlm.called) != 1 || vlm.called[0] != 2 {
		t.Fatalf("expected only page 2 VLM-extracted, got %v", vlm.called)
	}
	if len(res.VLMPages) != 1 || res.VLMPages[0] != 2 || res.VLMTokens != 120 {
		t.Fatalf("unexpected VLM stats: %+v", res)
	}
	if *res.Document.Pages[0].Blocks[0].Text != "Native digital page" {
		t.Fatalf("page 1 should remain untouched")
	}
	if *res.Document.Pages[1].Blocks[0].Text != "OCR Heading" || *res.Document.Pages[1].Blocks[1].Text != "Recovered text on page." {
		t.Fatalf("unexpected page 2 VLM blocks: %+v", res.Document.Pages[1].Blocks)
	}
}
