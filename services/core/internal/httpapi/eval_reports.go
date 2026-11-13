package httpapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

type ImportEvalReportInput struct {
	ID   int64  `path:"kb" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body eval.RunReport
}

type EvalReportPath struct {
	ID     int64  `path:"kb" minimum:"1"`
	Digest string `path:"digest" pattern:"^[0-9a-f]{64}$"`
}

type EvalReportOutput struct{ Body eval.StoredReport }

type EvalReportListInput struct {
	ID     int64  `path:"kb" minimum:"1"`
	Cursor string `query:"cursor" pattern:"^([0-9a-f]{64})?$"`
	Limit  int32  `query:"limit" minimum:"1" maximum:"10" default:"10"`
}
type EvalReportListOutput struct{ Body eval.ReportPage }

type EvalReportCompareInput struct {
	ID       int64  `path:"kb" minimum:"1"`
	Baseline string `query:"baseline" required:"true" pattern:"^[0-9a-f]{64}$"`
	Current  string `query:"current" required:"true" pattern:"^[0-9a-f]{64}$"`
	Offset   int32  `query:"offset" minimum:"0" maximum:"10000" default:"0"`
	Limit    int32  `query:"limit" minimum:"1" maximum:"50" default:"20"`
}
type EvalReportCompareOutput struct{ Body eval.ReportComparison }

func registerEvalReports(api huma.API, deps Dependencies) {
	// No-answer metrics are explicitly null in CLI artifacts. Huma's nullable
	// field tag does not support struct references; declare the union here.
	registry := api.OpenAPI().Components.Schemas
	for _, field := range []struct {
		typ  reflect.Type
		name string
	}{{reflect.TypeFor[eval.Summary](), "Metrics"}, {reflect.TypeFor[eval.RunRow](), "metrics"}, {reflect.TypeFor[eval.RowComparison](), "delta"}} {
		ref := registry.Schema(field.typ, true, "")
		schema := registry.SchemaFromRef(ref.Ref)
		schema.Properties[field.name] = &huma.Schema{AnyOf: []*huma.Schema{schema.Properties[field.name], {Type: huma.TypeNull}}}
		schema.PrecomputeMessages()
	}
	// A fixed-size latency pair cannot be null: JSON null would otherwise
	// decode to [0,0] and fabricate a zero-duration measurement.
	ref := registry.Schema(reflect.TypeFor[eval.RunReport](), true, "")
	pair := registry.Schema(reflect.TypeFor[[2]float64](), false, "")
	pair.Nullable = false
	schema := registry.SchemaFromRef(ref.Ref)
	schema.Properties["latency_ms_p50_p95"].AdditionalProperties = pair
	schema.PrecomputeMessages()
	huma.Get(api, "/kbs/{kb}/eval-report-comparison", func(ctx context.Context, input *EvalReportCompareInput) (*EvalReportCompareOutput, error) {
		if deps.Reports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		result, err := deps.Reports.Compare(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Baseline, input.Current, input.Offset, input.Limit)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &EvalReportCompareOutput{Body: result}, nil
	})
	huma.Get(api, "/kbs/{kb}/eval-reports", func(ctx context.Context, input *EvalReportListInput) (*EvalReportListOutput, error) {
		if deps.Reports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		page, err := deps.Reports.List(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Cursor, input.Limit)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &EvalReportListOutput{Body: page}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "import-eval-report", Method: http.MethodPost, Path: "/kbs/{kb}/eval-reports", MaxBodyBytes: 8 * 1024 * 1024, BodyReadTimeout: 30 * time.Second},
		func(ctx context.Context, input *ImportEvalReportInput) (*EvalReportOutput, error) {
			if deps.Reports == nil {
				return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation reports unavailable", true)
			}
			a := currentActor(ctx)
			report, err := deps.Reports.Import(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Body)
			if err != nil {
				return nil, evalReportError(ctx, err)
			}
			return &EvalReportOutput{Body: report}, nil
		})
	huma.Get(api, "/kbs/{kb}/eval-reports/{digest}", func(ctx context.Context, input *EvalReportPath) (*EvalReportOutput, error) {
		if deps.Reports == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "evaluation reports unavailable", true)
		}
		a := currentActor(ctx)
		report, err := deps.Reports.Get(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, input.ID, input.Digest)
		if err != nil {
			return nil, evalReportError(ctx, err)
		}
		return &EvalReportOutput{Body: report}, nil
	})
}

func evalReportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, eval.ErrReportsIncomparable):
		return statusError(ctx, 409, "reports_incomparable", "reports require the same dataset, mode, model kind and question population", false)
	case errors.Is(err, eval.ErrReportInvalid):
		return statusError(ctx, 400, "invalid_report", "invalid complete evaluation report", false)
	case errors.Is(err, eval.ErrReportNotFound):
		return statusError(ctx, 404, "not_found", "report or evidence not found", false)
	case errors.Is(err, eval.ErrReportForbidden):
		return statusError(ctx, 403, "forbidden", "knowledge base admin required", false)
	case errors.Is(err, eval.ErrReportArchived):
		return statusError(ctx, 409, "knowledge_base_archived", "knowledge base archived", false)
	default:
		return operationError(ctx, "evaluation report", err)
	}
}
