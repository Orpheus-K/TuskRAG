package ingest

import (
	"context"
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"

	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/parsereport"
)

type QualityReason = parsereport.Reason

const (
	QualityGarbled      = parsereport.Garbled
	QualityMissingFont  = parsereport.MissingFont
	QualityImageHeavy   = parsereport.ImageHeavy
	QualityTableSuspect = parsereport.TableSuspect
)

type PageQuality struct {
	Page    int64
	Reasons []QualityReason
}

type QualityReport struct {
	Pages       []PageQuality
	NeedsReview bool
}

var cidMarker = regexp.MustCompile(`(?i)\(cid:[0-9]+\)`)

// AssessQuality evaluates parser signals and raw text without recording text.
// It does not infer image quality when the parser has not supplied that signal.
func AssessQuality(ctx context.Context, document parser.Document) (QualityReport, error) {
	if ctx == nil || len(document.Pages) == 0 || len(document.Pages) > 2000 {
		return QualityReport{}, errors.New("context and bounded parsed pages are required")
	}
	report := QualityReport{Pages: make([]PageQuality, 0, len(document.Pages))}
	for _, page := range document.Pages {
		if err := ctx.Err(); err != nil {
			return QualityReport{}, err
		}
		if page.Number <= 0 || page.Signals.CharCount < 0 || !validQualityRatio(page.Signals.GarbledRatio) ||
			!validQualityRatio(page.Signals.MissingToUnicodeRatio) || !validQualityRatio(page.Signals.ImageCoverage) {
			return QualityReport{}, ErrInvalidParsedDocument
		}
		var total, garbled int64
		for _, block := range page.Blocks {
			if block.Text != nil {
				if err := countQualityText(ctx, *block.Text, &total, &garbled); err != nil {
					return QualityReport{}, err
				}
			}
			for _, row := range block.Cells {
				for _, value := range row {
					if err := countQualityText(ctx, value, &total, &garbled); err != nil {
						return QualityReport{}, err
					}
				}
			}
		}
		quality := PageQuality{Page: page.Number}
		if (total > 0 && garbled*100 > total*5) || qualityAbove(page.Signals.GarbledRatio, 0.05) {
			quality.Reasons = append(quality.Reasons, QualityGarbled)
		}
		if qualityAbove(page.Signals.MissingToUnicodeRatio, 0.10) {
			quality.Reasons = append(quality.Reasons, QualityMissingFont)
		}
		if page.Signals.CharCount < 50 && qualityAbove(page.Signals.ImageCoverage, 0.70) {
			quality.Reasons = append(quality.Reasons, QualityImageHeavy)
		}
		if page.Signals.TableSuspect != nil && *page.Signals.TableSuspect {
			quality.Reasons = append(quality.Reasons, QualityTableSuspect)
		}
		if len(quality.Reasons) > 0 {
			report.NeedsReview = true
		}
		report.Pages = append(report.Pages, quality)
	}
	return report, nil
}

func validQualityRatio(value *float64) bool {
	return value == nil || (*value >= 0 && *value <= 1)
}

func qualityAbove(value *float64, threshold float64) bool {
	return value != nil && *value > threshold
}

func countQualityText(ctx context.Context, value string, total, garbled *int64) error {
	if !utf8.ValidString(value) {
		return ErrInvalidParsedDocument
	}
	nextCheck := 0
	for offset, character := range value {
		if offset >= nextCheck {
			if err := ctx.Err(); err != nil {
				return err
			}
			nextCheck = offset + 4096
		}
		if unicode.IsSpace(character) {
			continue
		}
		*total++
		if character == utf8.RuneError || unicode.Is(unicode.Co, character) {
			*garbled++
		}
	}
	for offset := 0; offset < len(value); {
		if err := ctx.Err(); err != nil {
			return err
		}
		location := cidMarker.FindStringIndex(value[offset:])
		if location == nil {
			break
		}
		*garbled += int64(location[1] - location[0])
		offset += location[1]
	}
	return nil
}
