package retrieval

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestEncodeVectorRejectsInvalidHalfVectors(t *testing.T) {
	valid := make([]float32, 64)
	valid[0] = 1
	encoded, err := encodeVector(valid, 64)
	if err != nil || !strings.HasPrefix(encoded, "[1,0,") || !strings.HasSuffix(encoded, "]") {
		t.Fatalf("valid vector: %q %v", encoded, err)
	}
	for name, vector := range map[string][]float32{
		"wrong dimensions": {1},
		"zero":             make([]float32, 64),
		"underflow":        append([]float32{float32(math.Ldexp(1, -26))}, make([]float32, 63)...),
		"infinite":         append([]float32{float32(math.Inf(1))}, make([]float32, 63)...),
		"nan":              append([]float32{float32(math.NaN())}, make([]float32, 63)...),
		"overflow":         append([]float32{65505}, make([]float32, 63)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := encodeVector(vector, 64); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("invalid vector accepted: %v", err)
			}
		})
	}
}

func TestCopyFiltersValidatesAndCopiesInput(t *testing.T) {
	values := []string{"policy", "expense"}
	result, err := copyFilters(values, 256)
	if err != nil || len(result) != 2 {
		t.Fatalf("valid filters: %v %v", result, err)
	}
	values[0] = "changed"
	if result[0] != "policy" {
		t.Fatal("filters share input storage")
	}
	for _, values := range [][]string{{""}, {"\x00"}, {"\xff"}, {strings.Repeat("a", 257)}, make([]string, 65)} {
		if _, err := copyFilters(values, 256); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("invalid filters accepted: %v", err)
		}
	}
}
