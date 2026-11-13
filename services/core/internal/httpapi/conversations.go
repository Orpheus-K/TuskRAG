package httpapi

import (
	"context"
	"errors"
	"github.com/danielgtaylor/huma/v2"
	"log/slog"
	"net/http"
	"time"
	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type ConversationService interface {
	Create(context.Context, retrieval.Actor, answer.NewConversation) (answer.Conversation, error)
	List(context.Context, retrieval.Actor, int64, int64) (answer.ConversationPage, error)
	Get(context.Context, retrieval.Actor, int64) (answer.ConversationDetail, error)
	Stream(context.Context, retrieval.Actor, int64, string, func(answer.Event) error) error
	PutFeedback(context.Context, retrieval.Actor, string, answer.Feedback) error
}
type CreateConversationInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body answer.NewConversation
}
type ConversationInput struct {
	ID int64 `path:"id" minimum:"1"`
}
type ConversationListInput struct {
	KnowledgeBaseID int64 `path:"kb" minimum:"1"`
	Cursor          int64 `query:"cursor" minimum:"0" default:"0"`
}
type ConversationMessageInput struct {
	ID   int64  `path:"id" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Question string `json:"question" minLength:"1" maxLength:"4096"`
	}
}
type AnswerFeedbackInput struct {
	ID   string `path:"id" minLength:"16" maxLength:"64"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body answer.Feedback
}

func answerActor(ctx context.Context) retrieval.Actor {
	a := currentActor(ctx)
	return retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}
}
func registerConversations(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "create-conversation", Method: http.MethodPost, Path: "/conversations", MaxBodyBytes: 4096}, func(ctx context.Context, in *CreateConversationInput) (*struct{ Body answer.Conversation }, error) {
		if deps.Conversations == nil {
			return nil, conversationError(ctx, models.ErrRuntimeUnavailable)
		}
		c, err := deps.Conversations.Create(ctx, answerActor(ctx), in.Body)
		if err != nil {
			return nil, conversationError(ctx, err)
		}
		return &struct{ Body answer.Conversation }{Body: c}, nil
	})
	huma.Get(api, "/kbs/{kb}/conversations", func(ctx context.Context, in *ConversationListInput) (*struct{ Body answer.ConversationPage }, error) {
		if deps.Conversations == nil {
			return nil, conversationError(ctx, models.ErrRuntimeUnavailable)
		}
		c, err := deps.Conversations.List(ctx, answerActor(ctx), in.KnowledgeBaseID, in.Cursor)
		if err != nil {
			return nil, conversationError(ctx, err)
		}
		return &struct{ Body answer.ConversationPage }{Body: c}, nil
	})
	huma.Get(api, "/conversations/{id}", func(ctx context.Context, in *ConversationInput) (*struct{ Body answer.ConversationDetail }, error) {
		if deps.Conversations == nil {
			return nil, conversationError(ctx, models.ErrRuntimeUnavailable)
		}
		c, err := deps.Conversations.Get(ctx, answerActor(ctx), in.ID)
		if err != nil {
			return nil, conversationError(ctx, err)
		}
		return &struct{ Body answer.ConversationDetail }{Body: c}, nil
	})
	registerAnswerStream(api, deps, huma.Operation{OperationID: "conversation-answer", Method: http.MethodPost, Path: "/conversations/{id}/messages", MaxBodyBytes: 16 * 1024, BodyReadTimeout: 10 * time.Second}, func(ctx context.Context, a retrieval.Actor, in *ConversationMessageInput, emit func(answer.Event) error) error {
		if deps.Conversations == nil {
			return models.ErrRuntimeUnavailable
		}
		return deps.Conversations.Stream(ctx, a, in.ID, in.Body.Question, emit)
	})
	huma.Register(api, huma.Operation{OperationID: "answer-feedback", Method: http.MethodPost, Path: "/messages/{id}/feedback", MaxBodyBytes: 16 * 1024}, func(ctx context.Context, in *AnswerFeedbackInput) (*struct {
		Body struct {
			Status string `json:"status"`
		}
	}, error) {
		if deps.Conversations == nil {
			return nil, conversationError(ctx, models.ErrRuntimeUnavailable)
		}
		if err := deps.Conversations.PutFeedback(ctx, answerActor(ctx), in.ID, in.Body); err != nil {
			return nil, conversationError(ctx, err)
		}
		out := &struct {
			Body struct {
				Status string `json:"status"`
			}
		}{}
		out.Body.Status = "pending"
		return out, nil
	})
}
func conversationError(ctx context.Context, err error) error {
	if errors.Is(err, retrieval.ErrNotFound) {
		return statusError(ctx, 404, "not_found", "会话不存在或不可见", false)
	}
	if errors.Is(err, retrieval.ErrEvidenceChanged) {
		return statusError(ctx, 409, "evidence_unavailable", "历史引用已变化，请新建会话", false)
	}
	if errors.Is(err, retrieval.ErrInvalidQuery) {
		return statusError(ctx, 422, "invalid_request", "会话请求无效", false)
	}
	if errors.Is(err, models.ErrRuntimeUnavailable) {
		return statusError(ctx, 503, "dependency_unavailable", "会话服务不可用", true)
	}
	// PostgreSQL constraint details may contain stored questions or answer text.
	slog.Error("conversation operation failed", "trace_id", ctx.Value(traceKey{}))
	return statusError(ctx, 500, "internal", "会话操作未完成", false)
}
