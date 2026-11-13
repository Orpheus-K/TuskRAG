package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
)

const JudgePromptVersion = "judge-v1"

const judgeSystemPrompt = `你是引用准确性评审器。给定 question、evidence（按 n 编号的证据数组）和 answer（待评回答），逐句核查 answer 中带有方括号引用 [n] 的事实断言是否确实得到对应编号 evidence[n] 原文的直接支持。只返回严格 JSON 对象 {"total_claims": 整数, "unsupported_claims": 整数}，不要输出代码围栏或任何额外文字。total_claims 为回答中带引用的事实断言句总数；unsupported_claims 为其中缺乏对应引用证据直接支持或曲解原文的断言数。`

// CitationJudgment is the strict output of evaluating citation grounding for one answer.
type CitationJudgment struct {
	TotalClaims       int `json:"total_claims"`
	UnsupportedClaims int `json:"unsupported_claims"`
}

// CitationJudge evaluates whether cited claims in an answer are supported by their evidence.
type CitationJudge interface {
	Judge(ctx context.Context, question, answerText string, evidence []answer.AnswerEvidence) (CitationJudgment, error)
}

type modelCitationJudge struct {
	pool    *pgxpool.Pool
	catalog *models.Catalog
	actor   retrieval.Actor
	kbID    int64
	modelID int64
	guard   *retrieval.Driver
}

// NewModelCitationJudge creates an egress-checked citation judge backed by a registered chat_model.
func NewModelCitationJudge(pool *pgxpool.Pool, vault *secrets.Vault, actor retrieval.Actor, kbID, chatModelID int64) (CitationJudge, error) {
	if pool == nil || actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 || chatModelID <= 0 {
		return nil, errors.New("valid pool, actor, knowledge base and chat model are required for citation judge")
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		return nil, err
	}
	guard, err := retrieval.NewDriver(pool)
	if err != nil {
		return nil, err
	}
	return &modelCitationJudge{
		pool:    pool,
		catalog: catalog,
		actor:   actor,
		kbID:    kbID,
		modelID: chatModelID,
		guard:   guard,
	}, nil
}

func (j *modelCitationJudge) Judge(ctx context.Context, question, answerText string, evidence []answer.AnswerEvidence) (CitationJudgment, error) {
	if strings.TrimSpace(question) == "" || strings.TrimSpace(answerText) == "" || len(evidence) == 0 {
		return CitationJudgment{}, errors.New("judge requires non-empty question, answer and evidence")
	}
	auditor, err := models.NewEmbeddingDenialAuditor(j.pool, j.actor.TenantID, j.actor.UserID)
	if err != nil {
		return CitationJudgment{}, err
	}
	scope := func(ctx context.Context) (models.EgressScope, error) {
		return j.guard.AuthorizeEvidence(ctx, j.actor, j.kbID, nil)
	}
	client, err := j.catalog.OpenChatClient(ctx, j.actor.TenantID, j.actor.UserID, j.modelID, scope, auditor)
	if err != nil {
		return CitationJudgment{}, err
	}
	defer client.Close()

	payload, err := json.Marshal(struct {
		Question string                  `json:"question"`
		Answer   string                  `json:"answer"`
		Evidence []answer.AnswerEvidence `json:"evidence"`
	}{
		Question: question,
		Answer:   answerText,
		Evidence: evidence,
	})
	if err != nil {
		return CitationJudgment{}, err
	}
	judgeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var out strings.Builder
	res, err := client.Stream(judgeCtx, []models.ChatMessage{
		{Role: "system", Content: judgeSystemPrompt},
		{Role: "user", Content: string(payload)},
	}, func(delta string) error {
		if out.Len()+len(delta) > 4096 {
			return models.ErrInvalidResponse
		}
		out.WriteString(delta)
		return nil
	})
	if err != nil {
		return CitationJudgment{}, err
	}
	if res.Finish != "stop" {
		return CitationJudgment{}, models.ErrInvalidResponse
	}
	return decodeCitationJudgment(out.String())
}

func decodeCitationJudgment(raw string) (CitationJudgment, error) {
	trimmed := strings.TrimSpace(raw)
	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.DisallowUnknownFields()
	var parsed struct {
		TotalClaims       *int `json:"total_claims"`
		UnsupportedClaims *int `json:"unsupported_claims"`
	}
	if err := dec.Decode(&parsed); err != nil {
		return CitationJudgment{}, fmt.Errorf("decode citation judgment: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return CitationJudgment{}, errors.New("citation judgment has trailing JSON")
	}
	if parsed.TotalClaims == nil || parsed.UnsupportedClaims == nil {
		return CitationJudgment{}, errors.New("citation judgment missing required fields")
	}
	total, unsupported := *parsed.TotalClaims, *parsed.UnsupportedClaims
	if total < 0 || unsupported < 0 || unsupported > total {
		return CitationJudgment{}, errors.New("citation judgment counts out of bounds")
	}
	return CitationJudgment{TotalClaims: total, UnsupportedClaims: unsupported}, nil
}
