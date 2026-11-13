package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type VLMClient struct {
	client *EmbeddingClient
	scope  func(context.Context) (EgressScope, error)
}

func NewVLMClient(provider Provider, model string, timeout time.Duration, scope func(context.Context) (EgressScope, error), auditor Auditor) (*VLMClient, error) {
	if scope == nil {
		return nil, ErrInvalidConfiguration
	}
	c, err := NewEmbeddingClient(provider, EmbeddingConfig{
		Model:            model,
		Dimensions:       64,
		MaxBatch:         1,
		MaxRequestBytes:  8 * 1024 * 1024,
		MaxResponseBytes: 4 * 1024 * 1024,
		Timeout:          timeout,
		Concurrency:      1,
	}, auditor)
	if err != nil {
		return nil, err
	}
	c.endpoint, err = url.JoinPath(provider.BaseURL, "v1", "chat", "completions")
	if err != nil {
		c.Close()
		return nil, err
	}
	c.operation = "vlm.chat.completions"
	return &VLMClient{client: c, scope: scope}, nil
}

func (c *VLMClient) Close() { c.client.Close() }

func (c *VLMClient) Check(ctx context.Context) error {
	scope, err := c.scope(ctx)
	if err != nil {
		return err
	}
	return c.client.checkEgress(ctx, scope)
}

func (c *VLMClient) ExtractPage(ctx context.Context, page int, pngBytes []byte) (string, *ChatUsage, error) {
	if c == nil || ctx == nil || page <= 0 || len(pngBytes) == 0 {
		return "", nil, ErrInvalidConfiguration
	}
	scope, err := c.scope(ctx)
	if err != nil {
		return "", nil, err
	}
	if err := c.client.checkEgress(ctx, scope); err != nil {
		return "", nil, err
	}

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	payload := map[string]any{
		"model":      c.client.config.Model,
		"max_tokens": 4096,
		"messages": []map[string]any{
			{
				"role":    "system",
				"content": "Extract all text, headings, lists, and tables from the page image into clean Markdown. Output only Markdown.",
			},
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("Extract page %d to Markdown.", page)},
					{"type": "image_url", "image_url": map[string]any{"url": dataURL}},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > c.client.config.MaxRequestBytes {
		return "", nil, ErrInvalidConfiguration
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.client.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.client.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.client.provider.APIKey)
	}

	resp, err := c.client.http.Do(req)
	if err != nil {
		return "", nil, &UpstreamError{Retryable: true, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, &UpstreamError{
			Status:    resp.StatusCode,
			Retryable: resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode < 600),
		}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.client.config.MaxResponseBytes+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(raw)) > c.client.config.MaxResponseBytes {
		return "", nil, fmt.Errorf("%w: vlm response too large", ErrInvalidResponse)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *ChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", nil, fmt.Errorf("%w: invalid vlm response", ErrInvalidResponse)
	}
	md := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if md == "" {
		return "", nil, fmt.Errorf("%w: empty vlm content", ErrInvalidResponse)
	}
	return md, parsed.Usage, nil
}
