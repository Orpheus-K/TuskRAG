package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

type DeleteDocumentInput struct {
	KBID       int64  `path:"kb" minimum:"1"`
	DocumentID int64  `path:"doc" minimum:"1"`
	CSRF       string `header:"X-CSRF-Token" required:"true"`
}

type DeleteDocumentOutput struct {
	Body struct {
		Status string `json:"status" enum:"deleting"`
	}
}

func registerDocumentDelete(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "delete-document", Method: http.MethodDelete,
		Path: "/kbs/{kb}/documents/{doc}", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *DeleteDocumentInput) (*DeleteDocumentOutput, error) {
			if deps.Documents == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "document storage unavailable", true)
			}
			if err := deps.Documents.Delete(ctx, currentActor(ctx), input.KBID, input.DocumentID); err != nil {
				if errors.Is(err, kb.ErrArchived) {
					return nil, statusError(ctx, http.StatusConflict, "knowledge_base_archived", "knowledge base is archived", false)
				}
				if errors.Is(err, kb.ErrDeleteVersionLimit) {
					return nil, statusError(ctx, http.StatusConflict, "version_limit_exceeded", "document has too many versions to delete", false)
				}
				return nil, knowledgeError(ctx, "delete document", err)
			}
			output := &DeleteDocumentOutput{}
			output.Body.Status = "deleting"
			return output, nil
		})
}
