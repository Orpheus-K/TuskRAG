package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
)

type SearchInput struct {
	CSRF string `header:"X-CSRF-Token" required:"true"`
	Body struct {
		Query   string  `json:"query" minLength:"1" maxLength:"4096"`
		KBIDs   []int64 `json:"kb_ids" minItems:"1" maxItems:"1" nullable:"false"`
		TopK    *int    `json:"top_k,omitempty" minimum:"1" maximum:"30"`
		Filters *struct {
			DocumentTags []string `json:"doc_tags,omitempty" maxItems:"64"`
			MIMEs        []string `json:"mimes,omitempty" maxItems:"64"`
			UpdatedAfter string   `json:"updated_after,omitempty"`
		} `json:"filters,omitempty"`
		Options *struct {
			Rerank       *bool                    `json:"rerank,omitempty"`
			Reranker     *retrieval.RerankOptions `json:"reranker,omitempty"`
			Debug        bool                     `json:"debug,omitempty"`
			ExpandParent *bool                    `json:"expand_parent,omitempty"`
		} `json:"options,omitempty"`
	}
}

type SearchEvidence struct {
	ID        string  `json:"id"`
	Score     float64 `json:"score"`
	ScoreKind string  `json:"score_kind" enum:"rrf,rerank"`
	Content   string  `json:"content"`
	Document  struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		VersionID int64  `json:"version_id"`
	} `json:"document"`
	Section struct {
		ID          int64    `json:"id"`
		HeadingPath []string `json:"heading_path" nullable:"false"`
	} `json:"section"`
	Pages []int32 `json:"pages" nullable:"true"`
}

type SearchOutput struct {
	Body struct {
		RetrievalRevision int32            `json:"retrieval_revision"`
		Status            string           `json:"status" enum:"ok,no_match"`
		Degraded          []string         `json:"degraded" nullable:"false"`
		Evidence          []SearchEvidence `json:"evidence" nullable:"false"`
		TraceID           string           `json:"trace_id"`
		Trace             *retrieval.Trace `json:"trace,omitempty"`
	}
}

func registerSearch(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "search", Method: http.MethodPost, Path: "/search", MaxBodyBytes: 32 * 1024, BodyReadTimeout: 10 * time.Second},
		func(ctx context.Context, input *SearchInput) (*SearchOutput, error) {
			if deps.Search == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "search unavailable", true)
			}
			if len(input.Body.KBIDs) != 1 || input.Body.KBIDs[0] <= 0 {
				return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "exactly one knowledge base is required", false)
			}
			var reranker *retrieval.RerankOptions
			var enabled *bool
			var debug bool
			if input.Body.Options != nil {
				reranker = input.Body.Options.Reranker
				enabled, debug = input.Body.Options.Rerank, input.Body.Options.Debug
				if input.Body.Options.ExpandParent != nil && *input.Body.Options.ExpandParent {
					return nil, statusError(ctx, http.StatusNotImplemented, "unsupported", "parent expansion is not available", false)
				}
			}
			filters := retrieval.Filters{}
			if input.Body.Filters != nil {
				filters.DocumentTags, filters.MIMEs = input.Body.Filters.DocumentTags, input.Body.Filters.MIMEs
				if input.Body.Filters.UpdatedAfter != "" {
					value, err := parseSearchDate(input.Body.Filters.UpdatedAfter)
					if err != nil {
						return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid updated_after", false)
					}
					filters.UpdatedAfter = &value
				}
			}
			limit := 0
			if input.Body.TopK != nil {
				limit = *input.Body.TopK
			}
			principal := ctx.Value(sessionKey{}).(requestSession).session.Principal
			searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			result, err := deps.Search.Search(searchCtx, retrieval.Actor{TenantID: principal.TenantID, UserID: principal.UserID},
				retrieval.Request{KnowledgeBaseID: input.Body.KBIDs[0], Query: input.Body.Query, Filters: filters, Limit: limit, Rerank: reranker, RerankEnabled: enabled, Debug: debug})
			if err != nil {
				return nil, searchError(ctx, err)
			}
			output := &SearchOutput{}
			output.Body.RetrievalRevision = result.RetrievalRevision
			if debug {
				output.Body.Trace = result.Trace
			}
			output.Body.Status = "ok"
			if len(result.Candidates) == 0 {
				output.Body.Status = "no_match"
			}
			output.Body.Degraded = append([]string{}, result.Degraded...)
			output.Body.Evidence = make([]SearchEvidence, 0, len(result.Candidates))
			output.Body.TraceID, _ = ctx.Value(traceKey{}).(string)
			for _, candidate := range result.Candidates {
				evidence := SearchEvidence{ID: fmt.Sprintf("c_%d", candidate.ChunkID), Score: candidate.Score, ScoreKind: "rrf", Content: candidate.Content}
				if candidate.RerankScore != nil {
					evidence.ScoreKind = "rerank"
				}
				evidence.Document.ID, evidence.Document.Title, evidence.Document.VersionID = candidate.DocumentID, candidate.Title, candidate.VersionID
				evidence.Section.ID, evidence.Section.HeadingPath = candidate.SectionID, append([]string{}, candidate.HeadingPath...)
				if candidate.PageStart != nil && candidate.PageEnd != nil {
					evidence.Pages = []int32{*candidate.PageStart, *candidate.PageEnd}
				}
				output.Body.Evidence = append(output.Body.Evidence, evidence)
			}
			return output, nil
		})
}

