package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func testVector(first, second float32) []float32 {
	values := make([]float32, 64)
	values[0], values[1] = first, second
	return values
}

func testPlanIdentity(t *testing.T) PlanIdentity {
	t.Helper()
	plan := samplePreparedPlan(7)
	hashes, err := validatePreparedPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return PlanIdentity{Digest: hashes.Digest, ContentDigest: hashes.Content, EmbeddingSpaceID: plan.EmbeddingSpaceID, EmbeddingProfile: plan.EmbeddingProfile,
		ConfigRevision: plan.ConfigRevision, SectionCount: int32(len(plan.Sections)), ChunkCount: int32(len(plan.Chunks))}
}

func TestValidateVectorBatchRejectsInvalidInput(t *testing.T) {
	identity := testPlanIdentity(t)
	valid := VectorInput{ChunkID: 1, Ordinal: 1, Values: testVector(3, 4)}
	if _, err := validateVectorBatch(context.Background(), identity, []VectorInput{valid}); err != nil {
		t.Fatal(err)
	}
	for name, batch := range map[string][]VectorInput{
		"empty":      nil,
		"zero id":    {{ChunkID: 0, Ordinal: 1, Values: testVector(1, 0)}},
		"ordinal":    {{ChunkID: 1, Ordinal: 3, Values: testVector(1, 0)}},
		"dimension":  {{ChunkID: 1, Ordinal: 1, Values: []float32{1}}},
		"zero":       {{ChunkID: 1, Ordinal: 1, Values: testVector(0, 0)}},
		"nan":        {{ChunkID: 1, Ordinal: 1, Values: testVector(float32(math.NaN()), 1)}},
		"infinite":   {{ChunkID: 1, Ordinal: 1, Values: testVector(float32(math.Inf(1)), 1)}},
		"duplicate":  {valid, valid},
		"batch size": make([]VectorInput, maxVectorBatch+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateVectorBatch(context.Background(), identity, batch); !errors.Is(err, ErrInvalidVector) {
				t.Fatalf("invalid vector batch accepted: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validateVectorBatch(ctx, identity, []VectorInput{valid}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestPlanIdentityBindsProfileAndContent(t *testing.T) {
	identity := testPlanIdentity(t)
	if err := validatePlanIdentity(identity); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PlanIdentity){
		"space":        func(i *PlanIdentity) { i.EmbeddingSpaceID++ },
		"provider":     func(i *PlanIdentity) { i.EmbeddingProfile.ProviderID++ },
		"model":        func(i *PlanIdentity) { i.EmbeddingProfile.Model = "other" },
		"dimensions":   func(i *PlanIdentity) { i.EmbeddingProfile.Dimensions++ },
		"normalized":   func(i *PlanIdentity) { i.EmbeddingProfile.Normalized = !i.EmbeddingProfile.Normalized },
		"distance":     func(i *PlanIdentity) { i.EmbeddingProfile.Distance = "ip" },
		"revision":     func(i *PlanIdentity) { i.ConfigRevision++ },
		"sections":     func(i *PlanIdentity) { i.SectionCount++ },
		"chunks":       func(i *PlanIdentity) { i.ChunkCount++ },
		"content hash": func(i *PlanIdentity) { i.ContentDigest[0] ^= 1 },
		"plan hash":    func(i *PlanIdentity) { i.Digest[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			input := identity
			mutate(&input)
			if err := validatePlanIdentity(input); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("modified identity accepted: %v", err)
			}
		})
	}
}

func TestEncodeHalfVectorChecksRangeAndNormalizes(t *testing.T) {
	encoded, err := encodeHalfVector(testVector(3, 4), true)
	if err != nil {
		t.Fatal(err)
	}
	var values []float32
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || len(values) != 64 ||
		math.Abs(float64(values[0])-0.6) > 1e-6 || math.Abs(float64(values[1])-0.8) > 1e-6 {
		t.Fatalf("incorrect normalized vector: %v", err)
	}
	for name, vector := range map[string][]float32{
		"zero":      testVector(0, 0),
		"overflow":  testVector(65505, 0),
		"underflow": testVector(float32(math.Ldexp(1, -25)), 0),
		"nan":       testVector(float32(math.NaN()), 0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := encodeHalfVector(vector, false); !errors.Is(err, ErrInvalidVector) {
				t.Fatalf("unrepresentable half vector accepted: %v", err)
			}
		})
	}
	if _, err := encodeHalfVector(testVector(65505, 0), true); err != nil {
		t.Fatalf("normalizable vector rejected: %v", err)
	}
	if _, err := encodeHalfVector(testVector(float32(math.Ldexp(1, -24)), 0), false); err != nil {
		t.Fatalf("representable half subnormal rejected: %v", err)
	}
}
