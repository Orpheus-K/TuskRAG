package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrTokenizerMismatch     = errors.New("embedding service token count differs from local counter")
	ErrTokenizerUsageMissing = errors.New("embedding service did not report token usage")
)

// These synthetic probes are sent separately so opposite count differences cannot cancel.
var tokenizerProbes = [...]string{
	"差旅报销制度",
	"第一章 › 第三条\n住宿标准为 600 元。",
	"alpha beta",
	"  alpha   beta\tgamma ",
	"Ａ́ Å ① ½",
	"emoji 👩‍💻 🇨🇳",
	"第十二条：一、报销申请；二、票据审核。",
	"<mask> Ａ <s> </s>",
	"ภาษาไทย العربية עברית",
	"line one\nline two\r\nline three",
	"café and naïve",
	"编码、标点：— … / 2026-10-28",
	strings.Repeat("差旅报销住宿审批。", 113),
}

type TokenizerCheck struct {
	Samples int
}

// CheckTokenizer compares fixed synthetic samples with the configured service's
// reported usage. A match is evidence for these samples, not proof for all inputs.
func (c *EmbeddingClient) CheckTokenizer(ctx context.Context, scope EgressScope, count func(context.Context, string) (int, error)) (TokenizerCheck, error) {
	if c == nil || c.http == nil || c.slots == nil || c.auditor == nil || ctx == nil || count == nil {
		return TokenizerCheck{}, errors.New("embedding client, context and token counter are required")
	}
	if err := ctx.Err(); err != nil {
		return TokenizerCheck{}, err
	}
	if err := c.checkEgress(ctx, scope); err != nil {
		return TokenizerCheck{}, err
	}
	var local [len(tokenizerProbes)]int
	for index, probe := range tokenizerProbes {
		if _, err := c.requestBody([]string{probe}); err != nil {
			return TokenizerCheck{}, fmt.Errorf("prepare tokenizer probe %d: %w", index+1, err)
		}
		n, err := count(ctx, probe)
		if err != nil {
			return TokenizerCheck{}, fmt.Errorf("count tokenizer probe %d: %w", index+1, err)
		}
		if err := ctx.Err(); err != nil {
			return TokenizerCheck{}, err
		}
		if n <= 0 {
			return TokenizerCheck{}, errors.New("token counter returned an invalid count")
		}
		local[index] = n
	}
	for index, probe := range tokenizerProbes {
		result, err := c.Embed(ctx, scope, []string{probe})
		if err != nil {
			return TokenizerCheck{}, fmt.Errorf("request tokenizer probe %d: %w", index+1, err)
		}
		if err := ctx.Err(); err != nil {
			return TokenizerCheck{}, err
		}
		if result.Usage == nil {
			return TokenizerCheck{}, fmt.Errorf("%w: sample %d", ErrTokenizerUsageMissing, index+1)
		}
		if result.Usage.PromptTokens != local[index] {
			return TokenizerCheck{}, fmt.Errorf("%w: sample %d, local %d, service %d", ErrTokenizerMismatch, index+1, local[index], result.Usage.PromptTokens)
		}
	}
	return TokenizerCheck{Samples: len(tokenizerProbes)}, nil
}
