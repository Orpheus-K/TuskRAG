package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type ImportAnswerEvalReportInput struct {
	ID   int64  `path:"kb" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body eval.AnswerReport
}

type AnswerEvalReportPath struct {
	ID     int64  `path:"kb" minimum:"1"`
	Digest string `path:"digest" pattern:"^[0-9a-f]{64}$"`
}

type AnswerEvalReportOutput struct{ Body eval.StoredAnswerReport }

type AnswerEvalReportListInput struct {
	ID     int64  `path:"kb" minimum:"1"`
	Cursor string `query:"cursor" pattern:"^([0-9a-f]{64})?$"`
	Limit  int32  `query:"limit" minimum:"1" maximum:"10" default:"10"`
}
type AnswerEvalReportListOutput struct{ Body eval.AnswerReportPage }

type AnswerEvalReportCompareInput struct {
	ID       int64  `path:"kb" minimum:"1"`
	Baseline string `query:"baseline" required:"true" pattern:"^[0-9a-f]{64}$"`
	Current  string `query:"current" required:"true" pattern:"^[0-9a-f]{64}$"`
	Offset   int32  `query:"offset" minimum:"0" maximum:"10000" default:"0"`
	Limit    int32  `query:"limit" minimum:"1" maximum:"50" default:"20"`
}
type AnswerEvalReportCompareOutput struct {
	Body eval.AnswerReportComparisonView
}

func registerAnswerEvalReports(api huma.API, deps Dependencies) {
	registry := api.OpenAPI().Components.Schemas
	ref := registry.Schema(reflect.TypeFor[eval.AnswerReport](), true, "")
	pair := registry.Schema(reflect.TypeFor[[2]float64](), false, "")
	pair.Nullable = false
	schema := registry.SchemaFromRef(ref.Ref)
	schema.Properties["latency_ms_p50_p95"].AdditionalProperties = pair
	schema.PrecomputeMessages()
	huma.Get(api, "/kbs/{kb}/answer-eval-report-comparison", func(ctx context.Context, input *AnswerEvalReportCompareInput) (*AnswerEvalReportCompareOutput, error) {
		if deps.AnswerReports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "answer evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		comparison, err := deps.AnswerReports.Compare(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Baseline, input.Current, input.Offset, input.Limit)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &AnswerEvalReportCompareOutput{Body: comparison}, nil
	})
	huma.Get(api, "/kbs/{kb}/answer-eval-reports", func(ctx context.Context, input *AnswerEvalReportListInput) (*AnswerEvalReportListOutput, error) {
		if deps.AnswerReports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "answer evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		page, err := deps.AnswerReports.List(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Cursor, input.Limit)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &AnswerEvalReportListOutput{Body: page}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "import-answer-eval-report", Method: http.MethodPost, Path: "/kbs/{kb}/answer-eval-reports", MaxBodyBytes: 8 * 1024 * 1024, BodyReadTimeout: 30 * time.Second},
		func(ctx context.Context, input *ImportAnswerEvalReportInput) (*AnswerEvalReportOutput, error) {
			if deps.AnswerReports == nil {
				return nil, statusError(ctx, 503, "dependency_unavailable", "answer evaluation reports unavailable", true)
			}
			a := currentActor(ctx)
			report, err := deps.AnswerReports.Import(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Body)
			if err != nil {
				return nil, evalReportError(ctx, err)
			}
			return &AnswerEvalReportOutput{Body: report}, nil
		})
	huma.Get(api, "/kbs/{kb}/answer-eval-reports/{digest}", func(ctx context.Context, input *AnswerEvalReportPath) (*AnswerEvalReportOutput, error) {
		if deps.AnswerReports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "answer evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		report, err := deps.AnswerReports.Get(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Digest)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &AnswerEvalReportOutput{Body: report}, nil
	})
	huma.Get(api, "/kbs/{kb}/answer-eval-reports/{digest}/acceptance", func(ctx context.Context, input *AnswerEvalReportPath) (*struct {
		Body struct {
			Markdown string `json:"markdown"`
		}
	}, error) {
		if deps.AnswerReports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "answer evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		report, err := deps.AnswerReports.Get(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Digest)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		out := &struct {
			Body struct {
				Markdown string `json:"markdown"`
			}
		}{}
		out.Body.Markdown = eval.RenderAcceptanceReportMarkdown(nil, &report.Report)
		return out, nil
	})
}
