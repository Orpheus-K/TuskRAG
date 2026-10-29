package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type identityFixture struct {
	ctx     context.Context
	admin   *pgx.Conn
	pool    *pgxpool.Pool
	service *Service
	tenants []int64
	prefix  string
}

func setupIdentity(t *testing.T) *identityFixture {
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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
	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(pool, hasher, SessionConfig{IdleTimeout: 8 * time.Hour, AbsoluteTimeout: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	f := &identityFixture{ctx: ctx, admin: admin, pool: pool, service: service, prefix: "identity-test-" + hex.EncodeToString(nonce[:])}
	t.Cleanup(func() {
		for _, table := range []string{"login_session", "local_account", "audit_log", "app_user"} {
			if _, err := admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", f.tenants); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(context.Background(), `DELETE FROM tenant WHERE id = ANY($1)`, f.tenants); err != nil {
			t.Errorf("clean tenants: %v", err)
		}
	})
	return f
}

func (f *identityFixture) bootstrap(t *testing.T, suffix string) Principal {
	t.Helper()
	principal, err := f.service.Bootstrap(f.ctx, BootstrapRequest{
		TenantName: f.prefix + suffix, DisplayName: "Administrator", Login: "admin", Password: []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.tenants = append(f.tenants, principal.TenantID)
	return principal
}

func TestLocalLoginSessionAndLogout(t *testing.T) {
	f := setupIdentity(t)
	principal := f.bootstrap(t, "-a")
	other := f.bootstrap(t, "-b")
	if _, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("wrong password")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong password accepted: %v", err)
	}
	if _, err := f.service.Login(f.ctx, principal.TenantID, "missing", []byte("wrong password")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown login accepted: %v", err)
	}
	session, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("correct horse battery staple"))
	if err != nil || session.Principal != principal || session.Token == "" || session.CSRF == "" {
		t.Fatalf("login failed: principal=%+v err=%v", session.Principal, err)
	}
	authenticated, err := f.service.Authenticate(f.ctx, session.Token)
	if err != nil || authenticated.Principal != principal || authenticated.Token != "" || !ValidCSRF(session.Token, session.CSRF) || ValidCSRF(session.Token, "wrong") {
		t.Fatalf("session or CSRF validation failed: principal=%+v err=%v", authenticated.Principal, err)
	}
	parts := strings.Split(session.Token, ".")
	foreignToken := fmt.Sprintf("%d.%s", other.TenantID, parts[1])
	if _, err := f.service.Authenticate(f.ctx, foreignToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session crossed tenant boundary: %v", err)
	}
	_, secret, err := parseToken(session.Token)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(secret)
	var storedHash []byte
	var storedPassword string
	if err := f.admin.QueryRow(f.ctx, `SELECT token_hash FROM login_session WHERE tenant_id = $1`, principal.TenantID).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, expected[:]) || bytes.Equal(storedHash, secret) {
		t.Fatalf("session secret stored incorrectly: err=%v", err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT password_hash FROM local_account WHERE tenant_id = $1`, principal.TenantID).Scan(&storedPassword); err != nil || strings.Contains(storedPassword, "correct horse") {
		t.Fatalf("password stored incorrectly: err=%v", err)
	}
	if err := f.service.Logout(f.ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Authenticate(f.ctx, session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session still works: %v", err)
	}
	var allow, deny int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE result = 'allow'), count(*) FILTER (WHERE result = 'deny') FROM audit_log WHERE tenant_id = $1 AND object_type = 'identity'`, principal.TenantID).Scan(&allow, &deny); err != nil || allow != 3 || deny != 2 {
		t.Fatalf("identity audit: allow=%d deny=%d err=%v", allow, deny, err)
	}
}

func TestLoginLockoutAndImmediateDisable(t *testing.T) {
	f := setupIdentity(t)
	principal := f.bootstrap(t, "-lock")
	for range 5 {
		if _, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("wrong password")); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("invalid login result: %v", err)
		}
	}
	if _, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("correct horse battery staple")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("locked account logged in: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE local_account SET locked_until = clock_timestamp() - interval '1 second' WHERE tenant_id = $1`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	session, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE local_account SET disabled = true WHERE tenant_id = $1`, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Authenticate(f.ctx, session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled account's session remained valid: %v", err)
	}
}

func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	f := setupIdentity(t)
	principal := f.bootstrap(t, "-expiry")
	for _, column := range []string{"last_seen_at", "absolute_expires_at"} {
		session, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("correct horse battery staple"))
		if err != nil {
			t.Fatal(err)
		}
		_, secret, err := parseToken(session.Token)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(secret)
		if _, err := f.admin.Exec(f.ctx, "UPDATE login_session SET "+column+" = clock_timestamp() - interval '8 days' WHERE tenant_id = $1 AND token_hash = $2", principal.TenantID, digest[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Authenticate(f.ctx, session.Token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("expired %s session accepted: %v", column, err)
		}
	}
}

func TestLoginAuditFailureDoesNotCreateSession(t *testing.T) {
	f := setupIdentity(t)
	principal := f.bootstrap(t, "-audit")
	name := fmt.Sprintf("tuskrag_test_deny_audit_%d", principal.TenantID)
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
	if _, err := f.admin.Exec(f.ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d) EXECUTE FUNCTION %s()", name, principal.TenantID, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Login(f.ctx, principal.TenantID, "admin", []byte("correct horse battery staple")); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("audit failure was hidden: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM login_session WHERE tenant_id = $1`, principal.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited session persisted: count=%d err=%v", count, err)
	}
}
