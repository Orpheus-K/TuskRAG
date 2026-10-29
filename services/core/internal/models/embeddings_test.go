package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testAuditor struct {
	mu     sync.Mutex
	events []DeniedCall
	err    error
}

type embeddingScopeResolverFunc func(context.Context, EmbeddingBinding) (EgressScope, error)

func (f embeddingScopeResolverFunc) ResolveEmbeddingScope(ctx context.Context, binding EmbeddingBinding) (EgressScope, error) {
	return f(ctx, binding)
}

func (a *testAuditor) RecordModelDenied(_ context.Context, event DeniedCall) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
	return a.err
}

func newTestClient(t *testing.T, baseURL string, locality Locality, auditor *testAuditor) *EmbeddingClient {
	t.Helper()
	client, err := NewEmbeddingClient(Provider{ID: 9, BaseURL: baseURL, Locality: locality, APIKey: "test-secret"}, EmbeddingConfig{
		Model: "test-model", Dimensions: 64, Normalize: true, MaxBatch: 4,
		MaxRequestBytes: 1024, MaxResponseBytes: 16384, Timeout: 5 * time.Second, Concurrency: 1,
	}, auditor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func vector(first, second float32) []float32 {
	result := make([]float32, 64)
	result[0], result[1] = first, second
	return result
}

func testScope(policy EgressPolicy) EgressScope {
	return EgressScope{TenantID: 1, KnowledgeBaseID: 2, TenantPolicy: Allow, KnowledgeBasePolicy: policy}
}

func TestEmbeddingsPreserveOrderAndNormalize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected request: method=%s path=%s", r.Method, r.URL.Path)
		}
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "test-model" || len(request.Input) != 2 {
			t.Errorf("invalid request: model=%q count=%d err=%v", request.Model, len(request.Input), err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": vector(0, 2)},
			map[string]any{"index": 0, "embedding": vector(3, 4)},
		}}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Internal, &testAuditor{})
	got, err := client.Embed(context.Background(), testScope(Deny), []string{"first", "second"})
	if err != nil || len(got.Vectors) != 2 || math.Abs(float64(got.Vectors[0][0])-0.6) > 1e-6 || math.Abs(float64(got.Vectors[0][1])-0.8) > 1e-6 || got.Vectors[1][1] != 1 || got.Usage != nil {
		t.Fatalf("unexpected vectors: %v err=%v", got, err)
	}
}

func TestEmbeddingsDenyEgressAndAudit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	auditor := &testAuditor{}
	client := newTestClient(t, server.URL, External, auditor)
	for _, policy := range []EgressPolicy{Deny, InternalOnly} {
		if _, err := client.Embed(context.Background(), testScope(policy), []string{"private content"}); !errors.Is(err, ErrEgressDenied) {
			t.Fatalf("policy %s allowed an external call: %v", policy, err)
		}
	}
	if calls.Load() != 0 || len(auditor.events) != 2 || auditor.events[0] != (DeniedCall{
		TenantID: 1, KnowledgeBaseID: 2, ProviderID: 9, TenantPolicy: Allow, KnowledgeBasePolicy: Deny, Locality: External, Operation: "embeddings",
	}) {
		t.Fatalf("denial was not audited or reached the network: calls=%d events=%v", calls.Load(), auditor.events)
	}
	auditErr := errors.New("audit unavailable")
	auditor.err = auditErr
	_, err := client.Embed(context.Background(), testScope(Deny), []string{"private content"})
	if !errors.Is(err, auditErr) || errors.Is(err, ErrEgressDenied) || calls.Load() != 0 {
		t.Fatalf("audit failure hidden or request sent: calls=%d err=%v", calls.Load(), err)
	}
}

func TestEmbeddingsEnforceTenantPolicyCeiling(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	auditor := &testAuditor{}
	client := newTestClient(t, server.URL, Internal, auditor)
	scope := testScope(Allow)
	scope.TenantPolicy = Deny
	if _, err := client.Embed(context.Background(), scope, []string{"private content"}); !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("knowledge base exceeded tenant policy: %v", err)
	}
	if calls.Load() != 0 || len(auditor.events) != 1 || auditor.events[0].TenantPolicy != Deny {
		t.Fatalf("tenant ceiling was not audited or reached the network: calls=%d events=%v", calls.Load(), auditor.events)
	}
}

