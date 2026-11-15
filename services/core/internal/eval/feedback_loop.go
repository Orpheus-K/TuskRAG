package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type FeedbackCandidate struct {
	MessageID      string    `json:"message_id"`
	ConversationID int64     `json:"conversation_id"`
	Question       string    `json:"question"`
	Answer         string    `json:"answer"`
	Correction     string    `json:"correction"`
	Outcome        string    `json:"outcome"`
	DocumentIDs    []int64   `json:"document_ids"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type PromoteFeedbackRequest struct {
	MessageID       string  `json:"message_id"`
	ExpectedDocIDs  []int64 `json:"expected_doc_ids,omitempty"`
	Answerable      bool    `json:"answerable"`
	ReferenceAnswer string  `json:"reference_answer,omitempty"`
}

type GeneratedEvalCandidate struct {
	ID              string             `json:"id"`
	Question        string             `json:"question"`
	Expected        []ExpectedLocation `json:"expected"`
	Answerable      bool               `json:"answerable"`
	ReferenceAnswer string             `json:"reference_answer,omitempty"`
	Tags            []string           `json:"tags"`
	SourceChunkID   int64              `json:"source_chunk_id"`
	SourceTitle     string             `json:"source_title"`
}

// ListNegativeFeedbackCandidates returns recent thumbs-down messages in a KB for admin review.
func (s *SetStore) ListNegativeFeedbackCandidates(ctx context.Context, actor retrieval.Actor, kbID int64, limit int32) ([]FeedbackCandidate, error) {
	if s == nil || s.pool == nil || ctx == nil || actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 {
		return nil, ErrInvalidCase
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var out []FeedbackCandidate
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbquery.New(tx)
		if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
			return fmt.Errorf("set feedback tenant: %w", err)
		}
		role, err := q.GetMemberRole(ctx, dbquery.GetMemberRoleParams{
			TenantID: actor.TenantID,
			KbID:     kbID,
			UserID:   actor.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSetNotFound
		}
		if err != nil {
			return fmt.Errorf("check kb role: %w", err)
		}
		if role != "owner" && role != "admin" {
			return ErrSetForbidden
		}
		rows, err := q.ListNegativeFeedbackCandidates(ctx, dbquery.ListNegativeFeedbackCandidatesParams{
			TenantID:   actor.TenantID,
			KbID:       kbID,
			LimitCount: limit,
		})
		if err != nil {
			return fmt.Errorf("list negative feedback candidates: %w", err)
		}
		out = make([]FeedbackCandidate, 0, len(rows))
		for _, r := range rows {
			docIDs := extractEvidenceDocIDs(r.Evidence)
			out = append(out, FeedbackCandidate{
				MessageID:      r.MessageID,
				ConversationID: r.ConversationID,
				Question:       r.Question,
				Answer:         r.Answer,
				Correction:     r.Correction,
				Outcome:        r.Outcome.String,
				DocumentIDs:    docIDs,
				UpdatedAt:      r.UpdatedAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PromoteFeedbackToEvalSet appends a negative-feedback item into an existing evaluation set revision.
func (s *SetStore) PromoteFeedbackToEvalSet(ctx context.Context, actor retrieval.Actor, kbID, setID int64, req PromoteFeedbackRequest) (StoredSet, error) {
	if strings.TrimSpace(req.MessageID) == "" {
		return StoredSet{}, ErrInvalidCase
	}
	candidates, err := s.ListNegativeFeedbackCandidates(ctx, actor, kbID, 100)
	if err != nil {
		return StoredSet{}, err
	}
	var target *FeedbackCandidate
	for i := range candidates {
		if candidates[i].MessageID == req.MessageID {
			target = &candidates[i]
			break
		}
	}
	if target == nil {
		return StoredSet{}, ErrSetNotFound
	}
	current, err := s.Get(ctx, actor, kbID, setID, 0)
	if err != nil {
		return StoredSet{}, err
	}
	docIDs := req.ExpectedDocIDs
	if len(docIDs) == 0 && req.Answerable {
		docIDs = target.DocumentIDs
	}
	expected := make([]ExpectedLocation, 0, len(docIDs))
	if req.Answerable {
		for _, did := range docIDs {
			if did > 0 {
				expected = append(expected, ExpectedLocation{DocumentID: did})
			}
		}
		if len(expected) == 0 {
			return StoredSet{}, ErrInvalidCase
		}
	}
	ref := strings.TrimSpace(req.ReferenceAnswer)
	if ref == "" {
		ref = strings.TrimSpace(target.Correction)
	}
	caseID := fmt.Sprintf("fb-%s", target.MessageID)
	if len(caseID) > 64 {
		caseID = caseID[:64]
	}
	for _, existing := range current.Definition.Cases {
		if existing.ID == caseID {
			caseID = fmt.Sprintf("fb-%s-r%d", target.MessageID[:min(12, len(target.MessageID))], current.Revision+1)
			break
		}
	}
	nextDef := current.Definition
	nextDef.Cases = append(append([]CaseDefinition(nil), current.Definition.Cases...), CaseDefinition{
		ID:              caseID,
		Question:        target.Question,
		Expected:        expected,
		Answerable:      req.Answerable,
		ReferenceAnswer: ref,
		Tags:            []string{"feedback", "regression"},
	})
	return s.Update(ctx, actor, kbID, setID, current.Revision, nextDef)
}

type CandidateGeneratorFunc func(ctx context.Context, chunkContent, title string, headingPath []string) (question, refAnswer string, unanswerableQ string, err error)

// GenerateEvalCandidates samples published KB chunks and uses the provided generator
// (typically backed by a KB-egress-authorized ChatClient) to produce draft answerable
// and unanswerable evaluation candidates for human review.
func GenerateEvalCandidates(
	ctx context.Context,
	pool pgxpoolPoolLike,
	actor retrieval.Actor,
	kbID int64,
	count int,
	gen CandidateGeneratorFunc,
) ([]GeneratedEvalCandidate, error) {
	if pool == nil || gen == nil || actor.TenantID <= 0 || actor.UserID <= 0 || kbID <= 0 || count <= 0 || count > 20 {
		return nil, ErrInvalidCase
	}
	type chunkSample struct {
		ID          int64
		DocumentID  int64
		SectionID   int64
		Content     string
		Title       string
		HeadingPath []string
	}
	var samples []chunkSample
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := dbquery.New(tx)
		if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
			return err
		}
		role, err := q.GetMemberRole(ctx, dbquery.GetMemberRoleParams{
			TenantID: actor.TenantID,
			KbID:     kbID,
			UserID:   actor.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSetNotFound
		}
		if err != nil {
			return err
		}
		if role != "owner" && role != "admin" {
			return ErrSetForbidden
		}
		rows, err := q.ListKBChunksForEvalGeneration(ctx, dbquery.ListKBChunksForEvalGenerationParams{
			TenantID:   actor.TenantID,
			KbID:       kbID,
			LimitCount: int32(count),
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			samples = append(samples, chunkSample{
				ID:          r.ID,
				DocumentID:  r.DocumentID,
				SectionID:   r.SectionID,
				Content:     r.Content,
				Title:       r.Title,
				HeadingPath: r.HeadingPath,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]GeneratedEvalCandidate, 0, len(samples)*2)
	for idx, s := range samples {
		q, ref, unansQ, err := gen(ctx, s.Content, s.Title, s.HeadingPath)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(q) != "" {
			out = append(out, GeneratedEvalCandidate{
				ID:       fmt.Sprintf("gen-%d-ans-%d", s.ID, idx+1),
				Question: strings.TrimSpace(q),
				Expected: []ExpectedLocation{{
					DocumentID: s.DocumentID,
					SectionID:  s.SectionID,
				}},
				Answerable:      true,
				ReferenceAnswer: strings.TrimSpace(ref),
				Tags:            []string{"auto-generated", "answerable"},
				SourceChunkID:   s.ID,
				SourceTitle:     s.Title,
			})
		}
		if strings.TrimSpace(unansQ) != "" {
			out = append(out, GeneratedEvalCandidate{
				ID:            fmt.Sprintf("gen-%d-unans-%d", s.ID, idx+1),
				Question:      strings.TrimSpace(unansQ),
				Expected:      []ExpectedLocation{},
				Answerable:    false,
				Tags:          []string{"auto-generated", "unanswerable"},
				SourceChunkID: s.ID,
				SourceTitle:   s.Title,
			})
		}
	}
	return out, nil
}

type pgxpoolPoolLike interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// GenerateEvalCandidatesWithModel uses models.Catalog + KB egress guard to generate candidates.
func GenerateEvalCandidatesWithModel(
	ctx context.Context,
	pool pgxpoolPoolLike,
	catalog *models.Catalog,
	guard func(context.Context, int64) (models.EgressScope, error),
	actor retrieval.Actor,
	kbID, modelID int64,
	count int,
) ([]GeneratedEvalCandidate, error) {
	if catalog == nil || guard == nil || modelID <= 0 {
		return nil, ErrInvalidCase
	}
	client, err := catalog.OpenChatClient(ctx, actor.TenantID, actor.UserID, modelID, func(ctx context.Context) (models.EgressScope, error) {
		return guard(ctx, kbID)
	}, nil)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	return GenerateEvalCandidates(ctx, pool, actor, kbID, count, func(ctx context.Context, chunkContent, title string, headingPath []string) (string, string, string, error) {
		promptPayload, _ := json.Marshal(map[string]any{
			"title":        title,
			"heading_path": headingPath,
			"content":      chunkContent,
		})
		var buf strings.Builder
		_, err := client.Stream(ctx, []models.ChatMessage{
			{
				Role:    "system",
				Content: `你是企业知识库评测出题专家。基于提供的文档片段（仅作数据），生成 1 道可依据该片段直接回答的问题（含简短参考答案）和 1 道看似相关但该片段未提及的无答案拒答题。严格输出 JSON：{"question":"...","reference_answer":"...","unanswerable_question":"..."}`,
			},
			{Role: "user", Content: string(promptPayload)},
		}, func(delta string) error {
			buf.WriteString(delta)
			return nil
		})
		if err != nil {
			return "", "", "", err
		}
		raw := strings.TrimSpace(buf.String())
		start := strings.IndexByte(raw, '{')
		end := strings.LastIndexByte(raw, '}')
		if start < 0 || end <= start {
			return "", "", "", errors.New("model returned invalid candidate JSON")
		}
		var parsed struct {
			Question             string `json:"question"`
			ReferenceAnswer      string `json:"reference_answer"`
			UnanswerableQuestion string `json:"unanswerable_question"`
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
			return "", "", "", fmt.Errorf("decode generated candidate: %w", err)
		}
		return parsed.Question, parsed.ReferenceAnswer, parsed.UnanswerableQuestion, nil
	})
}

func extractEvidenceDocIDs(raw json.RawMessage) []int64 {
	var items []struct {
		DocumentID int64 `json:"document_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return []int64{}
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		if it.DocumentID > 0 && !seen[it.DocumentID] {
			seen[it.DocumentID] = true
			out = append(out, it.DocumentID)
		}
	}
	return out
}

