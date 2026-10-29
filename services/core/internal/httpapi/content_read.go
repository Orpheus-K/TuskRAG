package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

type DocumentContentsInput struct {
	DocumentID int64 `path:"doc" minimum:"1"`
	Cursor     int32 `query:"cursor" minimum:"0" default:"0"`
	Limit      int32 `query:"limit" minimum:"1" maximum:"50" default:"30"`
}

type SectionInput struct {
	SectionID int64 `path:"section" minimum:"1"`
}

type ContentDocumentBody struct {
	ID        int64  `json:"id"`
	VersionID int64  `json:"version_id"`
	Title     string `json:"title"`
}

type SectionReferenceBody struct {
	ID          int64    `json:"id"`
	ParentID    *int64   `json:"parent_id" nullable:"true"`
	Ordinal     int32    `json:"ordinal"`
	Level       int16    `json:"level"`
	Heading     string   `json:"heading"`
	HeadingPath []string `json:"heading_path" nullable:"false"`
	Pages       []int32  `json:"pages" nullable:"true"`
}

type DocumentContentsOutput struct {
	Body struct {
		Document   ContentDocumentBody    `json:"document"`
		Sections   []SectionReferenceBody `json:"sections" nullable:"false"`
		NextCursor *int32                 `json:"next_cursor" nullable:"true"`
	}
}

type SectionOutput struct {
	Body struct {
		Document   ContentDocumentBody  `json:"document"`
		Section    SectionReferenceBody `json:"section"`
		Markdown   string               `json:"markdown"`
		TokenCount int32                `json:"token_count"`
	}
}

func registerDocumentReader(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "get-document-contents", Method: http.MethodGet, Path: "/documents/{doc}/toc"},
		func(ctx context.Context, input *DocumentContentsInput) (*DocumentContentsOutput, error) {
			if deps.Reader == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "document reader unavailable", true)
			}
			result, err := deps.Reader.DocumentContents(ctx, currentActor(ctx), input.DocumentID, input.Cursor, input.Limit)
			if err != nil {
				return nil, contentReadError(ctx, "read document contents", err)
			}
			output := &DocumentContentsOutput{}
			output.Body.Document = ContentDocumentBody{ID: result.DocumentID, VersionID: result.VersionID, Title: result.Title}
			output.Body.Sections = make([]SectionReferenceBody, 0, len(result.Sections))
			for _, section := range result.Sections {
				output.Body.Sections = append(output.Body.Sections, sectionReferenceBody(section))
			}
			output.Body.NextCursor = result.NextAfter
			return output, nil
		})
	huma.Register(api, huma.Operation{OperationID: "read-section", Method: http.MethodGet, Path: "/sections/{section}"},
		func(ctx context.Context, input *SectionInput) (*SectionOutput, error) {
			if deps.Reader == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "document reader unavailable", true)
			}
			result, err := deps.Reader.ReadSection(ctx, currentActor(ctx), input.SectionID)
			if err != nil {
				return nil, contentReadError(ctx, "read section", err)
			}
			output := &SectionOutput{}
			output.Body.Document = ContentDocumentBody{ID: result.DocumentID, VersionID: result.VersionID, Title: result.Title}
			output.Body.Section = sectionReferenceBody(result.Section)
			output.Body.Markdown, output.Body.TokenCount = result.Markdown, result.TokenCount
			return output, nil
		})
}

func sectionReferenceBody(section kb.SectionReference) SectionReferenceBody {
	result := SectionReferenceBody{ID: section.ID, ParentID: section.ParentID, Ordinal: section.Ordinal, Level: section.Level,
		Heading: section.Heading, HeadingPath: append([]string{}, section.HeadingPath...)}
	if section.PageStart != nil && section.PageEnd != nil {
		result.Pages = []int32{*section.PageStart, *section.PageEnd}
	}
	return result
}

func contentReadError(ctx context.Context, action string, err error) error {
	switch {
	case errors.Is(err, kb.ErrNotFound):
		return statusError(ctx, http.StatusNotFound, "not_found", "content not found", false)
	case errors.Is(err, kb.ErrInvalidRequest):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid content request", false)
	case errors.Is(err, kb.ErrContentLimit):
		return statusError(ctx, http.StatusUnprocessableEntity, "limit_exceeded", "content exceeds read limit", false)
	default:
		return operationError(ctx, action, err)
	}
}
