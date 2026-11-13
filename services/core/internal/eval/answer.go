package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/retrieval"
)

const answerGateTolerance = 0.02 + 1e-12

type AnswerQuestion struct {
	ID         string   `json:"id"`
	Question   string   `json:"question"`
	Expected   []string `json:"expected"`
	Answerable bool     `json:"answerable"`
	Contains   []string `json:"contains,omitempty"`
	Forbidden  []string `json:"forbidden,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

func (q *AnswerQuestion) UnmarshalJSON(data []byte) error {
	type rawQuestion AnswerQuestion
	wire := struct {
		*rawQuestion
		Answerable *bool `json:"answerable"`
	}{rawQuestion: (*rawQuestion)(q)}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return err
	}
	if wire.Answerable == nil {
		return fmt.Errorf("%w: explicit answerable boolean required", ErrInvalidCase)
	}
	q.Answerable = *wire.Answerable
	return nil
}

type AnswerDataset struct {
	Version   string           `json:"version"`
	Documents []Document       `json:"documents"`
	Questions []AnswerQuestion `json:"questions"`
}

type AnswerJudgeConfig struct {
	ChatModelID   int64  `json:"chat_model_id"`
	ModelLabel    string `json:"model_label"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

type AnswerRunConfig struct {
	Actor               retrieval.Actor    `json:"actor"`
	KnowledgeBaseID     int64              `json:"knowledge_base_id"`
	ChatModelID         int64              `json:"chat_model_id"`
	Documents           map[string]int64   `json:"documents"`
	AllowedDocumentIDs  []int64            `json:"allowed_document_ids"`
	ModelLabel          string             `json:"model_label"`
	ModelKind           string             `json:"model_kind"`
	ParentBacktrack     *bool              `json:"parent_backtrack,omitempty"`
	ContextBudgetTokens int                `json:"context_budget_tokens,omitempty"`
	Judge               *AnswerJudgeConfig `json:"judge,omitempty"`
}

type AnswerAssemblyReport struct {
	ParentBacktrack     bool `json:"parent_backtrack"`
	ContextBudgetTokens int  `json:"context_budget_tokens"`
}

type AnswerRow struct {
	ID                  string             `json:"id"`
	Answerable          bool               `json:"answerable"`
	Outcome             string             `json:"outcome"`
	ValidCitation       bool               `json:"valid_citation"`
	SourceIntegrity     bool               `json:"source_integrity"`
	EvidenceHit         *bool              `json:"evidence_hit"`
	DeterministicPass   *bool              `json:"deterministic_pass"`
	EvidenceIDs         []string           `json:"evidence_ids"`
	EvidenceChunkCounts []int              `json:"evidence_chunk_counts"`
	EvidenceDocuments   []int64            `json:"evidence_documents"`
	Degraded            []string           `json:"degraded"`
	CitationCount       int                `json:"citation_count"`
	InvalidCitations    int                `json:"invalid_citations"`
	JudgeClaims         *int               `json:"judge_claims,omitempty"`
	JudgeUnsupported    *int               `json:"judge_unsupported,omitempty"`
	TimingsMS           map[string]float64 `json:"timings_ms"`
}

type AnswerMetrics struct {
	ValidCitationRate     float64  `json:"valid_citation_rate"`
	SourceIntegrityRate   float64  `json:"source_integrity_rate"`
	EvidenceHitRate       *float64 `json:"evidence_hit_rate"`
	DeterministicPassRate *float64 `json:"deterministic_pass_rate"`
	RefusalCorrectRate    *float64 `json:"refusal_correct_rate"`
	FalseRefusalRate      *float64 `json:"false_refusal_rate"`
	UnsupportedRate       *float64 `json:"unsupported_rate"`
	CitationSupportRate   *float64 `json:"citation_support_rate"`
}

type AnswerSummary struct {
	Cases              int           `json:"cases"`
	AnswerableCases    int           `json:"answerable_cases"`
	UnanswerableCases  int           `json:"unanswerable_cases"`
	DeterministicCases int           `json:"deterministic_cases"`
	JudgedClaims       int           `json:"judged_claims"`
	SupportedClaims    int           `json:"supported_claims"`
	Metrics            AnswerMetrics `json:"metrics"`
}

type AnswerReport struct {
	DatasetVersion       string                `json:"dataset_version"`
	DatasetSHA256        string                `json:"dataset_sha256"`
	ConfigSHA256         string                `json:"config_sha256"`
	ModelLabel           string                `json:"model_label"`
	ModelKind            string                `json:"model_kind"`
	PromptVersion        string                `json:"prompt_version"`
	Assembly             AnswerAssemblyReport  `json:"assembly"`
	Judge                *AnswerJudgeConfig    `json:"judge,omitempty"`
	Status               string                `json:"status"`
	Summary              AnswerSummary         `json:"summary"`
	UnauthorizedEvidence int                   `json:"unauthorized_evidence"`
	Rows                 []AnswerRow           `json:"rows"`
	LatencyMS            map[string][2]float64 `json:"latency_ms_p50_p95"`
}

type AnswerMetricDelta struct {
	ValidCitationRate     float64  `json:"valid_citation_rate"`
	SourceIntegrityRate   float64  `json:"source_integrity_rate"`
	EvidenceHitRate       *float64 `json:"evidence_hit_rate"`
	DeterministicPassRate *float64 `json:"deterministic_pass_rate"`
	RefusalCorrectRate    *float64 `json:"refusal_correct_rate"`
	FalseRefusalRate      *float64 `json:"false_refusal_rate"`
	UnsupportedRate       *float64 `json:"unsupported_rate"`
	CitationSupportRate   *float64 `json:"citation_support_rate"`
}

type AnswerReportComparison struct {
	Gate     string            `json:"gate" enum:"passed,regressed"`
	Baseline AnswerSummary     `json:"baseline"`
	Current  AnswerSummary     `json:"current"`
	Delta    AnswerMetricDelta `json:"delta"`
}

// AnswerStreamer abstracts answer.Service.Stream for deterministic testing and CLI evaluation.
type AnswerStreamer interface {
	Stream(ctx context.Context, actor retrieval.Actor, req answer.Request, emit func(answer.Event) error) error
}

func ValidateAnswerDataset(data AnswerDataset) error {
	if strings.TrimSpace(data.Version) == "" || len(data.Documents) == 0 || len(data.Questions) == 0 {
		return ErrInvalidCase
	}
	docs := make(map[string]bool, len(data.Documents))
	for _, doc := range data.Documents {
		if strings.TrimSpace(doc.Key) == "" || strings.TrimSpace(doc.Title) == "" || strings.TrimSpace(doc.Content) == "" || docs[doc.Key] {
			return ErrInvalidCase
		}
		docs[doc.Key] = true
	}
	ids := make(map[string]bool, len(data.Questions))
	for _, q := range data.Questions {
		if strings.TrimSpace(q.ID) == "" || ids[q.ID] || strings.TrimSpace(q.Question) == "" || q.Answerable != (len(q.Expected) > 0) {
			return ErrInvalidCase
		}
		ids[q.ID] = true
		seen := make(map[string]bool, len(q.Expected))
		for _, key := range q.Expected {
			if !docs[key] || seen[key] {
				return ErrInvalidCase
			}
			seen[key] = true
		}
		for _, term := range q.Contains {
			if strings.TrimSpace(term) == "" {
				return ErrInvalidCase
			}
		}
		for _, term := range q.Forbidden {
			if strings.TrimSpace(term) == "" {
				return ErrInvalidCase
			}
		}
	}
	return nil
}

// ResolveAssembly resolves explicit defaults for assembly options.
func (c AnswerRunConfig) ResolveAssembly() answer.AssemblyOptions {
	opts := answer.DefaultAssemblyOptions()
	if c.ParentBacktrack != nil {
		opts.ParentBacktrack = *c.ParentBacktrack
	}
	if c.ContextBudgetTokens > 0 {
		opts.ContextBudgetTokens = c.ContextBudgetTokens
	}
	return opts
}

// RunAnswer executes a full generation evaluation suite and records only aggregate metrics and non-text metadata.
func RunAnswer(ctx context.Context, streamer AnswerStreamer, judge CitationJudge, dataset AnswerDataset, config AnswerRunConfig) (AnswerReport, error) {
	if err := ValidateAnswerDataset(dataset); err != nil {
		return AnswerReport{}, err
	}
	if streamer == nil || ctx == nil || config.Actor.TenantID <= 0 || config.Actor.UserID <= 0 ||
		config.KnowledgeBaseID <= 0 || config.ChatModelID <= 0 || strings.TrimSpace(config.ModelLabel) == "" ||
		(config.ModelKind != "stub" && config.ModelKind != "real") || config.ContextBudgetTokens < 0 {
		return AnswerReport{}, ErrInvalidCase
	}
	var resolvedJudge *AnswerJudgeConfig
	if config.Judge != nil {
		if judge == nil || config.Judge.ChatModelID <= 0 || strings.TrimSpace(config.Judge.ModelLabel) == "" {
			return AnswerReport{}, ErrInvalidCase
		}
		promptVer := config.Judge.PromptVersion
		if strings.TrimSpace(promptVer) == "" {
			promptVer = JudgePromptVersion
		}
		resolvedJudge = &AnswerJudgeConfig{
			ChatModelID:   config.Judge.ChatModelID,
			ModelLabel:    config.Judge.ModelLabel,
			PromptVersion: promptVer,
		}
	}
	allowed := make(map[int64]bool, len(config.AllowedDocumentIDs))
	for _, id := range config.AllowedDocumentIDs {
		if id <= 0 {
			return AnswerReport{}, ErrInvalidCase
		}
		allowed[id] = true
	}
	for _, doc := range dataset.Documents {
		if config.Documents[doc.Key] <= 0 || !allowed[config.Documents[doc.Key]] {
			return AnswerReport{}, fmt.Errorf("%w: missing or unauthorized document binding for %s", ErrInvalidCase, doc.Key)
		}
	}

	datasetBytes, err := json.Marshal(dataset)
	if err != nil {
		return AnswerReport{}, err
	}
	datasetHash := sha256.Sum256(datasetBytes)
	configBytes, err := json.Marshal(config)
	if err != nil {
		return AnswerReport{}, err
	}
	configHash := sha256.Sum256(configBytes)
	assemblyOpts := config.ResolveAssembly()

	report := AnswerReport{
		DatasetVersion: dataset.Version,
		DatasetSHA256:  hex.EncodeToString(datasetHash[:]),
		ConfigSHA256:   hex.EncodeToString(configHash[:]),
		ModelLabel:     config.ModelLabel,
		ModelKind:      config.ModelKind,
		PromptVersion:  answer.PromptVersion,
		Assembly: AnswerAssemblyReport{
			ParentBacktrack:     assemblyOpts.ParentBacktrack,
			ContextBudgetTokens: assemblyOpts.ContextBudgetTokens,
		},
		Judge:     resolvedJudge,
		Status:    "failed",
		Rows:      make([]AnswerRow, 0, len(dataset.Questions)),
		LatencyMS: map[string][2]float64{},
	}

	latencies := map[string][]float64{}
	for _, q := range dataset.Questions {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		expectedDocs := make(map[int64]bool, len(q.Expected))
		for _, key := range q.Expected {
			expectedDocs[config.Documents[key]] = true
		}

		var evidence []answer.AnswerEvidence
		var degraded []string
		var citations *answer.Citations
		var done *answer.Done
		var text strings.Builder
		var firstDeltaMS *float64
		started := time.Now()

		err := streamer.Stream(ctx, config.Actor, answer.Request{
			KnowledgeBaseID: config.KnowledgeBaseID,
			ModelID:         config.ChatModelID,
			Question:        q.Question,
		}, func(ev answer.Event) error {
			if ev.Retrieval != nil {
				evidence = append([]answer.AnswerEvidence(nil), ev.Retrieval.Evidence...)
				degraded = append([]string{}, ev.Retrieval.Degraded...)
			}
			if ev.Delta != nil {
				if firstDeltaMS == nil && ev.Delta.Text != "" {
					ms := float64(time.Since(started).Microseconds()) / 1000
					firstDeltaMS = &ms
				}
				text.WriteString(ev.Delta.Text)
			}
			if ev.Citations != nil {
				copied := *ev.Citations
				copied.Items = append([]answer.Citation(nil), ev.Citations.Items...)
				citations = &copied
			}
			if ev.Done != nil {
				copied := *ev.Done
				done = &copied
			}
			return nil
		})
		totalMS := float64(time.Since(started).Microseconds()) / 1000
		if err != nil {
			return report, fmt.Errorf("answer evaluation case %s: %w", q.ID, err)
		}
		if citations == nil || done == nil {
			return report, fmt.Errorf("answer evaluation case %s: incomplete stream", q.ID)
		}

		evidenceIDs := make([]string, 0, len(evidence))
		chunkCounts := make([]int, 0, len(evidence))
		evidenceDocs := make([]int64, 0, len(evidence))
		hit := false
		for _, item := range evidence {
			if !allowed[item.DocumentID] {
				report.UnauthorizedEvidence++
			}
			if expectedDocs[item.DocumentID] {
				hit = true
			}
			evidenceIDs = append(evidenceIDs, item.ID)
			cnt := len(item.ChunkIDs)
			if cnt == 0 {
				cnt = 1
			}
			chunkCounts = append(chunkCounts, cnt)
			evidenceDocs = append(evidenceDocs, item.DocumentID)
		}
		if report.UnauthorizedEvidence > 0 {
			return report, errors.New("answer evaluation returned unauthorized evidence")
		}

		sourceIntegrity := true
		for _, cite := range citations.Items {
			if cite.N < 1 || cite.N > len(evidence) {
				sourceIntegrity = false
				break
			}
			item := evidence[cite.N-1]
			if item.ID != cite.EvidenceID || !allowed[item.DocumentID] || strings.TrimSpace(item.Content) == "" {
				sourceIntegrity = false
				break
			}
		}
		validCitation := citations.InvalidCount == 0 && sourceIntegrity

		var evidenceHit *bool
		if q.Answerable {
			h := hit
			evidenceHit = &h
		}
		var detPass *bool
		if len(q.Contains) > 0 || len(q.Forbidden) > 0 {
			ok := checkDeterministic(q, text.String(), done.Outcome, len(citations.Items), citations.InvalidCount)
			detPass = &ok
		}

		var judgeClaims, judgeUnsupported *int
		if resolvedJudge != nil && (done.Outcome == "answered" || done.Outcome == "truncated") && len(citations.Items) > 0 {
			judgment, err := judge.Judge(ctx, q.Question, text.String(), evidence)
			if err != nil {
				return report, fmt.Errorf("judge case %s: %w", q.ID, err)
			}
			jc, ju := judgment.TotalClaims, judgment.UnsupportedClaims
			judgeClaims, judgeUnsupported = &jc, &ju
		}

		timings := map[string]float64{"total": totalMS}
		if firstDeltaMS != nil {
			timings["first_delta"] = *firstDeltaMS
		}
		for stage, ms := range timings {
			latencies[stage] = append(latencies[stage], ms)
		}
		if degraded == nil {
			degraded = []string{}
		}

		report.Rows = append(report.Rows, AnswerRow{
			ID:                  q.ID,
			Answerable:          q.Answerable,
			Outcome:             done.Outcome,
			ValidCitation:       validCitation,
			SourceIntegrity:     sourceIntegrity,
			EvidenceHit:         evidenceHit,
			DeterministicPass:   detPass,
			EvidenceIDs:         evidenceIDs,
			EvidenceChunkCounts: chunkCounts,
			EvidenceDocuments:   evidenceDocs,
			Degraded:            degraded,
			CitationCount:       len(citations.Items),
			InvalidCitations:    citations.InvalidCount,
			JudgeClaims:         judgeClaims,
			JudgeUnsupported:    judgeUnsupported,
			TimingsMS:           timings,
		})
	}

	summary, err := summarizeAnswerRows(report.Rows, resolvedJudge != nil)
	if err != nil {
		return report, err
	}
	report.Summary = summary
	for stage, values := range latencies {
		sort.Float64s(values)
		report.LatencyMS[stage] = [2]float64{values[(len(values)-1)/2], values[(95*len(values)+99)/100-1]}
	}
	report.Status = "passed"
	return report, nil
}

func checkDeterministic(q AnswerQuestion, answerText, outcome string, citationCount, invalidCount int) bool {
	if invalidCount != 0 {
		return false
	}
	if q.Answerable {
		if outcome != "answered" || citationCount == 0 {
			return false
		}
	} else if outcome != "refused" || citationCount != 0 {
		return false
	}
	for _, term := range q.Contains {
		if !strings.Contains(answerText, term) {
			return false
		}
	}
	for _, term := range q.Forbidden {
		if strings.Contains(answerText, term) {
			return false
		}
	}
	return true
}

func summarizeAnswerRows(rows []AnswerRow, judged bool) (AnswerSummary, error) {
	if len(rows) == 0 {
		return AnswerSummary{}, errors.New("answer evaluation report has no rows")
	}
	var s AnswerSummary
	s.Cases = len(rows)
	var validCitations, sourceIntegrity int
	var evidenceHits, detPasses int
	var refusalCorrect, falseRefusal, unsupported int

	for _, row := range rows {
		switch row.Outcome {
		case "answered", "refused", "unsupported", "truncated":
		default:
			return AnswerSummary{}, fmt.Errorf("invalid answer outcome %q", row.Outcome)
		}
		if row.ValidCitation {
			validCitations++
		}
		if row.SourceIntegrity {
			sourceIntegrity++
		}
		if row.DeterministicPass != nil {
			s.DeterministicCases++
			if *row.DeterministicPass {
				detPasses++
			}
		}
		if row.Answerable {
			s.AnswerableCases++
			if row.EvidenceHit != nil && *row.EvidenceHit {
				evidenceHits++
			}
			if row.Outcome == "refused" {
				falseRefusal++
			}
			if row.Outcome == "unsupported" {
				unsupported++
			}
		} else {
			s.UnanswerableCases++
			if row.Outcome == "refused" && row.CitationCount == 0 && row.InvalidCitations == 0 {
				refusalCorrect++
			}
		}
		if row.JudgeClaims != nil || row.JudgeUnsupported != nil {
			if !judged || row.JudgeClaims == nil || row.JudgeUnsupported == nil || *row.JudgeClaims < 0 || *row.JudgeUnsupported < 0 || *row.JudgeUnsupported > *row.JudgeClaims {
				return AnswerSummary{}, errors.New("invalid judge claim counts in row")
			}
			s.JudgedClaims += *row.JudgeClaims
			s.SupportedClaims += *row.JudgeClaims - *row.JudgeUnsupported
		}
	}

	s.Metrics.ValidCitationRate = float64(validCitations) / float64(s.Cases)
	s.Metrics.SourceIntegrityRate = float64(sourceIntegrity) / float64(s.Cases)
	if s.AnswerableCases > 0 {
		hitRate := float64(evidenceHits) / float64(s.AnswerableCases)
		falseRate := float64(falseRefusal) / float64(s.AnswerableCases)
		unsupRate := float64(unsupported) / float64(s.AnswerableCases)
		s.Metrics.EvidenceHitRate = &hitRate
		s.Metrics.FalseRefusalRate = &falseRate
		s.Metrics.UnsupportedRate = &unsupRate
	}
	if s.UnanswerableCases > 0 {
		refRate := float64(refusalCorrect) / float64(s.UnanswerableCases)
		s.Metrics.RefusalCorrectRate = &refRate
	}
	if s.DeterministicCases > 0 {
		detRate := float64(detPasses) / float64(s.DeterministicCases)
		s.Metrics.DeterministicPassRate = &detRate
	}
	if judged {
		supportRate := 0.0
		if s.JudgedClaims > 0 {
			supportRate = float64(s.SupportedClaims) / float64(s.JudgedClaims)
		}
		s.Metrics.CitationSupportRate = &supportRate
	}
	return s, nil
}

// ValidateAnswerReport checks complete consistency of a passed answer evaluation artifact.
func ValidateAnswerReport(report AnswerReport) error {
	if report.Status != "passed" || report.UnauthorizedEvidence != 0 ||
		strings.TrimSpace(report.DatasetVersion) == "" || strings.TrimSpace(report.ModelLabel) == "" ||
		strings.TrimSpace(report.PromptVersion) == "" || report.Assembly.ContextBudgetTokens <= 0 ||
		(report.ModelKind != "real" && report.ModelKind != "stub") {
		return errors.New("invalid answer evaluation report identity or status")
	}
	for _, digest := range []string{report.DatasetSHA256, report.ConfigSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
			return errors.New("invalid answer report digest")
		}
	}
	if report.Judge != nil && (report.Judge.ChatModelID <= 0 || strings.TrimSpace(report.Judge.ModelLabel) == "" || strings.TrimSpace(report.Judge.PromptVersion) == "") {
		return errors.New("invalid judge configuration in answer report")
	}
	if len(report.Rows) == 0 || len(report.Rows) != report.Summary.Cases {
		return errors.New("answer report rows disagree with summary case count")
	}
	ids := make(map[string]bool, len(report.Rows))
	latencies := map[string][]float64{}
	for _, row := range report.Rows {
		if strings.TrimSpace(row.ID) == "" || ids[row.ID] {
			return errors.New("empty or duplicate answer report question ID")
		}
		ids[row.ID] = true
		if row.Answerable != (row.EvidenceHit != nil) {
			return errors.New("answerable flag disagrees with evidence_hit presence")
		}
		if len(row.EvidenceIDs) != len(row.EvidenceDocuments) || len(row.EvidenceIDs) != len(row.EvidenceChunkCounts) {
			return errors.New("answer report row evidence arrays mismatch")
		}
		if _, ok := row.TimingsMS["total"]; !ok {
			return errors.New("answer report question missing total latency")
		}
		for stage, value := range row.TimingsMS {
			if strings.TrimSpace(stage) == "" || !finiteNonnegative(value) {
				return errors.New("invalid answer report latency")
			}
			latencies[stage] = append(latencies[stage], value)
		}
	}
	expected, err := summarizeAnswerRows(report.Rows, report.Judge != nil)
	if err != nil {
		return err
	}
	if !answerSummaryEqual(report.Summary, expected) {
		return errors.New("answer report summary disagrees with per-question rows")
	}
	if len(latencies) != len(report.LatencyMS) {
		return errors.New("answer report latency stages disagree with rows")
	}
	for stage, values := range latencies {
		sort.Float64s(values)
		pair, ok := report.LatencyMS[stage]
		if !ok || !finiteNonnegative(pair[0]) || !finiteNonnegative(pair[1]) ||
			math.Abs(pair[0]-values[(len(values)-1)/2]) > 1e-12 ||
			math.Abs(pair[1]-values[(95*len(values)+99)/100-1]) > 1e-12 {
			return errors.New("answer report latency summary disagrees with row timings")
		}
	}
	return nil
}

