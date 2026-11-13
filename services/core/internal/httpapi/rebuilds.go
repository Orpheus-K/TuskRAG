package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"tuskrag.local/core/internal/kb"
)

type RebuildService interface {
	Create(context.Context, kb.Actor, int64, string, kb.RebuildRequest) (kb.Rebuild, error)
	Get(context.Context, kb.Actor, int64, int64) (kb.Rebuild, error)
	Cancel(context.Context, kb.Actor, int64, int64) error
}

type CreateRebuildInput struct {
	KB             int64  `path:"kb" minimum:"1"`
	CSRF           string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"128" pattern:"^[!-~]+$"`
	Body           kb.RebuildRequest
}
type RebuildPath struct {
	KB  int64 `path:"kb" minimum:"1"`
	Job int64 `path:"job" minimum:"1"`
}
type CancelRebuildInput struct {
	KB   int64  `path:"kb" minimum:"1"`
	Job  int64  `path:"job" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
}
type RebuildOutput struct{ Body kb.Rebuild }
type CancelRebuildOutput struct {
	Body struct {
		Status string `json:"status" enum:"cancelled"`
	}
}

func registerRebuilds(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "create-rebuild", Method: http.MethodPost, Path: "/kbs/{kb}/rebuilds", MaxBodyBytes: 4096}, func(ctx context.Context, input *CreateRebuildInput) (*RebuildOutput, error) {
		if deps.Rebuilds == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "embedding rebuild unavailable", true)
		}
		result, err := deps.Rebuilds.Create(ctx, currentActor(ctx), input.KB, input.IdempotencyKey, input.Body)
		if err != nil {
			return nil, rebuildError(ctx, err)
		}
		return &RebuildOutput{Body: result}, nil
	})
	huma.Get(api, "/kbs/{kb}/rebuilds/{job}", func(ctx context.Context, input *RebuildPath) (*RebuildOutput, error) {
		if deps.Rebuilds == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "embedding rebuild unavailable", true)
		}
		result, err := deps.Rebuilds.Get(ctx, currentActor(ctx), input.KB, input.Job)
		if err != nil {
			return nil, rebuildError(ctx, err)
		}
		return &RebuildOutput{Body: result}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "cancel-rebuild", Method: http.MethodPost, Path: "/kbs/{kb}/rebuilds/{job}/cancel", MaxBodyBytes: 4096}, func(ctx context.Context, input *CancelRebuildInput) (*CancelRebuildOutput, error) {
		if deps.Rebuilds == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "embedding rebuild unavailable", true)
		}
		if err := deps.Rebuilds.Cancel(ctx, currentActor(ctx), input.KB, input.Job); err != nil {
			return nil, rebuildError(ctx, err)
		}
		result := &CancelRebuildOutput{}
		result.Body.Status = "cancelled"
		return result, nil
	})
}
func rebuildError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, kb.ErrRebuildInProgress):
		return statusError(ctx, 409, "rebuild_in_progress", "knowledge base already has a pending rebuild", false)
	case errors.Is(err, kb.ErrRebuildTargetUnsupported):
		return statusError(ctx, 422, "unsupported_embedding", "embedding target is unsupported", false)
	default:
		return knowledgeError(ctx, "embedding rebuild", err)
	}
}
