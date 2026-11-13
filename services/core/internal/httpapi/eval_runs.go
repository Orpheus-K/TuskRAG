package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type EvalRunService interface {
	Compare(context.Context, retrieval.Actor, int64, int64, int64, int32, int32) (eval.RunComparison, error)
	List(context.Context, retrieval.Actor, int64, int64, int32) (eval.RunPage, error)
	Start(context.Context, retrieval.Actor, int64, string, eval.StartRequest) (eval.StartedRun, error)
	Status(context.Context, retrieval.Actor, int64, int64) (eval.RunStatus, error)
	GetResult(context.Context, retrieval.Actor, int64, int64) (eval.StoredReport, error)
}

type EvalRunPath struct {
	KB int64 `path:"kb" minimum:"1"`
	ID int64 `path:"run" minimum:"1"`
}
type EvalRunStatusOutput struct{ Body eval.RunStatus }

type StartEvalRunInput struct {
	KB             int64  `path:"kb" minimum:"1"`
	CSRF           string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"128" pattern:"^[!-~]+$"`
	Body           eval.StartRequest
}
type StartedEvalRunOutput struct{ Body eval.StartedRun }

type EvalRunListInput struct {
	KB     int64 `path:"kb" minimum:"1"`
	Cursor int64 `query:"cursor" default:"0" minimum:"0"`
	Limit  int32 `query:"limit" default:"10" minimum:"1" maximum:"10"`
}
type EvalRunListOutput struct{ Body eval.RunPage }

type EvalRunCompareInput struct {
	KB       int64 `path:"kb" minimum:"1"`
	Baseline int64 `query:"baseline" required:"true" minimum:"1"`
	Current  int64 `query:"current" required:"true" minimum:"1"`
	Offset   int32 `query:"offset" default:"0" minimum:"0" maximum:"10000"`
	Limit    int32 `query:"limit" default:"20" minimum:"1" maximum:"50"`
}
type EvalRunCompareOutput struct{ Body eval.RunComparison }

func registerEvalRuns(api huma.API, deps Dependencies) {
	huma.Get(api, "/kbs/{kb}/eval-run-comparison", func(ctx context.Context, input *EvalRunCompareInput) (*EvalRunCompareOutput, error) {
		if deps.EvalRuns == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation runs unavailable", true)
		}
		a := currentActor(ctx)
		result, err := deps.EvalRuns.Compare(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.Baseline, input.Current, input.Offset, input.Limit)
		if err != nil {
			return nil, evalRunError(ctx, err)
		}
		return &EvalRunCompareOutput{Body: result}, nil
	})
	huma.Get(api, "/kbs/{kb}/eval-runs", func(ctx context.Context, input *EvalRunListInput) (*EvalRunListOutput, error) {
		if deps.EvalRuns == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation runs unavailable", true)
		}
		a := currentActor(ctx)
		page, err := deps.EvalRuns.List(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.Cursor, input.Limit)
		if err != nil {
			return nil, evalRunError(ctx, err)
		}
		return &EvalRunListOutput{Body: page}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "start-eval-run", Method: http.MethodPost, Path: "/kbs/{kb}/eval-runs", MaxBodyBytes: 4096}, func(ctx context.Context, input *StartEvalRunInput) (*StartedEvalRunOutput, error) {
		if deps.EvalRuns == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation runs unavailable", true)
		}
		a := currentActor(ctx)
		run, err := deps.EvalRuns.Start(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.IdempotencyKey, input.Body)
		if err != nil {
			return nil, evalRunError(ctx, err)
		}
		return &StartedEvalRunOutput{Body: run}, nil
	})
	huma.Get(api, "/kbs/{kb}/eval-runs/{run}", func(ctx context.Context, input *EvalRunPath) (*EvalRunStatusOutput, error) {
		if deps.EvalRuns == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation runs unavailable", true)
		}
		a := currentActor(ctx)
		status, err := deps.EvalRuns.Status(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.ID)
		if err != nil {
			return nil, evalRunError(ctx, err)
		}
		return &EvalRunStatusOutput{Body: status}, nil
	})
	huma.Get(api, "/kbs/{kb}/eval-runs/{run}/result", func(ctx context.Context, input *EvalRunPath) (*EvalReportOutput, error) {
		if deps.EvalRuns == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation runs unavailable", true)
		}
		a := currentActor(ctx)
		report, err := deps.EvalRuns.GetResult(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.KB, input.ID)
		if err != nil {
			return nil, evalRunError(ctx, err)
		}
		return &EvalReportOutput{Body: report}, nil
	})
}

func evalRunError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, eval.ErrReportsIncomparable):
		return statusError(ctx, 409, "reports_incomparable", "runs require the same dataset, mode, model kind and question population", false)
	case errors.Is(err, eval.ErrRunIdempotencyConflict):
		return statusError(ctx, 409, "idempotency_conflict", "evaluation run key reused with different input", false)
	case errors.Is(err, eval.ErrSetArchived):
		return statusError(ctx, 409, "knowledge_base_archived", "knowledge base archived", false)
	case errors.Is(err, retrieval.ErrRerankUnavailable):
		return statusError(ctx, 409, "rerank_unavailable", "configure a reranker before starting this mode", false)
	case errors.Is(err, retrieval.ErrConfigurationChanged):
		return statusError(ctx, 409, "configuration_changed", "evaluation configuration changed", false)
	case errors.Is(err, eval.ErrRunScopeTooLarge):
		return statusError(ctx, 422, "evaluation_scope_too_large", "evaluation scope exceeds 10000 documents", false)
	case errors.Is(err, eval.ErrRunResultUnavailable):
		return statusError(ctx, 409, "result_unavailable", "evaluation run has no successful result", false)
	case errors.Is(err, eval.ErrRunNotFound), errors.Is(err, eval.ErrSetNotFound), errors.Is(err, eval.ErrReportNotFound):
		return statusError(ctx, 404, "not_found", "evaluation run or evidence not found", false)
	case errors.Is(err, eval.ErrSetForbidden), errors.Is(err, eval.ErrReportForbidden):
		return statusError(ctx, 403, "forbidden", "knowledge base admin required", false)
	case errors.Is(err, eval.ErrInvalidCase):
		return statusError(ctx, 400, "invalid_eval_run", "invalid evaluation run", false)
	default:
		return operationError(ctx, "evaluation run", err)
	}
}