func answerSummaryEqual(a, b AnswerSummary) bool {
	if a.Cases != b.Cases || a.AnswerableCases != b.AnswerableCases || a.UnanswerableCases != b.UnanswerableCases ||
		a.DeterministicCases != b.DeterministicCases || a.JudgedClaims != b.JudgedClaims || a.SupportedClaims != b.SupportedClaims {
		return false
	}
	if !floatEqual(a.Metrics.ValidCitationRate, b.Metrics.ValidCitationRate) ||
		!floatEqual(a.Metrics.SourceIntegrityRate, b.Metrics.SourceIntegrityRate) {
		return false
	}
	pairs := [][2]*float64{
		{a.Metrics.EvidenceHitRate, b.Metrics.EvidenceHitRate},
		{a.Metrics.DeterministicPassRate, b.Metrics.DeterministicPassRate},
		{a.Metrics.RefusalCorrectRate, b.Metrics.RefusalCorrectRate},
		{a.Metrics.FalseRefusalRate, b.Metrics.FalseRefusalRate},
		{a.Metrics.UnsupportedRate, b.Metrics.UnsupportedRate},
		{a.Metrics.CitationSupportRate, b.Metrics.CitationSupportRate},
	}
	for _, p := range pairs {
		if (p[0] == nil) != (p[1] == nil) {
			return false
		}
		if p[0] != nil && !floatEqual(*p[0], *p[1]) {
			return false
		}
	}
	return true
}

