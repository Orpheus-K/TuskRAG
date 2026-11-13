package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuditLogsAndTenantEgressPolicyPostgres(t *testing.T) {
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)

	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	defer pool.Close()

	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	svc, err := NewService(pool, hasher, SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	tName := "audit-test-" + hex.EncodeToString(nonce[:])
	adminPrincipal, err := svc.Bootstrap(ctx, BootstrapRequest{
		TenantName:  tName,
		DisplayName: "Admin User",
		Login:       "admin",
		Password:    []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	var regularUserID int64
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'Regular') RETURNING id`, adminPrincipal.TenantID).Scan(&regularUserID); err != nil {
		t.Fatalf("insert regular user: %v", err)
	}
	regularPrincipal := Principal{TenantID: adminPrincipal.TenantID, UserID: regularUserID}

	t.Cleanup(func() {
		cctx := context.Background()
		for _, tbl := range []string{"login_session", "local_account", "audit_log", "app_user"} {
			_, _ = admin.Exec(cctx, "DELETE FROM "+tbl+" WHERE tenant_id = $1", adminPrincipal.TenantID)
		}
		_, _ = admin.Exec(cctx, `DELETE FROM tenant WHERE id = $1`, adminPrincipal.TenantID)
	})

	// Non-admin is forbidden from reading/exporting audit logs or changing tenant egress policy
	if _, err := svc.ListAuditLogs(ctx, regularPrincipal, AuditLogFilter{}); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("non-admin ListAuditLogs err = %v, want ErrAdminRequired", err)
	}
	if _, _, err := svc.ExportAuditLogs(ctx, regularPrincipal, AuditLogFilter{}, "jsonl"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("non-admin ExportAuditLogs err = %v, want ErrAdminRequired", err)
	}
	if _, err := svc.UpdateTenantEgressPolicy(ctx, regularPrincipal, "allow"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("non-admin UpdateTenantEgressPolicy err = %v, want ErrAdminRequired", err)
	}

	// Admin updates tenant egress policy
	policy, err := svc.GetTenantEgressPolicy(ctx, adminPrincipal)
	if err != nil || policy != "deny" {
		t.Fatalf("initial GetTenantEgressPolicy = %q, err=%v", policy, err)
	}
	updated, err := svc.UpdateTenantEgressPolicy(ctx, adminPrincipal, "internal_only")
	if err != nil || updated != "internal_only" {
		t.Fatalf("UpdateTenantEgressPolicy = %q, err=%v", updated, err)
	}

	// Admin lists audit logs filtered by action
	logs, err := svc.ListAuditLogs(ctx, adminPrincipal, AuditLogFilter{Action: "tenant.egress_policy.update"})
	if err != nil || len(logs) != 1 || logs[0].ActorName != "Admin User" {
		t.Fatalf("ListAuditLogs = %+v, err=%v", logs, err)
	}

	// Admin exports JSONL & CSV, triggering self-audit audit.export
	jsonl, ctJSONL, err := svc.ExportAuditLogs(ctx, adminPrincipal, AuditLogFilter{}, "jsonl")
	if err != nil || !strings.Contains(ctJSONL, "ndjson") || !strings.Contains(string(jsonl), "tenant.egress_policy.update") {
		t.Fatalf("ExportAuditLogs jsonl err=%v ct=%s body=%s", err, ctJSONL, string(jsonl))
	}
	csvData, ctCSV, err := svc.ExportAuditLogs(ctx, adminPrincipal, AuditLogFilter{Action: "audit.export"}, "csv")
	if err != nil || !strings.Contains(ctCSV, "text/csv") || !strings.Contains(string(csvData), "audit.export") {
		t.Fatalf("ExportAuditLogs csv err=%v ct=%s body=%s", err, ctCSV, string(csvData))
	}
}
