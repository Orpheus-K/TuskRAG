package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/parsereport"
)

type dirPageRenderer struct {
	dir string
}

func (r dirPageRenderer) RenderPages(_ context.Context, _ string, pages []int64, _ int) (map[int64][]byte, error) {
	out := make(map[int64][]byte, len(pages))
	for _, p := range pages {
		candidates := []string{
			filepath.Join(r.dir, fmt.Sprintf("page_%d.png", p)),
			filepath.Join(r.dir, fmt.Sprintf("page-%d.png", p)),
			filepath.Join(r.dir, fmt.Sprintf("%d.png", p)),
		}
		var data []byte
		var err error
		for _, path := range candidates {
			data, err = os.ReadFile(path)
			if err == nil && len(data) > 0 {
				break
			}
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("rendered PNG for page %d not found in %s", p, r.dir)
		}
		out[p] = data
	}
	return out, nil
}

type noopEvalVLMAuditor struct{}

func (noopEvalVLMAuditor) RecordModelDenied(context.Context, models.DeniedCall) error { return nil }

func TestRealVLMReparseAblation(t *testing.T) {
	cfgPath := os.Getenv("TUSKRAG_TEST_VLM_CONFIG")
	if cfgPath == "" {
		t.Skip("real VLM E5 ablation requires TUSKRAG_TEST_VLM_CONFIG")
	}
	pngDir := os.Getenv("TUSKRAG_TEST_SCANNED_PNG_DIR")
	if pngDir == "" {
		t.Skip("real VLM E5 ablation requires TUSKRAG_TEST_SCANNED_PNG_DIR")
	}

	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var vlmCfg struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(cfgBytes, &vlmCfg); err != nil || vlmCfg.BaseURL == "" || vlmCfg.Model == "" {
		t.Fatal("invalid TUSKRAG_TEST_VLM_CONFIG")
	}
	clear(cfgBytes)

	baseURL := strings.TrimSuffix(strings.TrimSpace(vlmCfg.BaseURL), "/v1")
	provider := models.Provider{
		ID:       1,
		BaseURL:  baseURL,
		APIKey:   vlmCfg.APIKey,
		Locality: "external",
	}
	allowScope := func(context.Context) (models.EgressScope, error) {
		return models.EgressScope{
			TenantID:            1,
			KnowledgeBaseID:     1,
			TenantPolicy:        models.Allow,
			KnowledgeBasePolicy: models.Allow,
		}, nil
	}
	vlmClient, err := models.NewVLMClient(provider, vlmCfg.Model, 90*time.Second, allowScope, noopEvalVLMAuditor{})
	if err != nil {
		t.Fatal(err)
	}
	defer vlmClient.Close()

	// Baseline: native PDF parser output on image-only scanned PDF (0 text blocks).
	baselineDoc := parser.Document{
		SchemaVersion: "1.0.0",
		Pages: []parser.Page{
			{Number: 1, Blocks: nil},
			{Number: 2, Blocks: nil},
		},
	}
	quality := QualityReport{
		NeedsReview: true,
		Pages: []PageQuality{
			{Page: 1, Reasons: []parsereport.Reason{parsereport.ImageHeavy}},
			{Page: 2, Reasons: []parsereport.Reason{parsereport.ImageHeavy}},
		},
	}

	expectedFacts := []string{
		"TuskRAG",
		"VLM-2026",
		"99.95%",
		"150",
		"PostgreSQL 17",
		"AES-256-GCM",
	}

	baselineText := collectDocText(baselineDoc)
	baselineHits := countFactHits(baselineText, expectedFacts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	started := time.Now()
	reparsed, err := ReparseLowScorePages(ctx, "scanned.pdf", baselineDoc, quality, dirPageRenderer{dir: pngDir}, vlmClient, 10)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("ReparseLowScorePages: %v", err)
	}

	vlmText := collectDocText(reparsed.Document)
	vlmHits := countFactHits(vlmText, expectedFacts)
	var vlmBlocks int
	for _, p := range reparsed.Document.Pages {
		vlmBlocks += len(p.Blocks)
	}

	t.Logf("E5_BASELINE: model=%s chars=%d blocks=0 fact_hits=%d/%d",
		vlmCfg.Model, len(baselineText), baselineHits, len(expectedFacts))
	t.Logf("E5_VLM_REPARSE: model=%s vlm_pages=%v vlm_tokens=%d elapsed_ms=%d chars=%d blocks=%d fact_hits=%d/%d",
		vlmCfg.Model, reparsed.VLMPages, reparsed.VLMTokens, elapsed.Milliseconds(), len(vlmText), vlmBlocks, vlmHits, len(expectedFacts))
	t.Logf("E5_VLM_EXTRACTED_TEXT:\n%s", vlmText)

	if len(reparsed.VLMPages) != 2 {
		t.Fatalf("expected 2 reparsed pages, got %v", reparsed.VLMPages)
	}
	if vlmHits < 5 {
		t.Fatalf("expected at least 5/%d fact hits after VLM reparse, got %d; text=%q", len(expectedFacts), vlmHits, vlmText)
	}
}

func collectDocText(doc parser.Document) string {
	var b strings.Builder
	for _, p := range doc.Pages {
		for _, blk := range p.Blocks {
			if blk.Text != nil && *blk.Text != "" {
				b.WriteString(*blk.Text)
				b.WriteByte('\n')
			}
			for _, row := range blk.Cells {
				b.WriteString(strings.Join(row, " | "))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func countFactHits(text string, facts []string) int {
	lower := strings.ToLower(text)
	hits := 0
	for _, f := range facts {
		if strings.Contains(lower, strings.ToLower(f)) {
			hits++
		}
	}
	return hits
}
