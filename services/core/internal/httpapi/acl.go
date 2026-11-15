package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

type UserGroupBody struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type GroupMemberBody struct {
	UserID      int64     `json:"user_id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreateGroupInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"120"`
	}
}

type GroupPath struct {
	GroupID int64 `path:"group" minimum:"1"`
}

type DeleteGroupInput struct {
	GroupID int64  `path:"group" minimum:"1"`
	CSRF    string `header:"X-CSRF-Token" required:"true"`
}

type GroupMemberInput struct {
	GroupID int64  `path:"group" minimum:"1"`
	UserID  int64  `path:"user" minimum:"1"`
	CSRF    string `header:"X-CSRF-Token" required:"true"`
}

type ACLSubjectBody struct {
	Type string `json:"type" enum:"user,group"`
	ID   int64  `json:"id" minimum:"1"`
}

type DocumentACLBody struct {
	DocumentID int64            `json:"document_id"`
	ACLMode    string           `json:"acl_mode" enum:"inherit_kb,restricted"`
	Revision   int32            `json:"acl_revision"`
	Subjects   []ACLSubjectBody `json:"subjects" nullable:"false"`
}

type DocumentACLPath struct {
	ID         int64 `path:"kb" minimum:"1"`
	DocumentID int64 `path:"doc" minimum:"1"`
}

type SetDocumentACLInput struct {
	ID         int64  `path:"kb" minimum:"1"`
	DocumentID int64  `path:"doc" minimum:"1"`
	CSRF       string `header:"X-CSRF-Token" required:"true"`
	Body       struct {
		ACLMode  string           `json:"acl_mode" enum:"inherit_kb,restricted"`
		Subjects []ACLSubjectBody `json:"subjects" nullable:"false"`
	}
}

