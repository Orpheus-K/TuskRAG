package kb

import (
	"errors"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
)

func answerReportFixture(t *testing.T) (*fixture, KnowledgeBase, *eval.AnswerReportStore, eval.AnswerReport, int64) {
	t.Helper()
	f := setup(t)
	base := f.create(t)
	store, err := eval.NewAnswerReportStore(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	var doc int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES($1,$2,'answer-fixture','text/plain') RETURNING id`, f.tenantA, base.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	rate1 := 1.0
	rate0 := 0.0
	hit := true
	pass := true
	report := eval.AnswerReport{
		DatasetVersion: "r07-e6-zh-v1",
		DatasetSHA256:  strings.Repeat("ab", 32),
		ConfigSHA256:   strings.Repeat("cd", 32),
		ModelLabel:     "stub-chat",
		ModelKind:      "stub",
		PromptVersion:  "answer-v1",
		Assembly:       eval.AnswerAssemblyReport{ParentBacktrack: true, ContextBudgetTokens: 6000},
		Status:         "passed",
		Summary: eval.AnswerSummary{
			Cases:              2,
			AnswerableCases:    1,
			UnanswerableCases:  1,
			DeterministicCases: 1,
			Metrics: eval.AnswerMetrics{
				ValidCitationRate:     1.0,
				SourceIntegrityRate:   1.0,
				EvidenceHitRate:       &rate1,
				DeterministicPassRate: &rate1,
				RefusalCorrectRate:    &rate1,
				FalseRefusalRate:      &rate0,
				UnsupportedRate:       &rate0,
			},
		},
		UnauthorizedEvidence: 0,
		Rows: []eval.AnswerRow{
			{
				ID:                  "q1",
				Answerable:          true,
				Outcome:             "answered",
				ValidCitation:       true,
				SourceIntegrity:     true,
				EvidenceHit:         &hit,
				DeterministicPass:   &pass,
				EvidenceIDs:         []string{"E1"},
				EvidenceChunkCounts: []int{2},
				EvidenceDocuments:   []int64{doc},
				Degraded:            []string{},
				CitationCount:       1,
				InvalidCitations:    0,
				TimingsMS:           map[string]float64{"ttft": 12.0, "total": 40.0},
			},
			{
				ID:                  "q2",
				Answerable:          false,
				Outcome:             "refused",
				ValidCitation:       true,
				SourceIntegrity:     true,
				EvidenceIDs:         []string{},
				EvidenceChunkCounts: []int{},
				EvidenceDocuments:   []int64{},
				Degraded:            []string{},
				CitationCount:       0,
				InvalidCitations:    0,
				TimingsMS:           map[string]float64{"ttft": 8.0, "total": 20.0},
			},
		},
		LatencyMS: map[string][2]float64{
			"ttft":  {8.0, 12.0},
			"total": {20.0, 40.0},
		},
	}
	return f, base, store, report, doc
}

func TestAnswerEvalReportImportListGetCompareAndSecurity(t *testing.T) {
	f, base, store, baseline, doc := answerReportFixture(t)
	owner := retrieval.Actor{TenantID: f.tenantA, UserID: f.owner}
	editor := retrieval.Actor{TenantID: f.tenantA, UserID: f.other}
	outsider := retrieval.Actor{TenantID: f.tenantA, UserID: f.third}
	foreign := retrieval.Actor{TenantID: f.tenantB, UserID: f.foreignUser}
	if err := f.service.SetMember(f.ctx, f.actor(f.owner), base.ID, f.other, Editor); err != nil {
		t.Fatal(err)
	}

	invalid := baseline
	invalid.UnauthorizedEvidence = 1
	if _, err := store.Import(f.ctx, owner, base.ID, invalid); !errors.Is(err, eval.ErrReportInvalid) {
		t.Fatalf("unauthorized report accepted: %v", err)
	}
	if _, err := store.Import(f.ctx, editor, base.ID, baseline); !errors.Is(err, eval.ErrReportForbidden) {
		t.Fatalf("editor imported answer report: %v", err)
	}
	if _, err := store.Import(f.ctx, outsider, base.ID, baseline); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("outsider imported answer report: %v", err)
	}
	if _, err := store.Import(f.ctx, foreign, base.ID, baseline); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("foreign tenant imported answer report: %v", err)
	}

	storedBase, err := store.Import(f.ctx, owner, base.ID, baseline)
	if err != nil || storedBase.Source != "imported" {
		t.Fatalf("import baseline: %+v err=%v", storedBase, err)
	}
	storedRetry, err := store.Import(f.ctx, owner, base.ID, baseline)
	if err != nil || storedRetry.Digest != storedBase.Digest {
		t.Fatalf("idempotent import: %+v err=%v", storedRetry, err)
	}

	var auditCount int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='eval.answer_report.import' AND object_id=$2`, f.tenantA, base.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}

	regressed := baseline
	regressed.ConfigSHA256 = strings.Repeat("ef", 32)
	regressed.Rows = append([]eval.AnswerRow(nil), baseline.Rows...)
	regressed.Rows[0].ValidCitation = false
	regressed.Rows[0].InvalidCitations = 1
	regressed.Summary.Metrics.ValidCitationRate = 0.5
	storedCurrent, err := store.Import(f.ctx, owner, base.ID, regressed)
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.List(f.ctx, owner, base.ID, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list answer reports: %+v err=%v", page, err)
	}
	if _, err := store.List(f.ctx, editor, base.ID, "", 10); !errors.Is(err, eval.ErrReportForbidden) {
		t.Fatalf("editor listed answer reports: %v", err)
	}
	got, err := store.Get(f.ctx, owner, base.ID, storedBase.Digest)
	if err != nil || got.Digest != storedBase.Digest || len(got.Report.Rows) != 2 {
		t.Fatalf("get answer report: %+v err=%v", got, err)
	}

	cmp, err := store.Compare(f.ctx, owner, base.ID, storedBase.Digest, storedCurrent.Digest, 0, 20)
	if err != nil || cmp.Gate != "regressed" || cmp.Total != 2 || len(cmp.Rows) != 2 {
		t.Fatalf("compare answer reports: %+v err=%v", cmp, err)
	}

	// Archived KB allows read & compare, rejects import.
	if err := f.service.Archive(f.ctx, f.actor(f.owner), base.ID); err != nil {
		t.Fatal(err)
	}
	archivedReport := baseline
	archivedReport.ConfigSHA256 = strings.Repeat("12", 32)
	if _, err := store.Import(f.ctx, owner, base.ID, archivedReport); !errors.Is(err, eval.ErrReportArchived) {
		t.Fatalf("archived KB imported answer report: %v", err)
	}
	if _, err := store.Compare(f.ctx, owner, base.ID, storedBase.Digest, storedCurrent.Digest, 0, 20); err != nil {
		t.Fatalf("archived KB compare failed: %v", err)
	}

	// Soft-deleting the referenced document hides affected reports everywhere.
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=now() WHERE tenant_id=$1 AND id=$2`, f.tenantA, doc); err != nil {
		t.Fatal(err)
	}
	pageAfterDelete, err := store.List(f.ctx, owner, base.ID, "", 10)
	if err != nil || len(pageAfterDelete.Items) != 0 {
		t.Fatalf("deleted document report still visible: %+v err=%v", pageAfterDelete, err)
	}
	if _, err := store.Get(f.ctx, owner, base.ID, storedBase.Digest); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("deleted document report readable: %v", err)
	}
	if _, err := store.Compare(f.ctx, owner, base.ID, storedBase.Digest, storedCurrent.Digest, 0, 20); !errors.Is(err, eval.ErrReportNotFound) {
		t.Fatalf("deleted document report comparable: %v", err)
	}
}
