package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type EvalSetService interface {
	List(context.Context, retrieval.Actor, int64, int64, int32) (eval.SetPage, error)
	Create(context.Context, retrieval.Actor, int64, string, eval.SetDefinition) (eval.StoredSet, error)
	Update(context.Context, retrieval.Actor, int64, int64, int32, eval.SetDefinition) (eval.StoredSet, error)
	Get(context.Context, retrieval.Actor, int64, int64, int32) (eval.StoredSet, error)
}

type CreateEvalSetInput struct {
	KB             int64  `path:"kb" minimum:"1"`
	CSRF           string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128"`
	Body           eval.SetDefinition
}
type UpdateEvalSetInput struct {
	KB      int64  `path:"kb" minimum:"1"`
	ID      int64  `path:"set" minimum:"1"`
	CSRF    string `header:"X-CSRF-Token" required:"true"`
	IfMatch string `header:"If-Match" required:"true" maxLength:"12"`
	Body    eval.SetDefinition
}
type GetEvalSetInput struct {
	KB       int64 `path:"kb" minimum:"1"`
	ID       int64 `path:"set" minimum:"1"`
	Revision int32 `query:"revision" minimum:"0" default:"0"`
}
type EvalSetOutput struct {
	ETag string `header:"ETag"`
	Body eval.StoredSet
}

type ListEvalSetsInput struct {
	KB     int64 `path:"kb" minimum:"1"`
	Cursor int64 `query:"cursor" minimum:"0" default:"0"`
	Limit  int32 `query:"limit" minimum:"1" maximum:"10" default:"10"`
}
type EvalSetPageOutput struct{ Body eval.SetPage }

func registerEvalSets(api huma.API, deps Dependencies) {
	huma.Get(api, "/kbs/{kb}/eval-sets", func(ctx context.Context, input *ListEvalSetsInput) (*EvalSetPageOutput, error) {
		if deps.EvalSets == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation sets unavailable", true)
		}
		a := currentActor(ctx)
		page, err := deps.EvalSets.List(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.Cursor, input.Limit)
		if err != nil {
			return nil, evalSetError(ctx, err)
		}
		return &EvalSetPageOutput{Body: page}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "create-eval-set", Method: http.MethodPost, Path: "/kbs/{kb}/eval-sets", MaxBodyBytes: eval.MaxSetBytes, BodyReadTimeout: 30 * time.Second}, func(ctx context.Context, input *CreateEvalSetInput) (*EvalSetOutput, error) {
		if deps.EvalSets == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation sets unavailable", true)
		}
		a := currentActor(ctx)
		result, err := deps.EvalSets.Create(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.IdempotencyKey, input.Body)
		if err != nil {
			return nil, evalSetError(ctx, err)
		}
		return evalSetOutput(result), nil
	})
	huma.Register(api, huma.Operation{OperationID: "update-eval-set", Method: http.MethodPut, Path: "/kbs/{kb}/eval-sets/{set}", MaxBodyBytes: eval.MaxSetBytes, BodyReadTimeout: 30 * time.Second}, func(ctx context.Context, input *UpdateEvalSetInput) (*EvalSetOutput, error) {
		if deps.EvalSets == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation sets unavailable", true)
		}
		value, err := strconv.Unquote(input.IfMatch)
		if err != nil {
			return nil, evalSetError(ctx, eval.ErrInvalidCase)
		}
		revision, err := strconv.ParseInt(value, 10, 32)
		if err != nil || revision <= 0 || strconv.Quote(strconv.FormatInt(revision, 10)) != input.IfMatch {
			return nil, evalSetError(ctx, eval.ErrInvalidCase)
		}
		a := currentActor(ctx)
		result, err := deps.EvalSets.Update(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.ID, int32(revision), input.Body)
		if err != nil {
			return nil, evalSetError(ctx, err)
		}
		return evalSetOutput(result), nil
	})
	huma.Get(api, "/kbs/{kb}/eval-sets/{set}", func(ctx context.Context, input *GetEvalSetInput) (*EvalSetOutput, error) {
		if deps.EvalSets == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation sets unavailable", true)
		}
		a := currentActor(ctx)
		result, err := deps.EvalSets.Get(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.ID, input.Revision)
		if err != nil {
			return nil, evalSetError(ctx, err)
		}
		return evalSetOutput(result), nil
	})
}

func evalSetOutput(value eval.StoredSet) *EvalSetOutput {
	return &EvalSetOutput{ETag: strconv.Quote(strconv.FormatInt(int64(value.Revision), 10)), Body: value}
}

func evalSetError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, eval.ErrSetIdempotencyConflict):
		return statusError(ctx, 409, "idempotency_conflict", "evaluation set idempotency key reused", false)
	case errors.Is(err, eval.ErrSetConflict):
		return statusError(ctx, 409, "revision_conflict", "evaluation set revision changed", false)
	case errors.Is(err, eval.ErrSetNotFound):
		return statusError(ctx, 404, "not_found", "evaluation set or expected location not found", false)
	case errors.Is(err, eval.ErrSetForbidden):
		return statusError(ctx, 403, "forbidden", "knowledge base admin required", false)
	case errors.Is(err, eval.ErrSetArchived):
		return statusError(ctx, 409, "knowledge_base_archived", "knowledge base archived", false)
	case errors.Is(err, eval.ErrInvalidCase):
		return statusError(ctx, 400, "invalid_eval_set", "invalid evaluation set", false)
	default:
		return operationError(ctx, "evaluation set", err)
	}
}
