package ingest

import (
	"context"
	"errors"
	"testing"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

func testStoredRerankConfiguration(t *testing.T, f *fixture, service *retrieval.Service, actor retrieval.Actor, reranker *retrieval.RerankOptions, firstChunk int64) {
	t.Helper()
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	owner := kb.Actor{TenantID: actor.TenantID, UserID: actor.UserID}
	unconfigured, err := service.Snapshot(f.ctx, actor, f.kbID)
	if err != nil || unconfigured.RetrievalRevision != 0 {
		t.Fatalf("unconfigured snapshot: %+v %v", unconfigured, err)
	}
	saved, err := knowledge.PutConfig(f.ctx, owner, f.kbID, 0, kb.RetrievalConfig{TopK: 1, Reranker: reranker})
	if err != nil {
		t.Fatal(err)
	}
	request := retrieval.Request{KnowledgeBaseID: f.kbID, Query: "申请", Debug: true}
	pinned, err := service.Snapshot(f.ctx, actor, f.kbID)
	if err != nil || pinned.RetrievalRevision != 1 || pinned.Embedding.SpaceID <= 0 {
		t.Fatalf("saved snapshot: %+v %v", pinned, err)
	}
	request.ExpectedConfiguration = &pinned
	countCalls := 0
	guarded, err := retrieval.NewService(f.pool, nil, func(context.Context, string) (int, error) {
		countCalls++
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"unconfigured", "space", "embedding revision", "dimensions", "distance", "normalization"} {
		altered := pinned
		switch change {
		case "unconfigured":
			altered = unconfigured
		case "space":
			altered.Embedding.SpaceID++
		case "embedding revision":
			altered.Embedding.Revision++
		case "dimensions":
			altered.Embedding.Dimensions++
		case "distance":
			altered.Embedding.Distance = "l2"
		case "normalization":
			altered.Embedding.Normalized = !altered.Embedding.Normalized
		}
		check := request
		check.ExpectedConfiguration = &altered
		if got, err := guarded.Search(f.ctx, actor, check); !errors.Is(err, retrieval.ErrConfigurationChanged) || len(got.Candidates) != 0 || countCalls != 0 {
			t.Fatalf("%s mismatch reached tokenizer/model: %+v %v calls=%d", change, got, err, countCalls)
		}
	}
	result, err := service.Search(f.ctx, actor, request)
	if err != nil || result.RetrievalRevision != saved.Revision || len(result.Candidates) != 1 || result.Candidates[0].ChunkID != firstChunk || result.Trace == nil || result.Trace.RetrievalRevision != saved.Revision || len(result.Degraded) != 0 {
		t.Fatalf("stored settings not consumed: %+v err=%v", result, err)
	}
	no := false
	disabled := request
	disabled.Limit, disabled.RerankEnabled = 2, &no
	result, err = service.Search(f.ctx, actor, disabled)
	if err != nil || len(result.Candidates) != 2 || result.Candidates[0].RerankScore != nil || len(result.Degraded) != 1 {
		t.Fatalf("explicit disable/limit ignored: %+v err=%v", result, err)
	}
	threshold := 2.0
	override := request
	rank := *reranker
	rank.MinScore = &threshold
	override.Rerank = &rank
	result, err = service.Search(f.ctx, actor, override)
	if err != nil || len(result.Candidates) != 0 || len(result.Degraded) != 0 {
		t.Fatalf("request override ignored: %+v err=%v", result, err)
	}
	// A config update during token counting cannot block on a retained transaction.
	// The in-flight request keeps revision 1; the next request uses revision 2.
	counter, err := tokenizer.NewBGEM3(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshotService, err := retrieval.NewService(f.pool, nil, func(ctx context.Context, query string) (int, error) {
		_, err := knowledge.PutConfig(ctx, owner, f.kbID, 1, kb.RetrievalConfig{TopK: 2, Reranker: reranker})
		if err != nil {
			return 0, err
		}
		return counter.Count(ctx, query)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = snapshotService.Search(f.ctx, actor, request)
	if err != nil || result.RetrievalRevision != 1 || len(result.Candidates) != 1 {
		t.Fatalf("in-flight settings mixed: %+v err=%v", result, err)
	}
	result, err = service.Search(f.ctx, actor, request)
	if !errors.Is(err, retrieval.ErrConfigurationChanged) || len(result.Candidates) != 0 {
		t.Fatalf("stale snapshot accepted: %+v %v", result, err)
	}
	request.ExpectedConfiguration = nil
	result, err = service.Search(f.ctx, actor, request)
	if err != nil || result.RetrievalRevision != 2 || len(result.Candidates) != 2 {
		t.Fatalf("new revision not observed: %+v err=%v", result, err)
	}
	// Saving no reranker disables the stored default without changing caller options.
	if _, err := knowledge.PutConfig(f.ctx, owner, f.kbID, 2, kb.RetrievalConfig{TopK: 8}); err != nil {
		t.Fatal(err)
	}
	yes := true
	request.RerankEnabled = &yes
	if result, err := service.Search(f.ctx, actor, request); !errors.Is(err, retrieval.ErrRerankUnavailable) || len(result.Candidates) != 0 {
		t.Fatalf("required reranker silently disabled: %+v err=%v", result, err)
	}
}
