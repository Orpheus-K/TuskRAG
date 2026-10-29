package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/parsereport"
)

// buildParseReport records bounded structure and quality signals without body text.
func buildParseReport(ctx context.Context, document parser.Document, quality QualityReport, mime string) ([]byte, error) {
	if ctx == nil || len(document.Pages) == 0 || len(document.Pages) > parsereport.MaxPages || len(quality.Pages) != len(document.Pages) {
		return nil, errors.New("invalid parse report input")
	}
	report := parsereport.Report{
		Version: parsereport.Version, ParserProfile: document.Meta.ParserProfile, PhysicalPages: mime == "application/pdf",
		PageCount: int32(len(document.Pages)), NeedsReview: quality.NeedsReview,
		Pages: make([]parsereport.Page, 0, len(document.Pages)), Headings: make([]parsereport.Heading, 0),
	}
	if len(report.ParserProfile) > parsereport.MaxProfileBytes {
		report.ParserProfile, report.ProfileTruncated = "", true
	}
	headingBytes := 0
	for index, page := range document.Pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if quality.Pages[index].Page != page.Number {
			return nil, errors.New("quality report page mismatch")
		}
		summary := parsereport.Page{Number: page.Number, CharacterCount: page.Signals.CharCount, ImageCoverage: page.Signals.ImageCoverage,
			Reasons: append([]parsereport.Reason{}, quality.Pages[index].Reasons...)}
		for blockIndex, block := range page.Blocks {
			if blockIndex%128 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if block.Type == "table" {
				summary.TableCount++
				report.TableCount++
			}
			if block.Type != "heading" || report.HeadingsTruncated {
				continue
			}
			if block.Text == nil || block.Level == nil || *block.Level < 1 || *block.Level > 9 ||
				len(*block.Text) > parsereport.MaxHeadingBytes || len(report.Headings) >= parsereport.MaxHeadings ||
				headingBytes+len(*block.Text) > parsereport.MaxHeadingTotal {
				report.HeadingsTruncated = true
				continue
			}
			headingBytes += len(*block.Text)
			report.Headings = append(report.Headings, parsereport.Heading{Page: page.Number, Level: int32(*block.Level), Title: *block.Text})
		}
		report.Pages = append(report.Pages, summary)
	}
	if err := report.Validate(); err != nil {
		return nil, fmt.Errorf("validate parse report: %w", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("encode parse report: %w", err)
	}
	if len(encoded) > parsereport.MaxBytes {
		return nil, errors.New("parse report exceeds size limit")
	}
	return encoded, nil
}
