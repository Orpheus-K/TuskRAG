package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func samplePreparedPlan(spaceID int64) PreparedPlan {
	return PreparedPlan{
		EmbeddingSpaceID: spaceID,
		EmbeddingProfile: EmbeddingProfile{ProviderID: 9, Model: "test", Dimensions: 64, Normalized: true, Distance: "cosine"},
		ConfigRevision:   1,
		Sections: []PreparedSection{
			{ParentIndex: -1, Level: 1, Heading: "制度", HeadingPath: []string{"制度"}, PageStart: 1, PageEnd: 2},
			{ParentIndex: 0, Level: 2, Heading: "差旅", HeadingPath: []string{"制度", "差旅"}, PageStart: 1, PageEnd: 2, BodyMarkdown: "申请。\n\n复核。", TokenCount: 6},
		},
		Chunks: []PreparedChunk{
			{SectionIndex: 1, Kind: "text", Content: "申请。", SearchText: "制度 › 差旅\n申请。", PageStart: 1, PageEnd: 1, TokenCount: 6},
			{SectionIndex: 1, Kind: "text", Content: "复核。", SearchText: "制度 › 差旅\n复核。", PageStart: 2, PageEnd: 2, TokenCount: 6},
		},
	}
}

func TestPreparedPlanDigestAndValidation(t *testing.T) {
	ctx := context.Background()
	plan := samplePreparedPlan(7)
	first, err := validatePreparedPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := validatePreparedPlan(ctx, samplePreparedPlan(7))
	if err != nil || first != second {
		t.Fatalf("identical plan digest changed: %v", err)
	}
	changed := samplePreparedPlan(7)
	changed.Chunks[0].Content = "批准。"
	changed.Chunks[0].SearchText = "制度 › 差旅\n批准。"
	other, err := validatePreparedPlan(ctx, changed)
	if err != nil || first.Digest == other.Digest || first.Content == other.Content {
		t.Fatalf("different content used the same digest: %v", err)
	}
	changed = samplePreparedPlan(7)
	changed.EmbeddingProfile.Model = "other-model"
	other, err = validatePreparedPlan(ctx, changed)
	if err != nil || first.Digest == other.Digest || first.Content != other.Content {
		t.Fatalf("model configuration was not bound to the plan: %v", err)
	}
	for name, mutate := range map[string]func(*PreparedPlan){
		"space":          func(p *PreparedPlan) { p.EmbeddingSpaceID = 0 },
		"revision":       func(p *PreparedPlan) { p.ConfigRevision = 0 },
		"model":          func(p *PreparedPlan) { p.EmbeddingProfile.Model = "" },
		"dimensions":     func(p *PreparedPlan) { p.EmbeddingProfile.Dimensions = 63 },
		"missing chunks": func(p *PreparedPlan) { p.Chunks = nil },
		"cycle":          func(p *PreparedPlan) { p.Sections[0].ParentIndex = 0 },
		"heading path":   func(p *PreparedPlan) { p.Sections[1].HeadingPath = []string{"其他", "差旅"} },
		"child page":     func(p *PreparedPlan) { p.Sections[1].PageEnd = 3 },
		"empty body":     func(p *PreparedPlan) { p.Sections[1].BodyMarkdown = "" },
		"section index":  func(p *PreparedPlan) { p.Chunks[0].SectionIndex = 2 },
		"chunk kind":     func(p *PreparedPlan) { p.Chunks[0].Kind = "unknown" },
		"chunk tokens":   func(p *PreparedPlan) { p.Chunks[0].TokenCount = 801 },
		"chunk page":     func(p *PreparedPlan) { p.Chunks[0].PageStart = 0 },
		"search prefix":  func(p *PreparedPlan) { p.Chunks[0].SearchText = "其他内容" },
		"NUL":            func(p *PreparedPlan) { p.Chunks[0].Content = "包含\x00" },
		"invalid UTF8":   func(p *PreparedPlan) { p.Sections[1].BodyMarkdown = "invalid\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			input := samplePreparedPlan(7)
			mutate(&input)
			if _, err := validatePreparedPlan(ctx, input); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("invalid plan accepted: %v", err)
			}
		})
	}
	tooLarge := samplePreparedPlan(7)
	tooLarge.Sections[1].BodyMarkdown = strings.Repeat("a", maxPlanRowBytes+1)
	if _, err := validatePreparedPlan(ctx, tooLarge); !errors.Is(err, ErrPlanLimit) {
		t.Fatalf("oversized row accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := validatePreparedPlan(cancelled, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestPreparedPlanAllowsPseudoSectionWithoutInventedHeading(t *testing.T) {
	plan := samplePreparedPlan(7)
	plan.Sections[1] = PreparedSection{ParentIndex: 0, Level: 1, Heading: "制度", HeadingPath: []string{"制度"},
		PageStart: 1, PageEnd: 2, BodyMarkdown: "申请。\n\n复核。", TokenCount: 6}
	plan.Chunks[0].SearchText = "制度\n申请。"
	plan.Chunks[1].SearchText = "制度\n复核。"
	if _, err := validatePreparedPlan(context.Background(), plan); err != nil {
		t.Fatalf("pseudo section rejected: %v", err)
	}
}