func parseSearchDate(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02", value)
}

func searchError(ctx context.Context, err error) error {
	var upstream *models.UpstreamError
	switch {
	case errors.Is(err, retrieval.ErrRerankUnavailable):
		return statusError(ctx, http.StatusServiceUnavailable, "rerank_unavailable", "reranking requires a configured provider and model", false)
	case errors.Is(err, retrieval.ErrInvalidQuery):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid search request", false)
	case errors.Is(err, retrieval.ErrNotFound):
		return statusError(ctx, http.StatusNotFound, "not_found", "knowledge base not found", false)
	case errors.Is(err, retrieval.ErrBindingChanged), errors.Is(err, models.ErrRuntimeChanged):
		return statusError(ctx, http.StatusConflict, "configuration_changed", "search configuration changed", true)
	case errors.Is(err, indexing.ErrUnsupportedDistance):
		return statusError(ctx, http.StatusUnprocessableEntity, "unsupported_embedding", "embedding distance is not supported", false)
	case errors.Is(err, models.ErrEgressDenied):
		return statusError(ctx, http.StatusUnprocessableEntity, "egress_denied", "model call denied by policy", false)
	case errors.Is(err, models.ErrTokenizerMismatch), errors.Is(err, models.ErrTokenizerUsageMissing), errors.Is(err, models.ErrInvalidResponse):
		return statusError(ctx, http.StatusBadGateway, "model_invalid_response", "model response is incompatible", false)
	case errors.As(err, &upstream):
		return statusError(ctx, http.StatusServiceUnavailable, "model_unavailable", "model service unavailable", upstream.Retryable)
	case errors.Is(err, models.ErrRuntimeUnavailable), errors.Is(err, secrets.ErrKeyUnavailable), errors.Is(err, secrets.ErrInvalidSecret):
		return statusError(ctx, http.StatusServiceUnavailable, "model_unavailable", "model configuration unavailable", false)
	case errors.Is(err, indexing.ErrIndexMissing), errors.Is(err, indexing.ErrIndexBusy), errors.Is(err, indexing.ErrIndexInvalid), errors.Is(err, indexing.ErrIndexConflict):
		return statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "search index unavailable", true)
	case errors.Is(err, context.DeadlineExceeded):
		return statusError(ctx, http.StatusServiceUnavailable, "timeout", "search timed out", true)
	default:
		return operationError(ctx, "search", err)
	}
}
