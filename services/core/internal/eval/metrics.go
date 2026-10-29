package eval

import (
	"errors"
	"fmt"
	"math"
)

var ErrInvalidCase = errors.New("invalid evaluation case")

// Target describes one expected document, optionally narrowed to a section or page.
type Target struct {
	DocumentID int64
	SectionID  int64
	Page       int32
}

// Evidence describes a ranked result; zero page bounds mean the location is unknown.
type Evidence struct {
	ChunkID    int64
	DocumentID int64
	SectionID  int64
	PageStart  int32
	PageEnd    int32
}

type Case struct {
	Answerable bool
	Expected   []Target
	Fused      []Evidence
	Reranked   []Evidence
}

type Metrics struct {
	RecallAt20 float64
	HitAt5     float64
	MRRAt10    float64
	NDCGAt10   float64
}

type Summary struct {
	Cases             int
	AnswerableCases   int
	UnanswerableCases int
	Metrics           *Metrics
}

// Score uses fused results for recall and reranked results for ranking metrics.
// An unanswerable case has no retrieval metrics and returns nil.
func Score(c Case) (*Metrics, error) {
	if err := validate(c); err != nil {
		return nil, err
	}
	if !c.Answerable {
		return nil, nil
	}
	fused := relevance(c.Expected, c.Fused, 20)
	reranked := relevance(c.Expected, c.Reranked, 10)
	metrics := &Metrics{RecallAt20: float64(count(fused)) / float64(len(c.Expected))}
	for index, relevant := range reranked {
		if !relevant {
			continue
		}
		if index < 5 {
			metrics.HitAt5 = 1
		}
		if metrics.MRRAt10 == 0 {
			metrics.MRRAt10 = 1 / float64(index+1)
		}
		metrics.NDCGAt10 += discount(index)
	}
	var ideal float64
	for index := range min(len(c.Expected), 10) {
		ideal += discount(index)
	}
	metrics.NDCGAt10 /= ideal
	return metrics, nil
}

// Aggregate computes macro averages over answerable cases; unanswerable cases are counted separately.
func Aggregate(cases []Case) (Summary, error) {
	if len(cases) == 0 {
		return Summary{}, errors.New("evaluation dataset is empty")
	}
	summary := Summary{Cases: len(cases)}
	var sum Metrics
	for index, item := range cases {
		metrics, err := Score(item)
		if err != nil {
			return Summary{}, fmt.Errorf("case %d: %w", index+1, err)
		}
		if metrics == nil {
			summary.UnanswerableCases++
			continue
		}
		summary.AnswerableCases++
		sum.RecallAt20 += metrics.RecallAt20
		sum.HitAt5 += metrics.HitAt5
		sum.MRRAt10 += metrics.MRRAt10
		sum.NDCGAt10 += metrics.NDCGAt10
	}
	if summary.AnswerableCases != 0 {
		denominator := float64(summary.AnswerableCases)
		summary.Metrics = &Metrics{
			RecallAt20: sum.RecallAt20 / denominator,
			HitAt5:     sum.HitAt5 / denominator,
			MRRAt10:    sum.MRRAt10 / denominator,
			NDCGAt10:   sum.NDCGAt10 / denominator,
		}
	}
	return summary, nil
}

func validate(c Case) error {
	if c.Answerable != (len(c.Expected) != 0) {
		return fmt.Errorf("%w: expected targets must match answerability", ErrInvalidCase)
	}
	seen := make(map[Target]struct{}, len(c.Expected))
	for _, target := range c.Expected {
		if target.DocumentID <= 0 || target.SectionID < 0 || target.Page < 0 {
			return fmt.Errorf("%w: invalid expected target", ErrInvalidCase)
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("%w: duplicate expected target", ErrInvalidCase)
		}
		seen[target] = struct{}{}
	}
	chunks := make(map[int64]Evidence, len(c.Fused)+len(c.Reranked))
	for _, results := range [][]Evidence{c.Fused, c.Reranked} {
		for _, evidence := range results {
			if evidence.ChunkID <= 0 || evidence.DocumentID <= 0 || evidence.SectionID < 0 || evidence.PageStart < 0 ||
				evidence.PageEnd < evidence.PageStart || (evidence.PageStart == 0) != (evidence.PageEnd == 0) {
				return fmt.Errorf("%w: invalid evidence location", ErrInvalidCase)
			}
			if previous, ok := chunks[evidence.ChunkID]; ok && previous != evidence {
				return fmt.Errorf("%w: inconsistent chunk location", ErrInvalidCase)
			}
			chunks[evidence.ChunkID] = evidence
		}
	}
	return nil
}

func relevance(expected []Target, evidence []Evidence, limit int) []bool {
	result := make([]bool, min(len(evidence), limit))
	owner := make([]int, len(expected))
	for index := range owner {
		owner[index] = -1
	}
	var assign func(int, []bool) bool
	assign = func(index int, visited []bool) bool {
		for targetIndex, target := range expected {
			if visited[targetIndex] || !matches(target, evidence[index]) {
				continue
			}
			visited[targetIndex] = true
			if owner[targetIndex] < 0 || assign(owner[targetIndex], visited) {
				owner[targetIndex] = index
				return true
			}
		}
		return false
	}
	seenChunks := make(map[int64]struct{}, len(result))
	for index := range result {
		if _, duplicate := seenChunks[evidence[index].ChunkID]; duplicate {
			continue
		}
		seenChunks[evidence[index].ChunkID] = struct{}{}
		result[index] = assign(index, make([]bool, len(expected)))
	}
	return result
}

func matches(target Target, item Evidence) bool {
	return target.DocumentID == item.DocumentID && (target.SectionID == 0 || target.SectionID == item.SectionID) &&
		(target.Page == 0 || (item.PageStart > 0 && item.PageStart <= target.Page && target.Page <= item.PageEnd))
}

func count(values []bool) int {
	var total int
	for _, value := range values {
		if value {
			total++
		}
	}
	return total
}

func discount(index int) float64 { return 1 / math.Log2(float64(index+2)) }
