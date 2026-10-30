package ingest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/retrieval"
)

func TestLexicalAdvancesPastRejectedCandidates(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	var space int64
	if err := f.admin.QueryRow(f.ctx, `SELECT embedding_space_id FROM knowledge_base WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID).Scan(&space); err != nil {
		t.Fatal(err)
	}
	vector := testVector(1, 0)
	// Every rejected group exceeds Top-K and ties with the visible text on BM25.
	for i := 0; i < 180; i++ {
		id := insertEvaluationDocument(t, f, space, eval.Document{Key: fmt.Sprintf("hidden-%d", i), Title: "申请", Content: "申请"}, i < 60, vector)
		if i >= 60 && i < 120 {
			if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET disabled=true,is_current=false WHERE tenant_id=$1 AND document_id=$2`, f.tenant, id); err != nil {
				t.Fatal(err)
			}
		}
		if i >= 120 {
			if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET access_tags='{}' WHERE tenant_id=$1 AND document_id=$2`, f.tenant, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	visible := insertEvaluationDocument(t, f, space, eval.Document{Key: "visible", Title: "申请", Content: "申请"}, false, vector)
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	driver, err := retrieval.NewDriver(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	binding, err := driver.Binding(f.ctx, actor, f.kbID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{}, 20)
	if err != nil || len(rows) != 1 || rows[0].DocumentID != visible || rows[0].LexicalRank != 1 {
		t.Fatalf("lexical filtering must advance beyond rejected candidates: rows=%+v err=%v", rows, err)
	}
}
