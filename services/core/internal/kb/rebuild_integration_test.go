package kb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
)

func rebuildFixture(t *testing.T) (*fixture, *RebuildService, KnowledgeBase, int64) {
	t.Helper()
	f := setup(t)
	base := f.create(t)
	var target int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) SELECT tenant_id,provider_id,'replacement',dims,normalized,distance FROM embedding_space WHERE id=$1 RETURNING id`, f.spaceA).Scan(&target); err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewStore(f.pool, jobs.RetryPolicy{Initial: time.Millisecond, Maximum: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRebuildService(f.pool, queue)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DELETE FROM embedding_rebuild WHERE tenant_id=$1`, f.tenantA); err != nil {
			t.Error(err)
		}
	})
	return f, service, base, target
}
func TestRebuildAtomicReservationAndReplay(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
	actor := f.actor(f.owner)
	input := RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision}
	type outcome struct {
		value Rebuild
		err   error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() { value, err := s.Create(f.ctx, actor, base.ID, "same", input); done <- outcome{value, err} }()
	}
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.value.JobID <= 0 || a.value.JobID != b.value.JobID || a.value.Status != "queued" || a.value.SourceSpaceID != base.EmbeddingSpaceID {
		t.Fatalf("concurrent %+v %+v", a, b)
	}
	var active, pending int64
	var revision int32
	if err := f.admin.QueryRow(f.ctx, `SELECT embedding_space_id,pending_space_id,config_revision FROM knowledge_base WHERE id=$1`, base.ID).Scan(&active, &pending, &revision); err != nil || active != f.spaceA || pending != target || revision != base.ConfigRevision {
		t.Fatalf("binding %d %d %d %v", active, pending, revision, err)
	}
	if _, err := s.Create(f.ctx, actor, base.ID, "other", input); !errors.Is(err, ErrRebuildInProgress) {
		t.Fatalf("duplicate pending %v", err)
	}
	changed := input
	changed.Revision++
	if _, err := s.Create(f.ctx, actor, base.ID, "same", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed key %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET config_revision=config_revision+1 WHERE id=$1`, base.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Create(f.ctx, actor, base.ID, "same", input); err != nil || result.JobID != a.value.JobID {
		t.Fatalf("stable retry %+v %v", result, err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("job count %d %v", count, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM embedding_rebuild`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS %d %v", count, err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE embedding_rebuild SET source_revision=2`); err == nil {
		t.Fatal("app can mutate immutable rebuild")
	}
}
func TestRebuildAccessAndEgress(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
	actor := f.actor(f.owner)
	input := RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision}
	wrong := input
	wrong.TargetSpaceID = f.spaceB
	if _, err := s.Create(f.ctx, actor, base.ID, "foreign-space", wrong); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign target %v", err)
	}
	if err := f.service.SetMember(f.ctx, actor, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(f.ctx, f.actor(f.other), base.ID, "viewer", input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET locality='external' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(f.ctx, actor, base.ID, "egress", input); !errors.Is(err, models.ErrEgressDenied) {
		t.Fatalf("egress %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET locality='internal' WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Create(f.ctx, actor, base.ID, "egress", input)
	if err != nil {
		t.Fatalf("denial poisoned key %v", err)
	}
	if _, err := s.Get(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, base.ID, saved.JobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read %v", err)
	}
	if err := f.service.Archive(f.ctx, actor, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(f.ctx, actor, base.ID, saved.JobID); err != nil {
		t.Fatalf("archive read %v", err)
	}
	if _, err := s.Create(f.ctx, actor, base.ID, "egress", input); !errors.Is(err, ErrArchived) {
		t.Fatalf("archive retry %v", err)
	}
}
func TestRebuildAuditRollback(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
	name := fmt.Sprintf("rebuild_audit_%d", f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='kb.rebuild.create' AND NEW.result='allow' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)
	if _, err := f.admin.Exec(f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Create(f.ctx, f.actor(f.owner), base.ID, "rollback", RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision}); err == nil {
		t.Fatal("audit failure accepted")
	}
	var clean bool
	if err := f.admin.QueryRow(f.ctx, `SELECT pending_space_id IS NULL AND NOT EXISTS(SELECT 1 FROM embedding_rebuild WHERE tenant_id=$1) AND NOT EXISTS(SELECT 1 FROM job WHERE tenant_id=$1) AND NOT EXISTS(SELECT 1 FROM idempotency_record WHERE tenant_id=$1 AND operation LIKE 'kb.rebuild/%') FROM knowledge_base WHERE id=$2`, f.tenantA, base.ID).Scan(&clean); err != nil || !clean {
		t.Fatalf("partial commit %t %v", clean, err)
	}
}
func TestRebuildRevokedWhileWaiting(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
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
	done := make(chan error, 1)
	go func() {
		_, err := s.Create(f.ctx, f.actor(f.other), base.ID, "revoked", RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision})
		done <- err
	}()
	waitSetListBlock(t, f, tx, done)
	if _, err := tx.Exec(f.ctx, `DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, f.tenantA, base.ID, f.other); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked %v", err)
	}
}

func TestRebuildLifecycle(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
	actor := f.actor(f.owner)
	create := func(key string) Rebuild {
		t.Helper()
		r, err := s.Create(f.ctx, actor, base.ID, key, RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	check := func(pending bool) {
		t.Helper()
		var ok bool
		err := f.admin.QueryRow(f.ctx, `SELECT (pending_space_id IS NOT NULL)=$2 AND embedding_space_id=$3 AND config_revision=$4 FROM knowledge_base WHERE id=$1`, base.ID, pending, base.EmbeddingSpaceID, base.ConfigRevision).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("binding %t %v", ok, err)
		}
	}
	claim := func() jobs.ClaimedJob {
		t.Helper()
		j, err := s.queue.ClaimKindsWithRecovery(f.ctx, f.tenantA, "rebuild-test", time.Minute, []jobs.Kind{jobs.Reembed}, s.Expire)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	r := create("cancel")
	if err := f.service.SetMember(f.ctx, actor, base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(f.ctx, f.actor(f.other), base.ID, r.JobID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer cancellation %v", err)
	}
	check(true)
	j := claim()
	if err := s.Cancel(f.ctx, actor, base.ID, r.JobID); err != nil {
		t.Fatal(err)
	}
	check(false)
	next := create("retry")
	if _, err := s.Fail(f.ctx, j.Lease, jobs.Internal); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late failure %v", err)
	}
	check(true)
	j = claim()
	if j.Lease.JobID != next.JobID {
		t.Fatal("wrong job")
	}
	if status, err := s.Fail(f.ctx, j.Lease, jobs.ModelUnavailable); err != nil || status != jobs.Queued {
		t.Fatalf("retry %s %v", status, err)
	}
	check(true)
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET run_after=clock_timestamp()-interval '1 second' WHERE id=$1`, next.JobID); err != nil {
		t.Fatal(err)
	}
	j = claim()
	if status, err := s.Fail(f.ctx, j.Lease, jobs.ModelInvalid); err != nil || status != jobs.Failed {
		t.Fatalf("terminal %s %v", status, err)
	}
	check(false)
	r = create("expired")
	j = claim()
	if err := s.queue.WithLease(f.ctx, j.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		return s.Expire(ctx, tx, jobs.ExpiredJob{ID: r.JobID, TenantID: f.tenantA, Kind: jobs.Reembed})
	}); err == nil {
		t.Fatal("checkpoint allowed expiration")
	}
	check(true)
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET attempts=max_attempts,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.queue.ClaimKindsWithRecovery(f.ctx, f.tenantA, "recovery", time.Minute, []jobs.Kind{jobs.Reembed}, s.Expire); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal(err)
	}
	check(false)
	got, err := s.Get(f.ctx, actor, base.ID, r.JobID)
	if err != nil || got.Status != "dead" || got.FailureCode == nil || *got.FailureCode != "lease_expired" {
		t.Fatalf("expired %+v %v", got, err)
	}
}

func TestRebuildCancellationRollback(t *testing.T) {
	f, s, base, target := rebuildFixture(t)
	r, err := s.Create(f.ctx, f.actor(f.owner), base.ID, "rollback-cancel", RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("rebuild_cancel_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='kb.rebuild.cancel' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if err := s.Cancel(f.ctx, f.actor(f.owner), base.ID, r.JobID); err == nil {
		t.Fatal("audit failure accepted")
	}
	got, err := s.Get(f.ctx, f.actor(f.owner), base.ID, r.JobID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("partial cancellation %+v %v", got, err)
	}
	var pending int64
	if err := f.admin.QueryRow(f.ctx, `SELECT pending_space_id FROM knowledge_base WHERE id=$1`, base.ID).Scan(&pending); err != nil || pending != target {
		t.Fatalf("partial release %d %v", pending, err)
	}
}

func TestRebuildTargetScope(t *testing.T) {
	for _, change := range []string{"model", "revision", "pending", "archive", "role", "cancel", "tenant_policy"} {
		t.Run(change, func(t *testing.T) {
			f, s, base, target := rebuildFixture(t)
			r, err := s.Create(f.ctx, f.actor(f.owner), base.ID, "scope", RebuildRequest{TargetSpaceID: target, Revision: base.ConfigRevision})
			if err != nil {
				t.Fatal(err)
			}
			job, err := s.queue.ClaimKindsWithRecovery(f.ctx, f.tenantA, "scope-worker", time.Minute, []jobs.Kind{jobs.Reembed}, s.Expire)
			if err != nil {
				t.Fatal(err)
			}
			binding, resolver, err := s.Target(f.ctx, job.Lease)
			if err != nil || binding.EmbeddingSpaceID != target {
				t.Fatalf("target %+v %v", binding, err)
			}
			if _, err := f.service.ResolveEmbeddingScope(f.ctx, binding); !errors.Is(err, ErrNotFound) {
				t.Fatalf("active scope accepted pending %v", err)
			}
			wrong := binding
			wrong.EmbeddingSpaceID = base.EmbeddingSpaceID
			if _, err := resolver.ResolveEmbeddingScope(f.ctx, wrong); !errors.Is(err, models.ErrScopeMismatch) {
				t.Fatalf("wrong binding %v", err)
			}
			switch change {
			case "model":
				_, err = f.admin.Exec(f.ctx, `UPDATE embedding_space SET model='changed' WHERE id=$1`, target)
			case "revision":
				_, err = f.admin.Exec(f.ctx, `UPDATE knowledge_base SET config_revision=config_revision+1 WHERE id=$1`, base.ID)
			case "pending":
				_, err = f.admin.Exec(f.ctx, `UPDATE knowledge_base SET pending_space_id=NULL WHERE id=$1`, base.ID)
			case "archive":
				err = f.service.Archive(f.ctx, f.actor(f.owner), base.ID)
			case "role":
				_, err = f.admin.Exec(f.ctx, `UPDATE kb_membership SET role='viewer' WHERE kb_id=$1 AND user_id=$2`, base.ID, f.owner)
			case "cancel":
				err = s.Cancel(f.ctx, f.actor(f.owner), base.ID, r.JobID)
			case "tenant_policy":
				_, err = f.admin.Exec(f.ctx, `UPDATE tenant SET egress_policy_max='deny' WHERE id=$1`, f.tenantA)
			}
			if err != nil {
				t.Fatal(err)
			}
			scope, err := resolver.ResolveEmbeddingScope(f.ctx, binding)
			if change == "tenant_policy" {
				if err != nil || scope.TenantPolicy != models.Deny {
					t.Fatalf("stale policy %+v %v", scope, err)
				}
			} else if err == nil || scope.TenantID != 0 {
				t.Fatalf("changed authorization accepted %+v %v", scope, err)
			}
		})
	}
}
