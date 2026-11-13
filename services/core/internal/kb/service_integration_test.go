package kb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/models"
)

type fixture struct {
	ctx                 context.Context
	admin               *pgx.Conn
	pool                *pgxpool.Pool
	service             *Service
	tenantA, tenantB    int64
	owner, other, third int64
	foreignUser         int64
	spaceA, spaceB      int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	appConfig, err := pgx.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(adminConfig.Database, "tuskrag_test_") || adminConfig.Database != appConfig.Database || appConfig.User != "tuskrag_app" ||
		adminConfig.Host != appConfig.Host || adminConfig.Port != appConfig.Port ||
		(adminConfig.Host != "127.0.0.1" && adminConfig.Host != "localhost" && adminConfig.Host != "::1") {
		t.Fatal("tests require the same loopback tuskrag_test_ database for both roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		if closeErr := admin.Close(context.Background()); closeErr != nil {
			t.Errorf("close admin connection: %v", closeErr)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	var adminSuper, appUnsafe bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&adminSuper); err != nil || !adminSuper {
		t.Fatalf("test admin must be a superuser: superuser=%t err=%v", adminSuper, err)
	}
	if err := pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&appUnsafe); err != nil || appUnsafe {
		t.Fatalf("test app must not bypass RLS: unsafe=%t err=%v", appUnsafe, err)
	}
	service, err := NewService(pool)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, admin: admin, pool: pool, service: service}
	prefix := "kb-test-" + hex.EncodeToString(nonce[:])
	if err := admin.QueryRow(ctx, `INSERT INTO tenant(name) VALUES ($1) RETURNING id`, prefix+"-a").Scan(&f.tenantA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := admin.Exec(cleanupCtx, `UPDATE document SET current_version_id = NULL WHERE tenant_id = ANY($1)`, []int64{f.tenantA, f.tenantB}); err != nil {
			t.Errorf("clear current versions: %v", err)
		}
		for _, table := range []string{"document_acl", "user_group_member", "user_group", "idempotency_record", "audit_log", "kb_membership", "job", "document_version", "document", "knowledge_base", "embedding_space", "model_provider", "app_user"} {
			if _, err := admin.Exec(cleanupCtx, "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", []int64{f.tenantA, f.tenantB}); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(cleanupCtx, `DELETE FROM tenant WHERE id = ANY($1)`, []int64{f.tenantA, f.tenantB}); err != nil {
			t.Errorf("clean tenants: %v", err)
		}
	})
	if err := admin.QueryRow(ctx, `INSERT INTO tenant(name) VALUES ($1) RETURNING id`, prefix+"-b").Scan(&f.tenantB); err != nil {
		t.Fatal(err)
	}
	for i, user := range []struct {
		tenant int64
		id     *int64
	}{{f.tenantA, &f.owner}, {f.tenantA, &f.other}, {f.tenantA, &f.third}, {f.tenantB, &f.foreignUser}} {
		if err := admin.QueryRow(ctx, `INSERT INTO app_user(tenant_id, display_name) VALUES ($1, $2) RETURNING id`, user.tenant, fmt.Sprintf("user-%d", i)).Scan(user.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, space := range []struct {
		tenant int64
		id     *int64
	}{{f.tenantA, &f.spaceA}, {f.tenantB, &f.spaceB}} {
		var provider int64
		if err := admin.QueryRow(ctx, `INSERT INTO model_provider(tenant_id, name, base_url, locality) VALUES ($1, 'test', 'http://localhost', 'internal') RETURNING id`, space.tenant).Scan(&provider); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `INSERT INTO embedding_space(tenant_id, provider_id, model, dims, normalized, distance) VALUES ($1, $2, 'test', 64, true, 'cosine') RETURNING id`, space.tenant, provider).Scan(space.id); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestCreationIdempotency(t *testing.T) {
	f := setup(t)
	actor := f.actor(f.owner)
	request := CreateRequest{Name: " concurrent ", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Deny, IdempotencyKey: "request-1"}
	type outcome struct {
		kb  KnowledgeBase
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			created, err := f.service.Create(f.ctx, actor, request)
			results <- outcome{created, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.kb.ID == 0 || first.kb.ID != second.kb.ID {
		t.Fatalf("concurrent retry: first=%+v second=%+v", first, second)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM knowledge_base WHERE tenant_id = $1 AND name = 'concurrent'`, f.tenantA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate creations: count=%d err=%v", count, err)
	}
	request.Name = "other payload"
	if _, err := f.service.Create(f.ctx, actor, request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload reused a key: %v", err)
	}
	request.Name = "foreign"
	request.EmbeddingSpaceID = f.spaceB
	foreign, err := f.service.Create(f.ctx, Actor{TenantID: f.tenantB, UserID: f.foreignUser}, request)
	if err != nil || foreign.ID == first.kb.ID {
		t.Fatalf("key was not tenant scoped: %+v err=%v", foreign, err)
	}
	request.Name, request.EmbeddingSpaceID = "concurrent", f.spaceA
	if err := f.service.SetMember(f.ctx, actor, first.kb.ID, f.other, Owner); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RemoveMember(f.ctx, actor, first.kb.ID, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Create(f.ctx, actor, request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay bypassed membership revocation: %v", err)
	}
	request.IdempotencyKey = "bad key"
	if _, err := f.service.Create(f.ctx, actor, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid idempotency key accepted: %v", err)
	}
}

func TestResolveEmbeddingScopeUsesCurrentTenantAndKnowledgeBasePolicy(t *testing.T) {
	f := setup(t)
	base, err := f.service.Create(f.ctx, f.actor(f.owner), CreateRequest{Name: "scope", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	binding := models.EmbeddingBinding{TenantID: f.tenantA, KnowledgeBaseID: base.ID, EmbeddingSpaceID: f.spaceA, ConfigRevision: base.ConfigRevision}
	scope, err := f.service.ResolveEmbeddingScope(f.ctx, binding)
	if err != nil || scope.TenantID != f.tenantA || scope.KnowledgeBaseID != base.ID ||
		scope.TenantPolicy != models.Deny || scope.KnowledgeBasePolicy != models.Deny {
		t.Fatalf("initial scope: %+v err=%v", scope, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE tenant SET egress_policy_max='allow' WHERE id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE tenant_id=$1 AND id=$2`, f.tenantA, base.ID); err != nil {
		t.Fatal(err)
	}
	scope, err = f.service.ResolveEmbeddingScope(f.ctx, binding)
	if err != nil || scope.TenantPolicy != models.Allow || scope.KnowledgeBasePolicy != models.Allow {
		t.Fatalf("policy changes were not observed: %+v err=%v", scope, err)
	}
	for name, change := range map[string]func(*models.EmbeddingBinding){
		"foreign tenant": func(b *models.EmbeddingBinding) { b.TenantID = f.tenantB },
		"foreign space":  func(b *models.EmbeddingBinding) { b.EmbeddingSpaceID = f.spaceB },
		"stale revision": func(b *models.EmbeddingBinding) { b.ConfigRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := binding
			change(&invalid)
			if scope, err := f.service.ResolveEmbeddingScope(f.ctx, invalid); !errors.Is(err, ErrNotFound) || scope.TenantID != 0 {
				t.Fatalf("unavailable binding resolved: %+v err=%v", scope, err)
			}
		})
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	if scope, err := f.service.ResolveEmbeddingScopeTx(f.ctx, tx, binding); !errors.Is(err, ErrInvalidRequest) || scope.TenantID != 0 {
		t.Fatalf("mismatched transaction tenant resolved: %+v err=%v", scope, err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET status='archived' WHERE tenant_id=$1 AND id=$2`, f.tenantA, base.ID); err != nil {
		t.Fatal(err)
	}
	if scope, err := f.service.ResolveEmbeddingScope(f.ctx, binding); !errors.Is(err, ErrNotFound) || scope.TenantID != 0 {
		t.Fatalf("archived knowledge base resolved: %+v err=%v", scope, err)
	}
}

func (f *fixture) actor(userID int64) Actor { return Actor{TenantID: f.tenantA, UserID: userID} }

func (f *fixture) create(t *testing.T) KnowledgeBase {
	t.Helper()
	result, err := f.service.Create(f.ctx, f.actor(f.owner), CreateRequest{Name: "knowledge", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreationEnforcesTenantAndEgress(t *testing.T) {
	f := setup(t)
	actor := f.actor(f.owner)
	if _, err := f.service.Create(f.ctx, actor, CreateRequest{Name: "blocked", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Allow}); !errors.Is(err, models.ErrEgressDenied) {
		t.Fatalf("tenant ceiling was bypassed: %v", err)
	}
	if _, err := f.service.Create(f.ctx, actor, CreateRequest{Name: "foreign", EmbeddingSpaceID: f.spaceB, EgressPolicy: models.Deny}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant embedding space accepted: %v", err)
	}
	created := f.create(t)
	if created.Role != Owner || created.Status != "active" || created.EgressPolicy != models.Deny {
		t.Fatalf("unexpected knowledge base: %+v", created)
	}
	if _, err := f.service.Create(f.ctx, actor, CreateRequest{Name: "knowledge", EmbeddingSpaceID: f.spaceA, EgressPolicy: models.Deny}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name accepted: %v", err)
	}
	var allowed, denied int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE result = 'allow'), count(*) FILTER (WHERE result = 'deny') FROM audit_log WHERE tenant_id = $1 AND action = 'kb.create'`, f.tenantA).Scan(&allowed, &denied); err != nil || allowed != 1 || denied != 1 {
		t.Fatalf("creation audit: allow=%d deny=%d err=%v", allowed, denied, err)
	}
}

func TestVisibilityMembershipAndAudit(t *testing.T) {
	f := setup(t)
	created := f.create(t)
	owner, viewer := f.actor(f.owner), f.actor(f.other)
	foreign := Actor{TenantID: f.tenantB, UserID: f.foreignUser}
	if _, err := f.service.Get(f.ctx, viewer, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonmember could read: %v", err)
	}
	if _, err := f.service.Get(f.ctx, foreign, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign tenant could read: %v", err)
	}
	if list, err := f.service.List(f.ctx, viewer); err != nil || len(list) != 0 {
		t.Fatalf("nonmember list revealed knowledge base: %v err=%v", list, err)
	}
	if err := f.service.SetMember(f.ctx, owner, created.ID, f.foreignUser, Viewer); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign member accepted: %v", err)
	}
	if err := f.service.SetMember(f.ctx, owner, created.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	visible, err := f.service.Get(f.ctx, viewer, created.ID)
	if err != nil || visible.ID != created.ID || visible.Role != Viewer {
		t.Fatalf("viewer cannot read: %+v err=%v", visible, err)
	}
	if err := f.service.Archive(f.ctx, viewer, created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer archived knowledge base: %v", err)
	}
	if err := f.service.SetMember(f.ctx, viewer, created.ID, f.third, Owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer changed members: %v", err)
	}
	if err := f.service.RemoveMember(f.ctx, owner, created.ID, f.other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Get(f.ctx, viewer, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revocation did not take effect: %v", err)
	}
	if err := f.service.Archive(f.ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := f.service.Get(f.ctx, owner, created.ID)
	if err != nil || archived.Status != "archived" {
		t.Fatalf("archive not visible to owner: %+v err=%v", archived, err)
	}
	var changes, denials int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE result = 'allow'), count(*) FILTER (WHERE result = 'deny') FROM audit_log WHERE tenant_id = $1 AND object_id = $2`, f.tenantA, created.ID).Scan(&changes, &denials); err != nil || changes < 4 || denials < 4 {
		t.Fatalf("action audit: allow=%d deny=%d err=%v", changes, denials, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.tenantA)); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, f.tenantB).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("cross-tenant audit entries visible: count=%d err=%v", leaked, err)
	}
	_, err = tx.Exec(f.ctx, `UPDATE audit_log SET result = 'allow'`)
	var permission *pgconn.PgError
	if !errors.As(err, &permission) || permission.Code != "42501" {
		t.Fatalf("application role updated audit log or failed for another reason: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.tenantA)); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(f.ctx, `DELETE FROM audit_log`)
	permission = nil
	if !errors.As(err, &permission) || permission.Code != "42501" {
		t.Fatalf("application role deleted audit log or failed for another reason: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.tenantA)); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(f.ctx, `INSERT INTO kb_membership (tenant_id, kb_id, user_id, role) VALUES ($1, $2, $3, 'viewer')`, f.tenantA, created.ID, f.foreignUser)
	var foreignKey *pgconn.PgError
	if !errors.As(err, &foreignKey) || foreignKey.Code != "23503" {
		t.Fatalf("cross-tenant member did not violate foreign key: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentOwnerChangesKeepAnOwner(t *testing.T) {
	f := setup(t)
	created := f.create(t)
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), created.ID, f.other, Owner); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, user := range []int64{f.owner, f.other} {
		go func(userID int64) {
			<-start
			results <- f.service.SetMember(f.ctx, f.actor(userID), created.ID, userID, Viewer)
		}(user)
	}
	close(start)
	allowed, rejected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			allowed++
		} else if errors.Is(err, ErrLastOwner) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent role change: %v", err)
		}
	}
	var owners int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM kb_membership WHERE tenant_id = $1 AND kb_id = $2 AND role = 'owner'`, f.tenantA, created.ID).Scan(&owners); err != nil || owners != 1 || allowed != 1 || rejected != 1 {
		t.Fatalf("owner invariant: owners=%d allow=%d reject=%d err=%v", owners, allowed, rejected, err)
	}
}

func TestAuditFailureRollsBackMembership(t *testing.T) {
	f := setup(t)
	created := f.create(t)
	name := fmt.Sprintf("tuskrag_test_deny_audit_%d", f.tenantA)
	if _, err := f.admin.Exec(f.ctx, "CREATE FUNCTION "+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name+" ON audit_log"); err != nil {
			t.Errorf("remove test audit trigger: %v", err)
		}
		if _, err := f.admin.Exec(context.Background(), "DROP FUNCTION "+name+"()"); err != nil {
			t.Errorf("remove test audit function: %v", err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d) EXECUTE FUNCTION %s()", name, f.tenantA, name)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), created.ID, f.other, Viewer); err == nil {
		t.Fatal("membership change succeeded without an audit record")
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM kb_membership WHERE tenant_id = $1 AND kb_id = $2 AND user_id = $3`, f.tenantA, created.ID, f.other).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited membership persisted: count=%d err=%v", count, err)
	}
	if _, err := f.service.Get(f.ctx, f.actor(f.other), created.ID); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("audit failure was hidden as a normal denial: %v", err)
	}
}
