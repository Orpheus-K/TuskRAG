package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tuskrag.local/core/internal/tokenizer"
)

// Counts were generated with the pinned BGE-M3 tokenizer.json and HF tokenizers 0.22.1.
var probeReferenceCounts = [...]int{8, 16, 5, 7, 6, 11, 18, 8, 6, 8, 7, 16, 794}

func newTokenizerTestClient(t *testing.T, baseURL string, locality Locality, auditor *testAuditor) *EmbeddingClient {
	t.Helper()
	client := newTestClient(t, baseURL, locality, auditor)
	client.config.MaxRequestBytes = 8192
	return client
}

func TestCheckTokenizerAgainstPinnedReference(t *testing.T) {
	ctx := context.Background()
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1) - 1)
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if index >= len(tokenizerProbes) || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "test-model" ||
			len(request.Input) != 1 || request.Input[0] != tokenizerProbes[index] {
			t.Error("unexpected tokenizer probe request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count := probeReferenceCounts[index]
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data":  []any{map[string]any{"index": 0, "embedding": vector(1, 0)}},
			"usage": map[string]int{"prompt_tokens": count, "total_tokens": count},
		}); err != nil {
			t.Errorf("encode reference response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := newTokenizerTestClient(t, server.URL, Internal, &testAuditor{})
	report, err := client.CheckTokenizer(ctx, testScope(Allow), counter.Count)
	if err != nil || report.Samples != len(probeReferenceCounts) || int(calls.Load()) != len(probeReferenceCounts) {
		t.Fatalf("reference check: samples=%d calls=%d err=%v", report.Samples, calls.Load(), err)
	}
}

func TestCheckTokenizerRejectsUnavailableAndMismatchedUsage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		index     int
		mode      string
		want      error
		wantCalls int32
	}{
		{name: "missing usage", index: 2, mode: "missing", want: ErrTokenizerUsageMissing, wantCalls: 3},
		{name: "different count", index: 3, mode: "different", want: ErrTokenizerMismatch, wantCalls: 4},
		{name: "invalid usage", index: 1, mode: "invalid", want: ErrInvalidResponse, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(calls.Add(1) - 1)
				if index >= len(probeReferenceCounts) {
					t.Error("too many requests")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body := map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(1, 0)}}}
				count := probeReferenceCounts[index]
				if index == tc.index && tc.mode == "different" {
					count++
				}
				if index == tc.index && tc.mode == "invalid" {
					count = 0
				}
				if index != tc.index || tc.mode != "missing" {
					body["usage"] = map[string]int{"prompt_tokens": count, "total_tokens": count}
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := newTokenizerTestClient(t, server.URL, Internal, &testAuditor{})
			local := func(_ context.Context, input string) (int, error) {
				for index, probe := range tokenizerProbes {
					if input == probe {
						return probeReferenceCounts[index], nil
					}
				}
				return 0, errors.New("unknown probe")
			}
			report, err := client.CheckTokenizer(context.Background(), testScope(Allow), local)
			if !errors.Is(err, tc.want) || report.Samples != 0 || calls.Load() != tc.wantCalls {
				t.Fatalf("check result=%+v calls=%d err=%v", report, calls.Load(), err)
			}
			if strings.Contains(err.Error(), tokenizerProbes[tc.index]) {
				t.Fatal("probe text leaked in check error")
			}
		})
	}
}

func TestCheckTokenizerPropagatesServiceFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	client := newTokenizerTestClient(t, server.URL, Internal, &testAuditor{})
	report, err := client.CheckTokenizer(context.Background(), testScope(Allow), func(context.Context, string) (int, error) { return 1, nil })
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || !upstream.Retryable || upstream.Status != http.StatusServiceUnavailable || report.Samples != 0 || calls.Load() != 1 {
		t.Fatalf("upstream failure: result=%+v calls=%d err=%v", report, calls.Load(), err)
	}
}

func TestCheckTokenizerStopsBeforeNetworkOnCounterFailureOrCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	client := newTokenizerTestClient(t, server.URL, Internal, &testAuditor{})
	localError := errors.New("local counter failed")
	index := 0
	count := func(context.Context, string) (int, error) {
		index++
		if index == len(tokenizerProbes) {
			return 0, localError
		}
		return 1, nil
	}
	if _, err := client.CheckTokenizer(context.Background(), testScope(Allow), count); !errors.Is(err, localError) || calls.Load() != 0 {
		t.Fatalf("counter failure: calls=%d err=%v", calls.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	count = func(context.Context, string) (int, error) { cancel(); return 1, nil }
	if _, err := client.CheckTokenizer(ctx, testScope(Allow), count); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("counter cancellation: calls=%d err=%v", calls.Load(), err)
	}
	count = func(context.Context, string) (int, error) { return 0, nil }
	if _, err := client.CheckTokenizer(context.Background(), testScope(Allow), count); err == nil || calls.Load() != 0 {
		t.Fatalf("invalid count accepted: calls=%d err=%v", calls.Load(), err)
	}
}

func TestCheckTokenizerEnforcesEgressPolicy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	auditor := &testAuditor{}
	client := newTokenizerTestClient(t, server.URL, External, auditor)
	countCalls := 0
	count := func(context.Context, string) (int, error) { countCalls++; return 1, nil }
	if _, err := client.CheckTokenizer(context.Background(), testScope(Deny), count); !errors.Is(err, ErrEgressDenied) || calls.Load() != 0 || countCalls != 0 || len(auditor.events) != 1 {
		t.Fatalf("egress denial: calls=%d counter=%d audits=%d err=%v", calls.Load(), countCalls, len(auditor.events), err)
	}
}

func TestCheckTokenizerPreflightsRequestLimits(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Internal, &testAuditor{})
	count := func(context.Context, string) (int, error) { return 1, nil }
	if report, err := client.CheckTokenizer(context.Background(), testScope(Allow), count); err == nil || report.Samples != 0 || calls.Load() != 0 {
		t.Fatalf("request limit not preflighted: result=%+v calls=%d err=%v", report, calls.Load(), err)
	}
}

func TestCheckTokenizerRejectsUninitializedDependencies(t *testing.T) {
	count := func(context.Context, string) (int, error) { return 1, nil }
	var client *EmbeddingClient
	if _, err := client.CheckTokenizer(context.Background(), testScope(Allow), count); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := new(EmbeddingClient).CheckTokenizer(context.Background(), testScope(Allow), count); err == nil {
		t.Fatal("uninitialized client accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	client = newTokenizerTestClient(t, server.URL, Internal, &testAuditor{})
	if _, err := client.CheckTokenizer(context.Background(), testScope(Allow), nil); err == nil {
		t.Fatal("nil counter accepted")
	}
}
