package kb

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func TestEvalSetVisiblePagination(t *testing.T) {
	f, base, store, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	hidden, err := store.Create(f.ctx, actor, base.ID, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	negative := eval.SetDefinition{Name: "visible", Cases: definition.Cases[1:]}
	a, err := store.Create(f.ctx, actor, base.ID, "", negative)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(f.ctx, actor, base.ID, "", negative)
	if err != nil {
		t.Fatal(err)
	}
	negative.Name = "renamed"
	if _, err := store.Update(f.ctx, actor, base.ID, a.ID, 1, negative); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(f.ctx, actor, base.ID, 0, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Items[0].Revision != 2 || page.Items[0].Name != "renamed" || page.Items[0].CaseCount != 1 || page.NextCursor == nil || *page.NextCursor != a.ID {
		t.Fatalf("first page after hidden %d: %+v %v", hidden.ID, page, err)
	}
	page, err = store.List(f.ctx, actor, base.ID, *page.NextCursor, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID || page.NextCursor != nil {
		t.Fatalf("last page: %+v %v", page, err)
	}
	page, err = store.List(f.ctx, actor, base.ID, b.ID, 10)
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("empty page: %+v %v", page, err)
	}
	for _, params := range []struct {
		cursor int64
		limit  int32
	}{{-1, 1}, {0, 0}, {0, 11}} {
		if _, err := store.List(f.ctx, actor, base.ID, params.cursor, params.limit); !errors.Is(err, eval.ErrInvalidCase) {
			t.Fatalf("invalid pagination: %v", err)
		}
	}
	for _, role := range []Role{Viewer, Editor, Admin} {
		if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, role); err != nil {
			t.Fatal(err)
		}
		_, err := store.List(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, base.ID, 0, 10)
		if role == Admin {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, eval.ErrSetForbidden) {
			t.Fatalf("role %s: %v", role, err)
		}
	}
	for _, who := range []retrieval.Actor{{TenantID: f.tenantA, UserID: f.third}, {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if _, err := store.List(f.ctx, who, base.ID, 0, 10); !errors.Is(err, eval.ErrSetNotFound) {
			t.Fatalf("scope: %v", err)
		}
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), base.ID); err != nil {
		t.Fatal(err)
	}
	if page, err := store.List(f.ctx, actor, base.ID, 0, 10); err != nil || len(page.Items) != 2 {
		t.Fatalf("archive: %+v %v", page, err)
	}
}

func TestEvalSetListRechecksLookahead(t *testing.T) {
	f, base, store, definition := setFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	if _, err := store.Create(f.ctx, actor, base.ID, "", eval.SetDefinition{Name: "visible", Cases: definition.Cases[1:]}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(f.ctx, actor, base.ID, "", definition); err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM document WHERE tenant_id=$1 FOR UPDATE`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := store.List(f.ctx, actor, base.ID, 0, 1); result <- err }()
	waitSetListBlock(t, f, tx, result)
	if _, err := tx.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, eval.ErrSetNotFound) {
		t.Fatalf("hidden lookahead disclosed next cursor: %v", err)
	}
}

func TestEvalSetListRevokedWhileWaiting(t *testing.T) {
	for _, cursor := range []int64{0, math.MaxInt64} {
		t.Run(map[bool]string{true: "empty", false: "populated"}[cursor != 0], func(t *testing.T) {
			f, base, store, definition := setFixture(t)
			if _, err := store.Create(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}, base.ID, "", definition); err != nil {
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
				_, err := store.List(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, base.ID, cursor, 10)
				result <- err
			}()
			waitSetListBlock(t, f, tx, result)
			if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, eval.ErrSetNotFound) {
				t.Fatalf("revoked list: %v", err)
			}
		})
	}
}

func waitSetListBlock(t *testing.T, f *fixture, tx pgx.Tx, result <-chan error) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := tx.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int32(f.admin.PgConn().PID())).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("list did not wait: %v", err)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-ticker.C:
		}
	}
}
