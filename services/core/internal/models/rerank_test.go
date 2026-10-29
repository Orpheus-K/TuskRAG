package models

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRerankWireAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"sort", `{"results":[{"index":0,"relevance_score":-1},{"index":1,"relevance_score":2}]}`, true},
		{"duplicate", `{"results":[{"index":0,"relevance_score":1},{"index":0,"relevance_score":2}]}`, false},
		{"missing", `{"results":[{"index":0,"relevance_score":1}]}`, false},
		{"missing_score", `{"results":[{"index":0},{"index":1,"relevance_score":2}]}`, false},
		{"invalid_index", `{"results":[{"index":-1,"relevance_score":1},{"index":1,"relevance_score":2}]}`, false},
		{"invalid_json", `{"results":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model, Query string
					Documents    []string
					TopN         int `json:"top_n"`
				}
				if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer test" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "rank" || request.Query != "住宿" || len(request.Documents) != 2 || request.TopN != 2 {
					t.Error("invalid rerank request")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewRerankClient(Provider{ID: 1, BaseURL: server.URL, Locality: Internal, APIKey: "test"}, RerankConfig{Model: "rank", Timeout: time.Second}, &testAuditor{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.Rerank(context.Background(), testScope(Allow), "住宿", []string{"审批", "酒店"})
			if tc.valid {
				if err != nil || len(result) != 2 || result[0].Index != 1 || result[0].Score != 2 {
					t.Fatalf("result=%v err=%v", result, err)
				}
			} else if !errors.Is(err, ErrInvalidResponse) || result != nil {
				t.Fatalf("invalid response accepted: %v %v", result, err)
			}
		})
	}
}

func TestRerankDenialAndCancellation(t *testing.T) {
	auditor := &testAuditor{}
	client, err := NewRerankClient(Provider{ID: 1, BaseURL: "https://127.0.0.1:9", Locality: External}, RerankConfig{Model: "rank", Timeout: time.Second}, auditor)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Rerank(context.Background(), testScope(Deny), "住宿", []string{"酒店"}); !errors.Is(err, ErrEgressDenied) || len(auditor.events) != 1 || auditor.events[0].Operation != "rerank" {
		t.Fatalf("denial: %v %+v", err, auditor.events)
	}
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	local, err := NewRerankClient(Provider{ID: 1, BaseURL: server.URL, Locality: Internal}, RerankConfig{Model: "rank", Timeout: time.Second}, auditor)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := local.Rerank(ctx, testScope(Allow), "住宿", []string{"酒店"}); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
