package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

type ImportURLInput struct {
	KBID                   int64  `path:"kb" minimum:"1"`
	CSRF                   string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey         string `header:"Idempotency-Key" maxLength:"128"`
	AllowDuplicate         bool   `query:"allow_duplicate"`
	ConfirmFirstRowHeaders bool   `query:"confirm_first_row_headers"`
	Body                   struct {
		URL   string `json:"url" minLength:"1" maxLength:"2048"`
		Title string `json:"title,omitempty" maxLength:"200"`
	}
}

func registerURLImport(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{
		OperationID: "import-document-url",
		Method:      http.MethodPost,
		Path:        "/kbs/{kb}/documents:import-url",
		MaxBodyBytes: 8192,
	}, func(ctx context.Context, input *ImportURLInput) (*struct{ Body UploadedDocumentBody }, error) {
		if deps.Documents == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "document storage unavailable", false)
		}
		actor := currentActor(ctx)
		if _, err := kb.ValidateImportURL(input.Body.URL); err != nil {
			return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid import url", false)
		}
		fetched, err := kb.FetchURLForImport(ctx, input.Body.URL)
		if err != nil {
			switch {
			case errors.Is(err, kb.ErrURLBlocked):
				return nil, statusError(ctx, http.StatusForbidden, "url_blocked", "url blocked by ssrf policy", false)
			case errors.Is(err, kb.ErrURLTooLarge):
				return nil, statusError(ctx, http.StatusRequestEntityTooLarge, "limit_exceeded", "remote document exceeds 20 MiB limit", false)
			case errors.Is(err, kb.ErrUnsupportedMIME):
				return nil, statusError(ctx, http.StatusBadRequest, "unsupported_format", "remote format is not supported", false)
			case errors.Is(err, kb.ErrInvalidRequest):
				return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid import url", false)
			default:
				return nil, statusError(ctx, http.StatusBadGateway, "upstream_fetch_failed", "failed to fetch remote document", true)
			}
		}
		title := fetched.Title
		if input.Body.Title != "" {
			title = input.Body.Title
		}
		doc, err := deps.Documents.Upload(ctx, kb.Actor{TenantID: actor.TenantID, UserID: actor.UserID}, kb.UploadRequest{
			KBID:                   input.KBID,
			Title:                  title,
			MIME:                   fetched.MIME,
			Content:                bytes.NewReader(fetched.Data),
			IdempotencyKey:         input.IdempotencyKey,
			AllowDuplicate:         input.AllowDuplicate,
			ConfirmFirstRowHeaders: input.ConfirmFirstRowHeaders,
		})
		if err != nil {
			return nil, documentUploadError(ctx, err)
		}
		return &struct{ Body UploadedDocumentBody }{
			Body: UploadedDocumentBody{
				DocumentID: doc.DocumentID,
				VersionID:  doc.VersionID,
				Title:      doc.Title,
				MIME:       doc.MIME,
				Status:     doc.Status,
				ByteSize:   doc.ByteSize,
				CreatedAt:  doc.CreatedAt,
			},
		}, nil
	})
}
