package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/identity"
)

type APIKeyBody struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Prefix            string     `json:"prefix"`
	KBIDs             []int64    `json:"kb_ids" nullable:"false"`
	Actions           []string   `json:"actions" nullable:"false"`
	IPAllowlist       []string   `json:"ip_allowlist" nullable:"false"`
	RateLimitRPM      int32      `json:"rate_limit_rpm"`
	MonthlyTokenQuota int64      `json:"monthly_token_quota"`
	MonthTokensUsed   int64      `json:"month_tokens_used"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type CreateAPIKeyInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Name              string     `json:"name" minLength:"1" maxLength:"120"`
		KBIDs             []int64    `json:"kb_ids,omitempty"`
		Actions           []string   `json:"actions" minItems:"1"`
		IPAllowlist       []string   `json:"ip_allowlist,omitempty"`
		RateLimitRPM      int32      `json:"rate_limit_rpm,omitempty"`
		MonthlyTokenQuota int64      `json:"monthly_token_quota,omitempty"`
		ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	}
}

type CreateAPIKeyOutput struct {
	Body struct {
		Key       APIKeyBody `json:"key"`
		Plaintext string     `json:"plaintext"`
	}
}

type RevokeAPIKeyInput struct {
	KeyID int64  `path:"key" minimum:"1"`
	CSRF  string `header:"X-CSRF-Token" required:"true"`
}

func resolveAPIKeyService(deps Dependencies) APIKeyService {
	if deps.APIKeys != nil {
		return deps.APIKeys
	}
	if svc, ok := deps.Identity.(APIKeyService); ok {
		return svc
	}
	return nil
}

func registerAPIKeys(protected *huma.Group, deps Dependencies) {
	huma.Get(protected, "/api-keys", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []APIKeyBody `json:"items" nullable:"false"`
		}
	}, error) {
		svc := resolveAPIKeyService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "api key service unavailable", true)
		}
		actor := currentActor(ctx)
		rows, err := svc.ListAPIKeys(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID})
		if err != nil {
			return nil, operationError(ctx, "list api keys", err)
		}
		out := &struct {
			Body struct {
				Items []APIKeyBody `json:"items" nullable:"false"`
			}
		}{}
		out.Body.Items = make([]APIKeyBody, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, apiKeyBody(r))
		}
		return out, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:     "create-api-key",
		Method:          http.MethodPost,
		Path:            "/api-keys",
		MaxBodyBytes:    8192,
		BodyReadTimeout: 10 * time.Second,
	}, func(ctx context.Context, input *CreateAPIKeyInput) (*CreateAPIKeyOutput, error) {
		svc := resolveAPIKeyService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "api key service unavailable", true)
		}
		actor := currentActor(ctx)
		created, err := svc.CreateAPIKey(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, identity.CreateAPIKeyRequest{
			Name:              input.Body.Name,
			KBIDs:             input.Body.KBIDs,
			Actions:           input.Body.Actions,
			IPAllowlist:       input.Body.IPAllowlist,
			RateLimitRPM:      input.Body.RateLimitRPM,
			MonthlyTokenQuota: input.Body.MonthlyTokenQuota,
			ExpiresAt:         input.Body.ExpiresAt,
		})
		if errors.Is(err, identity.ErrInvalidAPIKeyRequest) {
			return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid api key request", false)
		}
		if err != nil {
			return nil, operationError(ctx, "create api key", err)
		}
		out := &CreateAPIKeyOutput{}
		out.Body.Key = apiKeyBody(created.Key)
		out.Body.Plaintext = created.Plaintext
		return out, nil
	})

	huma.Register(protected, huma.Operation{
		OperationID:   "revoke-api-key",
		Method:        http.MethodDelete,
		Path:          "/api-keys/{key}",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *RevokeAPIKeyInput) (*struct{}, error) {
		svc := resolveAPIKeyService(deps)
		if svc == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "api key service unavailable", true)
		}
		actor := currentActor(ctx)
		err := svc.RevokeAPIKey(ctx, identity.Principal{TenantID: actor.TenantID, UserID: actor.UserID}, input.KeyID)
		if errors.Is(err, identity.ErrAPIKeyNotFound) {
			return nil, statusError(ctx, http.StatusNotFound, "not_found", "api key not found", false)
		}
		if errors.Is(err, identity.ErrInvalidAPIKeyRequest) {
			return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid api key id", false)
		}
		if err != nil {
			return nil, operationError(ctx, "revoke api key", err)
		}
		return &struct{}{}, nil
	})
}

func apiKeyBody(k identity.APIKey) APIKeyBody {
	kbIDs := k.KBIDs
	if kbIDs == nil {
		kbIDs = []int64{}
	}
	actions := k.Actions
	if actions == nil {
		actions = []string{}
	}
	ips := k.IPAllowlist
	if ips == nil {
		ips = []string{}
	}
	return APIKeyBody{
		ID:                k.ID,
		Name:              k.Name,
		Prefix:            k.Prefix,
		KBIDs:             kbIDs,
		Actions:           actions,
		IPAllowlist:       ips,
		RateLimitRPM:      k.RateLimitRPM,
		MonthlyTokenQuota: k.MonthlyTokenQuota,
		MonthTokensUsed:   k.MonthTokensUsed,
		ExpiresAt:         k.ExpiresAt,
		RevokedAt:         k.RevokedAt,
		LastUsedAt:        k.LastUsedAt,
		CreatedAt:         k.CreatedAt,
	}
}

func clientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(req.RemoteAddr)
}

func requestScopeForAPIKey(method, path string) (int64, string, bool) {
	if strings.HasPrefix(path, "/api/v1/api-keys") {
		return 0, "", false
	}
	var kbID int64
	if strings.HasPrefix(path, "/api/v1/kbs/") {
		rest := strings.TrimPrefix(path, "/api/v1/kbs/")
		seg, _, _ := strings.Cut(rest, "/")
		if parsed, err := strconv.ParseInt(seg, 10, 64); err == nil && parsed > 0 {
			kbID = parsed
		}
	}
	switch {
	case strings.Contains(path, "/search"):
		return kbID, "search", true
	case strings.Contains(path, "/answers") || strings.Contains(path, "/conversations"):
		return kbID, "answer", true
	case strings.Contains(path, "/documents") && (method == http.MethodPost || method == http.MethodDelete):
		return kbID, "ingest", true
	case method == http.MethodGet || method == http.MethodHead:
		return kbID, "read", true
	default:
		return kbID, "", false
	}
}
