package retrieval

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExactMatchACLAndTrustTierPostgres(t *testing.T) {
	adminURL, appURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL"), os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("isolated PostgreSQL test URLs are not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	driver, err := NewDriver(pool)
	if err != nil {
		t.Fatal(err)
	}

	var tenantID, user1, user2, providerID, spaceID, kbID, doc1, ver1, sec1, doc2, ver2, sec2 int64
	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ('exact-match-test-' || gen_random_uuid()) RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM chunk WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM section WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM document_acl WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM document_version WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM document WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM kb_membership WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM knowledge_base WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM embedding_space WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM model_provider WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM app_user WHERE tenant_id = $1`, tenantID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM tenant WHERE id = $1`, tenantID)
	})

	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'u1') RETURNING id`, tenantID).Scan(&user1); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'u2') RETURNING id`, tenantID).Scan(&user2); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality) VALUES ($1, 'stub', 'http://127.0.0.1:1', 'internal') RETURNING id`, tenantID).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, distance, normalized) VALUES ($1, $2, 'm', 64, 'cosine', true) RETURNING id`, tenantID, providerID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status, trust_tier) VALUES ($1, 'kb', $2, 'deny', 'active', 'trusted') RETURNING id`, tenantID, spaceID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{user1, user2} {
		if _, err := admin.Exec(ctx, `INSERT INTO kb_membership (tenant_id, kb_id, user_id, role) VALUES ($1, $2, $3, 'viewer')`, tenantID, kbID, uid); err != nil {
			t.Fatal(err)
		}
	}

	// doc1: inherit_kb (inherits 'trusted')
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, acl_mode) VALUES ($1, $2, 'Error Manual', 'text/markdown', 'inherit_kb') RETURNING id`, tenantID, kbID).Scan(&doc1); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, decode(repeat('01', 32), 'hex'), 10, 'k1', 'ready', $4) RETURNING id`, tenantID, kbID, doc1, user1).Scan(&ver1); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE id = $2`, ver1, doc1); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, 'Errors', ARRAY['Errors'], 'Code ERR-9001: connection timeout', 10) RETURNING id`, tenantID, kbID, doc1, ver1).Scan(&sec1); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, token_count, content, search_text, lexical_tokens, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', 10, 'Code ERR-9001: connection timeout', 'Code ERR-9001: connection timeout', to_tsvector('simple', 'Code ERR-9001'), true, false, ARRAY[$6], ARRAY['manual'], 'text/markdown', now())`, tenantID, kbID, doc1, ver1, sec1, fmt.Sprintf("kb:%d:all", kbID)); err != nil {
		t.Fatal(err)
	}

	// doc2: restricted to user1 only, trust_tier='untrusted'
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, acl_mode, trust_tier) VALUES ($1, $2, 'Secret Note', 'text/markdown', 'restricted', 'untrusted') RETURNING id`, tenantID, kbID).Scan(&doc2); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, decode(repeat('02', 32), 'hex'), 10, 'k2', 'ready', $4) RETURNING id`, tenantID, kbID, doc2, user1).Scan(&ver2); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE id = $2`, ver2, doc2); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO document_acl (tenant_id, kb_id, document_id, subject_type, subject_id) VALUES ($1, $2, $3, 'user', $4)`, tenantID, kbID, doc2, user1); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, 'Secret', ARRAY['Secret'], 'Code ERR-9001: secret override', 10) RETURNING id`, tenantID, kbID, doc2, ver2).Scan(&sec2); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, token_count, content, search_text, lexical_tokens, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', 10, 'Code ERR-9001: secret override', 'Code ERR-9001: secret override', to_tsvector('simple', 'Code ERR-9001'), true, false, ARRAY[$6], ARRAY['secret'], 'text/markdown', now())`, tenantID, kbID, doc2, ver2, sec2, fmt.Sprintf("u:%d", user1)); err != nil {
		t.Fatal(err)
	}

	// user1 sees both (trusted + untrusted)
	u1Cands, err := driver.ExactMatch(ctx, Actor{TenantID: tenantID, UserID: user1}, kbID, "ERR-9001", false, 10)
	if err != nil || len(u1Cands) != 2 {
		t.Fatalf("user1 ExactMatch = %d, err=%v", len(u1Cands), err)
	}
	if u1Cands[0].TrustTier != "trusted" || u1Cands[1].TrustTier != "untrusted" {
		t.Fatalf("unexpected trust tiers: %q, %q", u1Cands[0].TrustTier, u1Cands[1].TrustTier)
	}

	// user2 only sees doc1 (zero unauthorized evidence)
	u2Cands, err := driver.ExactMatch(ctx, Actor{TenantID: tenantID, UserID: user2}, kbID, `ERR-\d+`, true, 10)
	if err != nil || len(u2Cands) != 1 || u2Cands[0].DocumentID != doc1 {
		t.Fatalf("user2 ExactMatch = %+v, err=%v", u2Cands, err)
	}
}
