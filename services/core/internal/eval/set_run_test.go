package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tuskrag.local/core/internal/retrieval"
)

type setReaderFunc func(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error)

func (f setReaderFunc) Get(ctx context.Context, a retrieval.Actor, kb, id int64, revision int32) (StoredSet, error) {
	return f(ctx, a, kb, id, revision)
}

func runSetFixture(t *testing.T) (StoredSet, RunConfig, retrieval.Result) {
	t.Helper()
	definition := SetDefinition{Name: "pinned", Cases: []CaseDefinition{
		{ID: "q1", Question: "private query one", Answerable: true, Expected: []ExpectedLocation{{DocumentID: 1, SectionID: 2, Page: 3}}, ReferenceAnswer: "private reference"},
		{ID: "q2", Question: "private query two", Answerable: true, Expected: []ExpectedLocation{{DocumentID: 1, SectionID: 3, Page: 3}}},
		{ID: "q3", Question: "private query three", Answerable: true, Expected: []ExpectedLocation{{DocumentID: 1, SectionID: 2, Page: 5}}},
		{ID: "q4", Question: "private query four", Answerable: false},
	}}
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	stored := StoredSet{ID: 9, Revision: 2, Digest: hex.EncodeToString(hash[:]), Definition: definition}
	config := RunConfig{Actor: retrieval.Actor{TenantID: 1, UserID: 2}, KnowledgeBaseID: 3, AllowedDocumentIDs: []int64{1}, ModelLabel: "fixture", ModelKind: "stub", Reranker: &retrieval.RerankOptions{ProviderID: 1, Model: "test", TopN: 30, Fallback: true}}
	start, end := int32(2), int32(4)
	candidates := []retrieval.Candidate{{ChunkID: 1, DocumentID: 1, SectionID: 2, PageStart: &start, PageEnd: &end, LexicalRank: 1, SemanticRank: 1}}
	return stored, config, retrieval.Result{Candidates: candidates, Trace: &retrieval.Trace{Candidates: candidates}}
}

func TestRunSetPinnedLocationsAndNoAnswer(t *testing.T) {
	for _, mode := range []string{"lexical", "vector", "rrf", "rerank"} {
		t.Run(mode, func(t *testing.T) {
			stored, config, result := runSetFixture(t)
			reads, calls := 0, 0
			reader := setReaderFunc(func(_ context.Context, a retrieval.Actor, kb, id int64, revision int32) (StoredSet, error) {
				reads++
				if a != config.Actor || kb != 3 || id != 9 || revision != 2 {
					t.Fatal("unpinned or incorrect scope")
				}
				return stored, nil
			})
			search := searchFunc(func(_ context.Context, a retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
				calls++
				if a != config.Actor || request.KnowledgeBaseID != 3 || request.Limit != 30 || !request.Debug || request.RerankEnabled == nil || *request.RerankEnabled != (mode == "rerank") {
					t.Fatal("incorrect search request")
				}
				if mode == "rerank" && (request.Rerank == nil || request.Rerank.Fallback) {
					t.Fatal("fallback enabled")
				}
				return result, nil
			})
			report, err := RunSet(context.Background(), search, reader, SetBinding{SetID: 9, Revision: 2}, config, mode)
			if err != nil || reads != 2 || calls != 4 || report.DatasetVersion != "set:9:2" || report.DatasetSHA256 != stored.Digest {
				t.Fatalf("report %+v %v reads=%d calls=%d", report, err, reads, calls)
			}
			if report.Rows[0].Metrics.RecallAt20 != 1 || report.Rows[1].Metrics.RecallAt20 != 0 || report.Rows[2].Metrics.RecallAt20 != 0 || report.Rows[3].Metrics != nil || report.Summary.Metrics.MRRAt10 != 1.0/3 {
				t.Fatalf("location/no-answer scores: %+v", report)
			}
			if err := ValidateReport(report); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "private") {
				t.Fatal("query/reference disclosed")
			}
		})
	}
}

