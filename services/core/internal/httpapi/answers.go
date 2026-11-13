package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
)

type AnswerService interface {
	Stream(context.Context, retrieval.Actor, answer.Request, func(answer.Event) error) error
}
type ChatCatalog interface {
	CreateChatModel(context.Context, int64, int64, models.NewChatModel) (models.ChatModel, error)
	ListChatModels(context.Context, int64, int64) ([]models.ChatModel, error)
}
type ChatModelInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body models.NewChatModel
}
type AnswerInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body answer.Request
}
type AnswerError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func registerAnswers(api huma.API, deps Dependencies) {
	huma.Get(api, "/chat-models", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []models.ChatModel `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.ChatModels == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "chat configuration unavailable", false)
		}
		a := currentActor(ctx)
		rows, err := deps.ChatModels.ListChatModels(ctx, a.TenantID, a.UserID)
		if err != nil {
			return nil, modelCatalogError(ctx, "list chat models", err)
		}
		result := &struct {
			Body struct {
				Items []models.ChatModel `json:"items" nullable:"false"`
			}
		}{}
		result.Body.Items = rows
		return result, nil
	})
	huma.Register(api, huma.Operation{OperationID: "create-chat-model", Method: http.MethodPost, Path: "/admin/chat-models", MaxBodyBytes: 8192, BodyReadTimeout: 10 * time.Second}, func(ctx context.Context, in *ChatModelInput) (*struct{ Body models.ChatModel }, error) {
		if deps.ChatModels == nil {
			return nil, statusError(ctx, 503, "dependency_unavailable", "chat configuration unavailable", false)
		}
		a := currentActor(ctx)
		model, err := deps.ChatModels.CreateChatModel(ctx, a.TenantID, a.UserID, in.Body)
		if err != nil {
			return nil, modelCatalogError(ctx, "create chat model", err)
		}
		return &struct{ Body models.ChatModel }{Body: model}, nil
	})
	registerAnswerStream(api, deps, huma.Operation{OperationID: "answer", Method: http.MethodPost, Path: "/answers", MaxBodyBytes: 16 * 1024, BodyReadTimeout: 10 * time.Second}, func(ctx context.Context, a retrieval.Actor, in *AnswerInput, emit func(answer.Event) error) error {
		if deps.Answers == nil {
			return models.ErrRuntimeUnavailable
		}
		return deps.Answers.Stream(ctx, a, in.Body, emit)
	})
}

func registerAnswerStream[I any](api huma.API, deps Dependencies, operation huma.Operation, run func(context.Context, retrieval.Actor, *I, func(answer.Event) error) error) {
	streamAPI := huma.NewGroup(api)
	streamAPI.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		ctx.SetHeader("Cache-Control", "no-store")
		ctx.SetHeader("X-Accel-Buffering", "no")
		next(ctx)
	})
	sse.Register(streamAPI, operation, map[string]any{
		"retrieval": answer.RetrievalEvent{}, "step": answer.StepEvent{}, "delta": answer.Delta{}, "citations": answer.Citations{}, "done": answer.Done{}, "error": AnswerError{},
	}, func(ctx context.Context, in *I, send sse.Sender) {
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		session := ctx.Value(sessionKey{}).(requestSession)
		a := session.session.Principal
		events := make(chan answer.Event)
		finished := make(chan error, 1)
		go func() {
			finished <- run(ctx, retrieval.Actor{TenantID: a.TenantID, UserID: a.UserID}, in, func(event answer.Event) error {
				select {
				case events <- event:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		joined := false
		defer func() {
			cancel()
			if !joined {
				// The response is already closed; report only sanitized failure metadata.
				if err := <-finished; err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("answer stream stopped", "code", answerStreamError(err).Code, "trace_id", ctx.Value(traceKey{}))
				}
			}
		}()
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		authorization := time.NewTicker(time.Second)
		defer authorization.Stop()
		check := func() error { _, err := deps.Identity.Authenticate(ctx, session.token); return err }
		for {
			select {
			case <-ctx.Done():
				if !errors.Is(ctx.Err(), context.Canceled) {
					_ = send.Data(answerStreamError(ctx.Err()))
				}
				return
			case <-authorization.C:
				if err := check(); err != nil {
					_ = send.Data(answerStreamError(err))
					return
				}
			case <-heartbeat.C:
				if err := send.Comment("heartbeat"); err != nil {
					return
				}
			case err := <-finished:
				joined = true
				if err != nil {
					_ = send.Data(answerStreamError(err))
				}
				return
			case event := <-events:
				if err := check(); err != nil {
					_ = send.Data(answerStreamError(err))
					return
				}
				var err error
				switch {
				case event.Retrieval != nil:
					err = send.Data(*event.Retrieval)
				case event.Step != nil:
					err = send.Data(*event.Step)
				case event.Delta != nil:
					err = send.Data(*event.Delta)
				case event.Citations != nil:
					err = send.Data(*event.Citations)
				case event.Done != nil:
					err = send.Data(*event.Done)
				}
				if err != nil {
					return
				}
			}
		}
	})
}

func answerStreamError(err error) AnswerError {
	var upstream *models.UpstreamError
	switch {
	case errors.Is(err, answer.ErrConversationBusy):
		return AnswerError{Code: "conversation_busy", Message: "此会话已有正在生成的回答，请等待或刷新"}
	case errors.Is(err, answer.ErrConversationFull):
		return AnswerError{Code: "conversation_full", Message: "会话已达到 50 轮，请新建会话"}
	case errors.Is(err, identity.ErrUnauthenticated):
		return AnswerError{Code: "unauthenticated", Message: "登录已过期，请重新登录"}
	case errors.Is(err, retrieval.ErrNotFound), errors.Is(err, retrieval.ErrEvidenceChanged):
		return AnswerError{Code: "evidence_unavailable", Message: "权限或原文版本已变化，请重新提问"}
	case errors.Is(err, context.DeadlineExceeded):
		return AnswerError{Code: "timeout", Message: "回答超时，请重试", Retryable: true}
	case errors.Is(err, context.Canceled):
		return AnswerError{Code: "cancelled", Message: "回答已取消"}
	case errors.Is(err, models.ErrEgressDenied):
		return AnswerError{Code: "egress_denied", Message: "知识库策略不允许调用此模型"}
	case errors.Is(err, models.ErrInvalidResponse):
		return AnswerError{Code: "model_invalid_response", Message: "模型返回了无效或不完整的回答"}
	case errors.Is(err, models.ErrRuntimeUnavailable), errors.Is(err, secrets.ErrKeyUnavailable), errors.Is(err, secrets.ErrInvalidSecret):
		return AnswerError{Code: "model_unavailable", Message: "回答模型配置不可用"}
	case errors.Is(err, models.ErrRuntimeChanged), errors.Is(err, retrieval.ErrBindingChanged):
		return AnswerError{Code: "configuration_changed", Message: "模型配置已变化，请重试", Retryable: true}
	case errors.Is(err, retrieval.ErrInvalidQuery):
		return AnswerError{Code: "invalid_request", Message: "问题无效或过长"}
	case errors.As(err, &upstream):
		return AnswerError{Code: "model_unavailable", Message: "模型服务不可用", Retryable: upstream.Retryable}
	default:
		return AnswerError{Code: "internal_error", Message: "回答未完成，请重试"}
	}
}
