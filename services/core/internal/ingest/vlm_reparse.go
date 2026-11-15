package ingest

import (
	"context"
	"fmt"
	"strings"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
)

// PageRenderer renders specific 1-based PDF page numbers to PNG byte slices.
type PageRenderer interface {
	RenderPages(ctx context.Context, pdfPath string, pages []int64, dpi int) (map[int64][]byte, error)
}

// PageExtractor extracts Markdown from a rendered page PNG.
type PageExtractor interface {
	ExtractPage(ctx context.Context, page int, pngBytes []byte) (string, *models.ChatUsage, error)
}

type VLMReparseResult struct {
	Document          parser.Document
	VLMPages          []int64
	VLMTokens         int
	InjectionFindings []InjectionFinding
}

// LowScorePages returns sorted 1-based page numbers flagged by the PDF quality gate.
func LowScorePages(quality QualityReport, maxPages int) []int64 {
	if maxPages <= 0 {
		maxPages = 20
	}
	var pages []int64
	for _, p := range quality.Pages {
		if len(p.Reasons) > 0 && p.Page > 0 {
			pages = append(pages, p.Page)
			if len(pages) >= maxPages {
				break
			}
		}
	}
	return pages
}

// ReparseLowScorePages renders only low-score pages and replaces their blocks with VLM Markdown.
func ReparseLowScorePages(
	ctx context.Context,
	pdfPath string,
	doc parser.Document,
	quality QualityReport,
	renderer PageRenderer,
	vlm PageExtractor,
	maxPages int,
) (VLMReparseResult, error) {
	if renderer == nil || vlm == nil {
		return VLMReparseResult{Document: doc}, nil
	}
	targets := LowScorePages(quality, maxPages)
	if len(targets) == 0 {
		return VLMReparseResult{Document: doc}, nil
	}
	images, err := renderer.RenderPages(ctx, pdfPath, targets, 150)
	if err != nil {
		return VLMReparseResult{}, fmt.Errorf("render low-score pages: %w", err)
	}

	targetSet := make(map[int64]struct{}, len(targets))
	for _, p := range targets {
		targetSet[p] = struct{}{}
	}

	updatedPages := make([]parser.Page, len(doc.Pages))
	copy(updatedPages, doc.Pages)

	var vlmPages []int64
	var findings []InjectionFinding
	totalTokens := 0
	for i := range updatedPages {
		pageNo := updatedPages[i].Number
		if _, ok := targetSet[pageNo]; !ok {
			continue
		}
		pngBytes := images[pageNo]
		if len(pngBytes) == 0 {
			continue
		}
		md, usage, err := vlm.ExtractPage(ctx, int(pageNo), pngBytes)
		if err != nil {
			return VLMReparseResult{}, fmt.Errorf("vlm extract page %d: %w", pageNo, err)
		}
		if usage != nil {
			totalTokens += usage.TotalTokens
		}
		if scan := ScanInjection(md, "vlm"); scan.Detected {
			findings = append(findings, scan.Findings...)
		}
		updatedPages[i].Blocks = markdownToBlocks(md)
		updatedPages[i].Signals.CharCount = int64(len([]rune(strings.TrimSpace(md))))
		vlmPages = append(vlmPages, pageNo)
	}

	doc.Pages = updatedPages
	return VLMReparseResult{
		Document:          doc,
		VLMPages:          vlmPages,
		VLMTokens:         totalTokens,
		InjectionFindings: findings,
	}, nil
}

func markdownToBlocks(md string) []parser.Block {
	var blocks []parser.Block
	for _, para := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n\n") {
		trimmed := strings.TrimSpace(para)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			heading := strings.TrimSpace(trimmed[level:])
			if level >= 1 && level <= 6 && heading != "" {
				lvl := level
				blocks = append(blocks, parser.Block{Type: "heading", Level: &lvl, Text: &heading})
				continue
			}
		}
		text := trimmed
		blocks = append(blocks, parser.Block{Type: "paragraph", Text: &text})
	}
	if len(blocks) == 0 {
		empty := ""
		blocks = append(blocks, parser.Block{Type: "paragraph", Text: &empty})
	}
	return blocks
}