func TestRunSetRejectsFailuresWithoutPartialRows(t *testing.T) {
	for _, scenario := range []string{"source denied", "wrong revision", "wrong digest", "final revoked", "late search error", "missing trace", "unauthorized", "degraded", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			stored, config, result := runSetFixture(t)
			reads, calls := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := setReaderFunc(func(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error) {
				reads++
				if scenario == "source denied" || (scenario == "final revoked" && reads == 2) {
					return StoredSet{}, ErrSetNotFound
				}
				if scenario == "wrong revision" {
					stored.Revision++
				}
				if scenario == "wrong digest" {
					stored.Digest = strings.Repeat("f", 64)
				}
				return stored, nil
			})
			search := searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
				calls++
				switch scenario {
				case "late search error":
					if calls == 2 {
						return retrieval.Result{}, errors.New("search failed")
					}
				case "missing trace":
					result.Trace = nil
				case "unauthorized":
					result.Trace.Candidates[0].DocumentID = 99
				case "degraded":
					result.Degraded = []string{"rerank"}
				case "cancelled":
					cancel()
				}
				return result, nil
			})
			report, err := RunSet(ctx, search, reader, SetBinding{SetID: 9, Revision: 2}, config, "rerank")
			if err == nil || report.Status == "passed" || len(report.Rows) != 0 || report.Summary.Metrics != nil {
				t.Fatalf("partial success: %+v %v", report, err)
			}
			if (scenario == "source denied" || scenario == "wrong revision" || scenario == "wrong digest") && calls != 0 {
				t.Fatal("invalid source searched")
			}
			if scenario == "cancelled" && calls != 1 {
				t.Fatal("cancelled run continued")
			}
		})
	}
}

func TestRunSetRequiresExplicitRevisionAndAllowedTargets(t *testing.T) {
	stored, config, result := runSetFixture(t)
	calls := 0
	reader := setReaderFunc(func(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error) { return stored, nil })
	search := searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
		calls++
		return result, nil
	})
	if _, err := RunSet(context.Background(), search, reader, SetBinding{SetID: 9}, config, "rrf"); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("unpinned run: %v", err)
	}
	config.AllowedDocumentIDs = nil
	if _, err := RunSet(context.Background(), search, reader, SetBinding{SetID: 9, Revision: 2}, config, "rrf"); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("unallowed targets: %v", err)
	}
	if calls != 0 {
		t.Fatal("invalid scope searched")
	}
}

func TestRunSetRetrievalRevisionConsistency(t *testing.T) {
	for _, scenario := range []struct {
		name                 string
		first, second, trace int32
		wantCalls            int
		wantError            bool
	}{
		{"default", 0, 0, 0, 4, false},
		{"saved", 5, 5, 5, 4, false},
		{"first saved configuration", 0, 1, 1, 2, true},
		{"updated configuration", 5, 6, 6, 2, true},
		{"inconsistent trace", 5, 5, 6, 2, true},
		{"negative revision", -1, -1, -1, 1, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stored, config, result := runSetFixture(t)
			reader := setReaderFunc(func(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error) { return stored, nil })
			calls := 0
			search := searchFunc(func(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error) {
				calls++
				result.RetrievalRevision, result.Trace.RetrievalRevision = scenario.first, scenario.first
				if calls > 1 {
					result.RetrievalRevision, result.Trace.RetrievalRevision = scenario.second, scenario.trace
				}
				return result, nil
			})
			report, err := RunSet(context.Background(), search, reader, SetBinding{SetID: 9, Revision: 2}, config, "rrf")
			if (err != nil) != scenario.wantError || calls != scenario.wantCalls {
				t.Fatalf("err=%v calls=%d, want error=%v calls=%d", err, calls, scenario.wantError, scenario.wantCalls)
			}
			if scenario.wantError {
				if report.Status != "failed" || len(report.Rows) != 0 || report.Summary.Metrics != nil || len(report.LatencyMS) != 0 {
					t.Fatalf("mixed configuration published: %+v", report)
				}
			} else if err := ValidateReport(report); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunSetBindsConfigurationAndDigest(t *testing.T) {
	stored, config, result := runSetFixture(t)
	reader := setReaderFunc(func(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error) { return stored, nil })
	var previousDigest string
	for _, revision := range []int32{0, 1} {
		config.Configuration = &retrieval.ConfigurationSnapshot{Embedding: retrieval.Binding{SpaceID: 1, Revision: 1, Dimensions: 64, Distance: "cosine", Normalized: true}, RetrievalRevision: revision}
		calls := 0
		search := searchFunc(func(_ context.Context, _ retrieval.Actor, request retrieval.Request) (retrieval.Result, error) {
			calls++
			if request.ExpectedConfiguration == nil || *request.ExpectedConfiguration != *config.Configuration {
				t.Fatal("configuration snapshot not bound to query")
			}
			result.RetrievalRevision, result.Trace.RetrievalRevision = revision, revision
			return result, nil
		})
		report, err := RunSet(context.Background(), search, reader, SetBinding{SetID: 9, Revision: 2}, config, "rrf")
		if err != nil || calls != 4 || report.ConfigSHA256 == previousDigest {
			t.Fatalf("snapshot missing from run/digest: %+v %v calls=%d", report, err, calls)
		}
		previousDigest = report.ConfigSHA256
	}
}
