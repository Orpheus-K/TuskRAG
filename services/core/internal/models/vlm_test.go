package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type noopVLMAuditor struct{}

func (noopVLMAuditor) RecordModelDenied(context.Context, DeniedCall) error { return nil }

func TestVLMClientExtractPageAndEgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rawUser, _ := json.Marshal(req.Messages[1].Content)
		if !strings.Contains(string(rawUser), "data:image/png;base64,") {
			http.Error(w, "missing image_url", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "# Scanned Page\n\nExtracted text."}},
			},
			"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 15, "total_tokens": 135},
		})
	}))
	defer srv.Close()

	auditor := noopVLMAuditor{}
	client, err := NewVLMClient(
		Provider{ID: 1, BaseURL: srv.URL, Locality: "internal"},
		"vlm-test",
		5*time.Second,
		func(context.Context) (EgressScope, error) {
			return EgressScope{TenantID: 1, KnowledgeBaseID: 2, TenantPolicy: InternalOnly, KnowledgeBasePolicy: InternalOnly}, nil
		},
		auditor,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	md, usage, err := client.ExtractPage(context.Background(), 1, []byte("fake-png-bytes"))
	if err != nil {
		t.Fatalf("ExtractPage failed: %v", err)
	}
	if !strings.Contains(md, "Extracted text.") || usage == nil || usage.TotalTokens != 135 {
		t.Fatalf("unexpected ExtractPage result: md=%q usage=%+v", md, usage)
	}

	// Verify egress_denied for external provider under deny policy.
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	deniedClient, err := NewVLMClient(
		Provider{ID: 2, BaseURL: tlsSrv.URL, Locality: External},
		"vlm-ext",
		5*time.Second,
		func(context.Context) (EgressScope, error) {
			return EgressScope{TenantID: 1, KnowledgeBaseID: 2, TenantPolicy: Deny, KnowledgeBasePolicy: Deny}, nil
		},
		auditor,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer deniedClient.Close()
	if _, _, err := deniedClient.ExtractPage(context.Background(), 1, []byte("fake-png-bytes")); !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("expected ErrEgressDenied, got %v", err)
	}
}
