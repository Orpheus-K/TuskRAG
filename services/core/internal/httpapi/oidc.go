package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/secrets"
)

type PublicOIDCProviderBody struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TenantAuthPolicyBody struct {
	DisableLocalLogin bool                     `json:"disable_local_login"`
	Providers         []PublicOIDCProviderBody `json:"providers" nullable:"false"`
}

type OIDCProviderBody struct {
	ID              int64     `json:"id"`
	TenantID        int64     `json:"tenant_id"`
	Name            string    `json:"name"`
	IssuerURL       string    `json:"issuer_url"`
	ClientID        string    `json:"client_id"`
	HasClientSecret bool      `json:"has_client_secret"`
	Scopes          string    `json:"scopes"`
	GroupClaim      string    `json:"group_claim"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

type CreateOIDCProviderInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Name         string `json:"name" minLength:"1" maxLength:"120"`
		IssuerURL    string `json:"issuer_url" minLength:"1" maxLength:"2048"`
		ClientID     string `json:"client_id" minLength:"1" maxLength:"256"`
		ClientSecret string `json:"client_secret,omitempty" maxLength:"4096"`
		Scopes       string `json:"scopes,omitempty" maxLength:"512"`
		GroupClaim   string `json:"group_claim,omitempty" maxLength:"120"`
		Enabled      *bool  `json:"enabled,omitempty"`
	}
}

type OIDCProviderPathInput struct {
	ProviderID int64  `path:"provider" minimum:"1"`
	CSRF       string `header:"X-CSRF-Token" required:"true"`
}

type UpdateAuthPolicyInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		DisableLocalLogin bool `json:"disable_local_login"`
	}
}

type StartOIDCInput struct {
	ProviderID  int64  `path:"provider" minimum:"1"`
	TenantID    int64  `query:"tenant_id" minimum:"1"`
	RedirectURI string `query:"redirect_uri" minLength:"1"`
}

type OIDCCallbackInput struct {
	State string `query:"state" minLength:"1"`
	Code  string `query:"code" minLength:"1"`
}

type OIDCCallbackOutput struct {
	Status    int         `status:"302"`
	Location  string      `header:"Location"`
	SetCookie http.Cookie `header:"Set-Cookie"`
}

func resolveOIDCService(deps Dependencies) OIDCService {
	if deps.OIDC != nil {
		return deps.OIDC
	}
	if svc, ok := deps.Identity.(OIDCService); ok {
		return svc
	}
	return nil
}

func oidcError(ctx context.Context, action string, err error) error {
	switch {
	case errors.Is(err, identity.ErrAdminRequired):
		return statusError(ctx, http.StatusForbidden, "forbidden", "administrator access required", false)
	case errors.Is(err, identity.ErrInvalidOIDCRequest):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid OIDC request", false)
	case errors.Is(err, identity.ErrOIDCProviderUsed):
		return statusError(ctx, http.StatusConflict, "name_conflict", "OIDC provider already exists", false)
	case errors.Is(err, identity.ErrOIDCNotFound):
		return statusError(ctx, http.StatusNotFound, "not_found", "OIDC provider not found", false)
	case errors.Is(err, identity.ErrOIDCCannotDisable):
		return statusError(ctx, http.StatusConflict, "oidc_required", "enable an OIDC provider before disabling local login", false)
	case errors.Is(err, identity.ErrUnauthenticated):
		return statusError(ctx, http.StatusUnauthorized, "unauthenticated", "OIDC authentication failed", false)
	case errors.Is(err, secrets.ErrKeyUnavailable):
		return statusError(ctx, http.StatusServiceUnavailable, "secret_unavailable", "master key is not configured", false)
	default:
		return operationError(ctx, action, err)
	}
}

func oidcProviderBody(p identity.OIDCProvider) OIDCProviderBody {
	return OIDCProviderBody{
		ID:              p.ID,
		TenantID:        p.TenantID,
		Name:            p.Name,
		IssuerURL:       p.IssuerURL,
		ClientID:        p.ClientID,
		HasClientSecret: p.HasClientSecret,
		Scopes:          p.Scopes,
		GroupClaim:      p.GroupClaim,
		Enabled:         p.Enabled,
		CreatedAt:       p.CreatedAt,
	}
}

func registerOIDC(api huma.API, protected *huma.Group, deps Dependencies) {
	huma.Get(api, "/api/v1/auth/oidc/providers", func(ctx context.Context, input *struct {
		TenantID int64 `query:"tenant_id" minimum:"1"`
	}) (*struct{ Body TenantAuthPolicyBody }, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		policy, err := svc.GetTenantAuthPolicy(ctx, input.TenantID)
		if err != nil {
			return nil, oidcError(ctx, "get public oidc providers", err)
		}
		providers := make([]PublicOIDCProviderBody, 0, len(policy.Providers))
		for _, p := range policy.Providers {
			providers = append(providers, PublicOIDCProviderBody{ID: p.ID, Name: p.Name})
		}
		return &struct{ Body TenantAuthPolicyBody }{
			Body: TenantAuthPolicyBody{
				DisableLocalLogin: policy.DisableLocalLogin,
				Providers:         providers,
			},
		}, nil
	})

	huma.Get(api, "/api/v1/auth/oidc/{provider}/start", func(ctx context.Context, input *StartOIDCInput) (*struct {
		Body struct {
			AuthorizeURL string `json:"authorize_url"`
		}
	}, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		authURL, err := svc.StartOIDCLogin(ctx, input.TenantID, input.ProviderID, input.RedirectURI)
		if err != nil {
			return nil, oidcError(ctx, "start oidc login", err)
		}
		out := &struct {
			Body struct {
				AuthorizeURL string `json:"authorize_url"`
			}
		}{}
		out.Body.AuthorizeURL = authURL
		return out, nil
	})

	huma.Get(api, "/api/v1/auth/oidc/callback", func(ctx context.Context, input *OIDCCallbackInput) (*OIDCCallbackOutput, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		session, err := svc.CompleteOIDCLogin(ctx, input.State, input.Code)
		if err != nil {
			return nil, oidcError(ctx, "complete oidc login", err)
		}
		return &OIDCCallbackOutput{
			Status:    http.StatusFound,
			Location:  "/",
			SetCookie: sessionCookie(deps.InsecureLocalCookies, session.Token, false),
		}, nil
	})

	huma.Get(protected, "/admin/oidc-providers", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []OIDCProviderBody `json:"items" nullable:"false"`
		}
	}, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		actor := currentActor(ctx)
		rows, err := svc.ListOIDCProviders(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID})
		if err != nil {
			return nil, oidcError(ctx, "list oidc providers", err)
		}
		out := &struct {
			Body struct {
				Items []OIDCProviderBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]OIDCProviderBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, oidcProviderBody(r))
		}
		return out, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "create-oidc-provider",
		Method:          http.MethodPost,
		Path:            "/admin/oidc-providers",
		MaxBodyBytes:    8192,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *CreateOIDCProviderInput) (*struct{ Body OIDCProviderBody }, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		enabled := true
		if input.Body.Enabled != nil {
			enabled = *input.Body.Enabled
		}
		actor := currentActor(ctx)
		created, err := svc.CreateOIDCProvider(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, identity.CreateOIDCProviderRequest{
			Name:         input.Body.Name,
			IssuerURL:    input.Body.IssuerURL,
			ClientID:     input.Body.ClientID,
			ClientSecret: input.Body.ClientSecret,
			Scopes:       input.Body.Scopes,
			GroupClaim:   input.Body.GroupClaim,
			Enabled:      enabled,
		})
		if err != nil {
			return nil, oidcError(ctx, "create oidc provider", err)
		}
		return &struct{ Body OIDCProviderBody }{Body: oidcProviderBody(created)}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "delete-oidc-provider",
		Method:        http.MethodDelete,
		Path:          "/admin/oidc-providers/{provider}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *OIDCProviderPathInput) (*struct{}, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		actor := currentActor(ctx)
		if err := svc.DeleteOIDCProvider(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, input.ProviderID); err != nil {
			return nil, oidcError(ctx, "delete oidc provider", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "update-auth-policy",
		Method:          http.MethodPut,
		Path:            "/admin/auth-policy",
		MaxBodyBytes:    2048,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *UpdateAuthPolicyInput) (*struct {
		Body struct {
			DisableLocalLogin bool `json:"disable_local_login"`
		}
	}, error) {
		svc := resolveOIDCService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "oidc service unavailable", true)
		}
		actor := currentActor(ctx)
		disabled, err := svc.SetDisableLocalLogin(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, input.Body.DisableLocalLogin)
		if err != nil {
			return nil, oidcError(ctx, "update auth policy", err)
		}
		out := &struct {
			Body struct {
				DisableLocalLogin bool `json:"disable_local_login"`
			}
		}{}
		out.Body.DisableLocalLogin = disabled
		return out, nil
	})
}