func floatEqual(a, b float64) bool {
	return finiteNonnegative(a) && finiteNonnegative(b) && math.Abs(a-b) <= 1e-12
}

// CompareAnswerReports enforces dataset/population/judge comparability and a two-percentage-point gate.
func CompareAnswerReports(baseline, current AnswerReport) (AnswerReportComparison, error) {
	if err := ValidateAnswerReport(baseline); err != nil {
		return AnswerReportComparison{}, fmt.Errorf("baseline answer report: %w", err)
	}
	if err := ValidateAnswerReport(current); err != nil {
		return AnswerReportComparison{}, fmt.Errorf("current answer report: %w", err)
	}
	if baseline.DatasetSHA256 != current.DatasetSHA256 ||
		baseline.ModelKind != current.ModelKind ||
		baseline.Summary.Cases != current.Summary.Cases ||
		baseline.Summary.AnswerableCases != current.Summary.AnswerableCases ||
		(baseline.Judge != nil) != (current.Judge != nil) {
		return AnswerReportComparison{}, ErrReportsIncomparable
	}
	byID := make(map[string]AnswerRow, len(current.Rows))
	for _, r := range current.Rows {
		byID[r.ID] = r
	}
	for _, bRow := range baseline.Rows {
		cRow, ok := byID[bRow.ID]
		if !ok || bRow.Answerable != cRow.Answerable || (bRow.DeterministicPass == nil) != (cRow.DeterministicPass == nil) {
			return AnswerReportComparison{}, ErrReportsIncomparable
		}
	}

	delta := AnswerMetricDelta{
		ValidCitationRate:     current.Summary.Metrics.ValidCitationRate - baseline.Summary.Metrics.ValidCitationRate,
		SourceIntegrityRate:   current.Summary.Metrics.SourceIntegrityRate - baseline.Summary.Metrics.SourceIntegrityRate,
		EvidenceHitRate:       ptrDelta(baseline.Summary.Metrics.EvidenceHitRate, current.Summary.Metrics.EvidenceHitRate),
		DeterministicPassRate: ptrDelta(baseline.Summary.Metrics.DeterministicPassRate, current.Summary.Metrics.DeterministicPassRate),
		RefusalCorrectRate:    ptrDelta(baseline.Summary.Metrics.RefusalCorrectRate, current.Summary.Metrics.RefusalCorrectRate),
		FalseRefusalRate:      ptrDelta(baseline.Summary.Metrics.FalseRefusalRate, current.Summary.Metrics.FalseRefusalRate),
		UnsupportedRate:       ptrDelta(baseline.Summary.Metrics.UnsupportedRate, current.Summary.Metrics.UnsupportedRate),
		CitationSupportRate:   ptrDelta(baseline.Summary.Metrics.CitationSupportRate, current.Summary.Metrics.CitationSupportRate),
	}
	gate := "passed"
	if answerMetricRegressed(delta) {
		gate = "regressed"
	}
	return AnswerReportComparison{
		Gate:     gate,
		Baseline: baseline.Summary,
		Current:  current.Summary,
		Delta:    delta,
	}, nil
}

func ptrDelta(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	d := *b - *a
	return &d
}

func answerMetricRegressed(delta AnswerMetricDelta) bool {
	positiveDeltas := []*float64{
		&delta.ValidCitationRate,
		&delta.SourceIntegrityRate,
		delta.EvidenceHitRate,
		delta.DeterministicPassRate,
		delta.RefusalCorrectRate,
		delta.CitationSupportRate,
	}
	for _, v := range positiveDeltas {
		if v != nil && *v < -answerGateTolerance {
			return true
		}
	}
	negativeDeltas := []*float64{
		delta.FalseRefusalRate,
		delta.UnsupportedRate,
	}
	for _, v := range negativeDeltas {
		if v != nil && *v > answerGateTolerance {
			return true
		}
	}
	return false
}
