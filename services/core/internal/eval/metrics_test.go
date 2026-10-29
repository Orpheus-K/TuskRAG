package eval

import (
	"errors"
	"math"
	"testing"
)

func TestScoreUsesCorrectStagesAndLocations(t *testing.T) {
	first := Evidence{ChunkID: 1, DocumentID: 1, SectionID: 11}
	duplicateSection := Evidence{ChunkID: 2, DocumentID: 1, SectionID: 11}
	second := Evidence{ChunkID: 3, DocumentID: 2, PageStart: 2, PageEnd: 4}
	other := Evidence{ChunkID: 4, DocumentID: 9}
	wrongSection := Evidence{ChunkID: 5, DocumentID: 1, SectionID: 99}
	item := Case{
		Answerable: true,
		Expected:   []Target{{DocumentID: 1, SectionID: 11}, {DocumentID: 2, Page: 3}},
		Fused:      []Evidence{first, duplicateSection, second},
		Reranked:   []Evidence{other, second, wrongSection, first},
	}
	metrics, err := Score(item)
	if err != nil || metrics == nil {
		t.Fatalf("score: %+v %v", metrics, err)
	}
	wantNDCG := (1/math.Log2(3) + 1/math.Log2(5)) / (1 + 1/math.Log2(3))
	if metrics.RecallAt20 != 1 || metrics.HitAt5 != 1 || metrics.MRRAt10 != 0.5 || math.Abs(metrics.NDCGAt10-wantNDCG) > 1e-12 {
		t.Fatalf("unexpected metrics: %+v, expected nDCG=%v", *metrics, wantNDCG)
	}
}

func TestScoreDeduplicatesChunksAndMatchesOverlappingTargets(t *testing.T) {
	first := Evidence{ChunkID: 1, DocumentID: 1, SectionID: 1, PageStart: 2, PageEnd: 2}
	second := Evidence{ChunkID: 2, DocumentID: 1, SectionID: 1, PageStart: 4, PageEnd: 4}
	item := Case{
		Answerable: true,
		Expected:   []Target{{DocumentID: 1, SectionID: 1}, {DocumentID: 1, Page: 2}},
		Fused:      []Evidence{first, first, second},
		Reranked:   []Evidence{first, first, second},
	}
	metrics, err := Score(item)
	if err != nil || metrics == nil {
		t.Fatalf("score: %+v %v", metrics, err)
	}
	wantNDCG := (1 + 1/math.Log2(4)) / (1 + 1/math.Log2(3))
	if metrics.RecallAt20 != 1 || metrics.HitAt5 != 1 || metrics.MRRAt10 != 1 || math.Abs(metrics.NDCGAt10-wantNDCG) > 1e-12 {
		t.Fatalf("duplicate result inflated metrics or matching missed a target: %+v", *metrics)
	}
	item.Expected[0], item.Expected[1] = item.Expected[1], item.Expected[0]
	otherOrder, err := Score(item)
	if err != nil || otherOrder == nil || *otherOrder != *metrics {
		t.Fatalf("target order changed score: %+v %v", otherOrder, err)
	}
}

func TestScoreEnforcesRankCutoffs(t *testing.T) {
	item := Case{Answerable: true, Expected: []Target{{DocumentID: 1}}}
	for index := 1; index <= 21; index++ {
		item.Fused = append(item.Fused, Evidence{ChunkID: int64(index), DocumentID: 2})
	}
	item.Fused[20].DocumentID = 1
	for index := 22; index <= 32; index++ {
		item.Reranked = append(item.Reranked, Evidence{ChunkID: int64(index), DocumentID: 2})
	}
	item.Reranked[10].DocumentID = 1
	metrics, err := Score(item)
	if err != nil || metrics == nil || *metrics != (Metrics{}) {
		t.Fatalf("results beyond cutoffs counted: %+v %v", metrics, err)
	}
	item.Fused[19].DocumentID = 1
	item.Reranked[9].DocumentID = 1
	metrics, err = Score(item)
	if err != nil || metrics == nil || metrics.RecallAt20 != 1 || metrics.HitAt5 != 0 || metrics.MRRAt10 != 0.1 || math.Abs(metrics.NDCGAt10-1/math.Log2(11)) > 1e-12 {
		t.Fatalf("boundary ranks not counted: %+v %v", metrics, err)
	}
}

func TestAggregateSeparatesUnanswerableCases(t *testing.T) {
	answerable := Case{Answerable: true, Expected: []Target{{DocumentID: 1}}, Fused: []Evidence{{ChunkID: 1, DocumentID: 1}}, Reranked: []Evidence{{ChunkID: 1, DocumentID: 1}}}
	miss := Case{Answerable: true, Expected: []Target{{DocumentID: 2}}}
	noAnswer := Case{Reranked: []Evidence{{ChunkID: 3, DocumentID: 3}}}
	if metrics, err := Score(noAnswer); err != nil || metrics != nil {
		t.Fatalf("unanswerable case has retrieval score: %+v %v", metrics, err)
	}
	summary, err := Aggregate([]Case{answerable, miss, noAnswer})
	if err != nil || summary.Cases != 3 || summary.AnswerableCases != 2 || summary.UnanswerableCases != 1 || summary.Metrics == nil || *summary.Metrics != (Metrics{RecallAt20: 0.5, HitAt5: 0.5, MRRAt10: 0.5, NDCGAt10: 0.5}) {
		t.Fatalf("aggregate: %+v %v", summary, err)
	}
	summary, err = Aggregate([]Case{noAnswer})
	if err != nil || summary.Metrics != nil || summary.UnanswerableCases != 1 {
		t.Fatalf("unanswerable-only aggregate invented scores: %+v %v", summary, err)
	}
	if _, err := Aggregate(nil); err == nil {
		t.Fatal("empty evaluation dataset accepted")
	}
}

func TestInvalidCasesAreRejected(t *testing.T) {
	tests := []Case{
		{Answerable: true},
		{Expected: []Target{{DocumentID: 1}}},
		{Answerable: true, Expected: []Target{{DocumentID: 0}}},
		{Answerable: true, Expected: []Target{{DocumentID: 1}, {DocumentID: 1}}},
		{Answerable: true, Expected: []Target{{DocumentID: 1, Page: -1}}},
		{Answerable: true, Expected: []Target{{DocumentID: 1}}, Fused: []Evidence{{ChunkID: 1, DocumentID: 1, PageStart: 1}}},
		{Answerable: true, Expected: []Target{{DocumentID: 1}}, Fused: []Evidence{{ChunkID: 1, DocumentID: 1}}, Reranked: []Evidence{{ChunkID: 1, DocumentID: 2}}},
	}
	for index, item := range tests {
		if _, err := Score(item); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("invalid case %d accepted: %v", index, err)
		}
	}
	if _, err := Aggregate([]Case{tests[0]}); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("invalid aggregate accepted: %v", err)
	}
}
