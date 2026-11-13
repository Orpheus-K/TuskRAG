package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatWireStream(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"申请 [1]\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\ndata: [DONE]\n\n"
	for _, tc := range []struct {
		name, input string
		bad         bool
	}{
		{"normal", stream, false}, {"missing end", strings.ReplaceAll(stream, "data: [DONE]\n\n", ""), true},
		{"early end", "data: [DONE]\n\n", true}, {"invalid usage", strings.Replace(stream, "\"total_tokens\":14", "\"total_tokens\":1", 1), true},
		{"tool call", strings.Replace(stream, "\"stop\"", "\"tool_calls\"", 1), true},
		{"wrong choice", strings.Replace(stream, "\"index\":0", "\"index\":1", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var text strings.Builder
			result, err := readChatStream(context.Background(), strings.NewReader(tc.input), func(delta string) error { text.WriteString(delta); return nil })
			if (err != nil) != tc.bad {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !tc.bad && (text.String() != "申请 [1]" || result.Usage.TotalTokens != 14) {
				t.Fatalf("text=%s result=%+v", text.String(), result)
			}
		})
	}
}

func TestChatCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			started := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-test" {
					t.Error("invalid chat request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			duration := time.Second
			if timeout {
				duration = 60 * time.Millisecond
			}
			client, err := NewChatClient(Provider{ID: 1, BaseURL: server.URL, Locality: Internal, APIKey: "private-test"}, "chat", duration, func(context.Context) (EgressScope, error) {
				return EgressScope{TenantID: 1, KnowledgeBaseID: 1, TenantPolicy: Deny, KnowledgeBasePolicy: Deny}, nil
			}, &testAuditor{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Stream(ctx, []ChatMessage{{Role: "user", Content: "test"}}, func(string) error { return nil })
				done <- err
			}()
			<-started
			if !timeout {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("chat not cancelled")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("upstream remained active")
			}
		})
	}
}

func TestChatEgressBeforeNetwork(t *testing.T) {
	auditor := &testAuditor{}
	client, err := NewChatClient(Provider{ID: 1, BaseURL: "https://example.invalid", Locality: External}, "chat", time.Second, func(context.Context) (EgressScope, error) {
		return EgressScope{TenantID: 1, KnowledgeBaseID: 1, TenantPolicy: Deny, KnowledgeBasePolicy: Deny}, nil
	}, auditor)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Stream(context.Background(), []ChatMessage{{Role: "user", Content: "secret"}}, func(string) error { return nil })
	if !errors.Is(err, ErrEgressDenied) || len(auditor.events) != 1 || auditor.events[0].Operation != "chat.completions" {
		t.Fatalf("err=%v events=%+v", err, auditor.events)
	}
}
