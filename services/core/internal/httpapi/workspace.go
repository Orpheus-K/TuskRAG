package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
)

type sessionKey struct{}

type requestSession struct {
	session identity.Session
	token   string
}

type LoginInput struct {
	LoginHeader string `header:"X-TuskRAG-Login" required:"true"`
	Body        struct {
		TenantID int64  `json:"tenant_id" minimum:"1"`
		Login    string `json:"login" minLength:"1" maxLength:"128"`
		Password string `json:"password" minLength:"1" maxLength:"1024"`
	}
}

type IdentityBody struct {
	TenantID int64  `json:"tenant_id"`
	UserID   int64  `json:"user_id"`
	CSRF     string `json:"csrf_token"`
}

type LoginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      IdentityBody
}

type LogoutInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
}

type LogoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type KnowledgeBaseBody struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	EmbeddingSpaceID int64     `json:"embedding_space_id"`
	ConfigRevision   int32     `json:"config_revision"`
	EgressPolicy     string    `json:"egress_policy"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	Role             string    `json:"role"`
}

type CreateKnowledgeBaseInput struct {
	CSRF           string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128"`
	Body           struct {
		Name             string `json:"name" minLength:"1" maxLength:"200"`
		EmbeddingSpaceID int64  `json:"embedding_space_id" minimum:"1"`
		EgressPolicy     string `json:"egress_policy" enum:"deny,internal_only,allow"`
	}
}

type KnowledgeBasePath struct {
	ID int64 `path:"kb" minimum:"1"`
}

type DocumentListInput struct {
	ID     int64 `path:"kb" minimum:"1"`
	Cursor int64 `query:"cursor" minimum:"0" default:"0"`
	Limit  int32 `query:"limit" minimum:"1" maximum:"100" default:"50"`
}

type DocumentBody struct {
	ID               int64     `json:"id"`
	Title            string    `json:"title"`
	MIME             string    `json:"mime"`
	CurrentVersionID *int64    `json:"current_version_id" nullable:"true"`
	LatestVersionID  int64     `json:"latest_version_id"`
	LatestStatus     string    `json:"latest_status"`
	LatestJobID      *int64    `json:"latest_job_id" nullable:"true"`
	Retryable        bool      `json:"retryable"`
	ByteSize         int64     `json:"byte_size"`
	FailureCode      *string   `json:"failure_code" nullable:"true"`
	LatestCreatedAt  time.Time `json:"latest_created_at"`
}

type DocumentListOutput struct {
	Body struct {
		Items      []DocumentBody `json:"items" nullable:"false"`
		NextCursor *int64         `json:"next_cursor" nullable:"true"`
	}
}

