package ingest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"tuskrag.local/core/internal/parser"
)

func TestAssessQualityUsesPageSignalsAndSourceText(t *testing.T) {
	cleanBoundary := strings.Repeat("a", 19) + "�"
	garbledCells := strings.Repeat("b", 18) + "\ue000\ue001"
	cidText := "(cid:123)ok"
	garbled, missing, image := 0.051, 0.11, 0.71
	exactGarbled, exactMissing, exactImage := 0.05, 0.10, 0.70
	suspect := true
	document := parser.Document{Pages: []parser.Page{
		{Number: 1, Blocks: []parser.Block{{Type: "paragraph", Text: &cleanBoundary}}},
		{Number: 2, Blocks: []parser.Block{{Type: "table", Cells: [][]string{{garbledCells}}}}},
		{Number: 3, Blocks: []parser.Block{{Type: "paragraph", Text: &cidText}}},
		{Number: 4, Signals: parser.Signals{CharCount: 49, GarbledRatio: &garbled, MissingToUnicodeRatio: &missing, ImageCoverage: &image, TableSuspect: &suspect}},
		{Number: 5, Signals: parser.Signals{CharCount: 50, GarbledRatio: &exactGarbled, MissingToUnicodeRatio: &exactMissing, ImageCoverage: &exactImage}},
	}}
	report, err := AssessQuality(context.Background(), document)
	if err != nil || !report.NeedsReview || len(report.Pages) != 5 {
		t.Fatalf("quality report=%+v err=%v", report, err)
	}
	want := [][]QualityReason{nil, {QualityGarbled}, {QualityGarbled}, {QualityGarbled, QualityMissingFont, QualityImageHeavy, QualityTableSuspect}, nil}
	for index, page := range report.Pages {
		if page.Page != int64(index+1) || !reflect.DeepEqual(page.Reasons, want[index]) {
			t.Fatalf("page %d reasons=%v want=%v", page.Page, page.Reasons, want[index])
		}
	}
}

func TestAssessQualityRejectsInvalidSignalsAndCancellation(t *testing.T) {
	invalid := math.NaN()
	if _, err := AssessQuality(context.Background(), parser.Document{Pages: []parser.Page{{Number: 1, Signals: parser.Signals{GarbledRatio: &invalid}}}}); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("invalid ratio accepted: %v", err)
	}
	text := "\xff"
	if _, err := AssessQuality(context.Background(), parser.Document{Pages: []parser.Page{{Number: 1, Blocks: []parser.Block{{Text: &text}}}}}); !errors.Is(err, ErrInvalidParsedDocument) {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AssessQuality(ctx, parser.Document{Pages: []parser.Page{{Number: 1}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
