package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

func TestMCPRESTConsistencyPostgres(t *testing.T) {
	adminURL := os.Getenv("TUSKRAG_TEST_ADMIN_URL")
	appURL := os.Getenv("TUSKRAG_TEST_APP_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set TUSKRAG_TEST_ADMIN_URL and TUSKRAG_TEST_APP_URL to run PostgreSQL MCP/REST consistency test")
	}
	provisionURL := os.Getenv("TUSKRAG_TEST_MIGRATOR_URL")
	if provisionURL == "" {
		provisionURL = adminURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, provisionURL); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()
	admin, err := adminPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire admin conn: %v", err)
	}
	defer admin.Release()
	appPool, err := db.OpenWithLimit(ctx, appURL, 4)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()

	// Stub OpenAI-compatible embedding endpoint returning deterministic 64-dim unit vector.
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		toks := 1
		if len(req.Input) > 0 && len([]rune(req.Input[0])) > 0 {
			toks = len([]rune(req.Input[0]))
		}
		vec := make([]float64, 64)
		vec[0] = 1.0
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": vec}},
			"usage": map[string]any{"prompt_tokens": toks, "total_tokens": toks},
		})
	}))
	defer embedSrv.Close()

	lex, err := tokenizer.NewLexicalTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var tenantID, userID, providerID, spaceID, kbID int64
	var docAllowedID, verAllowedID, secAllowedID, chunkAllowedID int64
	var docRestrictedID, verRestrictedID, secRestrictedID, chunkRestrictedID int64

	if err := admin.QueryRow(ctx, `INSERT INTO tenant (name, egress_policy_max) VALUES ($1, 'internal_only') RETURNING id`, "mcp-v14-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT set_config('tuskrag.tenant_id', $1, false)`, fmt.Sprintf("%d", tenantID)); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'AgentUser') RETURNING id`, tenantID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality) VALUES ($1, 'stub-embed', $2, 'internal') RETURNING id`, tenantID, embedSrv.URL).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO embedding_space (tenant_id, provider_id, model, dims, distance, normalized) VALUES ($1, $2, 'stub-m', 64, 'cosine', true) RETURNING id`, tenantID, providerID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status) VALUES ($1, 'V14 KB', $2, 'internal_only', 'active') RETURNING id`, tenantID, spaceID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO kb_membership (tenant_id, kb_id, user_id, role) VALUES ($1, $2, $3, 'viewer')`, tenantID, kbID, userID); err != nil {
		t.Fatal(err)
	}
	if err := indexing.Provision(ctx, provisionURL, tenantID, spaceID); err != nil {
		t.Fatalf("provision indexes: %v", err)
	}

	vecLiteral := "[1"
	for range 63 {
		vecLiteral += ",0"
	}
	vecLiteral += "]"
	kbTag := fmt.Sprintf("kb:%d:all", kbID)

	// Allowed document
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, tags, acl_mode) VALUES ($1, $2, '公开差旅制度', 'text/markdown', ARRAY['制度'], 'inherit_kb') RETURNING id`, tenantID, kbID).Scan(&docAllowedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, sha256('a1'::bytea), 10, 'v14-a1-'||$5, 'ready', $4) RETURNING id`, tenantID, kbID, docAllowedID, userID, suffix).Scan(&verAllowedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE tenant_id = $2 AND id = $3`, verAllowedID, tenantID, docAllowedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, '住宿', ARRAY['住宿'], '差旅住宿标准 500 元', 10) RETURNING id`, tenantID, kbID, docAllowedID, verAllowedID).Scan(&secAllowedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, lexical_tokens, token_count, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', '差旅住宿标准 500 元', '住宿 差旅住宿标准 500 元', to_tsvector('simple', $6), 10, true, false, ARRAY[$7], ARRAY['制度'], 'text/markdown', now()) RETURNING id`,
		tenantID, kbID, docAllowedID, verAllowedID, secAllowedID, lex.TSVectorText("住宿 差旅住宿标准 500 元"), kbTag).Scan(&chunkAllowedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, is_current, access_tags, embedding) VALUES ($1, $2, $3, $4, true, ARRAY[$5], $6::text::halfvec)`,
		tenantID, kbID, chunkAllowedID, spaceID, kbTag, vecLiteral); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO ingest_plan (tenant_id, kb_id, document_id, version_id, embedding_space_id, config_revision, plan_sha256, section_count, chunk_count) VALUES ($1, $2, $3, $4, $5, 1, sha256('p1'::bytea), 1, 1)`,
		tenantID, kbID, docAllowedID, verAllowedID, spaceID); err != nil {
		t.Fatal(err)
	}

	// Restricted document
	if err := admin.QueryRow(ctx, `INSERT INTO document (tenant_id, kb_id, title, mime, tags, acl_mode) VALUES ($1, $2, '机密高管差旅', 'text/markdown', ARRAY['制度'], 'restricted') RETURNING id`, tenantID, kbID).Scan(&docRestrictedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO document_version (tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by) VALUES ($1, $2, $3, sha256('r1'::bytea), 10, 'v14-r1-'||$5, 'ready', $4) RETURNING id`, tenantID, kbID, docRestrictedID, userID, suffix).Scan(&verRestrictedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET current_version_id = $1 WHERE tenant_id = $2 AND id = $3`, verRestrictedID, tenantID, docRestrictedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO section (tenant_id, kb_id, document_id, version_id, ordinal, level, heading, heading_path, body_md, token_count) VALUES ($1, $2, $3, $4, 1, 1, '机密', ARRAY['机密'], '机密差旅标准', 8) RETURNING id`, tenantID, kbID, docRestrictedID, verRestrictedID).Scan(&secRestrictedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO chunk (tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind, content, search_text, lexical_tokens, token_count, is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at) VALUES ($1, $2, $3, $4, $5, 1, 'text', '机密差旅标准', '机密 机密差旅标准', to_tsvector('simple', $6), 8, true, false, ARRAY['u:999999'], ARRAY['制度'], 'text/markdown', now()) RETURNING id`,
		tenantID, kbID, docRestrictedID, verRestrictedID, secRestrictedID, lex.TSVectorText("机密 机密差旅标准")).Scan(&chunkRestrictedID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, is_current, access_tags, embedding) VALUES ($1, $2, $3, $4, true, ARRAY['u:999999'], $5::text::halfvec)`,
		tenantID, kbID, chunkRestrictedID, spaceID, vecLiteral); err != nil {
		t.Fatal(err)
	}

	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	idSvc, err := identity.NewService(appPool, hasher, identity.SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	createdKey, err := idSvc.CreateAPIKey(ctx, identity.Principal{TenantID: tenantID, UserID: userID}, identity.CreateAPIKeyRequest{
		Name: "v14-key", KBIDs: []int64{kbID}, Actions: []string{"search", "read", "mcp"}, RateLimitRPM: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	kbSvc, err := kb.NewService(appPool)
	if err != nil {
		t.Fatal(err)
	}
	dummyCounter := func(_ context.Context, s string) (int, error) { return len([]rune(s)), nil }
	searchSvc, _, err := retrieval.NewServiceWithMode(ctx, appPool, nil, dummyCounter, "auto")
	if err != nil {
		t.Fatal(err)
	}

	api := New(Dependencies{
		Identity:  idSvc,
		APIKeys:   idSvc,
		Knowledge: kbSvc,
		Reader:    kbSvc,
		Search:    searchSvc,
	})

	// 1. REST POST /api/v1/search
	restBody, _ := json.Marshal(map[string]any{"query": "差旅住宿", "kb_ids": []int64{kbID}, "top_k": 5})
	restReq := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewReader(restBody))
	restReq.Header.Set("Authorization", "Bearer "+createdKey.Plaintext)
	restReq.Header.Set("Content-Type", "application/json")
	restRec := httptest.NewRecorder()
	api.Handler.ServeHTTP(restRec, restReq)
	if restRec.Code != http.StatusOK {
		t.Fatalf("REST search failed: %d %s", restRec.Code, restRec.Body.String())
	}
	var restResp struct {
		Evidence []struct {
			ID string `json:"id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(restRec.Body.Bytes(), &restResp); err != nil {
		t.Fatal(err)
	}

	// 2. MCP POST /mcp tools/call search
	mcpBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "search", "arguments": map[string]any{"query": "差旅住宿", "kb_ids": []int64{kbID}, "top_k": 5}},
	})
	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcpBody))
	mcpReq.Header.Set("Authorization", "Bearer "+createdKey.Plaintext)
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpRec := httptest.NewRecorder()
	api.Handler.ServeHTTP(mcpRec, mcpReq)
	if mcpRec.Code != http.StatusOK {
		t.Fatalf("MCP search failed: %d %s", mcpRec.Code, mcpRec.Body.String())
	}
	var mcpResp struct {
		Result struct {
			IsError           bool `json:"isError"`
			StructuredContent struct {
				Evidence []struct {
					ChunkID int64 `json:"chunk_id"`
				} `json:"evidence"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpRec.Body.Bytes(), &mcpResp); err != nil {
		t.Fatal(err)
	}
	if mcpResp.Result.IsError {
		t.Fatalf("MCP search returned error: %s", mcpRec.Body.String())
	}

	// V14: REST and MCP must return identical evidence IDs and 0 unauthorized chunks.
	var restIDs, mcpIDs []string
	for _, ev := range restResp.Evidence {
		restIDs = append(restIDs, ev.ID)
	}
	for _, ev := range mcpResp.Result.StructuredContent.Evidence {
		mcpIDs = append(mcpIDs, fmt.Sprintf("c_%d", ev.ChunkID))
	}
	if !slices.Equal(restIDs, mcpIDs) || len(mcpIDs) != 1 || mcpIDs[0] != fmt.Sprintf("c_%d", chunkAllowedID) {
		t.Fatalf("V14 mismatch: rest=%v mcp=%v expected=[c_%d]", restIDs, mcpIDs, chunkAllowedID)
	}

	// T11: MCP get_toc -> read_section -> get_neighbors on allowed doc, and deny on restricted doc.
	for _, tool := range []struct {
		name string
		args map[string]any
		wantErr bool
	}{
		{"get_toc", map[string]any{"document_id": docAllowedID}, false},
		{"read_section", map[string]any{"section_id": secAllowedID}, false},
		{"get_neighbors", map[string]any{"chunk_id": chunkAllowedID, "before": 1, "after": 1}, false},
		{"get_toc", map[string]any{"document_id": docRestrictedID}, true},
	} {
		b, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": tool.name, "arguments": tool.args},
		})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+createdKey.Plaintext)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.Handler.ServeHTTP(rec, req)
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Result.IsError != tool.wantErr {
			t.Fatalf("tool %s wantErr=%v got=%v body=%s", tool.name, tool.wantErr, out.Result.IsError, rec.Body.String())
		}
	}
}