type ArchiveKnowledgeBaseInput struct {
	ID   int64  `path:"kb" minimum:"1"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
}

func registerWorkspace(api huma.API, deps Dependencies) {
	loginSlots := make(chan struct{}, 4)
	huma.Register(api, huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/api/v1/auth/login", MaxBodyBytes: 4096, BodyReadTimeout: 10 * time.Second}, func(ctx context.Context, input *LoginInput) (*LoginOutput, error) {
		if input.LoginHeader != "1" {
			return nil, statusError(ctx, http.StatusForbidden, "forbidden", "login request rejected", false)
		}
		if deps.Identity == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "identity unavailable", true)
		}
		select {
		case loginSlots <- struct{}{}:
			defer func() { <-loginSlots }()
		default:
			return nil, huma.ErrorWithHeaders(statusError(ctx, http.StatusTooManyRequests, "rate_limited", "too many login requests", true), http.Header{"Retry-After": []string{"1"}})
		}
		password := []byte(input.Body.Password)
		defer clear(password)
		session, err := deps.Identity.Login(ctx, input.Body.TenantID, input.Body.Login, password)
		if errors.Is(err, identity.ErrUnauthenticated) {
			return nil, statusError(ctx, http.StatusUnauthorized, "unauthenticated", "invalid credentials", false)
		}
		if err != nil {
			return nil, operationError(ctx, "login", err)
		}
		return &LoginOutput{SetCookie: sessionCookie(deps.InsecureLocalCookies, session.Token, false), Body: identityBody(session)}, nil
	})

	if api.OpenAPI().Components.SecuritySchemes == nil {
		api.OpenAPI().Components.SecuritySchemes = make(map[string]*huma.SecurityScheme)
	}
	api.OpenAPI().Components.SecuritySchemes["session"] = &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: "__Host-tuskrag_session"}
	protected := huma.NewGroup(api, "/api/v1")
	protected.UseSimpleModifier(func(operation *huma.Operation) {
		operation.Security = []map[string][]string{{"session": {}}}
	})
	protected.UseMiddleware(requireSession(api, deps))
	huma.Get(protected, "/me", func(ctx context.Context, _ *struct{}) (*struct{ Body IdentityBody }, error) {
		state := ctx.Value(sessionKey{}).(requestSession)
		return &struct{ Body IdentityBody }{Body: identityBody(state.session)}, nil
	})
	huma.Register(protected, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *LogoutInput) (*LogoutOutput, error) {
			state := ctx.Value(sessionKey{}).(requestSession)
			if err := deps.Identity.Logout(ctx, state.token); errors.Is(err, identity.ErrUnauthenticated) {
				return nil, statusError(ctx, http.StatusUnauthorized, "unauthenticated", "session expired", false)
			} else if err != nil {
				return nil, operationError(ctx, "logout", err)
			}
			return &LogoutOutput{SetCookie: sessionCookie(deps.InsecureLocalCookies, "", true)}, nil
		})
	huma.Get(protected, "/kbs", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []KnowledgeBaseBody `json:"items" nullable:"false"`
		}
	}, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		rows, err := deps.Knowledge.List(ctx, currentActor(ctx))
		if err != nil {
			return nil, operationError(ctx, "list knowledge bases", err)
		}
		output := &struct {
			Body struct {
				Items []KnowledgeBaseBody `json:"items" nullable:"false"`
			}
		}{}
		output.Body.Items = make([]KnowledgeBaseBody, 0, len(rows))
		for _, row := range rows {
			output.Body.Items = append(output.Body.Items, knowledgeBaseBody(row))
		}
		return output, nil
	})
	huma.Register(protected, huma.Operation{OperationID: "create-knowledge-base", Method: http.MethodPost, Path: "/kbs", MaxBodyBytes: 4096, BodyReadTimeout: 10 * time.Second}, func(ctx context.Context, input *CreateKnowledgeBaseInput) (*struct{ Body KnowledgeBaseBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		row, err := deps.Knowledge.Create(ctx, currentActor(ctx), kb.CreateRequest{
			Name: input.Body.Name, EmbeddingSpaceID: input.Body.EmbeddingSpaceID, EgressPolicy: models.EgressPolicy(input.Body.EgressPolicy),
			IdempotencyKey: input.IdempotencyKey,
		})
		if err != nil {
			return nil, knowledgeError(ctx, "create knowledge base", err)
		}
		return &struct{ Body KnowledgeBaseBody }{Body: knowledgeBaseBody(row)}, nil
	})
	huma.Get(protected, "/kbs/{kb}", func(ctx context.Context, input *KnowledgeBasePath) (*struct{ Body KnowledgeBaseBody }, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		row, err := deps.Knowledge.Get(ctx, currentActor(ctx), input.ID)
		if err != nil {
			return nil, knowledgeError(ctx, "get knowledge base", err)
		}
		return &struct{ Body KnowledgeBaseBody }{Body: knowledgeBaseBody(row)}, nil
	})
	huma.Get(protected, "/kbs/{kb}/documents", func(ctx context.Context, input *DocumentListInput) (*DocumentListOutput, error) {
		if deps.Knowledge == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
		}
		page, err := deps.Knowledge.ListDocuments(ctx, currentActor(ctx), input.ID, input.Cursor, input.Limit)
		if err != nil {
			return nil, knowledgeError(ctx, "list documents", err)
		}
		output := &DocumentListOutput{}
		output.Body.Items = make([]DocumentBody, 0, len(page.Items))
		output.Body.NextCursor = page.NextAfter
		for _, row := range page.Items {
			output.Body.Items = append(output.Body.Items, DocumentBody{
				ID: row.ID, Title: row.Title, MIME: row.MIME, CurrentVersionID: row.CurrentVersionID,
				LatestVersionID: row.LatestVersionID, LatestStatus: row.LatestStatus, ByteSize: row.ByteSize,
				LatestJobID: row.LatestJobID, Retryable: row.Retryable, FailureCode: row.FailureCode, LatestCreatedAt: row.LatestCreatedAt,
			})
		}
		return output, nil
	})
	registerDocumentUpload(protected, deps)
	registerDocumentDelete(protected, deps)
	registerJobControl(protected, deps)
	registerDocumentReader(protected, deps)
	huma.Register(protected, huma.Operation{OperationID: "archive-knowledge-base", Method: http.MethodDelete, Path: "/kbs/{kb}", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, input *ArchiveKnowledgeBaseInput) (*struct{}, error) {
			if deps.Knowledge == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge base service unavailable", true)
			}
			if err := deps.Knowledge.Archive(ctx, currentActor(ctx), input.ID); err != nil {
				return nil, knowledgeError(ctx, "archive knowledge base", err)
			}
			return &struct{}{}, nil
		})
	registerModelCatalog(protected, deps)
	registerSearch(protected, deps)
	registerAnswers(protected, deps)
	registerConversations(protected, deps)
	registerKnowledgeConfig(protected, deps)
	registerKnowledgeMembers(protected, deps)
	registerEvalReports(protected, deps)
	registerAnswerEvalReports(protected, deps)
	registerEvalSets(protected, deps)
	registerEvalRuns(protected, deps)
	registerRebuilds(protected, deps)
}

func requireSession(api huma.API, deps Dependencies) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if deps.Identity == nil {
			if err := huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "identity unavailable"); err != nil {
				return
			}
			return
		}
		req, _ := humago.Unwrap(ctx)
		cookie, err := req.Cookie(cookieName(deps.InsecureLocalCookies))
		if err != nil {
			if err := huma.WriteErr(api, ctx, http.StatusUnauthorized, "authentication required"); err != nil {
				return
			}
			return
		}
		if ctx.Method() != http.MethodGet && ctx.Method() != http.MethodHead && !identity.ValidCSRF(cookie.Value, ctx.Header("X-CSRF-Token")) {
			if err := huma.WriteErr(api, ctx, http.StatusForbidden, "invalid CSRF token"); err != nil {
				return
			}
			return
		}
		session, err := deps.Identity.Authenticate(ctx.Context(), cookie.Value)
		if errors.Is(err, identity.ErrUnauthenticated) {
			if err := huma.WriteErr(api, ctx, http.StatusUnauthorized, "authentication required"); err != nil {
				return
			}
			return
		}
		if err != nil {
			slog.Error("authenticate session", "trace_id", ctx.Context().Value(traceKey{}), "error", err)
			if err := huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "identity unavailable"); err != nil {
				return
			}
			return
		}
		next(huma.WithValue(ctx, sessionKey{}, requestSession{session: session, token: cookie.Value}))
	}
}

func identityBody(session identity.Session) IdentityBody {
	return IdentityBody{TenantID: session.Principal.TenantID, UserID: session.Principal.UserID, CSRF: session.CSRF}
}

func currentActor(ctx context.Context) kb.Actor {
	principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
	return kb.Actor{TenantID: principal.TenantID, UserID: principal.UserID}
}

func knowledgeBaseBody(row kb.KnowledgeBase) KnowledgeBaseBody {
	return KnowledgeBaseBody{ID: row.ID, Name: row.Name, EmbeddingSpaceID: row.EmbeddingSpaceID,
		ConfigRevision: row.ConfigRevision, EgressPolicy: string(row.EgressPolicy), Status: row.Status, CreatedAt: row.CreatedAt, Role: string(row.Role)}
}

func cookieName(insecure bool) string {
	if insecure {
		return "tuskrag_dev_session"
	}
	return "__Host-tuskrag_session"
}

func sessionCookie(insecure bool, token string, clearCookie bool) http.Cookie {
	maxAge := 7 * 24 * 60 * 60
	if clearCookie {
		maxAge = -1
	}
	return http.Cookie{Name: cookieName(insecure), Value: token, Path: "/", Secure: !insecure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func knowledgeError(ctx context.Context, action string, err error) error {
	switch {
	case errors.Is(err, kb.ErrConfigProviderNotFound):
		return statusError(ctx, http.StatusNotFound, "provider_not_found", "model provider not found", false)
	case errors.Is(err, kb.ErrNotFound):
		return statusError(ctx, http.StatusNotFound, "not_found", "knowledge base not found", false)
	case errors.Is(err, kb.ErrForbidden):
		return statusError(ctx, http.StatusForbidden, "forbidden", "action forbidden", false)
	case errors.Is(err, kb.ErrNameTaken):
		return statusError(ctx, http.StatusConflict, "name_conflict", "knowledge base name already exists", false)
	case errors.Is(err, kb.ErrIdempotencyConflict):
		return statusError(ctx, http.StatusConflict, "idempotency_conflict", "idempotency key used for a different request", false)
	case errors.Is(err, kb.ErrRevisionConflict):
		return statusError(ctx, http.StatusConflict, "revision_conflict", "configuration revision changed", false)
	case errors.Is(err, kb.ErrArchived):
		return statusError(ctx, http.StatusConflict, "knowledge_base_archived", "knowledge base is archived", false)
	case errors.Is(err, kb.ErrInvalidRequest):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid knowledge base request", false)
	case errors.Is(err, kb.ErrLastOwner):
		return statusError(ctx, http.StatusConflict, "last_owner", "knowledge base must retain an owner", false)
	case errors.Is(err, models.ErrEgressDenied):
		return statusError(ctx, http.StatusUnprocessableEntity, "egress_denied", "tenant egress policy denies this setting", false)
	default:
		return operationError(ctx, action, err)
	}
}

func operationError(ctx context.Context, action string, err error) error {
	slog.Error(action, "trace_id", ctx.Value(traceKey{}), "error", err)
	return statusError(ctx, http.StatusInternalServerError, "internal", "internal error", false)
}