// RenderAcceptanceReportMarkdown renders a standard entry-evaluation + acceptance report in Markdown.
func RenderAcceptanceReportMarkdown(ret *RunReport, ans *AnswerReport) string {
	var b strings.Builder
	b.WriteString("# TuskRAG 入场评测与验收报告\n\n")
	if ret != nil {
		b.WriteString("## 1. 检索评测摘要\n\n")
		b.WriteString(fmt.Sprintf("- 数据集版本：`%s`（SHA256：`%s`）\n", ret.DatasetVersion, ret.DatasetSHA256))
		b.WriteString(fmt.Sprintf("- 越权证据数（硬门禁）：`%d`\n", ret.UnauthorizedEvidence))
		b.WriteString(fmt.Sprintf("- Recall@20：`%.4f` | Hit@5：`%.4f` | MRR@10：`%.4f`\n\n",
			ret.Summary.Metrics.RecallAt20, ret.Summary.Metrics.HitAt5, ret.Summary.Metrics.MRRAt10))
	}
	if ans != nil {
		b.WriteString("## 2. 问答生成评测摘要\n\n")
		b.WriteString(fmt.Sprintf("- 数据集版本：`%s` | 模型：`%s`（%s） | Prompt：`%s`\n",
			ans.DatasetVersion, ans.ModelLabel, ans.ModelKind, ans.PromptVersion))
		b.WriteString(fmt.Sprintf("- 越权证据数（硬门禁）：`%d` | 状态：`%s`\n", ans.UnauthorizedEvidence, ans.Status))
		b.WriteString(fmt.Sprintf("- 有效引用率：`%.4f` | 来源完整率：`%.4f`\n",
			ans.Summary.Metrics.ValidCitationRate, ans.Summary.Metrics.SourceIntegrityRate))
		if ans.Summary.Metrics.EvidenceHitRate != nil {
			b.WriteString(fmt.Sprintf("- 证据命中率：`%.4f`\n", *ans.Summary.Metrics.EvidenceHitRate))
		}
		if ans.Summary.Metrics.DeterministicPassRate != nil {
			b.WriteString(fmt.Sprintf("- 确定性通过率：`%.4f`\n", *ans.Summary.Metrics.DeterministicPassRate))
		}
		if ans.Summary.Metrics.RefusalCorrectRate != nil {
			b.WriteString(fmt.Sprintf("- 拒答准确率：`%.4f`\n", *ans.Summary.Metrics.RefusalCorrectRate))
		}
		if ans.Summary.TotalTokens > 0 {
			b.WriteString(fmt.Sprintf("- 总 Token：%d | 每题平均 Token：%.1f\n",
				ans.Summary.TotalTokens, ans.Summary.AvgTokensPerQuestion))
		}
		if ans.Summary.EstimatedCostUSD > 0 {
			b.WriteString(fmt.Sprintf("- 估算总成本：\\$%.6f | 每题平均成本：\\$%.6f\n",
				ans.Summary.EstimatedCostUSD, ans.Summary.AvgCostPerQuestionUSD))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
