package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func reportFixture(t *testing.T) (*fixture, KnowledgeBase, *eval.ReportStore, eval.RunReport) {
	t.Helper()
	f := setup(t)
	base := f.create(t)
	store, err := eval.NewReportStore(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	var doc, version, section, chunk int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES($1,$2,'fixture','text/plain') RETURNING id`, f.tenantA, base.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by) VALUES($1,$2,$3,decode(repeat('ab',32),'hex'),1,'fixture','ready',$4) RETURNING id`, f.tenantA, base.ID, doc, f.owner).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE id=$2`, version, doc); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO section(tenant_id,kb_id,document_id,version_id,ordinal,level,heading,heading_path,body_md,token_count) VALUES($1,$2,$3,$4,1,0,'','{}','fixture',1) RETURNING id`, f.tenantA, base.ID, doc, version).Scan(&section); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at) VALUES($1,$2,$3,$4,$5,1,'text','fixture','fixture',1,true,ARRAY['kb:'||$2::bigint::text||':all'],'text/plain',now()) RETURNING id`, f.tenantA, base.ID, doc, version, section).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"chunk", "section"} {
			if _, err := f.admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id=$1", f.tenantA); err != nil {
				t.Error(err)
			}
		}
	})
	evidence := eval.Evidence{ChunkID: chunk, DocumentID: doc, SectionID: section}
	report := eval.RunReport{DatasetVersion: "fixture", DatasetSHA256: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("b", 64), ModelLabel: "test", ModelKind: "stub", Mode: "rrf", Status: "passed", Summary: eval.Summary{Cases: 1, UnanswerableCases: 1}, Rows: []eval.RunRow{{ID: "q1", Fused: []eval.Evidence{evidence}, Ranked: []eval.Evidence{evidence}, TimingsMS: map[string]float64{"total": 1}}}, LatencyMS: map[string][2]float64{"total": {1, 1}}}
	return f, base, store, report
}

func TestEvalReportImportReadIdempotencyAndRLS(t *testing.T) {
	f, base, store, report := reportFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	type outcome struct {
		value eval.StoredReport
		err   error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() { value, err := store.Import(f.ctx, actor, base.ID, report); results <- outcome{value, err} }()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.value.Digest != b.value.Digest || a.value.Source != "imported" {
		t.Fatalf("imports: %+v %+v", a, b)
	}
	got, err := store.Get(f.ctx, actor, base.ID, a.value.Digest)
	if err != nil || got.Report.Rows[0].Fused[0] != report.Rows[0].Fused[0] {
		t.Fatalf("read: %+v %v", got, err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='eval.report.import'`, f.tenantA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenantB)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM eval_report`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross tenant count=%d err=%v", count, err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE eval_report SET source='imported'`); err == nil {
		t.Fatal("application can mutate reports")
	}
}

