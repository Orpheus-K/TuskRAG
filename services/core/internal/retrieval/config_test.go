package retrieval

import (
	"errors"
	"testing"
)

func TestSearchOptionInheritance(t *testing.T) {
	yes, no := true, false
	saved := &RerankOptions{ProviderID: 1, Model: "saved", TopN: 20}
	override := &RerankOptions{ProviderID: 2, Model: "override", TopN: 50}
	for _, tc := range []struct {
		name    string
		request Request
		saved   *RerankOptions
		want    *RerankOptions
		limit   int
		err     error
	}{
		{"inherit", Request{}, saved, saved, 5, nil},
		{"override limit", Request{Limit: 3}, saved, saved, 3, nil},
		{"disable", Request{RerankEnabled: &no}, saved, nil, 5, nil},
		{"explicit model", Request{Rerank: override}, saved, override, 5, nil},
		{"enable saved", Request{RerankEnabled: &yes}, saved, saved, 5, nil},
		{"unconfigured", Request{}, nil, nil, 5, nil},
		{"required missing model", Request{RerankEnabled: &yes}, nil, nil, 0, ErrRerankUnavailable},
		{"contradictory options", Request{RerankEnabled: &no, Rerank: override}, saved, nil, 0, ErrInvalidQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSearchOptions(tc.request, 5, tc.saved)
			if !errors.Is(err, tc.err) || (err == nil && (got.Limit != tc.limit || got.Rerank != tc.want)) {
				t.Fatalf("resolved=%+v err=%v", got, err)
			}
		})
	}
}
