package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"tuskrag.local/core/internal/retrieval"
)

type Document struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}
type Question struct {
	ID         string   `json:"id"`
	Question   string   `json:"question"`
	Expected   []string `json:"expected"`
	Answerable bool     `json:"answerable"`
	Tags       []string `json:"tags"`
}
type Dataset struct {
	Version   string     `json:"version"`
	Documents []Document `json:"documents"`
	Questions []Question `json:"questions"`
}
type RunConfig struct {
	Actor              retrieval.Actor                  `json:"actor"`
	KnowledgeBaseID    int64                            `json:"knowledge_base_id"`
	Documents          map[string]int64                 `json:"documents"`
	AllowedDocumentIDs []int64                          `json:"allowed_document_ids"`
	Reranker           *retrieval.RerankOptions         `json:"reranker"`
	ModelLabel         string                           `json:"model_label"`
	ModelKind          string                           `json:"model_kind"`
	Configuration      *retrieval.ConfigurationSnapshot `json:"configuration,omitempty"`
}
type Searcher interface {
	Search(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)
}

var ErrRetrievalRevisionChanged = errors.New("evaluation retrieval configuration changed")

type RunRow struct {
	ID        string             `json:"id"`
	Fused     []Evidence         `json:"fused"`
	Ranked    []Evidence         `json:"ranked"`
	Metrics   *Metrics           `json:"metrics"`
	TimingsMS map[string]float64 `json:"timings_ms"`
	Degraded  []string           `json:"degraded"`
}
type RunReport struct {
	DatasetVersion       string                `json:"dataset_version"`
	DatasetSHA256        string                `json:"dataset_sha256"`
	ConfigSHA256         string                `json:"config_sha256"`
	ModelLabel           string                `json:"model_label"`
	ModelKind            string                `json:"model_kind"`
	Mode                 string                `json:"mode"`
	Status               string                `json:"status"`
	Summary              Summary               `json:"summary"`
	UnauthorizedEvidence int                   `json:"unauthorized_evidence"`
	Rows                 []RunRow              `json:"rows"`
	LatencyMS            map[string][2]float64 `json:"latency_ms_p50_p95"`
}

func ValidateDataset(data Dataset) error {
	if data.Version == "" || len(data.Documents) == 0 || len(data.Questions) == 0 {
		return ErrInvalidCase
	}
	docs := map[string]bool{}
	ids := map[string]bool{}
	for _, doc := range data.Documents {
		if doc.Key == "" || doc.Title == "" || doc.Content == "" || docs[doc.Key] {
			return ErrInvalidCase
		}
		docs[doc.Key] = true
	}
	for _, q := range data.Questions {
		if q.ID == "" || ids[q.ID] || q.Question == "" || q.Answerable != (len(q.Expected) > 0) {
			return ErrInvalidCase
		}
		ids[q.ID] = true
		seen := map[string]bool{}
		for _, key := range q.Expected {
			if !docs[key] || seen[key] {
				return ErrInvalidCase
			}
			seen[key] = true
		}
	}
	return nil
}

// Run records only evidence IDs and scores; no queries, document bodies or credentials.
// lexical/vector are projections of the complete authorized Top50 union in trace.
func Run(ctx context.Context, service Searcher, dataset Dataset, config RunConfig, mode string) (RunReport, error) {
	if err := ValidateDataset(dataset); err != nil {
		return RunReport{}, err
	}
	allowed := map[int64]bool{}
	for _, id := range config.AllowedDocumentIDs {
		allowed[id] = true
	}
	for _, doc := range dataset.Documents {
		if config.Documents[doc.Key] <= 0 || !allowed[config.Documents[doc.Key]] {
			return RunReport{}, fmt.Errorf("%w: missing document binding", ErrInvalidCase)
		}
	}
	questions := make([]preparedQuestion, 0, len(dataset.Questions))
	for _, q := range dataset.Questions {
		item := preparedQuestion{ID: q.ID, Query: q.Question, Answerable: q.Answerable}
		for _, key := range q.Expected {
			item.Expected = append(item.Expected, Target{DocumentID: config.Documents[key]})
		}
		questions = append(questions, item)
	}
	data, err := json.Marshal(dataset)
	if err != nil {
		return RunReport{}, err
	}
	hash := sha256.Sum256(data)
	return runQuestions(ctx, service, dataset.Version, hex.EncodeToString(hash[:]), questions, config, mode)
}