func TestEvalReportPermissionsAndEvidenceRecheck(t *testing.T) {
	f, base, store, report := reportFixture(t)
	owner := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	saved, err := store.Import(f.ctx, owner, base.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{Viewer, Editor, Admin} {
		if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, role); err != nil {
			t.Fatal(err)
		}
		actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.other}
		_, readErr := store.Get(f.ctx, actor, base.ID, saved.Digest)
		_, writeErr := store.Import(f.ctx, actor, base.ID, report)
		if role == Admin {
			if readErr != nil || writeErr != nil {
				t.Fatalf("admin: %v %v", readErr, writeErr)
			}
		} else if !errors.Is(readErr, eval.ErrReportForbidden) || !errors.Is(writeErr, eval.ErrReportForbidden) {
			t.Fatalf("role %s: %v %v", role, readErr, writeErr)
		}
	}
	for _, actor := range []retrieval.Actor{{TenantID: f.tenantA, UserID: f.third}, {TenantID: f.tenantB, UserID: f.foreignUser}} {
		if _, err := store.Get(f.ctx, actor, base.ID, saved.Digest); !errors.Is(err, eval.ErrReportNotFound) {
			t.Fatalf("invisible: %v", err)
		}
		if _, err := store.Import(f.ctx, actor, base.ID, report); !errors.Is(err, eval.ErrReportNotFound) {
			t.Fatalf("invisible import: %v", err)
		}
	}
	other, err := f.service.Create(f.ctx, f.actor(f.owner), CreateRequest{Name: "other-report-kb", EmbeddingSpaceID: f.spaceA, EgressPolicy: base.EgressPolicy, IdempotencyKey: "other-report-kb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(f.ctx, owner, other.ID, report); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("foreign KB evidence: %v", err)
	}
	for name, sql := range map[string]string{
		"deleted":     "UPDATE document SET deleted_at=now() WHERE tenant_id=$1",
		"restricted":  "UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1",
		"old version": "UPDATE document SET current_version_id=NULL WHERE tenant_id=$1",
		"disabled":    "UPDATE chunk SET is_current=false,disabled=true WHERE tenant_id=$1",
		"tags":        "UPDATE chunk SET access_tags=ARRAY['private'] WHERE tenant_id=$1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.admin.Exec(f.ctx, sql, f.tenantA); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(f.ctx, owner, base.ID, saved.Digest); !errors.Is(err, eval.ErrReportNotFound) {
				t.Fatalf("invisible evidence read: %v", err)
			}
			if _, err := store.Import(f.ctx, owner, base.ID, report); !errors.Is(err, eval.ErrReportNotFound) {
				t.Fatalf("invisible evidence import: %v", err)
			}
			page, err := store.List(f.ctx, owner, base.ID, "", 10)
			if err != nil || len(page.Items) != 0 || page.NextCursor != nil {
				t.Fatalf("invisible report listing: %+v %v", page, err)
			}
			if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=NULL,acl_mode='inherit_kb',current_version_id=(SELECT id FROM document_version WHERE document_id=document.id LIMIT 1) WHERE tenant_id=$1`, f.tenantA); err != nil {
				t.Fatal(err)
			}
			if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=true,disabled=false,access_tags=ARRAY['kb:'||kb_id::text||':all'] WHERE tenant_id=$1`, f.tenantA); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := f.service.Archive(f.ctx, f.actor(f.owner), base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(f.ctx, owner, base.ID, report); !errors.Is(err, eval.ErrReportArchived) {
		t.Fatalf("archived import: %v", err)
	}
	if _, err := store.Get(f.ctx, owner, base.ID, saved.Digest); err != nil {
		t.Fatalf("archived read: %v", err)
	}
}

