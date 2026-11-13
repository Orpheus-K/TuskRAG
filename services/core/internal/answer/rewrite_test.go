package answer

import (
	"context"
	"errors"
	"testing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

func TestFollowupRewriteValidation(t *testing.T) {
	for _, tc := range []struct {
		name, text        string
		err               error
		fallback, failure bool
	}{
		{name: "standalone", text: `{"question":"上海差旅住宿上限是多少？"}`},
		{name: "empty", text: `{"question":""}`, fallback: true},
		{name: "trailing", text: `{"question":"问题"} {"question":"另一个"}`, fallback: true},
		{name: "extra", text: `{"question":"问题","answer":"伪造"}`, fallback: true},
		{name: "multiline", text: `{"question":"问题\n另一个问题"}`, fallback: true},
		{name: "timeout", err: context.DeadlineExceeded, fallback: true},
		{name: "revoked", err: retrieval.ErrNotFound, failure: true},
		{name: "egress", err: models.ErrEgressDenied, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := generatorFunc(func(ctx context.Context, m []models.ChatMessage, emit func(string) error) (models.ChatResult, error) {
				if tc.err != nil {
					return models.ChatResult{}, tc.err
				}
				if err := emit(tc.text); err != nil {
					return models.ChatResult{}, err
				}
				return models.ChatResult{Finish: "stop"}, nil
			})
			question, fallback, err := rewriteQuestion(context.Background(), g, []rewriteTurn{{Question: "北京住宿标准？", Answer: "500 元 [1]"}}, "那上海呢？")
			if fallback != tc.fallback || (err != nil) != tc.failure || (tc.failure && !errors.Is(err, tc.err)) {
				t.Fatalf("rewrite fallback=%t err=%v", fallback, err)
			}
			if tc.fallback && question != "那上海呢？" {
				t.Fatal("fallback changed question")
			}
		})
	}
}
