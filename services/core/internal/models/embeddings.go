package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrEgressDenied    = errors.New("model egress denied")
	ErrInvalidResponse = errors.New("invalid embedding response")
	ErrScopeMismatch   = errors.New("embedding client binding does not match scope")
)

type Locality string

const (
	Internal Locality = "internal"
	External Locality = "external"
)

type EgressPolicy string

const (
	Deny         EgressPolicy = "deny"
	InternalOnly EgressPolicy = "internal_only"
	Allow        EgressPolicy = "allow"
)

type DeniedCall struct {
	TenantID            int64
	KnowledgeBaseID     int64
	ProviderID          int64
	TenantPolicy        EgressPolicy
	KnowledgeBasePolicy EgressPolicy
	Locality            Locality
	Operation           string
}

type EgressScope struct {
	TenantID            int64
	KnowledgeBaseID     int64
	TenantPolicy        EgressPolicy
	KnowledgeBasePolicy EgressPolicy
}

type EmbeddingBinding struct {
	TenantID         int64
	KnowledgeBaseID  int64
	EmbeddingSpaceID int64
	ConfigRevision   int32
}

// EmbeddingScopeResolver must read the current policies for the bound space and revision.
type EmbeddingScopeResolver interface {
	ResolveEmbeddingScope(context.Context, EmbeddingBinding) (EgressScope, error)
}

// Auditor records denied model calls without recording inputs or credentials.
type Auditor interface {
	RecordModelDenied(context.Context, DeniedCall) error
}

type Provider struct {
	ID       int64
	BaseURL  string
	Locality Locality
	APIKey   string
}

type EmbeddingConfig struct {
	Model            string
	Dimensions       int
	Normalize        bool
	MaxBatch         int
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Timeout          time.Duration
	Concurrency      int
}

// EmbeddingUsage is absent when a provider does not report token usage.
type EmbeddingUsage struct {
	PromptTokens int
	TotalTokens  int
}

type EmbeddingResult struct {
	Vectors [][]float32
	Usage   *EmbeddingUsage
}

type UpstreamError struct {
	Status    int
	Retryable bool
	Cause     error
}

func (e *UpstreamError) Error() string {
	if e.Status == 0 {
		return "embedding service connection failed"
	}
	return fmt.Sprintf("embedding service returned HTTP %d", e.Status)
}

func (e *UpstreamError) Unwrap() error { return e.Cause }

type EmbeddingClient struct {
	operation string
	binding   *EmbeddingBinding
	resolver  EmbeddingScopeResolver
	provider  Provider
	config    EmbeddingConfig
	endpoint  string
	auditor   Auditor
	http      *http.Client
	transport *http.Transport
	slots     chan struct{}
}