func TestEvalReportAuditRollbackAndWrongBinding(t *testing.T) {
	f, base, store, report := reportFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	report.Rows[0].Fused[0].SectionID++
	if _, err := store.Import(f.ctx, actor, base.ID, report); !errors.Is(err, eval.ErrReportInvalid) {
		t.Fatalf("inconsistent location: %v", err)
	}
	report.Rows[0].Ranked[0].SectionID++
	if _, err := store.Import(f.ctx, actor, base.ID, report); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("forged location: %v", err)
	}
	report.Rows[0].Fused[0].SectionID--
	report.Rows[0].Ranked[0].SectionID--
	name := fmt.Sprintf("eval_audit_failure_%d", f.tenantA)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='eval.report.import' AND NEW.tenant_id=%d THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.tenantA, name, name)
	if _, err := f.admin.Exec(f.ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON audit_log; DROP FUNCTION %s()", name, name)); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.Import(f.ctx, actor, base.ID, report); err == nil {
		t.Fatal("audit failure succeeded")
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM eval_report WHERE tenant_id=$1`, f.tenantA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("report survived rollback: %d %v", count, err)
	}
}

func TestEvalReportRevokedWhileReadWaits(t *testing.T) {
	testEvalReportRevokedWhileReadWaits(t, "get")
}

func TestEvalReportListRevokedWhileReadWaits(t *testing.T) {
	testEvalReportRevokedWhileReadWaits(t, "list")
}

func TestEvalReportCompareRevokedWhileReadWaits(t *testing.T) {
	testEvalReportRevokedWhileReadWaits(t, "compare")
}

func testEvalReportRevokedWhileReadWaits(t *testing.T, operation string) {
	f, base, store, report := reportFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	saved, err := store.Import(f.ctx, actor, base.ID, report)
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
		switch operation {
		case "compare":
			_, err = store.Compare(f.ctx, actor, base.ID, saved.Digest, saved.Digest, 0, 20)
		case "list":
			_, err = store.List(f.ctx, actor, base.ID, "", 10)
		default:
			_, err = store.Get(f.ctx, actor, base.ID, saved.Digest)
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
			t.Fatalf("read did not wait: %v", err)
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
	if err := <-result; !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("revoked read: %v", err)
	}
}

func TestEvalReportComparisonScopesAndPagination(t *testing.T) {
	f, base, store, report := reportFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	ones := &eval.Metrics{RecallAt20: 1, HitAt5: 1, MRRAt10: 1, NDCGAt10: 1}
	report.Summary = eval.Summary{Cases: 2, AnswerableCases: 1, UnanswerableCases: 1, Metrics: ones}
	report.Rows[0].Metrics = ones
	report.Rows = append(report.Rows, eval.RunRow{ID: "q2", TimingsMS: map[string]float64{"total": 1}})
	before, err := store.Import(f.ctx, actor, base.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	current := report
	current.Rows = append([]eval.RunRow(nil), report.Rows...)
	current.Rows[0].Fused = nil
	current.Rows[0].Ranked = nil
	current.Rows[0].Metrics = &eval.Metrics{}
	current.Summary.Metrics = &eval.Metrics{}
	after, err := store.Import(f.ctx, actor, base.ID, current)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Compare(f.ctx, actor, base.ID, before.Digest, after.Digest, 0, 1)
	if err != nil || result.Gate != "regressed" || result.Delta.MRRAt10 != -1 || len(result.Rows) != 1 || result.TotalRows != 2 || result.NextOffset == nil || *result.NextOffset != 1 || !result.Rows[0].EvidenceChanged {
		t.Fatalf("comparison page: %+v %v", result, err)
	}
	result, err = store.Compare(f.ctx, actor, base.ID, before.Digest, after.Digest, 1, 1)
	if err != nil || result.Rows[0].ID != "q2" || result.Rows[0].Delta != nil || result.NextOffset != nil {
		t.Fatalf("last page: %+v %v", result, err)
	}
	if _, err := store.Compare(f.ctx, actor, base.ID, before.Digest, after.Digest, 3, 1); !errors.Is(err, eval.ErrReportInvalid) {
		t.Fatalf("past end: %v", err)
	}
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compare(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, base.ID, before.Digest, after.Digest, 0, 20); !errors.Is(err, eval.ErrReportForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := store.Compare(f.ctx, retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}, base.ID, before.Digest, after.Digest, 0, 20); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("foreign tenant: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=now() WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	// The selected row has no evidence, but the baseline's first row is hidden.
	if _, err := store.Compare(f.ctx, actor, base.ID, before.Digest, after.Digest, 1, 1); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("hidden baseline outside page: %v", err)
	}
	if _, err := store.Compare(f.ctx, actor, base.ID, after.Digest, before.Digest, 1, 1); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("hidden current outside page: %v", err)
	}
	if _, err := store.Compare(f.ctx, actor, base.ID, after.Digest, after.Digest, 0, 20); err != nil {
		t.Fatalf("independent visible report: %v", err)
	}
}

func TestEvalReportVisiblePagination(t *testing.T) {
	f, base, store, report := reportFixture(t)
	actor := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	page, err := store.List(f.ctx, actor, base.ID, "", 1)
	if err != nil || len(page.Items) != 0 || page.Items == nil || page.NextCursor != nil {
		t.Fatalf("empty page: %+v %v", page, err)
	}
	for _, label := range []string{"a", "b", "c"} {
		report.ModelLabel = label
		if label == "c" {
			report.Rows[0].Fused = nil
			report.Rows[0].Ranked = nil
		}
		if _, err := store.Import(f.ctx, actor, base.ID, report); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		page, err = store.List(f.ctx, actor, base.ID, cursor, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page %d: %+v %v", i, page, err)
		}
		item := page.Items[0]
		if item.Digest <= cursor || seen[item.Digest] || item.Summary.Cases != 1 || item.Source != "imported" {
			t.Fatalf("invalid page item: %+v", item)
		}
		seen[item.Digest] = true
		if i < 2 {
			if page.NextCursor == nil || *page.NextCursor != item.Digest {
				t.Fatal("missing forward cursor")
			}
			cursor = *page.NextCursor
		} else if page.NextCursor != nil {
			t.Fatal("extra page advertised")
		}
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=now() WHERE tenant_id=$1`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	page, err = store.List(f.ctx, actor, base.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ModelLabel != "c" || page.NextCursor != nil {
		t.Fatalf("hidden evidence leaked in page or cursor: %+v %v", page, err)
	}
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, Viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(f.ctx, retrieval.Actor{TenantID: f.tenantA, UserID: f.other}, base.ID, "", 10); !errors.Is(err, eval.ErrReportForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := store.List(f.ctx, retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}, base.ID, "", 10); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("foreign tenant: %v", err)
	}
	for _, input := range []struct {
		cursor string
		limit  int32
	}{{"bad", 1}, {"", 0}, {"", 11}} {
		if _, err := store.List(f.ctx, actor, base.ID, input.cursor, input.limit); !errors.Is(err, eval.ErrReportInvalid) {
			t.Fatalf("invalid pagination: %v", err)
		}
	}
}
