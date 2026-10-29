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
	Actor              retrieval.Actor          `json:"actor"`
	KnowledgeBaseID    int64                    `json:"knowledge_base_id"`
	Documents          map[string]int64         `json:"documents"`
	AllowedDocumentIDs []int64                  `json:"allowed_document_ids"`
	Reranker           *retrieval.RerankOptions `json:"reranker"`
	ModelLabel         string                   `json:"model_label"`
	ModelKind          string                   `json:"model_kind"`
}
type Searcher interface {
	Search(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)
}
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
	for _, doc := range dataset.Documents {
		if config.Documents[doc.Key] <= 0 || !allowed[config.Documents[doc.Key]] {
			return RunReport{}, fmt.Errorf("%w: missing document binding", ErrInvalidCase)
		}
	}
	datasetBytes, err := json.Marshal(dataset)
	if err != nil {
		return RunReport{}, err
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return RunReport{}, err
	}
	dh, ch := sha256.Sum256(datasetBytes), sha256.Sum256(configBytes)
	report := RunReport{DatasetVersion: dataset.Version, DatasetSHA256: hex.EncodeToString(dh[:]), ConfigSHA256: hex.EncodeToString(ch[:]), ModelLabel: config.ModelLabel, ModelKind: config.ModelKind, Mode: mode, Status: "failed", Rows: []RunRow{}, LatencyMS: map[string][2]float64{}}
	cases := make([]Case, 0, len(dataset.Questions))
	latencies := map[string][]float64{}
	for _, q := range dataset.Questions {
		request := retrieval.Request{KnowledgeBaseID: config.KnowledgeBaseID, Query: q.Question, Limit: 30, Debug: true}
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
		if result.Trace == nil {
			return report, errors.New("evaluation requires authorized trace")
		}
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
		item := Case{Answerable: q.Answerable, Fused: toEvidence(fused)}
		for _, key := range q.Expected {
			item.Expected = append(item.Expected, Target{DocumentID: config.Documents[key]})
		}
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
	if baseline.Status != "passed" || current.Status != "passed" || baseline.DatasetSHA256 != current.DatasetSHA256 || baseline.Mode != current.Mode || baseline.ModelKind != current.ModelKind || baseline.Summary.Cases != current.Summary.Cases || baseline.Summary.Metrics == nil || current.Summary.Metrics == nil {
		return errors.New("incomparable evaluation reports")
	}
	if current.UnauthorizedEvidence != 0 {
		return errors.New("unauthorized evidence regression")
	}
	a, b := baseline.Summary.Metrics, current.Summary.Metrics
	for _, delta := range []float64{a.RecallAt20 - b.RecallAt20, a.HitAt5 - b.HitAt5, a.MRRAt10 - b.MRRAt10, a.NDCGAt10 - b.NDCGAt10} {
		if delta > 0.02+1e-12 {
			return errors.New("retrieval metric declined by more than two percentage points")
		}
	}
	return nil
}