func TestBoundEmbeddingClientResolvesCurrentPolicies(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(1, 0)}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	auditor := &testAuditor{}
	client := newTestClient(t, server.URL, Internal, auditor)
	binding := EmbeddingBinding{TenantID: 1, KnowledgeBaseID: 2, EmbeddingSpaceID: 3, ConfigRevision: 4}
	current := EgressScope{TenantID: 1, KnowledgeBaseID: 2, TenantPolicy: Deny, KnowledgeBasePolicy: Allow}
	var resolveErr error
	var resolutions int
	client.binding = &binding
	client.resolver = embeddingScopeResolverFunc(func(ctx context.Context, got EmbeddingBinding) (EgressScope, error) {
		resolutions++
		if got != binding {
			t.Error("resolver received an incorrect binding")
		}
		return current, resolveErr
	})
	forged := EgressScope{TenantID: 1, KnowledgeBaseID: 2, TenantPolicy: Allow, KnowledgeBasePolicy: Allow}
	if _, err := client.Embed(context.Background(), forged, []string{"synthetic"}); !errors.Is(err, ErrEgressDenied) || calls.Load() != 0 ||
		len(auditor.events) != 1 || auditor.events[0].TenantPolicy != Deny {
		t.Fatalf("caller supplied policy bypassed current tenant ceiling: err=%v calls=%d", err, calls.Load())
	}
	current.TenantPolicy, current.KnowledgeBasePolicy = Deny, Deny
	if _, err := client.Embed(context.Background(), forged, []string{"synthetic"}); err != nil || calls.Load() != 1 {
		t.Fatalf("current allowed policy was not used: err=%v calls=%d", err, calls.Load())
	}
	resolveErr = ErrRuntimeUnavailable
	if _, err := client.Embed(context.Background(), forged, []string{"synthetic"}); !errors.Is(err, ErrRuntimeUnavailable) || calls.Load() != 1 {
		t.Fatalf("scope resolution failure reached the model: err=%v calls=%d", err, calls.Load())
	}
	forged.KnowledgeBaseID++
	if _, err := client.Embed(context.Background(), forged, []string{"synthetic"}); !errors.Is(err, ErrScopeMismatch) || calls.Load() != 1 || resolutions != 3 {
		t.Fatalf("foreign knowledge base reached resolver or model: err=%v calls=%d resolutions=%d", err, calls.Load(), resolutions)
	}
}

func TestEmbeddingsRejectInvalidResponsesAndClassifyFailures(t *testing.T) {
	valid := vector(1, 0)
	for _, tc := range []struct {
		name      string
		status    int
		body      any
		retryable bool
	}{
		{name: "missing index", body: map[string]any{"data": []any{map[string]any{"embedding": valid}}}},
		{name: "bad dimensions", body: map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []int{1}}}}},
		{name: "zero vector", body: map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(0, 0)}}}},
		{name: "wrong index", body: map[string]any{"data": []any{map[string]any{"index": 1, "embedding": valid}}}},
		{name: "rate limited", status: 429, retryable: true},
		{name: "server failure", status: 503, retryable: true},
		{name: "bad request", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					if _, err := w.Write([]byte("sensitive-upstream-body")); err != nil {
						t.Errorf("write error response: %v", err)
					}
					return
				}
				if err := json.NewEncoder(w).Encode(tc.body); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, Internal, &testAuditor{})
			_, err := client.Embed(context.Background(), testScope(Allow), []string{"private input"})
			if tc.status == 0 {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("invalid response accepted: %v", err)
				}
				return
			}
			var upstream *UpstreamError
			if !errors.As(err, &upstream) || upstream.Status != tc.status || upstream.Retryable != tc.retryable || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("incorrect upstream error: %v", err)
			}
		})
	}
}

func TestEmbeddingsDoNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Internal, &testAuditor{})
	_, err := client.Embed(context.Background(), testScope(Deny), []string{"private input"})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusTemporaryRedirect || redirected.Load() {
		t.Fatalf("redirect was followed: redirected=%t err=%v", redirected.Load(), err)
	}
}

func TestEmbeddingsConcurrencyAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(1, 0)}}}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Internal, &testAuditor{})
	first := make(chan error, 1)
	go func() {
		_, err := client.Embed(context.Background(), testScope(Allow), []string{"one"})
		first <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Embed(ctx, testScope(Allow), []string{"two"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting request did not cancel: %v", err)
	}
	close(release)
	if err := <-first; err != nil || requests.Load() != 1 {
		t.Fatalf("concurrency exceeded or first request failed: requests=%d err=%v", requests.Load(), err)
	}
}

func TestEmbeddingCancellationReachesUpstream(t *testing.T) {
	entered := make(chan struct{})
	upstreamCancelled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
			close(upstreamCancelled)
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := newTestClient(t, server.URL, Internal, &testAuditor{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.Embed(ctx, testScope(Allow), []string{"private input"})
		result <- err
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not returned: %v", err)
	}
	select {
	case <-upstreamCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request was not cancelled")
	}
}

func TestEmbeddingResponseLimitsAndDuplicateIndexes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "too large", body: strings.Repeat("x", 16385)},
		{name: "invalid number", body: `{"data":[{"index":0,"embedding":[1e999]}]}`},
	}
	duplicate, err := json.Marshal(map[string]any{"data": []any{
		map[string]any{"index": 0, "embedding": vector(1, 0)},
		map[string]any{"index": 0, "embedding": vector(0, 1)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct{ name, body string }{name: "duplicate index", body: string(duplicate)})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, Internal, &testAuditor{})
			inputs := []string{"one"}
			if tc.name == "duplicate index" {
				inputs = append(inputs, "two")
			}
			if _, err := client.Embed(context.Background(), testScope(Allow), inputs); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("invalid response accepted: %v", err)
			}
		})
	}
}

func TestEmbeddingUsageIsExplicitAndValidated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   any
		valid   bool
		present bool
	}{
		{name: "missing", valid: true},
		{name: "reported", usage: map[string]any{"prompt_tokens": 5, "total_tokens": 5}, valid: true, present: true},
		{name: "missing prompt", usage: map[string]any{"total_tokens": 5}},
		{name: "missing total", usage: map[string]any{"prompt_tokens": 5}},
		{name: "zero prompt", usage: map[string]any{"prompt_tokens": 0, "total_tokens": 0}},
		{name: "negative prompt", usage: map[string]any{"prompt_tokens": -1, "total_tokens": 5}},
		{name: "total below prompt", usage: map[string]any{"prompt_tokens": 5, "total_tokens": 4}},
		{name: "noninteger", usage: map[string]any{"prompt_tokens": 1.5, "total_tokens": 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(1, 0)}}}
				if tc.usage != nil {
					body["usage"] = tc.usage
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, Internal, &testAuditor{})
			result, err := client.Embed(context.Background(), testScope(Allow), []string{"synthetic"})
			if !tc.valid {
				if !errors.Is(err, ErrInvalidResponse) || result != nil {
					t.Fatalf("invalid usage accepted: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result == nil || (result.Usage != nil) != tc.present {
				t.Fatalf("usage presence: result=%+v err=%v", result, err)
			}
			if tc.present && (result.Usage.PromptTokens != 5 || result.Usage.TotalTokens != 5) {
				t.Fatalf("usage changed: %+v", result.Usage)
			}
		})
	}
}

func TestEmbeddingConfigDoesNotExposeCredentials(t *testing.T) {
	secret := "do-not-log"
	_, err := NewEmbeddingClient(Provider{ID: 1, BaseURL: fmt.Sprintf("https://user:%s@example.com", secret), Locality: External}, EmbeddingConfig{}, &testAuditor{})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid provider URL error leaked credentials: %v", err)
	}
}

func TestProviderURLRejectsCredentialsAndInsecureExternalTransport(t *testing.T) {
	config := EmbeddingConfig{Model: "test", Dimensions: 64, MaxBatch: 1, MaxRequestBytes: 1024, MaxResponseBytes: 1024, Timeout: time.Second, Concurrency: 1}
	for _, baseURL := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?key=secret", "http://:8080"} {
		_, err := NewEmbeddingClient(Provider{ID: 1, BaseURL: baseURL, Locality: External}, config, &testAuditor{})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe provider URL accepted or exposed: %v", err)
		}
	}
}
