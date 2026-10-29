package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type RerankConfig struct {
	Model   string
	Timeout time.Duration
}

type RerankScore struct {
	Index int
	Score float64
}

// RerankClient uses the same bounded transport and current-policy resolution
// as embeddings; its wire contract is Jina/Cohere-compatible /v1/rerank.
type RerankClient struct{ client *EmbeddingClient }

func NewRerankClient(provider Provider, config RerankConfig, auditor Auditor) (*RerankClient, error) {
	c, err := NewEmbeddingClient(provider, EmbeddingConfig{Model: config.Model, Dimensions: 64,
		MaxBatch: 50, MaxRequestBytes: 2 * 1024 * 1024, MaxResponseBytes: 64 * 1024,
		Timeout: config.Timeout, Concurrency: 1}, auditor)
	if err != nil {
		return nil, err
	}
	c.endpoint, err = url.JoinPath(provider.BaseURL, "v1", "rerank")
	if err != nil {
		c.Close()
		return nil, err
	}
	c.operation = "rerank"
	return &RerankClient{client: c}, nil
}

func (c *RerankClient) Close() {
	if c != nil && c.client != nil {
		c.client.Close()
	}
}

func (c *RerankClient) Rerank(ctx context.Context, scope EgressScope, query string, documents []string) (_ []RerankScore, resultErr error) {
	if c == nil || c.client == nil || ctx == nil || strings.TrimSpace(query) == "" || len(query) > 4096 || len(documents) == 0 || len(documents) > 50 {
		return nil, ErrInvalidConfiguration
	}
	if err := c.client.checkEgress(ctx, scope); err != nil {
		return nil, err
	}
	for _, document := range documents {
		if strings.TrimSpace(document) == "" {
			return nil, ErrInvalidConfiguration
		}
	}
	body, err := json.Marshal(struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		TopN      int      `json:"top_n"`
	}{c.client.config.Model, query, documents, len(documents)})
	if err != nil {
		return nil, fmt.Errorf("encode rerank request: %w", err)
	}
	if int64(len(body)) > c.client.config.MaxRequestBytes {
		return nil, ErrInvalidConfiguration
	}
	select {
	case c.client.slots <- struct{}{}:
		defer func() { <-c.client.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.client.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.client.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.client.provider.APIKey)
	}
	response, err := c.client.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &UpstreamError{Retryable: true, Cause: err}
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &UpstreamError{Status: response.StatusCode, Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.client.config.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read rerank response: %w", err)
	}
	if int64(len(data)) > c.client.config.MaxResponseBytes {
		return nil, ErrInvalidResponse
	}
	var decoded struct {
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if json.Unmarshal(data, &decoded) != nil || len(decoded.Results) != len(documents) {
		return nil, ErrInvalidResponse
	}
	seen := make([]bool, len(documents))
	result := make([]RerankScore, 0, len(documents))
	for _, item := range decoded.Results {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(documents) || seen[*item.Index] || item.Score == nil || math.IsNaN(*item.Score) || math.IsInf(*item.Score, 0) {
			return nil, ErrInvalidResponse
		}
		seen[*item.Index] = true
		result = append(result, RerankScore{Index: *item.Index, Score: *item.Score})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].Index < result[j].Index
		}
		return result[i].Score > result[j].Score
	})
	return result, nil
}
