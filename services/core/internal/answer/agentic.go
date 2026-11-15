package answer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

const (
	defaultAgentMaxSteps          = 6
	defaultAgentMaxDuration       = 30 * time.Second
	defaultAgentMaxEvidenceTokens = 20000
)

const agentPlannerPrompt = `你是检索规划器。根据 question 和已有 observations，决定下一步操作。
可用工具：
- search: {"query": string, "top_k"?: int}
- exact_match: {"pattern": string, "use_regex"?: bool, "limit"?: int}
- list_documents: {"query"?: string, "tags"?: string[], "page"?: int}
- get_toc: {"document_id": int}
- read_section: {"section_id": int, "max_tokens"?: int}
- get_neighbors: {"chunk_id": int, "before"?: int, "after"?: int}

停止规则：
- 一旦已有 observations 中的证据足以回答 question，或连续检索未带来新证据时，必须立即输出 {"action":"final"}。
- 不要重复调用完全相同的工具和参数。

只输出一个 JSON 对象，不要任何解释文字：
调用工具时：{"action":"tool","tool":"<工具名>","args":{...}}
证据已足够回答时：{"action":"final"}`

type agentPlan struct {
	Action string          `json:"action"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
}

type agentObservation struct {
	Step             int    `json:"step"`
	Tool             string `json:"tool"`
	NewEvidenceCount int    `json:"new_evidence_count"`
	TotalEvidence    int    `json:"total_evidence_count"`
	Result           any    `json:"result,omitempty"`
	Error            string `json:"error,omitempty"`
}

func (s *Service) effectiveAgentLimits() AgentLimits {
	limits := s.Agent
	if limits.MaxSteps <= 0 {
		limits.MaxSteps = defaultAgentMaxSteps
	}
	if limits.MaxDuration <= 0 {
		limits.MaxDuration = defaultAgentMaxDuration
	}
	if limits.MaxEvidenceTokens <= 0 {
		limits.MaxEvidenceTokens = defaultAgentMaxEvidenceTokens
	}
	return limits
}

func (s *Service) streamAgentic(ctx context.Context, actor retrieval.Actor, request Request, question string, options streamOptions, emit func(Event) error) error {
	limits := s.effectiveAgentLimits()
	started := time.Now()
	allowedKBs := []int64{request.KnowledgeBaseID}

	var candidates []retrieval.Candidate
	scope := func(ctx context.Context) (models.EgressScope, error) {
		if options.check != nil {
			if err := options.check(ctx); err != nil {
				return models.EgressScope{}, err
			}
		}
		return s.Guard.AuthorizeEvidence(ctx, actor, request.KnowledgeBaseID, candidates)
	}
	generator, err := s.Open(ctx, actor, request.ModelID, scope)
	if err != nil {
		return err
	}
	defer generator.Close()

	check := func(ctx context.Context) error {
		if _, err := scope(ctx); err != nil {
			return err
		}
		return generator.Check(ctx)
	}
	send := func(event Event) error {
		if err := check(ctx); err != nil {
			return err
		}
		return emit(event)
	}

	var (
		observations   []agentObservation
		evidence       []AnswerEvidence
		seenEvidence   = map[string]struct{}{}
		calledTools    = map[string]struct{}{}
		emptyNewSteps  int
		evidenceTokens int
		truncated      bool
		degradedSet    = map[string]struct{}{}
		rev            int32
	)
	for _, d := range options.degraded {
		degradedSet[d] = struct{}{}
	}

	for step := 1; step <= limits.MaxSteps; step++ {
		if time.Since(started) >= limits.MaxDuration || evidenceTokens >= limits.MaxEvidenceTokens {
			truncated = true
			break
		}
		planPayload, err := json.Marshal(struct {
			Question     string             `json:"question"`
			Observations []agentObservation `json:"observations"`
		}{Question: question, Observations: observations})
		if err != nil {
			return err
		}
		var planText strings.Builder
		if _, err := generator.Stream(ctx, []models.ChatMessage{
			{Role: "system", Content: agentPlannerPrompt},
			{Role: "user", Content: string(planPayload)},
		}, func(chunk string) error {
			planText.WriteString(chunk)
			return nil
		}); err != nil {
			return err
		}
		plan, ok := parseAgentPlan(planText.String())
		if !ok || plan.Action != "tool" || plan.Tool == "" {
			break
		}
		callKey := normalizeToolCallKey(plan.Tool, plan.Args)
		if _, dup := calledTools[callKey]; dup {
			break
		}
		calledTools[callKey] = struct{}{}

		res, count, newEv, newCands, addedTokens, degraded, revOut, toolErr := s.runAgentTool(
			ctx, actor, allowedKBs, request.KnowledgeBaseID, plan.Tool, plan.Args, seenEvidence, len(evidence),
		)
		if revOut > rev {
			rev = revOut
		}
		for _, d := range degraded {
			degradedSet[d] = struct{}{}
		}
		candidates = append(candidates, newCands...)
		evidence = append(evidence, newEv...)
		evidenceTokens += addedTokens

		obs := agentObservation{
			Step:             step,
			Tool:             plan.Tool,
			NewEvidenceCount: len(newEv),
			TotalEvidence:    len(evidence),
		}
		if toolErr != nil {
			obs.Error = toolErr.Error()
		} else {
			obs.Result = res
		}
		observations = append(observations, obs)

		if err := send(Event{Step: &StepEvent{
			Step:        step,
			Tool:        plan.Tool,
			ArgsSummary: summarizeToolArgs(plan.Args),
			ResultCount: count,
		}}); err != nil {
			return err
		}

		if toolErr == nil && len(newEv) == 0 {
			emptyNewSteps++
			if (len(evidence) > 0 && emptyNewSteps >= 1) || emptyNewSteps >= 2 {
				break
			}
		} else {
			emptyNewSteps = 0
		}

		if evidenceTokens >= limits.MaxEvidenceTokens || time.Since(started) >= limits.MaxDuration || step == limits.MaxSteps {
			truncated = true
			break
		}
	}

	if evidence == nil {
		evidence = []AnswerEvidence{}
	}
	degraded := make([]string, 0, len(degradedSet))
	for d := range degradedSet {
		degraded = append(degraded, d)
	}
	sort.Strings(degraded)
	if err := send(Event{Retrieval: &RetrievalEvent{
		Evidence:          evidence,
		Degraded:          degraded,
		RetrievalRevision: rev,
	}}); err != nil {
		return err
	}

	outcome := "refused"
	var usage *models.ChatUsage
	filter := newCitationFilter(len(evidence))

	if len(evidence) == 0 {
		if truncated {
			outcome = "truncated"
		}
		if err := send(Event{Delta: &Delta{Text: Refusal}}); err != nil {
			return err
		}
	} else {
		prompt, err := json.Marshal(struct {
			Evidence []AnswerEvidence `json:"evidence"`
			Question string           `json:"question"`
		}{evidence, question})
		if err != nil {
			return err
		}
		prefix := ""
		result, err := generator.Stream(ctx, []models.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(prompt)},
		}, func(delta string) error {
			if len(prefix) < len(Refusal) {
				prefix += delta
				if len(prefix) > len(Refusal) {
					prefix = prefix[:len(Refusal)]
				}
			}
			if prefix == Refusal {
				filter.limit = 0
			}
			if clean := filter.Push(delta, false); clean != "" {
				return send(Event{Delta: &Delta{Text: clean}})
			}
			return nil
		})
		if err != nil {
			return err
		}
		if rest := filter.Push("", true); rest != "" {
			if err := send(Event{Delta: &Delta{Text: rest}}); err != nil {
				return err
			}
		}
		usage = result.Usage
		outcome = "answered"
		if len(filter.used) == 0 {
			outcome = "unsupported"
		}
		if prefix == Refusal {
			outcome = "refused"
		}
		if truncated || result.Finish == "length" {
			outcome = "truncated"
		}
	}

	citations := Citations{Items: []Citation{}, InvalidCount: filter.invalid}
	for n := range filter.used {
		citations.Items = append(citations.Items, Citation{N: n, EvidenceID: evidence[n-1].ID})
	}
	sort.Slice(citations.Items, func(i, j int) bool { return citations.Items[i].N < citations.Items[j].N })
	if err := send(Event{Citations: &citations}); err != nil {
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	if err := s.Audit(ctx, actor, request, outcome, len(evidence), filter.invalid); err != nil {
		return err
	}
	return send(Event{Done: &Done{
		MessageID:     rand.Text(),
		Outcome:       outcome,
		Usage:         usage,
		PromptVersion: PromptVersion,
	}})
}

func (s *Service) runAgentTool(
	ctx context.Context,
	actor retrieval.Actor,
	allowedKBs []int64,
	kbID int64,
	toolName string,
	rawArgs json.RawMessage,
	seen map[string]struct{},
	startIdx int,
) (res any, count int, newEv []AnswerEvidence, newCands []retrieval.Candidate, tokens int, degraded []string, rev int32, err error) {
	if s.Tools == nil {
		if toolName == "search" {
			var args struct {
				Query string `json:"query"`
				TopK  int    `json:"top_k"`
			}
			_ = json.Unmarshal(rawArgs, &args)
			found, searchErr := s.Search.Search(ctx, actor, retrieval.Request{
				KnowledgeBaseID: kbID,
				Query:           args.Query,
				Limit:           args.TopK,
			})
			if searchErr != nil {
				return nil, 0, nil, nil, 0, nil, 0, searchErr
			}
			degraded = found.Degraded
			rev = found.RetrievalRevision
			for _, cand := range found.Candidates {
				evID := fmt.Sprintf("c_%d", cand.ChunkID)
				if _, exists := seen[evID]; exists {
					continue
				}
				seen[evID] = struct{}{}
				n := startIdx + len(newEv) + 1
				var pages []int32
				if cand.PageStart != nil && cand.PageEnd != nil {
					pages = []int32{*cand.PageStart, *cand.PageEnd}
				}
				newCands = append(newCands, cand)
				newEv = append(newEv, AnswerEvidence{
					N:           n,
					ID:          evID,
					ChunkIDs:    []int64{cand.ChunkID},
					Content:     cand.Content,
					DocumentID:  cand.DocumentID,
					VersionID:   cand.VersionID,
					Title:       cand.Title,
					SectionID:   cand.SectionID,
					HeadingPath: cand.HeadingPath,
					Pages:       pages,
					TrustTier:   cand.TrustTier,
				})
				t, _ := s.CountTokens(ctx, cand.Content)
				tokens += t
			}
			return map[string]any{"count": len(found.Candidates)}, len(found.Candidates), newEv, newCands, tokens, degraded, rev, nil
		}
		if toolName == "exact_match" {
			type exactSearcher interface {
				ExactMatch(context.Context, retrieval.Actor, int64, string, bool, int) ([]retrieval.Candidate, error)
			}
			es, ok := s.Search.(exactSearcher)
			if !ok {
				return nil, 0, nil, nil, 0, nil, 0, fmt.Errorf("agent tools unavailable for %q", toolName)
			}
			var args struct {
				Pattern  string `json:"pattern"`
				UseRegex bool   `json:"use_regex"`
				Limit    int    `json:"limit"`
			}
			_ = json.Unmarshal(rawArgs, &args)
			cands, matchErr := es.ExactMatch(ctx, actor, kbID, args.Pattern, args.UseRegex, args.Limit)
			if matchErr != nil {
				return nil, 0, nil, nil, 0, nil, 0, matchErr
			}
			for _, cand := range cands {
				evID := fmt.Sprintf("c_%d", cand.ChunkID)
				if _, exists := seen[evID]; exists {
					continue
				}
				seen[evID] = struct{}{}
				n := startIdx + len(newEv) + 1
				var pages []int32
				if cand.PageStart != nil && cand.PageEnd != nil {
					pages = []int32{*cand.PageStart, *cand.PageEnd}
				}
				newCands = append(newCands, cand)
				newEv = append(newEv, AnswerEvidence{
					N:           n,
					ID:          evID,
					ChunkIDs:    []int64{cand.ChunkID},
					Content:     cand.Content,
					DocumentID:  cand.DocumentID,
					VersionID:   cand.VersionID,
					Title:       cand.Title,
					SectionID:   cand.SectionID,
					HeadingPath: cand.HeadingPath,
					Pages:       pages,
					TrustTier:   cand.TrustTier,
				})
				t, _ := s.CountTokens(ctx, cand.Content)
				tokens += t
			}
			return map[string]any{"count": len(cands)}, len(cands), newEv, newCands, tokens, nil, 0, nil
		}
		return nil, 0, nil, nil, 0, nil, 0, fmt.Errorf("agent tools unavailable for %q", toolName)
	}

	rawArgs = normalizeAgentToolArgs(toolName, kbID, rawArgs)
	res, err = s.Tools.ExecuteTool(ctx, actor, allowedKBs, toolName, rawArgs)
	if err != nil {
		return nil, 0, nil, nil, 0, nil, 0, err
	}

	rawJSON, _ := json.Marshal(res)
	switch toolName {
	case "search", "exact_match":
		var parsed struct {
			Evidence []struct {
				ID          string   `json:"id"`
				Content     string   `json:"content"`
				DocumentID  int64    `json:"document_id"`
				VersionID   int64    `json:"version_id"`
				Title       string   `json:"title"`
				SectionID   int64    `json:"section_id"`
				HeadingPath []string `json:"heading_path"`
				Pages       []int32  `json:"pages"`
			} `json:"evidence"`
			Degraded []string `json:"degraded"`
		}
		if json.Unmarshal(rawJSON, &parsed) == nil {
			count = len(parsed.Evidence)
			degraded = parsed.Degraded
			for _, item := range parsed.Evidence {
				if _, exists := seen[item.ID]; exists {
					continue
				}
				chunkID, parseErr := strconv.ParseInt(strings.TrimPrefix(item.ID, "c_"), 10, 64)
				if parseErr != nil || chunkID <= 0 {
					continue
				}
				seen[item.ID] = struct{}{}
				n := startIdx + len(newEv) + 1
				newCands = append(newCands, retrieval.Candidate{
					ChunkID:     chunkID,
					DocumentID:  item.DocumentID,
					VersionID:   item.VersionID,
					SectionID:   item.SectionID,
					Title:       item.Title,
					Content:     item.Content,
					HeadingPath: item.HeadingPath,
				})
				newEv = append(newEv, AnswerEvidence{
					N:           n,
					ID:          item.ID,
					ChunkIDs:    []int64{chunkID},
					Content:     item.Content,
					DocumentID:  item.DocumentID,
					VersionID:   item.VersionID,
					Title:       item.Title,
					SectionID:   item.SectionID,
					HeadingPath: item.HeadingPath,
					Pages:       item.Pages,
				})
				t, _ := s.CountTokens(ctx, item.Content)
				tokens += t
			}
		}
	case "list_documents":
		var parsed struct {
			Items []any `json:"items"`
		}
		if json.Unmarshal(rawJSON, &parsed) == nil {
			count = len(parsed.Items)
		}
	case "get_toc":
		var parsed struct {
			Sections []any `json:"sections"`
		}
		if json.Unmarshal(rawJSON, &parsed) == nil {
			count = len(parsed.Sections)
		}
	case "read_section":
		var parsed struct {
			Citation struct {
				DocumentID  int64    `json:"document_id"`
				VersionID   int64    `json:"version_id"`
				Title       string   `json:"title"`
				SectionID   int64    `json:"section_id"`
				HeadingPath []string `json:"heading_path"`
				Pages       []int32  `json:"pages"`
			} `json:"citation"`
			Markdown string `json:"markdown"`
		}
		if json.Unmarshal(rawJSON, &parsed) == nil && parsed.Markdown != "" {
			count = 1
			evID := fmt.Sprintf("s_%d", parsed.Citation.SectionID)
			if _, exists := seen[evID]; !exists {
				seen[evID] = struct{}{}
				n := startIdx + len(newEv) + 1
				newEv = append(newEv, AnswerEvidence{
					N:           n,
					ID:          evID,
					Content:     parsed.Markdown,
					DocumentID:  parsed.Citation.DocumentID,
					VersionID:   parsed.Citation.VersionID,
					Title:       parsed.Citation.Title,
					SectionID:   parsed.Citation.SectionID,
					HeadingPath: parsed.Citation.HeadingPath,
					Pages:       parsed.Citation.Pages,
				})
				t, _ := s.CountTokens(ctx, parsed.Markdown)
				tokens += t
			}
		}
	case "get_neighbors":
		var parsed struct {
			Citation struct {
				DocumentID  int64    `json:"document_id"`
				VersionID   int64    `json:"version_id"`
				Title       string   `json:"title"`
				SectionID   int64    `json:"section_id"`
				HeadingPath []string `json:"heading_path"`
			} `json:"citation"`
			Chunks []struct {
				ID      int64   `json:"id"`
				Content string  `json:"content"`
				Pages   []int32 `json:"pages"`
			} `json:"chunks"`
		}
		if json.Unmarshal(rawJSON, &parsed) == nil {
			count = len(parsed.Chunks)
			for _, ch := range parsed.Chunks {
				evID := fmt.Sprintf("c_%d", ch.ID)
				if _, exists := seen[evID]; exists {
					continue
				}
				seen[evID] = struct{}{}
				n := startIdx + len(newEv) + 1
				newCands = append(newCands, retrieval.Candidate{
					ChunkID:     ch.ID,
					DocumentID:  parsed.Citation.DocumentID,
					VersionID:   parsed.Citation.VersionID,
					SectionID:   parsed.Citation.SectionID,
					Title:       parsed.Citation.Title,
					Content:     ch.Content,
					HeadingPath: parsed.Citation.HeadingPath,
				})
				newEv = append(newEv, AnswerEvidence{
					N:           n,
					ID:          evID,
					ChunkIDs:    []int64{ch.ID},
					Content:     ch.Content,
					DocumentID:  parsed.Citation.DocumentID,
					VersionID:   parsed.Citation.VersionID,
					Title:       parsed.Citation.Title,
					SectionID:   parsed.Citation.SectionID,
					HeadingPath: parsed.Citation.HeadingPath,
					Pages:       ch.Pages,
				})
				t, _ := s.CountTokens(ctx, ch.Content)
				tokens += t
			}
		}
	}
	return res, count, newEv, newCands, tokens, degraded, rev, nil
}

func normalizeAgentToolArgs(toolName string, kbID int64, raw json.RawMessage) json.RawMessage {
	args := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}
	switch toolName {
	case "search", "exact_match":
		args["kb_ids"] = []int64{kbID}
	case "list_documents":
		args["kb_id"] = kbID
	}
	out, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return out
}

func parseAgentPlan(text string) (agentPlan, bool) {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return agentPlan{}, false
	}
	var plan agentPlan
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &plan); err != nil {
		return agentPlan{}, false
	}
	return plan, true
}

func summarizeToolArgs(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "{}"
	}
	runes := []rune(trimmed)
	if len(runes) > 120 {
		return string(runes[:117]) + "..."
	}
	return trimmed
}

func normalizeToolCallKey(tool string, raw json.RawMessage) string {
	var parsed any
	if len(raw) > 0 && json.Unmarshal(raw, &parsed) == nil {
		if canonical, err := json.Marshal(parsed); err == nil {
			return tool + ":" + string(canonical)
		}
	}
	return tool + ":" + strings.TrimSpace(string(raw))
}
