package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/secrets"
)

type EmbeddingSpaceBody struct {
	ID            int64  `json:"id"`
	ProviderID    int64  `json:"provider_id"`
	ProviderName  string `json:"provider_name"`
	BaseURL       string `json:"base_url"`
	Locality      string `json:"locality"`
	Model         string `json:"model"`
	Dimensions    int32  `json:"dimensions"`
	Normalized    bool   `json:"normalized"`
	Distance      string `json:"distance"`
	HasCredential bool   `json:"has_credential"`
}

type CreateEmbeddingSpaceInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		ProviderName string `json:"provider_name" minLength:"1" maxLength:"200"`
		BaseURL      string `json:"base_url" minLength:"1" maxLength:"2048"`
		Locality     string `json:"locality" enum:"internal,external"`
		APIKey       string `json:"api_key,omitempty" maxLength:"4096"`
		Model        string `json:"model" minLength:"1" maxLength:"200"`
		Dimensions   int32  `json:"dimensions" minimum:"64" maximum:"4000"`
		Normalized   bool   `json:"normalized"`
		Distance     string `json:"distance" enum:"cosine,ip"`
	}
}

type EmbeddingSpaceList struct {
	Items []EmbeddingSpaceBody `json:"items" nullable:"false"`
}

func registerModelCatalog(api huma.API, deps Dependencies) {
	huma.Get(api, "/admin/embedding-spaces", func(ctx context.Context, _ *struct{}) (*struct{ Body EmbeddingSpaceList }, error) {
		if deps.Models == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model configuration unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		spaces, err := deps.Models.ListSpaces(ctx, principal.TenantID, principal.UserID)
		if err != nil {
			return nil, modelCatalogError(ctx, "list embedding spaces", err)
		}
		result := EmbeddingSpaceList{Items: make([]EmbeddingSpaceBody, 0, len(spaces))}
		for _, space := range spaces {
			result.Items = append(result.Items, embeddingSpaceBody(space))
		}
		return &struct{ Body EmbeddingSpaceList }{Body: result}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "create-embedding-space", Method: http.MethodPost, Path: "/admin/embedding-spaces", MaxBodyBytes: 8192, BodyReadTimeout: 10 * time.Second},
		func(ctx context.Context, input *CreateEmbeddingSpaceInput) (*struct{ Body EmbeddingSpaceBody }, error) {
			if deps.Models == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model configuration unavailable", true)
			}
			principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
			key := []byte(input.Body.APIKey)
			defer clear(key)
			space, err := deps.Models.CreateSpace(ctx, principal.TenantID, principal.UserID, models.NewSpace{
				ProviderName: input.Body.ProviderName, BaseURL: input.Body.BaseURL, Locality: models.Locality(input.Body.Locality),
				APIKey: key, Model: input.Body.Model, Dimensions: input.Body.Dimensions, Normalized: input.Body.Normalized, Distance: input.Body.Distance,
			})
			if err != nil {
				return nil, modelCatalogError(ctx, "create embedding space", err)
			}
			return &struct{ Body EmbeddingSpaceBody }{Body: embeddingSpaceBody(space)}, nil
		})
	registerModelProviders(api, deps)
}

type ModelProviderBody struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	BaseURL       string    `json:"base_url"`
	Locality      string    `json:"locality"`
	HasCredential bool      `json:"has_credential"`
	CreatedAt     time.Time `json:"created_at"`
}

type CreateModelProviderInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Name     string `json:"name" minLength:"1" maxLength:"200"`
		BaseURL  string `json:"base_url" minLength:"1" maxLength:"2048"`
		Locality string `json:"locality" enum:"internal,external"`
		APIKey   string `json:"api_key,omitempty" maxLength:"4096"`
	}
}

type UpdateModelProviderInput struct {
	ProviderID int64  `path:"provider" minimum:"1"`
	CSRF       string `header:"X-CSRF-Token" required:"true"`
	Body       struct {
		Name     string `json:"name" minLength:"1" maxLength:"200"`
		BaseURL  string `json:"base_url" minLength:"1" maxLength:"2048"`
		Locality string `json:"locality" enum:"internal,external"`
		APIKey   string `json:"api_key,omitempty" maxLength:"4096"`
	}
}

type ProviderIDInput struct {
	ProviderID int64  `path:"provider" minimum:"1"`
	CSRF       string `header:"X-CSRF-Token" required:"true"`
}

type ProviderConnectivityBody struct {
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	Message    string `json:"message"`
}

func resolveModelProviders(deps Dependencies) ModelProviderCatalog {
	if deps.ModelProviders != nil {
		return deps.ModelProviders
	}
	if p, ok := deps.Models.(ModelProviderCatalog); ok {
		return p
	}
	return nil
}

func modelProviderBody(p models.ProviderRecord) ModelProviderBody {
	return ModelProviderBody{
		ID:            p.ID,
		Name:          p.Name,
		BaseURL:       p.BaseURL,
		Locality:      string(p.Locality),
		HasCredential: p.HasCredential,
		CreatedAt:     p.CreatedAt,
	}
}

