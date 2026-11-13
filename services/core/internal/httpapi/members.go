package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

type KnowledgeBaseMemberBody struct {
	UserID      int64     `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type TenantUserBody struct {
	ID          int64     `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

type SetKnowledgeBaseMemberInput struct {
	ID     int64  `path:"kb" minimum:"1"`
	UserID int64  `path:"user" minimum:"1"`
	CSRF   string `header:"X-CSRF-Token" required:"true"`
	Body   struct {
		Role string `json:"role" enum:"owner,admin,editor,viewer"`
	}
}

type RemoveKnowledgeBaseMemberInput struct {
	ID     int64  `path:"kb" minimum:"1"`
	UserID int64  `path:"user" minimum:"1"`
	CSRF   string `header:"X-CSRF-Token" required:"true"`
}

type SetKnowledgeBaseEgressPolicyInput struct {
	ID   int64  `path:"kb" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		EgressPolicy string `json:"egress_policy" enum:"deny,internal_only,allow"`
	}
}

func registerKnowledgeMembers(protected *huma.Group, deps Dependencies) {
	huma.Get(protected, "/tenant/users", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []TenantUserBody `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		rows, err := deps.Knowledge.ListTenantUsers(ctx, currentActor(ctx))
		if err != nil {
			return nil, knowledgeError(ctx, "list tenant users", err)
		}
		output := &struct {
			Body struct {
				Items []TenantUserBody `json:"items" nullable:"false"`
			}
		}{}
		output.Body.Items = make([]TenantUserBody, 0, len(rows))
		for _, row := range rows {
			output.Body.Items = append(output.Body.Items, TenantUserBody{
				ID: row.ID, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt,
			})
		}
		return output, nil
	})

	huma.Get(protected, "/kbs/{kb}/members", func(ctx context.Context, input *KnowledgeBasePath) (*struct {
		Body struct {
			Items []KnowledgeBaseMemberBody `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		rows, err := deps.Knowledge.ListMembers(ctx, currentActor(ctx), input.ID)
		if err != nil {
			return nil, knowledgeError(ctx, "list knowledge base members", err)
		}
		output := &struct {
			Body struct {
				Items []KnowledgeBaseMemberBody `json:"items" nullable:"false"`
			}
		}{}
		output.Body.Items = make([]KnowledgeBaseMemberBody, 0, len(rows))
		for _, row := range rows {
			output.Body.Items = append(output.Body.Items, KnowledgeBaseMemberBody{
				UserID: row.UserID, DisplayName: row.DisplayName, Role: string(row.Role), CreatedAt: row.CreatedAt,
			})
		}
		return output, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "set-knowledge-base-member",
		Method:          http.MethodPut,
		Path:            "/kbs/{kb}/members/{user}",
		DefaultStatus:   http.StatusNoContent,
		MaxBodyBytes:    1024,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *SetKnowledgeBaseMemberInput) (*struct{}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		if err := deps.Knowledge.SetMember(ctx, currentActor(ctx), input.ID, input.UserID, kb.Role(input.Body.Role)); err != nil {
			return nil, knowledgeError(ctx, "set knowledge base member", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "remove-knowledge-base-member",
		Method:        http.MethodDelete,
		Path:          "/kbs/{kb}/members/{user}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *RemoveKnowledgeBaseMemberInput) (*struct{}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		if err := deps.Knowledge.RemoveMember(ctx, currentActor(ctx), input.ID, input.UserID); err != nil {
			return nil, knowledgeError(ctx, "remove knowledge base member", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "set-knowledge-base-egress-policy",
		Method:          http.MethodPut,
		Path:            "/kbs/{kb}/egress-policy",
		MaxBodyBytes:    1024,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *SetKnowledgeBaseEgressPolicyInput) (*struct{ Body KnowledgeBaseBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		updated, err := deps.Knowledge.SetEgressPolicy(ctx, currentActor(ctx), input.ID, models.EgressPolicy(input.Body.EgressPolicy))
		if err != nil {
			return nil, knowledgeError(ctx, "set knowledge base egress policy", err)
		}
		return &struct{ Body KnowledgeBaseBody }{Body: knowledgeBaseBody(updated)}, nil
	})
}
