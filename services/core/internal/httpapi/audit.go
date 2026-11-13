package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/identity"
)

type AuditLogEntryBody struct {
	ID           int64           `json:"id"`
	TenantID     int64           `json:"tenant_id"`
	ActorUserID  int64           `json:"actor_user_id"`
	ActorName    string          `json:"actor_name"`
	Action       string          `json:"action"`
	ObjectType   string          `json:"object_type"`
	ObjectID     int64           `json:"object_id"`
	Result       string          `json:"result"`
	TargetUserID *int64          `json:"target_user_id,omitempty"`
	NewRole      *string         `json:"new_role,omitempty"`
	Details      json.RawMessage `json:"details"`
	CreatedAt    time.Time       `json:"created_at"`
}

type ListAuditLogsInput struct {
	Action      string `query:"action"`
	ActorUserID int64  `query:"actor_user_id"`
	ObjectType  string `query:"object_type"`
	Result      string `query:"result"`
	From        string `query:"from"`
	To          string `query:"to"`
	BeforeID    int64  `query:"before_id"`
	Limit       int32  `query:"limit"`
}

type ExportAuditLogsInput struct {
	Action      string `query:"action"`
	ActorUserID int64  `query:"actor_user_id"`
	ObjectType  string `query:"object_type"`
	Result      string `query:"result"`
	From        string `query:"from"`
	To          string `query:"to"`
	Format      string `query:"format" enum:"jsonl,csv" default:"jsonl"`
}

type TenantEgressPolicyBody struct {
	EgressPolicyMax string `json:"egress_policy_max" enum:"deny,internal_only,allow"`
}

type UpdateTenantEgressPolicyInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body TenantEgressPolicyBody
}

func resolveAuditService(deps Dependencies) AuditService {
	if deps.Audit != nil {
		return deps.Audit
	}
	if svc, ok := deps.Identity.(AuditService); ok {
		return svc
	}
	return nil
}

func parseAuditFilter(action string, actorUserID int64, objectType, result, fromStr, toStr string, beforeID int64, limit int32) (identity.AuditLogFilter, error) {
	f := identity.AuditLogFilter{
		Action:      action,
		ActorUserID: actorUserID,
		ObjectType:  objectType,
		Result:      result,
		BeforeID:    beforeID,
		Limit:       limit,
	}
	if fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return identity.AuditLogFilter{}, identity.ErrInvalidAuditRequest
		}
		f.From = &t
	}
	if toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return identity.AuditLogFilter{}, identity.ErrInvalidAuditRequest
		}
		f.To = &t
	}
	return f, nil
}

func auditError(ctx context.Context, action string, err error) error {
	switch {
	case errors.Is(err, identity.ErrAdminRequired):
		return statusError(ctx, http.StatusForbidden, "forbidden", "administrator access required", false)
	case errors.Is(err, identity.ErrInvalidAuditRequest):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid audit request", false)
	default:
		return operationError(ctx, action, err)
	}
}

func registerAuditAndTenantPolicy(protected *huma.Group, deps Dependencies) {
	huma.Get(protected, "/admin/audit-logs", func(ctx context.Context, input *ListAuditLogsInput) (*struct {
		Body struct {
			Items []AuditLogEntryBody `json:"items" nullable:"false"`
		}
	}, error) {
		svc := resolveAuditService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "audit service unavailable", true)
		}
		filter, err := parseAuditFilter(input.Action, input.ActorUserID, input.ObjectType, input.Result, input.From, input.To, input.BeforeID, input.Limit)
		if err != nil {
			return nil, auditError(ctx, "list audit logs", err)
		}
		actor := currentActor(ctx)
		rows, err := svc.ListAuditLogs(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, filter)
		if err != nil {
			return nil, auditError(ctx, "list audit logs", err)
		}
		out := &struct {
			Body struct {
				Items []AuditLogEntryBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]AuditLogEntryBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, AuditLogEntryBody{
				ID:           r.ID,
				TenantID:     r.TenantID,
				ActorUserID:  r.ActorUserID,
				ActorName:    r.ActorName,
				Action:       r.Action,
				ObjectType:   r.ObjectType,
				ObjectID:     r.ObjectID,
				Result:       r.Result,
				TargetUserID: r.TargetUserID,
				NewRole:      r.NewRole,
				Details:      r.Details,
				CreatedAt:    r.CreatedAt,
			})
		}
		return out, nil
	})

	huma.Get(protected, "/admin/audit-logs/export", func(ctx context.Context, input *ExportAuditLogsInput) (*huma.StreamResponse, error) {
		svc := resolveAuditService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "audit service unavailable", true)
		}
		filter, err := parseAuditFilter(input.Action, input.ActorUserID, input.ObjectType, input.Result, input.From, input.To, 0, 5000)
		if err != nil {
			return nil, auditError(ctx, "export audit logs", err)
		}
		actor := currentActor(ctx)
		format := input.Format
		if format == "" {
			format = "jsonl"
		}
		data, contentType, err := svc.ExportAuditLogs(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, filter, format)
		if err != nil {
			return nil, auditError(ctx, "export audit logs", err)
		}
		ext := "jsonl"
		if format == "csv" {
			ext = "csv"
		}
		return &huma.StreamResponse{
			Body: func(hctx huma.Context) {
				hctx.SetHeader("Content-Type", contentType)
				hctx.SetHeader("Content-Disposition", `attachment; filename="audit-logs.`+ext+`"`)
				_, _ = hctx.BodyWriter().Write(data)
			},
		}, nil
	})

	huma.Get(protected, "/admin/tenant-egress", func(ctx context.Context, _ *struct{}) (*struct {
		Body TenantEgressPolicyBody
	}, error) {
		svc := resolveAuditService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "audit service unavailable", true)
		}
		actor := currentActor(ctx)
		policy, err := svc.GetTenantEgressPolicy(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID})
		if err != nil {
			return nil, auditError(ctx, "get tenant egress policy", err)
		}
		return &struct{ Body TenantEgressPolicyBody }{Body: TenantEgressPolicyBody{EgressPolicyMax: policy}}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "update-tenant-egress",
		Method:          http.MethodPut,
		Path:            "/admin/tenant-egress",
		MaxBodyBytes:    2048,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *UpdateTenantEgressPolicyInput) (*struct {
		Body TenantEgressPolicyBody
	}, error) {
		svc := resolveAuditService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "audit service unavailable", true)
		}
		actor := currentActor(ctx)
		policy, err := svc.UpdateTenantEgressPolicy(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, input.Body.EgressPolicyMax)
		if err != nil {
			return nil, auditError(ctx, "update tenant egress policy", err)
		}
		return &struct{ Body TenantEgressPolicyBody }{Body: TenantEgressPolicyBody{EgressPolicyMax: policy}}, nil
	})
}
