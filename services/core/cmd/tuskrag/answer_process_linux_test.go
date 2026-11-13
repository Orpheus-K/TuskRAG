//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/httpapi"
	"tuskrag.local/core/internal/models"
)

func testAnswerProcess(t *testing.T, ctx context.Context, address string, client *http.Client, cookie *http.Cookie, csrf string, kbID int64) {
	t.Helper()
	var blocking atomic.Bool
	entered, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []models.ChatMessage `json:"messages"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 {
			t.Error("invalid production chat request")
			w.WriteHeader(400)
			return
		}
		if strings.Contains(req.Messages[0].Content, "只返回 JSON") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"question\\\":\\\"申请材料\\\"}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		var prompt struct {
			Evidence []answer.AnswerEvidence `json:"evidence"`
		}
		if json.Unmarshal([]byte(req.Messages[1].Content), &prompt) != nil || len(prompt.Evidence) == 0 {
			t.Error("missing production evidence")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if blocking.Load() {
			entered <- struct{}{}
			<-r.Context().Done()
			stopped <- struct{}{}
			return
		}
		content := prompt.Evidence[0].Content
		runes := []rune(content)
		if len(runes) > 100 {
			content = string(runes[:100])
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content + " [1]"}, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()
	request := func(requestCtx context.Context, method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(requestCtx, method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			t.Fatalf("answer HTTP %s status=%d", path, resp.StatusCode)
		}
		return resp
	}
	body, _ := json.Marshal(models.NewChatModel{Name: "process answer", BaseURL: model.URL, Locality: models.Internal, Model: "chat"})
	res := request(ctx, "POST", "/api/v1/admin/chat-models", string(body))
	var configured models.ChatModel
	err := json.NewDecoder(res.Body).Decode(&configured)
	_ = res.Body.Close()
	if err != nil || configured.ID <= 0 {
		t.Fatalf("chat model config %v", err)
	}
	ask, _ := json.Marshal(answer.Request{KnowledgeBaseID: kbID, ModelID: configured.ID, Question: "申请"})
	res = request(ctx, "POST", "/api/v1/answers", string(ask))
	scanner := bufio.NewScanner(res.Body)
	name := ""
	var evidence []answer.AnswerEvidence
	var final answer.Done
	var cited answer.Citations
	var text strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := []byte(strings.TrimPrefix(line, "data: "))
		switch name {
		case "retrieval":
			var event answer.RetrievalEvent
			if err = json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			evidence = event.Evidence
		case "delta":
			var event answer.Delta
			if err = json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			text.WriteString(event.Text)
		case "citations":
			if err = json.Unmarshal(data, &cited); err != nil {
				t.Fatal(err)
			}
		case "done":
			if err = json.Unmarshal(data, &final); err != nil {
				t.Fatal(err)
			}
		case "error":
			t.Fatalf("answer stream error: %s", data)
		}
	}
	scanErr := scanner.Err()
	_ = res.Body.Close()
	if scanErr != nil || final.Outcome != "answered" || len(evidence) == 0 || len(cited.Items) == 0 || text.Len() == 0 {
		t.Fatalf("production answer: outcome=%s evidence=%d citations=%d text=%d err=%v", final.Outcome, len(evidence), len(cited.Items), text.Len(), scanErr)
	}
	first := evidence[0]
	res = request(ctx, "GET", fmt.Sprintf("/api/v1/sections/%d", first.SectionID), "")
	var source httpapi.SectionOutput
	err = json.NewDecoder(res.Body).Decode(&source.Body)
	_ = res.Body.Close()
	if err != nil || source.Body.Document.ID != first.DocumentID || source.Body.Document.VersionID != first.VersionID || !strings.Contains(source.Body.Markdown, first.Content) {
		t.Fatalf("citation source mismatch %v", err)
	}
	blocking.Store(true)
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	res = request(cancelCtx, "POST", "/api/v1/answers", string(ask))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		_ = res.Body.Close()
		t.Fatal("stream model did not start")
	}
	cancel()
	_ = res.Body.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("production cancellation did not reach model")
	}
	t.Logf("cited answer: %d evidence, validated source, upstream cancellation", len(evidence))
	blocking.Store(false)
	creation, _ := json.Marshal(answer.NewConversation{KnowledgeBaseID: kbID, ModelID: configured.ID, Title: "进程会话"})
	res = request(ctx, "POST", "/api/v1/conversations", string(creation))
	var conversation answer.Conversation
	err = json.NewDecoder(res.Body).Decode(&conversation)
	_ = res.Body.Close()
	if err != nil || conversation.ID <= 0 {
		t.Fatalf("create saved conversation: %v", err)
	}
	for _, question := range []string{"申请", "材料呢？"} {
		payload, _ := json.Marshal(map[string]string{"question": question})
		res = request(ctx, "POST", fmt.Sprintf("/api/v1/conversations/%d/messages", conversation.ID), string(payload))
		scanner = bufio.NewScanner(res.Body)
		terminal := false
		name = ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				if name == "error" {
					t.Fatalf("saved process answer failed: %s", line)
				}
				if name == "done" {
					terminal = true
				}
			}
		}
		scanErr = scanner.Err()
		_ = res.Body.Close()
		if scanErr != nil || !terminal {
			t.Fatalf("saved process stream missing terminal: %v", scanErr)
		}
	}
	readSaved := func() answer.ConversationDetail {
		res = request(ctx, "GET", fmt.Sprintf("/api/v1/conversations/%d", conversation.ID), "")
		var history answer.ConversationDetail
		err = json.NewDecoder(res.Body).Decode(&history)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return history
	}
	history := readSaved()
	if len(history.Messages) != 2 {
		t.Fatal("production history did not save two turns")
	}
	for _, m := range history.Messages {
		if m.Status != "complete" || m.Content == "" || len(m.Citations.Items) == 0 || m.Done == nil {
			t.Fatal("incomplete production saved message")
		}
	}
	res = request(ctx, "POST", fmt.Sprintf("/api/v1/messages/%s/feedback", history.Messages[1].ID), `{"rating":1,"correction":""}`)
	_ = res.Body.Close()
	history = readSaved()
	if history.Messages[1].Feedback == nil || history.Messages[1].Feedback.Rating != 1 {
		t.Fatal("production feedback not persisted")
	}
	t.Log("saved conversation: two turns, rewrite, history reload and feedback passed")
}
