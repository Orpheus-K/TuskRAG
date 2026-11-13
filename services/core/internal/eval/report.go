package eval

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ValidateReport checks a complete successful artifact before displaying its
// per-question results. It cannot verify relevance or authorization without the
// original dataset and current document permissions.
func ValidateReport(report RunReport) error {
	if report.Status != "passed" || report.UnauthorizedEvidence != 0 ||
		strings.TrimSpace(report.DatasetVersion) == "" || strings.TrimSpace(report.ModelLabel) == "" ||
		(report.ModelKind != "real" && report.ModelKind != "stub") ||
		(report.Mode != "lexical" && report.Mode != "vector" && report.Mode != "rrf" && report.Mode != "rerank") {
		return errors.New("invalid complete report identity or status")
	}
	for _, digest := range []string{report.DatasetSHA256, report.ConfigSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
			return errors.New("invalid report digest")
		}
	}
	s := report.Summary
	if s.Cases <= 0 || len(report.Rows) != s.Cases || s.AnswerableCases < 0 || s.AnswerableCases > s.Cases || s.UnanswerableCases != s.Cases-s.AnswerableCases {
		return errors.New("incomplete report or inconsistent counts")
	}
	ids := make(map[string]bool, len(report.Rows))
	latencies := map[string][]float64{}
	var sum Metrics
	answerable := 0
	for _, row := range report.Rows {
		if strings.TrimSpace(row.ID) == "" || ids[row.ID] {
			return errors.New("empty or duplicate report question ID")
		}
		ids[row.ID] = true
		if err := validate(Case{Fused: row.Fused, Reranked: row.Ranked}); err != nil {
			return fmt.Errorf("report evidence: %w", err)
		}
		if report.Mode == "rerank" && len(row.Degraded) != 0 {
			return errors.New("successful rerank report contains degraded results")
		}
		if row.Metrics != nil {
			if !validMetrics(row.Metrics) {
				return errors.New("invalid per-question metrics")
			}
			answerable++
			sum.RecallAt20 += row.Metrics.RecallAt20
			sum.HitAt5 += row.Metrics.HitAt5
			sum.MRRAt10 += row.Metrics.MRRAt10
			sum.NDCGAt10 += row.Metrics.NDCGAt10
		}
		if _, ok := row.TimingsMS["total"]; !ok {
			return errors.New("report question is missing total latency")
		}
		for stage, value := range row.TimingsMS {
			if strings.TrimSpace(stage) == "" || !finiteNonnegative(value) {
				return errors.New("invalid report latency")
			}
			latencies[stage] = append(latencies[stage], value)
		}
	}
	if answerable != s.AnswerableCases || (answerable == 0) != (s.Metrics == nil) {
		return errors.New("question metrics disagree with summary counts")
	}
	if answerable > 0 {
		if !validMetrics(s.Metrics) {
			return errors.New("invalid summary metrics")
		}
		n := float64(answerable)
		for _, pair := range [][2]float64{{sum.RecallAt20 / n, s.Metrics.RecallAt20}, {sum.HitAt5 / n, s.Metrics.HitAt5}, {sum.MRRAt10 / n, s.Metrics.MRRAt10}, {sum.NDCGAt10 / n, s.Metrics.NDCGAt10}} {
			if math.Abs(pair[0]-pair[1]) > 1e-12 {
				return errors.New("question metrics disagree with summary averages")
			}
		}
	}
	if len(latencies) != len(report.LatencyMS) {
		return errors.New("latency summary stages disagree with questions")
	}
	for stage, values := range latencies {
		sort.Float64s(values)
		pair, ok := report.LatencyMS[stage]
		if !ok || !finiteNonnegative(pair[0]) || !finiteNonnegative(pair[1]) ||
			math.Abs(pair[0]-values[(len(values)-1)/2]) > 1e-12 ||
			math.Abs(pair[1]-values[(95*len(values)+99)/100-1]) > 1e-12 {
			return errors.New("latency summary disagrees with question timings")
		}
	}
	return nil
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validMetrics(m *Metrics) bool {
	for _, value := range []float64{m.RecallAt20, m.HitAt5, m.MRRAt10, m.NDCGAt10} {
		if !finiteNonnegative(value) || value > 1 {
			return false
		}
	}
	return true
}