func registerGovernanceACL(protected *huma.Group, deps Dependencies) {
	huma.Get(protected, "/groups", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []UserGroupBody `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		rows, err := deps.Knowledge.ListGroups(ctx, currentActor(ctx))
		if err != nil {
			return nil, knowledgeError(ctx, "list user groups", err)
		}
		out := &struct {
			Body struct {
				Items []UserGroupBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]UserGroupBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, UserGroupBody{
				ID: r.ID, Name: r.Name, Source: r.Source, CreatedAt: r.CreatedAt,
			})
		}
		return out, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "create-group",
		Method:          http.MethodPost,
		Path:            "/groups",
		MaxBodyBytes:    1024,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *CreateGroupInput) (*struct{ Body UserGroupBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		created, err := deps.Knowledge.CreateGroup(ctx, currentActor(ctx), input.Body.Name)
		if err != nil {
			return nil, knowledgeError(ctx, "create user group", err)
		}
		return &struct{ Body UserGroupBody }{Body: UserGroupBody{
			ID: created.ID, Name: created.Name, Source: created.Source, CreatedAt: created.CreatedAt,
		}}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "delete-group",
		Method:        http.MethodDelete,
		Path:          "/groups/{group}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *DeleteGroupInput) (*struct{}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		if err := deps.Knowledge.DeleteGroup(ctx, currentActor(ctx), input.GroupID); err != nil {
			return nil, knowledgeError(ctx, "delete user group", err)
		}
		return &struct{}{}, nil
	})

	huma.Get(protected, "/groups/{group}/members", func(ctx context.Context, input *GroupPath) (*struct {
		Body struct {
			Items []GroupMemberBody `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		rows, err := deps.Knowledge.ListGroupMembers(ctx, currentActor(ctx), input.GroupID)
		if err != nil {
			return nil, knowledgeError(ctx, "list group members", err)
		}
		out := &struct {
			Body struct {
				Items []GroupMemberBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]GroupMemberBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, GroupMemberBody{
				UserID: r.UserID, DisplayName: r.DisplayName, CreatedAt: r.CreatedAt,
			})
		}
		return out, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "add-group-member",
		Method:        http.MethodPut,
		Path:          "/groups/{group}/members/{user}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *GroupMemberInput) (*struct{}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		if err := deps.Knowledge.AddGroupMember(ctx, currentActor(ctx), input.GroupID, input.UserID); err != nil {
			return nil, knowledgeError(ctx, "add group member", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "remove-group-member",
		Method:        http.MethodDelete,
		Path:          "/groups/{group}/members/{user}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *GroupMemberInput) (*struct{}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		if err := deps.Knowledge.RemoveGroupMember(ctx, currentActor(ctx), input.GroupID, input.UserID); err != nil {
			return nil, knowledgeError(ctx, "remove group member", err)
		}
		return &struct{}{}, nil
	})

	huma.Get(protected, "/kbs/{kb}/documents/{doc}/acl", func(ctx context.Context, input *DocumentACLPath) (*struct{ Body DocumentACLBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		acl, err := deps.Knowledge.GetDocumentACL(ctx, currentActor(ctx), input.ID, input.DocumentID)
		if err != nil {
			return nil, knowledgeError(ctx, "get document acl", err)
		}
		return &struct{ Body DocumentACLBody }{Body: documentACLBody(acl)}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "set-document-acl",
		Method:          http.MethodPut,
		Path:            "/kbs/{kb}/documents/{doc}/acl",
		MaxBodyBytes:    16384,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *SetDocumentACLInput) (*struct{ Body DocumentACLBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		subjects := make([]kb.ACLSubject, 0, len(input.Body.Subjects))
		for _, sub := range input.Body.Subjects {
			subjects = append(subjects, kb.ACLSubject{Type: kb.ACLSubjectType(sub.Type), ID: sub.ID})
		}
		acl, err := deps.Knowledge.SetDocumentACL(ctx, currentActor(ctx), input.ID, input.DocumentID, kb.ACLMode(input.Body.ACLMode), subjects)
		if err != nil {
			return nil, knowledgeError(ctx, "set document acl", err)
		}
		return &struct{ Body DocumentACLBody }{Body: documentACLBody(acl)}, nil
	})

	type TrustTierService interface {
		SetKnowledgeBaseTrustTier(context.Context, kb.Actor, int64, string) error
		SetDocumentTrustTier(context.Context, kb.Actor, int64, int64, string) error
	}
	huma.Put(protected, "/kbs/{kb}/trust-tier", func(ctx context.Context, input *struct {
		ID   int64  `path:"kb" minimum:"1"`
		CSRF string `header:"X-CSRF-Token" required:"true"`
		Body struct {
			TrustTier string `json:"trust_tier" enum:"trusted,standard,untrusted"`
		}
	}) (*struct{}, error) {
		tt, ok := deps.Knowledge.(TrustTierService)
		if !ok || tt == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "trust tier service unavailable", true)
		}
		if err := tt.SetKnowledgeBaseTrustTier(ctx, currentActor(ctx), input.ID, input.Body.TrustTier); err != nil {
			return nil, knowledgeError(ctx, "set kb trust tier", err)
		}
		return &struct{}{}, nil
	})
	huma.Put(protected, "/kbs/{kb}/documents/{doc}/trust-tier", func(ctx context.Context, input *struct {
		ID         int64  `path:"kb" minimum:"1"`
		DocumentID int64  `path:"doc" minimum:"1"`
		CSRF       string `header:"X-CSRF-Token" required:"true"`
		Body       struct {
			TrustTier string `json:"trust_tier" enum:",trusted,standard,untrusted"`
		}
	}) (*struct{}, error) {
		tt, ok := deps.Knowledge.(TrustTierService)
		if !ok || tt == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "trust tier service unavailable", true)
		}
		if err := tt.SetDocumentTrustTier(ctx, currentActor(ctx), input.ID, input.DocumentID, input.Body.TrustTier); err != nil {
			return nil, knowledgeError(ctx, "set document trust tier", err)
		}
		return &struct{}{}, nil
	})
}

func documentACLBody(acl kb.DocumentACL) DocumentACLBody {
	subjects := make([]ACLSubjectBody, 0, len(acl.Subjects))
	for _, s := range acl.Subjects {
		subjects = append(subjects, ACLSubjectBody{Type: string(s.Type), ID: s.ID})
	}
	return DocumentACLBody{
		DocumentID: acl.DocumentID,
		ACLMode:    string(acl.Mode),
		Revision:   acl.Revision,
		Subjects:   subjects,
	}
}