// BaseURL is the provider root; /v1/embeddings is appended to its path.
func NewEmbeddingClient(provider Provider, config EmbeddingConfig, auditor Auditor) (*EmbeddingClient, error) {
	base, err := providerURL(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	if provider.ID <= 0 || (provider.Locality != Internal && provider.Locality != External) || strings.ContainsAny(provider.APIKey, "\r\n") || auditor == nil ||
		strings.TrimSpace(config.Model) == "" || config.Dimensions < 64 || config.Dimensions > 4000 ||
		config.MaxBatch <= 0 || config.MaxRequestBytes <= 0 || config.MaxResponseBytes <= 0 ||
		config.MaxResponseBytes == math.MaxInt64 || config.Timeout <= 0 || config.Concurrency <= 0 {
		return nil, errors.New("invalid embedding provider configuration")
	}
	if provider.Locality == External && base.Scheme != "https" {
		return nil, errors.New("external model provider requires HTTPS")
	}
	endpoint, err := url.JoinPath(base.String(), "v1", "embeddings")
	if err != nil {
		return nil, errors.New("invalid model provider URL")
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          config.Concurrency,
		MaxIdleConnsPerHost:   config.Concurrency,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: config.Timeout,
	}
	return &EmbeddingClient{
		provider: provider, config: config, endpoint: endpoint, auditor: auditor,
		transport: transport, slots: make(chan struct{}, config.Concurrency),
		http: &http.Client{
			Transport:     transport,
			Timeout:       config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func providerURL(value string) (*url.URL, error) {
	base, err := url.Parse(value)
	if err != nil || base == nil || len(value) > 2048 || value != strings.TrimSpace(value) ||
		(base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("invalid model provider URL")
	}
	return base, nil
}

func (c *EmbeddingClient) Close() { c.transport.CloseIdleConnections() }

func (c *EmbeddingClient) Embed(ctx context.Context, scope EgressScope, inputs []string) (_ *EmbeddingResult, resultErr error) {
	if err := c.checkEgress(ctx, scope); err != nil {
		return nil, err
	}
	body, err := c.requestBody(inputs)
	if err != nil {
		return nil, err
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.provider.APIKey)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("call embedding service: %w", err)
		}
		return nil, &UpstreamError{Retryable: true, Cause: err}
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close embedding response: %w", err))
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &UpstreamError{Status: response.StatusCode, Retryable: response.StatusCode == http.StatusTooManyRequests || (response.StatusCode >= 500 && response.StatusCode < 600)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.config.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if int64(len(data)) > c.config.MaxResponseBytes {
		return nil, fmt.Errorf("%w: size limit exceeded", ErrInvalidResponse)
	}
	var result struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens *int `json:"prompt_tokens"`
			TotalTokens  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Data) != len(inputs) {
		return nil, fmt.Errorf("%w: malformed data or batch length", ErrInvalidResponse)
	}
	vectors := make([][]float32, len(inputs))
	for _, item := range result.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(vectors) || vectors[*item.Index] != nil || len(item.Embedding) != c.config.Dimensions {
			return nil, fmt.Errorf("%w: index or dimensions", ErrInvalidResponse)
		}
		var norm float64
		for _, value := range item.Embedding {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("%w: non-finite vector", ErrInvalidResponse)
			}
			norm = math.Hypot(norm, float64(value))
		}
		if norm == 0 {
			return nil, fmt.Errorf("%w: zero vector", ErrInvalidResponse)
		}
		if c.config.Normalize {
			for i := range item.Embedding {
				item.Embedding[i] = float32(float64(item.Embedding[i]) / norm)
			}
		}
		vectors[*item.Index] = item.Embedding
	}
	var usage *EmbeddingUsage
	if result.Usage != nil {
		if result.Usage.PromptTokens == nil || result.Usage.TotalTokens == nil || *result.Usage.PromptTokens <= 0 || *result.Usage.TotalTokens < *result.Usage.PromptTokens {
			return nil, fmt.Errorf("%w: token usage", ErrInvalidResponse)
		}
		usage = &EmbeddingUsage{PromptTokens: *result.Usage.PromptTokens, TotalTokens: *result.Usage.TotalTokens}
	}
	return &EmbeddingResult{Vectors: vectors, Usage: usage}, nil
}

func (c *EmbeddingClient) requestBody(inputs []string) ([]byte, error) {
	if len(inputs) == 0 || len(inputs) > c.config.MaxBatch {
		return nil, errors.New("embedding batch size is invalid")
	}
	var inputBytes int64
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" {
			return nil, errors.New("embedding input must not be empty")
		}
		if int64(len(input)) > c.config.MaxRequestBytes-inputBytes {
			return nil, errors.New("embedding request exceeds size limit")
		}
		inputBytes += int64(len(input))
	}
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: c.config.Model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	if int64(len(body)) > c.config.MaxRequestBytes {
		return nil, errors.New("embedding request exceeds size limit")
	}
	return body, nil
}

func (c *EmbeddingClient) checkEgress(ctx context.Context, scope EgressScope) error {
	if c.binding != nil {
		if scope.TenantID != c.binding.TenantID || scope.KnowledgeBaseID != c.binding.KnowledgeBaseID || c.resolver == nil {
			return ErrScopeMismatch
		}
		current, err := c.resolver.ResolveEmbeddingScope(ctx, *c.binding)
		if err != nil {
			return fmt.Errorf("resolve embedding scope: %w", err)
		}
		if current.TenantID != c.binding.TenantID || current.KnowledgeBaseID != c.binding.KnowledgeBaseID {
			return ErrScopeMismatch
		}
		scope = current
	}
	within, err := PolicyWithin(scope.TenantPolicy, scope.KnowledgeBasePolicy)
	if scope.TenantID <= 0 || scope.KnowledgeBaseID <= 0 || err != nil {
		return errors.New("invalid model egress scope")
	}
	if within && (scope.KnowledgeBasePolicy == Allow || c.provider.Locality == Internal) {
		return nil
	}
	err = c.auditor.RecordModelDenied(ctx, DeniedCall{
		TenantID: scope.TenantID, KnowledgeBaseID: scope.KnowledgeBaseID, ProviderID: c.provider.ID,
		TenantPolicy: scope.TenantPolicy, KnowledgeBasePolicy: scope.KnowledgeBasePolicy,
		Locality: c.provider.Locality, Operation: c.operationName(),
	})
	if err != nil {
		return fmt.Errorf("audit denied model call: %w", err)
	}
	return ErrEgressDenied
}

func (c *EmbeddingClient) operationName() string {
	if c.operation == "" {
		return "embeddings"
	}
	return c.operation
}

func PolicyWithin(tenant, knowledgeBase EgressPolicy) (bool, error) {
	tenantLimit, tenantValid := policyLimit(tenant)
	kbLimit, kbValid := policyLimit(knowledgeBase)
	if !tenantValid || !kbValid {
		return false, errors.New("invalid model egress policy")
	}
	return kbLimit <= tenantLimit, nil
}

func policyLimit(policy EgressPolicy) (int, bool) {
	switch policy {
	case Deny:
		return 0, true
	case InternalOnly:
		return 1, true
	case Allow:
		return 2, true
	default:
		return 0, false
	}
}
