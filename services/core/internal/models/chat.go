package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type ChatResult struct {
	Finish string
	Usage  *ChatUsage
}
type ChatClient struct {
	client *EmbeddingClient
	scope  func(context.Context) (EgressScope, error)
}

func NewChatClient(provider Provider, model string, timeout time.Duration, scope func(context.Context) (EgressScope, error), auditor Auditor) (*ChatClient, error) {
	if scope == nil {
		return nil, ErrInvalidConfiguration
	}
	c, err := NewEmbeddingClient(provider, EmbeddingConfig{Model: model, Dimensions: 64, MaxBatch: 1, MaxRequestBytes: 128 * 1024, MaxResponseBytes: 4 * 1024 * 1024, Timeout: timeout, Concurrency: 1}, auditor)
	if err != nil {
		return nil, err
	}
	c.endpoint, err = url.JoinPath(provider.BaseURL, "v1", "chat", "completions")
	if err != nil {
		c.Close()
		return nil, err
	}
	c.operation = "chat.completions"
	return &ChatClient{client: c, scope: scope}, nil
}
func (c *ChatClient) Close() { c.client.Close() }

func (c *ChatClient) Check(ctx context.Context) error {
	scope, err := c.scope(ctx)
	if err != nil {
		return err
	}
	return c.client.checkEgress(ctx, scope)
}

// Stream never retries after sending the request, and never retains the answer text.
func (c *ChatClient) Stream(ctx context.Context, messages []ChatMessage, emit func(string) error) (_ ChatResult, resultErr error) {
	if c == nil || ctx == nil || emit == nil || len(messages) == 0 || len(messages) > 20 {
		return ChatResult{}, ErrInvalidConfiguration
	}
	for _, m := range messages {
		if (m.Role != "system" && m.Role != "user" && m.Role != "assistant") || m.Content == "" || !utf8.ValidString(m.Content) {
			return ChatResult{}, ErrInvalidConfiguration
		}
	}
	scope, err := c.scope(ctx)
	if err != nil {
		return ChatResult{}, err
	}
	if err = c.client.checkEgress(ctx, scope); err != nil {
		return ChatResult{}, err
	}
	body, err := json.Marshal(struct {
		Model         string        `json:"model"`
		Messages      []ChatMessage `json:"messages"`
		Stream        bool          `json:"stream"`
		MaxTokens     int           `json:"max_tokens"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}{Model: c.client.config.Model, Messages: messages, Stream: true, MaxTokens: 2048, StreamOptions: struct {
		IncludeUsage bool `json:"include_usage"`
	}{true}})
	if err != nil {
		return ChatResult{}, err
	}
	if int64(len(body)) > c.client.config.MaxRequestBytes {
		return ChatResult{}, ErrInvalidConfiguration
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.client.endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.client.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.client.provider.APIKey)
	}
	resp, err := c.client.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ChatResult{}, err
		}
		return ChatResult{}, &UpstreamError{Cause: err, Retryable: true}
	}
	defer func() { resultErr = errors.Join(resultErr, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return ChatResult{}, &UpstreamError{Status: resp.StatusCode, Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	if media := strings.Split(resp.Header.Get("Content-Type"), ";")[0]; strings.TrimSpace(media) != "text/event-stream" {
		return ChatResult{}, ErrInvalidResponse
	}
	return readChatStream(ctx, io.LimitReader(resp.Body, c.client.config.MaxResponseBytes+1), emit)
}

func readChatStream(ctx context.Context, reader io.Reader, emit func(string) error) (ChatResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	var result ChatResult
	var data strings.Builder
	outputBytes, totalBytes := 0, 0
	dispatch := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		raw := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if raw == "[DONE]" {
			if result.Finish == "" || outputBytes == 0 {
				return false, ErrInvalidResponse
			}
			return true, nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   string          `json:"content"`
					Refusal   string          `json:"refusal"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *ChatUsage `json:"usage"`
		}
		if !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &chunk) != nil || len(chunk.Error) > 0 || len(chunk.Choices) > 1 {
			return false, ErrInvalidResponse
		}
		if chunk.Usage != nil {
			u := chunk.Usage
			// Some compatible gateways include reasoning tokens only in total_tokens.
			if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 || u.PromptTokens > 10_000_000 || u.CompletionTokens > 10_000_000 || u.TotalTokens > 20_000_000 || u.TotalTokens < u.PromptTokens+u.CompletionTokens {
				return false, ErrInvalidResponse
			}
			result.Usage = u
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage == nil {
				return false, ErrInvalidResponse
			}
			return false, nil
		}
		choice := chunk.Choices[0]
		if choice.Index != 0 || result.Finish != "" || choice.Delta.Refusal != "" || (len(choice.Delta.ToolCalls) > 0 && string(choice.Delta.ToolCalls) != "null") {
			return false, ErrInvalidResponse
		}
		if choice.Delta.Content != "" {
			outputBytes += len(choice.Delta.Content)
			if outputBytes > 64*1024 {
				return false, ErrInvalidResponse
			}
			if err := emit(choice.Delta.Content); err != nil {
				return false, err
			}
		}
		if choice.Finish != nil {
			if *choice.Finish != "stop" && *choice.Finish != "length" {
				return false, ErrInvalidResponse
			}
			result.Finish = *choice.Finish
		}
		return false, nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ChatResult{}, err
		}
		line := scanner.Text()
		totalBytes += len(line) + 1
		if totalBytes > 4*1024*1024 {
			return ChatResult{}, ErrInvalidResponse
		}
		if line == "" {
			done, err := dispatch()
			if err != nil {
				return ChatResult{}, err
			}
			if done {
				return result, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			part = strings.TrimPrefix(part, " ")
			data.WriteString(part)
			data.WriteByte('\n')
			if data.Len() > 64*1024 {
				return ChatResult{}, ErrInvalidResponse
			}
		}
	}
	if ctx.Err() != nil {
		return ChatResult{}, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return ChatResult{}, fmt.Errorf("read chat stream: %w", err)
	}
	// A terminal marker without a trailing blank line is valid SSE at EOF.
	done, err := dispatch()
	if err != nil {
		return ChatResult{}, err
	}
	if done {
		return result, nil
	}
	return ChatResult{}, fmt.Errorf("%w: incomplete chat stream", ErrInvalidResponse)
}