type preparedQuestion struct {
	ID         string
	Query      string
	Answerable bool
	Expected   []Target
}

func runQuestions(ctx context.Context, service Searcher, version, digest string, questions []preparedQuestion, config RunConfig, mode string) (RunReport, error) {
	if service == nil || ctx == nil || config.Actor.TenantID <= 0 || config.Actor.UserID <= 0 || config.KnowledgeBaseID <= 0 || config.ModelLabel == "" || (config.ModelKind != "stub" && config.ModelKind != "real") {
		return RunReport{}, ErrInvalidCase
	}
	if mode != "lexical" && mode != "vector" && mode != "rrf" && mode != "rerank" {
		return RunReport{}, ErrInvalidCase
	}
	if mode == "rerank" && config.Reranker == nil {
		return RunReport{}, ErrInvalidCase
	}
	allowed := map[int64]bool{}
	for _, id := range config.AllowedDocumentIDs {
		if id <= 0 {
			return RunReport{}, ErrInvalidCase
		}
		allowed[id] = true
	}
	for _, question := range questions {
		for _, target := range question.Expected {
			if !allowed[target.DocumentID] {
				return RunReport{}, fmt.Errorf("%w: expected document outside allowed scope", ErrInvalidCase)
			}
		}
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		return RunReport{}, err
	}
	ch := sha256.Sum256(configBytes)
	report := RunReport{DatasetVersion: version, DatasetSHA256: digest, ConfigSHA256: hex.EncodeToString(ch[:]), ModelLabel: config.ModelLabel, ModelKind: config.ModelKind, Mode: mode, Status: "failed", Rows: []RunRow{}, LatencyMS: map[string][2]float64{}}
	cases := make([]Case, 0, len(questions))
	latencies := map[string][]float64{}
	var retrievalRevision int32
	revisionObserved := false
	for _, q := range questions {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		rerankEnabled := mode == "rerank"
		request := retrieval.Request{KnowledgeBaseID: config.KnowledgeBaseID, Query: q.Query, Limit: 30, Debug: true, RerankEnabled: &rerankEnabled}
		request.ExpectedConfiguration = config.Configuration
		if mode == "rerank" {
			options := *config.Reranker
			options.Fallback = false
			request.Rerank = &options
		}
		started := time.Now()
		result, err := service.Search(ctx, config.Actor, request)
		if err != nil {
			return report, fmt.Errorf("evaluation case %s: %w", q.ID, err)
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if result.Trace == nil {
			return report, errors.New("evaluation requires authorized trace")
		}
		if result.RetrievalRevision < 0 || result.Trace.RetrievalRevision != result.RetrievalRevision {
			return report, fmt.Errorf("%w: inconsistent retrieval revision", ErrInvalidCase)
		}
		// Revision zero is the real unconfigured state, not an unset sentinel.
		// A batch must not score questions against different saved settings.
		if revisionObserved && result.RetrievalRevision != retrievalRevision {
			return report, ErrRetrievalRevisionChanged
		}
		retrievalRevision, revisionObserved = result.RetrievalRevision, true
		for _, list := range [][]retrieval.Candidate{result.Candidates, result.Trace.Candidates} {
			for _, c := range list {
				if !allowed[c.DocumentID] {
					report.UnauthorizedEvidence++
				}
			}
		}
		if report.UnauthorizedEvidence > 0 {
			return report, errors.New("evaluation returned unauthorized evidence")
		}
		if mode == "rerank" && len(result.Degraded) > 0 {
			return report, errors.New("rerank evaluation degraded")
		}
		fused := append([]retrieval.Candidate{}, result.Trace.Candidates...)
		if mode == "lexical" || mode == "vector" {
			filtered := fused[:0]
			for _, c := range fused {
				if (mode == "lexical" && c.LexicalRank > 0) || (mode == "vector" && c.SemanticRank > 0) {
					filtered = append(filtered, c)
				}
			}
			fused = filtered
			sort.SliceStable(fused, func(i, j int) bool {
				if mode == "lexical" {
					return fused[i].LexicalRank < fused[j].LexicalRank
				}
				return fused[i].SemanticRank < fused[j].SemanticRank
			})
		}
		item := Case{Answerable: q.Answerable, Expected: q.Expected, Fused: toEvidence(fused)}

		if mode == "rerank" {
			item.Reranked = toEvidence(result.Candidates)
		} else {
			item.Reranked = item.Fused
		}
		metrics, err := Score(item)
		if err != nil {
			return report, err
		}
		cases = append(cases, item)
		timings := map[string]float64{}
		for key, value := range result.Trace.TimingsMS {
			timings[key] = value
		}
		timings["total"] = float64(time.Since(started).Microseconds()) / 1000
		for stage, ms := range timings {
			latencies[stage] = append(latencies[stage], ms)
		}
		report.Rows = append(report.Rows, RunRow{ID: q.ID, Fused: item.Fused, Ranked: item.Reranked, Metrics: metrics, TimingsMS: timings, Degraded: result.Degraded})
	}
	report.Summary, err = Aggregate(cases)
	if err != nil {
		return report, err
	}
	for stage, values := range latencies {
		sort.Float64s(values)
		report.LatencyMS[stage] = [2]float64{values[(len(values)-1)/2], values[(95*len(values)+99)/100-1]}
	}
	report.Status = "passed"
	return report, nil
}

func toEvidence(candidates []retrieval.Candidate) []Evidence {
	result := make([]Evidence, 0, len(candidates))
	for _, c := range candidates {
		e := Evidence{ChunkID: c.ChunkID, DocumentID: c.DocumentID, SectionID: c.SectionID}
		if c.PageStart != nil && c.PageEnd != nil {
			e.PageStart = *c.PageStart
			e.PageEnd = *c.PageEnd
		}
		result = append(result, e)
	}
	return result
}

func Compare(baseline, current RunReport) error {
	if err := validateComparisonSummary(baseline); err != nil {
		return fmt.Errorf("baseline report: %w", err)
	}
	if err := validateComparisonSummary(current); err != nil {
		return fmt.Errorf("current report: %w", err)
	}
	if !sameComparisonPopulation(baseline, current) {
		return ErrReportsIncomparable
	}
	if len(baseline.Rows) > 0 && len(current.Rows) > 0 {
		if _, err := alignComparisonRows(baseline, current); err != nil {
			return err
		}
	}
	a, b := baseline.Summary.Metrics, current.Summary.Metrics
	if metricRegression(metricDifference(*a, *b)) {
		return errors.New("retrieval metric declined by more than two percentage points")
	}
	return nil
}

// Committed CI baselines intentionally omit rows; validate their summary rather
// than requiring per-question data that those artifacts do not contain.
func validateComparisonSummary(report RunReport) error {
	if len(report.Rows) > 0 {
		if err := ValidateReport(report); err != nil {
			return err
		}
	}
	s := report.Summary
	if report.Status != "passed" || report.DatasetSHA256 == "" ||
		(report.Mode != "lexical" && report.Mode != "vector" && report.Mode != "rrf" && report.Mode != "rerank") ||
		(report.ModelKind != "real" && report.ModelKind != "stub") {
		return errors.New("invalid evaluation report identity or status")
	}
	if report.UnauthorizedEvidence != 0 {
		return errors.New("evaluation report must have zero unauthorized evidence")
	}
	if s.Cases <= 0 || s.AnswerableCases <= 0 || s.AnswerableCases > s.Cases || s.UnanswerableCases != s.Cases-s.AnswerableCases || s.Metrics == nil {
		return errors.New("invalid evaluation summary counts or missing metrics")
	}
	if !validMetrics(s.Metrics) {
		return errors.New("evaluation metrics must be finite values between zero and one")
	}
	return nil
}
