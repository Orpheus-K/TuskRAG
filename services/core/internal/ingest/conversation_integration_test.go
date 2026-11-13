package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
)

func testSavedConversations(t *testing.T, f *fixture, service *answer.Service, modelID int64, knowledge *kb.Service, silent *atomic.Bool, started, stopped chan struct{}) (*answer.Conversations, int64) {
	t.Helper()
	store, err := answer.NewConversations(f.pool, service)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	conversation, err := store.Create(f.ctx, actor, answer.NewConversation{KnowledgeBaseID: f.kbID, ModelID: modelID, Title: "申请咨询"})
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"申请", "具体流程呢？"} {
		var done *answer.Done
		if err = store.Stream(f.ctx, actor, conversation.ID, question, func(e answer.Event) error {
			if e.Done != nil {
				done = e.Done
			}
			return nil
		}); err != nil || done == nil || done.Outcome != "answered" {
			t.Fatalf("saved answer: done=%+v err=%v", done, err)
		}
	}
	saved, err := store.Get(f.ctx, actor, conversation.ID)
	if err != nil || len(saved.Messages) != 2 {
		t.Fatalf("saved history: %d %v", len(saved.Messages), err)
	}
	for _, m := range saved.Messages {
		if m.Status != "complete" || m.Content == "" || m.Done == nil || m.Done.MessageID != m.ID || len(m.Evidence) == 0 || len(m.Citations.Items) == 0 {
			t.Fatalf("incomplete saved answer: %s", m.Status)
		}
	}
	id := saved.Messages[1].ID
	if err = store.PutFeedback(f.ctx, actor, id, answer.Feedback{Rating: -1, Correction: "请核对期限"}); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Get(f.ctx, actor, conversation.ID)
	if err != nil || saved.Messages[1].Feedback == nil || saved.Messages[1].Feedback.Rating != -1 {
		t.Fatalf("feedback: %v", err)
	}
	var raw, audit string
	if err = f.admin.QueryRow(f.ctx, `SELECT evidence::text FROM answer_message WHERE tenant_id=$1 AND id=$2`, f.tenant, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "content") || strings.Contains(raw, "title") {
		t.Fatal("evidence body persisted twice")
	}
	if err = f.admin.QueryRow(f.ctx, `SELECT details::text FROM audit_log WHERE tenant_id=$1 AND action='answer.feedback' ORDER BY id DESC LIMIT 1`, f.tenant).Scan(&audit); err != nil || strings.Contains(audit, "核对") {
		t.Fatalf("feedback audit leaked text: %v", err)
	}
	var other int64
	if err = f.admin.QueryRow(f.ctx, `INSERT INTO app_user(tenant_id,display_name)VALUES($1,'another reader')RETURNING id`, f.tenant).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err = knowledge.SetMember(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, other, kb.Viewer); err != nil {
		t.Fatal(err)
	}
	for _, stranger := range []retrieval.Actor{{TenantID: f.tenant, UserID: other}, {TenantID: f.foreign, UserID: f.user}} {
		if _, err = store.Get(f.ctx, stranger, conversation.ID); !errors.Is(err, retrieval.ErrNotFound) {
			t.Fatalf("foreign history: %v", err)
		}
		if err = store.PutFeedback(f.ctx, stranger, id, answer.Feedback{Rating: 1}); !errors.Is(err, retrieval.ErrNotFound) {
			t.Fatalf("foreign feedback: %v", err)
		}
	}
	page, err := store.List(f.ctx, retrieval.Actor{TenantID: f.tenant, UserID: other}, f.kbID, 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other reader list %v", err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"conversation", "answer_message", "answer_feedback"} {
		var count int
		if err = tx.QueryRow(f.ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("RLS %s visible=%d err=%v", table, count, err)
		}
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	reader := retrieval.Actor{TenantID: f.tenant, UserID: other}
	readerConversation, err := store.Create(f.ctx, reader, answer.NewConversation{KnowledgeBaseID: f.kbID, ModelID: modelID, Title: "撤权追问"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Stream(f.ctx, reader, readerConversation.ID, "申请", func(answer.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	silent.Store(true)
	revoked := make(chan error, 1)
	go func() {
		revoked <- store.Stream(f.ctx, reader, readerConversation.ID, "期限呢？", func(answer.Event) error { t.Error("revoked rewrite emitted an event"); return nil })
	}()
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal("rewrite did not start")
	}
	if err = knowledge.RemoveMember(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, other); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-revoked:
		if !errors.Is(err, retrieval.ErrNotFound) {
			t.Fatalf("revoked rewrite error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked rewrite continued")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("rewrite upstream remained active")
	}
	silent.Store(false)
	if _, err = store.Get(f.ctx, reader, readerConversation.ID); !errors.Is(err, retrieval.ErrNotFound) {
		t.Fatalf("revoked history visible: %v", err)
	}
	cancelled, err := store.Create(f.ctx, actor, answer.NewConversation{KnowledgeBaseID: f.kbID, ModelID: modelID, Title: "取消测试"})
	if err != nil {
		t.Fatal(err)
	}
	silent.Store(true)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- store.Stream(ctx, actor, cancelled.ID, "申请", func(answer.Event) error { return nil })
	}()
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal("saved stream did not start")
	}
	if err = store.Stream(f.ctx, actor, cancelled.ID, "重复", func(answer.Event) error { t.Error("concurrent event"); return nil }); !errors.Is(err, answer.ErrConversationBusy) {
		t.Fatalf("concurrent answer: %v", err)
	}
	running, err := store.Get(f.ctx, actor, cancelled.ID)
	if err != nil || len(running.Messages) != 1 || running.Messages[0].Content != "" || len(running.Messages[0].Evidence) != 0 {
		t.Fatalf("running answer exposed: %v", err)
	}
	cancel()
	select {
	case err = <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("saved stream cancellation stuck")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("saved stream did not cancel upstream")
	}
	silent.Store(false)
	savedCancel, err := store.Get(f.ctx, actor, cancelled.ID)
	if err != nil || savedCancel.Messages[0].Status != "cancelled" || savedCancel.Messages[0].Content != "" {
		t.Fatalf("partial answer saved: %v", err)
	}
	if _, err = f.admin.Exec(f.ctx, `INSERT INTO answer_message(tenant_id,id,conversation_id,ordinal,question,content,deadline)VALUES($1,'expired-answer-test',$2,2,'过期','不得显示的部分回答',now()-interval '1 second')`, f.tenant, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.Get(f.ctx, actor, cancelled.ID)
	if err != nil || expired.Messages[1].Status != "failed" || expired.Messages[1].Content != "" {
		t.Fatalf("crashed stream not expired: %v", err)
	}
	t.Log("conversation: saved two turns, feedback, owner isolation, concurrent rejection, cancellation and crash expiry passed")
	return store, conversation.ID
}
