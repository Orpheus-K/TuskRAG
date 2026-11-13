package parsereport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unicode/utf8"
)

const (
	Version         = 1
	MaxBytes        = 1024 * 1024
	MaxPages        = 2000
	MaxHeadings     = 1000
	MaxHeadingBytes = 8192
	MaxHeadingTotal = 128 * 1024
	MaxProfileBytes = 256
)

type Reason string

const (
	Garbled      Reason = "garbled_text"
	MissingFont  Reason = "missing_font_mapping"
	ImageHeavy   Reason = "image_heavy"
	TableSuspect Reason = "table_suspect"
)

type Page struct {
	Number        int64    `json:"number"`
	CharacterCount int64   `json:"character_count"`
	TableCount    int32    `json:"table_count"`
	ImageCoverage *float64 `json:"image_coverage" nullable:"true"`
	Reasons       []Reason `json:"reasons" nullable:"false"`
	VLMUsed       bool     `json:"vlm_used,omitempty"`
}

type Heading struct {
	Page  int64  `json:"page"`
	Level int32  `json:"level"`
	Title string `json:"title"`
}

type Report struct {
	Version           int       `json:"version"`
	ParserProfile     string    `json:"parser_profile"`
	ProfileTruncated  bool      `json:"profile_truncated"`
	PhysicalPages     bool      `json:"physical_pages"`
	PageCount         int32     `json:"page_count"`
	TableCount        int32     `json:"table_count"`
	NeedsReview       bool      `json:"needs_review"`
	Pages             []Page    `json:"pages" nullable:"false"`
	Headings          []Heading `json:"headings" nullable:"false"`
	HeadingsTruncated bool      `json:"headings_truncated"`
	VLMPages          []int64   `json:"vlm_pages,omitempty"`
	VLMTokens         int       `json:"vlm_tokens,omitempty"`
}

func Decode(data []byte) (Report, error) {
	if len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) {
		return Report{}, errors.New("invalid parse report size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report Report
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Report{}, errors.New("invalid parse report JSON")
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (report Report) Validate() error {
	if report.Version != Version || len(report.Pages) < 1 || len(report.Pages) > MaxPages ||
		report.PageCount != int32(len(report.Pages)) || report.Pages == nil || report.Headings == nil ||
		len(report.Headings) > MaxHeadings || len(report.ParserProfile) > MaxProfileBytes || !utf8.ValidString(report.ParserProfile) ||
		(report.ProfileTruncated && report.ParserProfile != "") {
		return errors.New("invalid parse report metadata")
	}
	var lastPage, tables int64
	needsReview := false
	pageNumbers := make(map[int64]struct{}, len(report.Pages))
	for _, page := range report.Pages {
		if page.Number <= lastPage || page.CharacterCount < 0 || page.TableCount < 0 || page.Reasons == nil || len(page.Reasons) > 4 ||
			(page.ImageCoverage != nil && (math.IsNaN(*page.ImageCoverage) || math.IsInf(*page.ImageCoverage, 0) || *page.ImageCoverage < 0 || *page.ImageCoverage > 1)) {
			return errors.New("invalid parse report page")
		}
		lastPage = page.Number
		pageNumbers[page.Number] = struct{}{}
		tables += int64(page.TableCount)
		seen := make(map[Reason]struct{}, len(page.Reasons))
		for _, reason := range page.Reasons {
			switch reason {
			case Garbled, MissingFont, ImageHeavy, TableSuspect:
			default:
				return errors.New("unknown parse quality reason")
			}
			if _, duplicate := seen[reason]; duplicate {
				return errors.New("duplicate parse quality reason")
			}
			seen[reason] = struct{}{}
			needsReview = true
		}
	}
	if tables != int64(report.TableCount) || report.NeedsReview != needsReview {
		return errors.New("inconsistent parse report summary")
	}
	var total int
	lastPage = 0
	for _, heading := range report.Headings {
		if _, exists := pageNumbers[heading.Page]; !exists || heading.Page < lastPage || heading.Level < 1 || heading.Level > 9 ||
			len(heading.Title) > MaxHeadingBytes || !utf8.ValidString(heading.Title) {
			return errors.New("invalid parse report heading")
		}
		lastPage = heading.Page
		total += len(heading.Title)
	}
	if total > MaxHeadingTotal {
		return errors.New("parse report headings exceed limit")
	}
	return nil
}
