package kb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func setFixture(t *testing.T) (*fixture, KnowledgeBase, *eval.SetStore, eval.SetDefinition) {
	t.Helper()
	f, base, _, report := reportFixture(t)
	store, err := eval.NewSetStore(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	e := report.Rows[0].Fused[0]
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET page_start=3,page_end=5 WHERE id=$1`, e.ChunkID); err != nil {
		t.Fatal(err)
	}
	definition := eval.SetDefinition{Name: "golden", Cases: []eval.CaseDefinition{
		{ID: "q1", Question: "条件？", Answerable: true, Expected: []eval.ExpectedLocation{{DocumentID: e.DocumentID, SectionID: e.SectionID, Page: 4}}, ReferenceAnswer: "参考答案", Tags: []string{"精确词"}},
		{ID: "q2", Question: "未知？", Answerable: false, Expected: []eval.ExpectedLocation{}, Tags: []string{}},
	}}
	return f, base, store, definition
}

func TestEvalSetRevisionsConcurrentAndRLS(t *testing.T) {
	f, base, store, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	saved, err := store.Create(f.ctx, actor, base.ID, "", definition)
	if err != nil || saved.Revision != 1 || saved.ID <= 0 {
		t.Fatalf("create: %+v %v", saved, err)
	}
	definition.Name = "updated"
	type outcome struct {
		value eval.StoredSet
		err   error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			value, err := store.Update(f.ctx, actor, base.ID, saved.ID, 1, definition)
			results <- outcome{value, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil {
		a, b = b, a
	}
	if a.err != nil || a.value.Revision != 2 || !errors.Is(b.err, eval.ErrSetConflict) {
		t.Fatalf("concurrent saves: %+v %+v", a, b)
	}
	old, err := store.Get(f.ctx, actor, base.ID, saved.ID, 1)
	if err != nil || old.Digest != saved.Digest || old.Definition.Name != "golden" {
		t.Fatalf("history: %+v %v", old, err)
	}
	latest, err := store.Get(f.ctx, actor, base.ID, saved.ID, 0)
	if err != nil || latest.Revision != 2 || latest.Digest == old.Digest || latest.Definition.Name != "updated" {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if _, err := store.Get(f.ctx, actor, base.ID, saved.ID, 3); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("unknown revision: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='eval.set.save'`, f.tenantA).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit: %d %v", count, err)
	}
	for _, table := range []string{"eval_set", "eval_set_revision"} {
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(f.ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("RLS %s: %d %v", table, count, err)
		}
		if _, err := tx.Exec(f.ctx, "DELETE FROM "+table); err == nil {
			t.Fatalf("app can delete %s", table)
		}
		if err := tx.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantA)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE eval_set_revision SET digest=repeat('a',64)`); err == nil {
		t.Fatal("app can rewrite historical revisions")
	}
}

func TestEvalSetScopesLocationsAndArchive(t *testing.T) {
	f, base, store, definition := setFixture(t)
	owner := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	saved, err := store.Create(f.ctx, owner, base.ID, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{Viewer, Editor, Admin} {
		if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, role); err != nil {
			t.Fatal(err)
		}
		actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.other}
		_, readErr := store.Get(f.ctx, actor, base.ID, saved.ID, 0)
		_, writeErr := store.Create(f.ctx, actor, base.ID, "", definition)
		if role == Admin {
			if readErr != nil || writeErr != nil {
				t.Fatalf("admin: %v %v", readErr, writeErr)
			}
		} else if !errors.Is(readErr, eval.ErrSetForbidden) || !errors.Is(writeErr, eval.ErrSetForbidden) {
			t.Fatalf("role %s: %v %v", role, readErr, writeErr)
		}
	}
	for _, actor := range []retrieval.Actor{{TenantID: f.tenantA, UserID: f.third}, {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if _, err := store.Get(f.ctx, actor, base.ID, saved.ID, 0); !errors.Is(err, eval.ErrSetNotFound) {
			t.Fatalf("scope read: %v", err)
		}
		if _, err := store.Create(f.ctx, actor, base.ID, "", definition); !errors.Is(err, eval.ErrSetNotFound) {
			t.Fatalf("scope write: %v", err)
		}
	}
	location := definition.Cases[0].Expected[0]
	other, err := f.service.Create(f.ctx, f.actor(f.owner), CreateRequest{Name: "other-set-kb", EmbeddingSpaceID: f.spaceA, EgressPolicy: base.EgressPolicy, IdempotencyKey: "other-set-kb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(f.ctx, owner, other.ID, "", definition); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("foreign KB targets: %v", err)
	}
	if _, err := store.Get(f.ctx, owner, other.ID, saved.ID, 0); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("foreign KB set: %v", err)
	}
	for _, bad := range []eval.ExpectedLocation{{DocumentID: location.DocumentID, SectionID: location.SectionID + 99999}, {DocumentID: location.DocumentID, Page: 6}, {DocumentID: location.DocumentID + 99999}} {
		definition.Cases[0].Expected[0] = bad
		if _, err := store.Create(f.ctx, owner, base.ID, "", definition); !errors.Is(err, eval.ErrSetNotFound) {
			t.Fatalf("forged location %+v: %v", bad, err)
		}
	}
	definition.Cases[0].Expected[0] = location
	for name, sql := range map[string]string{
		"disabled":   "UPDATE chunk SET is_current=false,disabled=true WHERE tenant_id=$1",
		"tags":       "UPDATE chunk SET access_tags=ARRAY['private'] WHERE tenant_id=$1",
		"restricted": "UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1",
		"deleted":    "UPDATE document SET deleted_at=now() WHERE tenant_id=$1",
		"version":    "UPDATE document SET current_version_id=NULL WHERE tenant_id=$1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.admin.Exec(f.ctx, sql, f.tenantA); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(f.ctx, owner, base.ID, saved.ID, 1); !errors.Is(err, eval.ErrSetNotFound) {
				t.Fatalf("revoked history: %v", err)
			}
			if _, err := store.Create(f.ctx, owner, base.ID, "", definition); !errors.Is(err, eval.ErrSetNotFound) {
				t.Fatalf("revoked save: %v", err)
			}
			if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=NULL,acl_mode='inherit_kb',current_version_id=(SELECT id FROM document_version WHERE document_id=document.id LIMIT 1) WHERE tenant_id=$1`, f.tenantA); err != nil {
				t.Fatal(err)
			}
			if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=true,disabled=false,access_tags=ARRAY['kb:'||kb_id::text||':all'] WHERE tenant_id=$1`, f.tenantA); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A set containing only negative examples still requires membership but no
	// document witness. It must remain a valid authored set.
	negative := eval.SetDefinition{Name: "negative", Cases: definition.Cases[1:]}
	negativeSaved, err := store.Create(f.ctx, owner, base.ID, "", negative)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(f.ctx, owner, base.ID, negativeSaved.ID, 0); err != nil {
		t.Fatalf("negative-only read: %v", err)
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(f.ctx, owner, base.ID, saved.ID, 1, definition); !errors.Is(err, eval.ErrSetArchived) {
		t.Fatalf("archived save: %v", err)
	}
	if _, err := store.Get(f.ctx, owner, base.ID, saved.ID, 0); err != nil {
		t.Fatalf("archived read: %v", err)
	}
}

func TestEvalSetAuditRollback(t *testing.T) {
	f, base, store, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	saved, err := store.Create(f.ctx, actor, base.ID, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("set_audit_failure_%d", f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='eval.set.save' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)
	if _, err := f.admin.Exec(f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []int64{0, saved.ID} {
		expected := int32(0)
		if id != 0 {
			expected = 1
		}
		var saveErr error
		if id == 0 {
			_, saveErr = store.Create(f.ctx, actor, base.ID, "failed-request", definition)
		} else {
			_, saveErr = store.Update(f.ctx, actor, base.ID, id, expected, definition)
		}
		if saveErr == nil {
			t.Fatal("audit failure succeeded")
		}
	}
	got, err := store.Get(f.ctx, actor, base.ID, saved.ID, 0)
	if err != nil || got.Revision != 1 {
		t.Fatalf("head survived rollback: %+v %v", got, err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM eval_set_revision WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revision rollback: %d %v", count, err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM eval_set WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("head rollback: %d %v", count, err)
	}
}

func TestEvalSetCreationIdempotency(t *testing.T) {
	f, base, store, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	type outcome struct {
		value eval.StoredSet
		err   error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			value, err := store.Create(f.ctx, actor, base.ID, "same-request", definition)
			results <- outcome{value, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.value.ID != b.value.ID || a.value.CreatedAt != b.value.CreatedAt {
		t.Fatalf("duplicate creation: %+v %+v", a, b)
	}
	definition.Name = "changed"
	if _, err := store.Create(f.ctx, actor, base.ID, "same-request", definition); !errors.Is(err, eval.ErrSetIdempotencyConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err := store.Update(f.ctx, actor, base.ID, a.value.ID, 1, definition); err != nil {
		t.Fatal(err)
	}
	definition.Name = "golden"
	retry, err := store.Create(f.ctx, actor, base.ID, "same-request", definition)
	if err != nil || retry.Revision != 1 || retry.Digest != a.value.Digest {
		t.Fatalf("retry after edit: %+v %v", retry, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(f.ctx, actor, base.ID, "same-request", definition); !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("retry bypassed current access: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='eval.set.save'`, f.tenantA).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate audits: %d %v", count, err)
	}
}

func TestEvalSetRevokedWhileWaiting(t *testing.T) {
	for _, writing := range []bool{false, true} {
		t.Run(fmt.Sprint(writing), func(t *testing.T) {
			f, base, store, definition := setFixture(t)
			owner := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
			saved, err := store.Create(f.ctx, owner, base.ID, "", definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, Admin); err != nil {
				t.Fatal(err)
			}
			tx, err := f.admin.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(f.ctx, `SELECT id FROM knowledge_base WHERE id=$1 FOR UPDATE`, base.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.other}
				var err error
				if writing {
					_, err = store.Update(f.ctx, actor, base.ID, saved.ID, 1, definition)
				} else {
					_, err = store.Get(f.ctx, actor, base.ID, saved.ID, 0)
				}
				result <- err
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := tx.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int32(f.admin.PgConn().PID())).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("operation did not wait: %v", err)
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				case <-ticker.C:
				}
			}
			if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, eval.ErrSetNotFound) {
				t.Fatalf("revoked operation: %v", err)
			}
		})
	}
}
