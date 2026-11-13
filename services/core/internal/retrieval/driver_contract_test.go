package retrieval

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/tokenizer"
)

func TestLexicalDriverContractPostgres(t *testing.T) {
	adminURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL")
	appURL := os.Getenv("TUSKRAG_TEST_APP_URL")
	provisionURL := os.Getenv("TUSKRAG_TEST_MIGRATOR_URL")
	if provisionURL == "" {
		provisionURL = adminURL
	}
	if adminURL == "" || appURL == "" {
		t.Skip("set TUSKRAG_TEST_ADMIN_URL and TUSKRAG_TEST_APP_URL to run PostgreSQL lexical contract tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, adminURL); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer admin.Close()
	appPool, err := db.OpenWithLimit(ctx, appURL, 4)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()

	hasPGSearch, err := indexing.DetectPGSearch(ctx, admin)
	if err != nil {
		t.Fatalf("detect pg_search: %v", err)
	}

	// Test NewServiceWithMode startup detection (T19).
	dummyCounter := func(_ context.Context, s string) (int, error) { return len([]rune(s)), nil }
	_, status, err := NewServiceWithMode(ctx, appPool, nil, dummyCounter, "compat")
	if err != nil {
		t.Fatalf("NewServiceWithMode(compat) failed: %v", err)
	}
	if status.Driver != "compat" || !slices.Contains(status.Degraded, "using_tsvector_gse") {
		t.Fatalf("unexpected compat driver status: %+v", status)
	}

	_, autoStatus, err := NewServiceWithMode(ctx, appPool, nil, dummyCounter, "auto")
	if err != nil {
		t.Fatalf("NewServiceWithMode(auto) failed: %v", err)
	}
	if hasPGSearch && autoStatus.Driver != "pg_search" {
		t.Fatalf("expected pg_search in auto mode when installed, got %+v", autoStatus)
	}
	if !hasPGSearch && autoStatus.Driver != "compat" {
		t.Fatalf("expected compat in auto mode when pg_search absent, got %+v", autoStatus)
	}

	t.Run("compat_tsvector_gse", func(t *testing.T) {
		runLexicalDriverContract(t, ctx, admin, provisionURL, appPool, true)
	})

	t.Run("pg_search_bm25", func(t *testing.T) {
		if !hasPGSearch {
			t.Skip("pg_search extension is not installed in test PostgreSQL")
		}
		runLexicalDriverContract(t, ctx, admin, provisionURL, appPool, false)
	})
}

func runLexicalDriverContract(t *testing.T, ctx context.Context, admin *pgxpool.Pool, provisionURL string, appPool *pgxpool.Pool, useCompat bool) {
	t.Helper()
	lex, err := tokenizer.NewLexicalTokenizer()
	if err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%d_%t", time.Now().UnixNano(), useCompat)
	var tenantID, userID, providerID, spaceID, kbID, docAllowedID, verAllowedID, secAllowedID, chunkAllowedID int64
	var docRestrictedID, verRestrictedID, secRestrictedID, chunkRestrictedID int64

	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ($1) RETURNING id`, "lex-contract-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT set_config('tuskrag.tenant_id', $1, false)`, fmt.Sprintf("%d", tenantID)); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'Reader') RETURNING id`, tenantID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality) VALUES ($1, 'stub-prov', 'http://127.0.0.1:9999', 'internal') RETURNING id`, tenantID).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, distance, normalized) VALUES ($1, $2, 'stub-embed', 64, 'cosine', true) RETURNING id`, tenantID, providerID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status) VALUES ($1, 'Contract KB', $2, 'deny', 'active') RETURNING id`, tenantID, spaceID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO kb_membership (tenant_id, kb_id, user_id, role) VALUES ($1, $2, $3, 'viewer')`, tenantID, kbID, userID); err != nil {
		t.Fatal(err)
	}

	// Provision vector index (and BM25 if pg_search is active; GIN index is migrated).
	if err := indexing.Provision(ctx, provisionURL, tenantID, spaceID); err != nil {
		t.Fatalf("provision indexes: %v", err)
	}

	vec := make([]float32, 64)
	vec[0] = 1.0
	encodedVec, err := encodeVector(vec, 64)
	if err != nil {
		t.Fatal(err)
	}

	kbTag := fmt.Sprintf("kb:%d:all", kbID)
	// Allowed document
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, tags, acl_mode) VALUES ($1, $2, '差旅规范', 'text/markdown', ARRAY['制度'], 'inherit_kb') RETURNING id`, tenantID, kbID).Scan(&docAllowedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, sha256('doc1'::bytea), 10, 'k1-'||$5, 'ready', $4) RETURNING id`, tenantID, kbID, docAllowedID, userID, suffix).Scan(&verAllowedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE tenant_id = $2 AND id = $3`, verAllowedID, tenantID, docAllowedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, '住宿', ARRAY['住宿'], '差旅住宿标准 TR-101 报销', 12) RETURNING id`, tenantID, kbID, docAllowedID, verAllowedID).Scan(&secAllowedID); err != nil {
		t.Fatal(err)
	}
	allowedText := "住宿\n差旅住宿标准 TR-101 报销"
	if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, lexical_tokens, token_count, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', $6, $6, to_tsvector('simple', $7), 12, true, false, ARRAY[$8], ARRAY['制度'], 'text/markdown', now()) RETURNING id`,
		tenantID, kbID, docAllowedID, verAllowedID, secAllowedID, allowedText, lex.TSVectorText(allowedText), kbTag).Scan(&chunkAllowedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, is_current, access_tags, embedding) VALUES ($1, $2, $3, $4, true, ARRAY[$6], $5::text::halfvec)`,
		tenantID, kbID, chunkAllowedID, spaceID, encodedVec, kbTag); err != nil {
		t.Fatal(err)
	}

	// Restricted document (must never be returned)
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, tags, acl_mode) VALUES ($1, $2, '机密文档', 'text/markdown', ARRAY['制度'], 'restricted') RETURNING id`, tenantID, kbID).Scan(&docRestrictedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, sha256('doc2'::bytea), 10, 'k2-'||$5, 'ready', $4) RETURNING id`, tenantID, kbID, docRestrictedID, userID, suffix).Scan(&verRestrictedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE tenant_id = $2 AND id = $3`, verRestrictedID, tenantID, docRestrictedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, '机密', ARRAY['机密'], '机密报销 TR-101', 8) RETURNING id`, tenantID, kbID, docRestrictedID, verRestrictedID).Scan(&secRestrictedID); err != nil {
		t.Fatal(err)
	}
	restrictedText := "机密\n机密报销 TR-101 秘密"
	if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, lexical_tokens, token_count, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', $6, $6, to_tsvector('simple', $7), 8, true, false, ARRAY['u:999999'], ARRAY['制度'], 'text/markdown', now()) RETURNING id`,
		tenantID, kbID, docRestrictedID, verRestrictedID, secRestrictedID, restrictedText, lex.TSVectorText(restrictedText)).Scan(&chunkRestrictedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, is_current, access_tags, embedding) VALUES ($1, $2, $3, $4, true, ARRAY['u:999999'], $5::text::halfvec)`,
		tenantID, kbID, chunkRestrictedID, spaceID, encodedVec); err != nil {
		t.Fatal(err)
	}

	driver, err := NewDriver(appPool)
	if err != nil {
		t.Fatal(err)
	}
	if useCompat {
		driver.compatibility = &CompatibilityTokenizer{LexicalTokenizer: lex}
	}

	actor := Actor{TenantID: tenantID, UserID: userID}
	binding := Binding{SpaceID: spaceID, Revision: 1, Dimensions: 64, Distance: "cosine", Normalized: true}

	candidates, err := driver.Recall(ctx, actor, kbID, binding, "差旅住宿 TR-101", vec, Filters{DocumentTags: []string{"制度"}}, 10)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChunkID != chunkAllowedID {
		t.Fatalf("expected only chunk %d, got %+v", chunkAllowedID, candidates)
	}
	if candidates[0].LexicalRank <= 0 || candidates[0].LexicalScore <= 0 {
		t.Fatalf("expected positive lexical rank and score, got %+v", candidates[0])
	}

	// Non-matching tag filter must return empty
	filtered, err := driver.Recall(ctx, actor, kbID, binding, "差旅住宿 TR-101", vec, Filters{DocumentTags: []string{"不存在"}}, 10)
	if err != nil {
		t.Fatalf("filtered Recall failed: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("expected 0 candidates for unmatched filter, got %d", len(filtered))
	}
}
