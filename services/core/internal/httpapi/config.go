package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

type PutKnowledgeConfigInput struct {
	ID      int64  `path:"kb" minimum:"1"`
	CSRF    string `header:"X-CSRF-Token" required:"true"`
	IfMatch string `header:"If-Match" required:"true" maxLength:"12"`
	Body    struct {
		Retrieval kb.RetrievalConfig `json:"retrieval"`
	}
}

type KnowledgeConfigOutput struct {
	ETag string `header:"ETag"`
	Body kb.KnowledgeConfig
}

func registerKnowledgeConfig(api huma.API, deps Dependencies) {
	huma.Get(api, "/kbs/{kb}/config", func(ctx context.Context, input *KnowledgeBasePath) (*KnowledgeConfigOutput, error) {
		if deps.Config == nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "configuration service unavailable", true)
		}
		config, err := deps.Config.GetConfig(ctx, currentActor(ctx), input.ID)
		if err != nil {
			return nil, knowledgeError(ctx, "read knowledge base configuration", err)
		}
		return configOutput(config), nil
	})
	huma.Register(api, huma.Operation{OperationID: "put-knowledge-config", Method: http.MethodPut, Path: "/kbs/{kb}/config", MaxBodyBytes: 4096, BodyReadTimeout: 10 * time.Second},
		func(ctx context.Context, input *PutKnowledgeConfigInput) (*KnowledgeConfigOutput, error) {
			if deps.Config == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "configuration service unavailable", true)
			}
			// Accept exactly the strong ETag returned by GET, never wildcard or a list.
			value, err := strconv.Unquote(input.IfMatch)
			if err != nil {
				return nil, knowledgeError(ctx, "parse configuration revision", kb.ErrInvalidRequest)
			}
			revision, err := strconv.ParseInt(value, 10, 32)
			if err != nil || revision < 0 || strconv.Quote(strconv.FormatInt(revision, 10)) != input.IfMatch {
				return nil, knowledgeError(ctx, "parse configuration revision", kb.ErrInvalidRequest)
			}
			config, err := deps.Config.PutConfig(ctx, currentActor(ctx), input.ID, int32(revision), input.Body.Retrieval)
			if err != nil {
				return nil, knowledgeError(ctx, "update knowledge base configuration", err)
			}
			return configOutput(config), nil
		})
}

func configOutput(config kb.KnowledgeConfig) *KnowledgeConfigOutput {
	return &KnowledgeConfigOutput{ETag: strconv.Quote(strconv.FormatInt(int64(config.Revision), 10)), Body: config}
}