func registerModelProviders(api huma.API, deps Dependencies) {
	huma.Get(api, "/admin/model-providers", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []ModelProviderBody `json:"items" nullable:"false"`
		}
	}, error) {
		svc := resolveModelProviders(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model provider catalog unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		rows, err := svc.ListProviders(ctx, principal.TenantID, principal.UserID)
		if err != nil {
			return nil, modelCatalogError(ctx, "list model providers", err)
		}
		out := &struct {
			Body struct {
				Items []ModelProviderBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]ModelProviderBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, modelProviderBody(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:     "create-model-provider",
		Method:          http.MethodPost,
		Path:            "/admin/model-providers",
		MaxBodyBytes:    8192,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *CreateModelProviderInput) (*struct{ Body ModelProviderBody }, error) {
		svc := resolveModelProviders(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model provider catalog unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		key := []byte(input.Body.APIKey)
		defer clear(key)
		p, err := svc.CreateProvider(ctx, principal.TenantID, principal.UserID, models.ProviderInput{
			Name:     input.Body.Name,
			BaseURL:  input.Body.BaseURL,
			Locality: models.Locality(input.Body.Locality),
			APIKey:   key,
		})
		if err != nil {
			return nil, modelCatalogError(ctx, "create model provider", err)
		}
		return &struct{ Body ModelProviderBody }{Body: modelProviderBody(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:     "update-model-provider",
		Method:          http.MethodPut,
		Path:            "/admin/model-providers/{provider}",
		MaxBodyBytes:    8192,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *UpdateModelProviderInput) (*struct{ Body ModelProviderBody }, error) {
		svc := resolveModelProviders(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model provider catalog unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		key := []byte(input.Body.APIKey)
		defer clear(key)
		p, err := svc.UpdateProvider(ctx, principal.TenantID, principal.UserID, input.ProviderID, models.ProviderInput{
			Name:     input.Body.Name,
			BaseURL:  input.Body.BaseURL,
			Locality: models.Locality(input.Body.Locality),
			APIKey:   key,
		})
		if err != nil {
			return nil, modelCatalogError(ctx, "update model provider", err)
		}
		return &struct{ Body ModelProviderBody }{Body: modelProviderBody(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-model-provider",
		Method:        http.MethodDelete,
		Path:          "/admin/model-providers/{provider}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *ProviderIDInput) (*struct{}, error) {
		svc := resolveModelProviders(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model provider catalog unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		if err := svc.DeleteProvider(ctx, principal.TenantID, principal.UserID, input.ProviderID); err != nil {
			return nil, modelCatalogError(ctx, "delete model provider", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:     "test-model-provider",
		Method:          http.MethodPost,
		Path:            "/admin/model-providers/{provider}/test",
		MaxBodyBytes:    1024,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *ProviderIDInput) (*struct{ Body ProviderConnectivityBody }, error) {
		svc := resolveModelProviders(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "model provider catalog unavailable", true)
		}
		principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
		res, err := svc.TestProviderConnectivity(ctx, principal.TenantID, principal.UserID, input.ProviderID)
		if err != nil {
			return nil, modelCatalogError(ctx, "test model provider", err)
		}
		return &struct{ Body ProviderConnectivityBody }{Body: ProviderConnectivityBody{
			Reachable:  res.Reachable,
			StatusCode: res.StatusCode,
			LatencyMS:  res.LatencyMS,
			Message:    res.Message,
		}}, nil
	})
}

func embeddingSpaceBody(space models.Space) EmbeddingSpaceBody {
	return EmbeddingSpaceBody{ID: space.ID, ProviderID: space.ProviderID, ProviderName: space.ProviderName,
		BaseURL: space.BaseURL, Locality: string(space.Locality), Model: space.Model, Dimensions: space.Dimensions,
		Normalized: space.Normalized, Distance: space.Distance, HasCredential: space.HasCredential}
}

func modelCatalogError(ctx context.Context, action string, err error) error {
	switch {
	case errors.Is(err, models.ErrForbidden):
		return statusError(ctx, http.StatusForbidden, "forbidden", "administrator access required", false)
	case errors.Is(err, models.ErrInvalidConfiguration):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid model configuration", false)
	case errors.Is(err, models.ErrProviderNotFound):
		return statusError(ctx, http.StatusNotFound, "not_found", "model provider not found", false)
	case errors.Is(err, models.ErrProviderInUse):
		return statusError(ctx, http.StatusConflict, "provider_in_use", "model provider is still in use", false)
	case errors.Is(err, models.ErrProviderNameUsed):
		return statusError(ctx, http.StatusConflict, "name_conflict", "provider name already exists", false)
	case errors.Is(err, secrets.ErrKeyUnavailable):
		return statusError(ctx, http.StatusServiceUnavailable, "secret_unavailable", "master key is not configured", false)
	default:
		return operationError(ctx, action, err)
	}
}
