package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type answerFunc func(context.Context, retrieval.Actor, answer.Request, func(answer.Event) error) error

func (f answerFunc) Stream(c context.Context, a retrieval.Actor, r answer.Request, e func(answer.Event) error) error {
	return f(c, a, r, e)
}
func TestAnswerSSEContract(t *testing.T) {
	service := answerFunc(func(ctx context.Context, actor retrieval.Actor, req answer.Request, send func(answer.Event) error) error {
		if actor.TenantID <= 0 || req.KnowledgeBaseID != 7 || req.ModelID != 1 {
			t.Error("answer request not bound")
		}
		for _, e := range []answer.Event{{Retrieval: &answer.RetrievalEvent{Evidence: []answer.AnswerEvidence{}, Degraded: []string{}}}, {Delta: &answer.Delta{Text: answer.Refusal}}, {Citations: &answer.Citations{Items: []answer.Citation{}}}, {Done: &answer.Done{MessageID: "test", Outcome: "refused", PromptVersion: answer.PromptVersion}}} {
			if err := send(e); err != nil {
				return err
			}
		}
		return nil
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Answers: service, InsecureLocalCookies: true})
	req := searchRequest(`{"kb_id":7,"model_id":1,"question":"期限"}`)
	req.URL.Path = "/api/v1/answers"
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	body := w.Body.String()
	last := -1
	for _, name := range []string{"retrieval", "delta", "citations", "done"} {
		idx := strings.Index(body, "event: "+name)
		if idx <= last {
			t.Fatalf("event order: %s", body)
		}
		last = idx
	}
	if w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
	req = searchRequest(`{"kb_id":7,"model_id":1,"question":"期限"}`)
	req.URL.Path = "/api/v1/answers"
	req.Header.Del("X-CSRF-Token")
	w = httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestAnswerDisconnectCancelsUpstream(t *testing.T) {
	stopped := make(chan struct{})
	service := answerFunc(func(ctx context.Context, _ retrieval.Actor, _ answer.Request, send func(answer.Event) error) error {
		if err := send(answer.Event{Delta: &answer.Delta{Text: "first"}}); err != nil {
			return err
		}
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Answers: service, InsecureLocalCookies: true})
	server := httptest.NewServer(app.Handler)
	defer server.Close()
	req := searchRequest(`{"kb_id":7,"model_id":1,"question":"期限"}`)
	req.URL.Path = "/api/v1/answers"
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(server.URL, "http://")
	req.RequestURI = ""
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if _, err = io.ReadFull(res.Body, buffer); err != nil {
		t.Fatal(err)
	}
	if err = res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream was not cancelled")
	}
}
func TestAnswerErrorsAreSafe(t *testing.T) {
	for _, err := range []error{errors.New("private-key secret-content"), retrieval.ErrEvidenceChanged, context.DeadlineExceeded} {
		e := answerStreamError(err)
		if strings.Contains(e.Message, "secret") || e.Code == "" {
			t.Fatal(e)
		}
	}
}

func TestAnswerStreamErrorMappingsAndLogging(t *testing.T) {
	cases := []struct {
		err       error
		code      string
		retryable bool
	}{
		{models.ErrTokenizerMismatch, "model_invalid_response", false},
		{models.ErrTokenizerUsageMissing, "model_invalid_response", false},
		{indexing.ErrUnsupportedDistance, "unsupported_embedding", false},
		{indexing.ErrIndexMissing, "dependency_unavailable", true},
	}
	for _, tc := range cases {
		got := answerStreamError(tc.err)
		if got.Code != tc.code || got.Retryable != tc.retryable || got.Message == "" {
			t.Fatalf("unexpected mapping for %v: %+v", tc.err, got)
		}
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	service := answerFunc(func(context.Context, retrieval.Actor, answer.Request, func(answer.Event) error) error {
		return errors.New("boom-detail")
	})
	app := New(Dependencies{Identity: uploadTestIdentity{}, Answers: service, InsecureLocalCookies: true})
	response := httptest.NewRecorder()
	request := searchRequest(`{"kb_id":7,"model_id":1,"question":"hello"}`)
	request.URL.Path = "/api/v1/answers"
	app.Handler.ServeHTTP(response, request)

	if response.Code != 200 || !strings.Contains(response.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("expected internal_error SSE event, got %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "answer stream failed") || !strings.Contains(logs.String(), "boom-detail") {
		t.Fatalf("expected failure log with error detail, got %s", logs.String())
	}
}
