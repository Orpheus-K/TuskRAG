package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/retrieval"
)

type conversationStub struct {
	t        *testing.T
	feedback bool
}

func (s *conversationStub) Create(_ context.Context, a retrieval.Actor, r answer.NewConversation) (answer.Conversation, error) {
	if a.TenantID <= 0 || r.KnowledgeBaseID != 7 || r.ModelID != 1 {
		s.t.Error("invalid conversation actor or body")
	}
	return answer.Conversation{ID: 3, KnowledgeBaseID: 7, ModelID: 1, Title: r.Title, CreatedAt: time.Now()}, nil
}
func (s *conversationStub) List(context.Context, retrieval.Actor, int64, int64) (answer.ConversationPage, error) {
	return answer.ConversationPage{Items: []answer.Conversation{}}, nil
}
func (s *conversationStub) Get(context.Context, retrieval.Actor, int64) (answer.ConversationDetail, error) {
	return answer.ConversationDetail{}, retrieval.ErrEvidenceChanged
}
func (s *conversationStub) Stream(ctx context.Context, a retrieval.Actor, id int64, q string, send func(answer.Event) error) error {
	if a.TenantID <= 0 || id != 3 || q != "追问" {
		s.t.Error("invalid conversation stream binding")
	}
	for _, e := range []answer.Event{{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{}, Degraded: []string{"rewrite"}}}, {Delta: &answer.Delta{Text: answer.Refusal}}, {Citations: &answer.Citations{Items: []answer.Citation{}}}, {Done: &answer.Done{MessageID: "message-123456789", Outcome: "refused", PromptVersion: answer.PromptVersion}}} {
		if err := send(e); err != nil {
			return err
		}
	}
	return nil
}
func (s *conversationStub) PutFeedback(_ context.Context, a retrieval.Actor, id string, f answer.Feedback) error {
	if a.TenantID <= 0 || id != "message-123456789" || f.Rating != -1 || f.Correction != "需核对" {
		s.t.Error("feedback binding invalid")
	}
	s.feedback = true
	return nil
}

func TestConversationHTTPContract(t *testing.T) {
	service := &conversationStub{t: t}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Conversations: service, InsecureLocalCookies: true})
	for _, tc := range []struct {
		method, path, body, contains string
		status                       int
	}{
		{"POST", "/api/v1/conversations", `{"kb_id":7,"model_id":1,"title":"咨询"}`, `"id":3`, 200},
		{"GET", "/api/v1/kbs/7/conversations", "", `"items":[]`, 200},
		{"GET", "/api/v1/conversations/3", "", "evidence_unavailable", 409},
		{"POST", "/api/v1/conversations/3/messages", `{"question":"追问"}`, "event: done", 200},
		{"POST", "/api/v1/messages/message-123456789/feedback", `{"rating":-1,"correction":"需核对"}`, "pending", 200},
		{"POST", "/api/v1/messages/message-123456789/feedback", `{"rating":0,"correction":"需核对"}`, "", 422},
	} {
		r := searchRequest(tc.body)
		r.Method = tc.method
		r.URL.Path = tc.path
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private history is cacheable")
		}
	}
	if !service.feedback {
		t.Fatal("feedback was not saved")
	}
	r := searchRequest(`{"question":"追问"}`)
	r.URL.Path = "/api/v1/conversations/3/messages"
	r.Header.Del("X-CSRF-Token")
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
}
