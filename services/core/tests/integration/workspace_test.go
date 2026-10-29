package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/httpapi"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/storage"
)

func TestWorkspaceAuthenticationAndTenantIsolation(t *testing.T) {
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
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: 8 * time.Hour, AbsoluteTimeout: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(pool)
	if err != nil {
		t.Fatal(err)
	}
	var masterKey [32]byte
	if _, err := rand.Read(masterKey[:]); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewVault(masterKey[:])
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "http-test-" + hex.EncodeToString(nonce[:])
	var tenants []int64
	t.Cleanup(func() {
		for _, table := range []string{"idempotency_record", "login_session", "local_account", "audit_log", "kb_membership", "job", "document_version", "document", "knowledge_base", "embedding_space", "model_provider", "app_user"} {
			if _, err := admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", tenants); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(context.Background(), `DELETE FROM tenant WHERE id = ANY($1)`, tenants); err != nil {
			t.Errorf("clean tenants: %v", err)
		}
	})
	bootstrap := func(suffix string) identity.Principal {
		principal, err := identities.Bootstrap(ctx, identity.BootstrapRequest{
			TenantName: prefix + suffix, DisplayName: "Administrator", Login: "admin", Password: []byte("correct horse battery staple"),
		})
		if err != nil {
			t.Fatal(err)
		}
		tenants = append(tenants, principal.TenantID)
		return principal
	}
	owner, outsider := bootstrap("-a"), bootstrap("-b")
	files, err := storage.NewLocal(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	queue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, files, queue)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(httpapi.Dependencies{Check: func(context.Context) error { return nil }, Identity: identities, Knowledge: knowledge, Documents: documents, Jobs: documents, Reader: knowledge, Models: catalog, InsecureLocalCookies: true}).Handler)
	t.Cleanup(server.Close)
	newClient := func() *http.Client {
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Jar: jar, Timeout: 10 * time.Second}
	}
	ownerClient, outsiderClient := newClient(), newClient()
	preflight, err := http.NewRequestWithContext(ctx, http.MethodOptions, server.URL+"/api/v1/auth/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "https://attacker.invalid")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "content-type,x-tuskrag-login")
	preflightResponse, err := ownerClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	if err := preflightResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if preflightResponse.Header.Get("Access-Control-Allow-Origin") != "" || len(preflightResponse.Cookies()) != 0 {
		t.Fatal("cross-origin login preflight was allowed")
	}
	request := func(client *http.Client, method, path string, body any, csrf string, loginMarker bool, idempotency ...string) (*http.Response, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if loginMarker {
			req.Header.Set("X-TuskRAG-Login", "1")
		}
		if len(idempotency) > 0 {
			req.Header.Set("Idempotency-Key", idempotency[0])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read response: read=%v close=%v", readErr, closeErr)
		}
		return resp, data
	}
	if resp, _ := request(ownerClient, http.MethodGet, "/api/v1/kbs", nil, "", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list status: %d", resp.StatusCode)
	}
	loginBody := func(tenantID int64) map[string]any {
		return map[string]any{"tenant_id": tenantID, "login": "admin", "password": "correct horse battery staple"}
	}
	if resp, _ := request(ownerClient, http.MethodPost, "/api/v1/auth/login", loginBody(owner.TenantID), "", false); resp.StatusCode < 400 {
		t.Fatalf("login without anti-CSRF header succeeded: %d", resp.StatusCode)
	}
	login := func(client *http.Client, tenantID int64) httpapi.IdentityBody {
		t.Helper()
		resp, data := request(client, http.MethodPost, "/api/v1/auth/login", loginBody(tenantID), "", true)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("login status=%d body=%s", resp.StatusCode, data)
		}
		cookies := resp.Cookies()
		if len(cookies) != 1 || cookies[0].Name != "tuskrag_dev_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
			t.Fatalf("invalid session cookie: %v", cookies)
		}
		var result httpapi.IdentityBody
		if err := json.Unmarshal(data, &result); err != nil || result.TenantID != tenantID || result.CSRF == "" {
			t.Fatalf("invalid login response: %s err=%v", data, err)
		}
		return result
	}
	ownerIdentity := login(ownerClient, owner.TenantID)
	outsiderIdentity := login(outsiderClient, outsider.TenantID)
	if resp, data := request(ownerClient, http.MethodGet, "/api/v1/me", nil, "", false); resp.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(ownerIdentity.CSRF)) {
		t.Fatalf("session lookup status=%d body=%s", resp.StatusCode, data)
	}
	modelBody := map[string]any{"provider_name": "private-provider", "base_url": "http://127.0.0.1:9000", "locality": "internal", "api_key": "test-model-secret", "model": "test-embedding", "dimensions": 64, "normalized": true, "distance": "cosine"}
	resp, data := request(ownerClient, http.MethodPost, "/api/v1/admin/embedding-spaces", modelBody, ownerIdentity.CSRF, false)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("model creation status=%d body=%s", resp.StatusCode, data)
	}
	if bytes.Contains(data, []byte("test-model-secret")) {
		t.Fatal("model creation returned the credential")
	}
	var space httpapi.EmbeddingSpaceBody
	if err := json.Unmarshal(data, &space); err != nil || space.ID <= 0 || !space.HasCredential {
		t.Fatalf("invalid model creation response: %s err=%v", data, err)
	}
	resp, data = request(ownerClient, http.MethodGet, "/api/v1/admin/embedding-spaces", nil, "", false)
	if resp.StatusCode != http.StatusOK || bytes.Contains(data, []byte("test-model-secret")) {
		t.Fatalf("model list leaked credential: status=%d body=%s", resp.StatusCode, data)
	}
	resp, data = request(outsiderClient, http.MethodGet, "/api/v1/admin/embedding-spaces", nil, "", false)
	if resp.StatusCode != http.StatusOK || bytes.Contains(data, []byte("private-provider")) {
		t.Fatalf("foreign model list leaked: status=%d body=%s", resp.StatusCode, data)
	}
	createBody := map[string]any{"name": "private-knowledge", "embedding_space_id": space.ID, "egress_policy": "deny"}
	if _, err := admin.Exec(ctx, `UPDATE login_session SET last_seen_at = clock_timestamp() - interval '7 hours' WHERE tenant_id = $1`, owner.TenantID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := request(ownerClient, http.MethodPost, "/api/v1/kbs", createBody, "", false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write without CSRF status: %d", resp.StatusCode)
	}
	if resp, _ := request(ownerClient, http.MethodPost, "/api/v1/kbs", createBody, outsiderIdentity.CSRF, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write with another session's CSRF token status: %d", resp.StatusCode)
	}
	var idleUnchanged bool
	if err := admin.QueryRow(ctx, `SELECT last_seen_at < clock_timestamp() - interval '6 hours' FROM login_session WHERE tenant_id = $1`, owner.TenantID).Scan(&idleUnchanged); err != nil || !idleUnchanged {
		t.Fatalf("rejected CSRF request extended the session: unchanged=%t err=%v", idleUnchanged, err)
	}
	resp, data = request(ownerClient, http.MethodPost, "/api/v1/kbs", createBody, ownerIdentity.CSRF, false, "http-create-key")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, data)
	}
	var created httpapi.KnowledgeBaseBody
	if err := json.Unmarshal(data, &created); err != nil || created.ID <= 0 || created.Role != "owner" {
		t.Fatalf("invalid create response: %s err=%v", data, err)
	}
	resp, data = request(ownerClient, http.MethodPost, "/api/v1/kbs", createBody, ownerIdentity.CSRF, false, "http-create-key")
	var replay httpapi.KnowledgeBaseBody
	if err := json.Unmarshal(data, &replay); err != nil || resp.StatusCode != http.StatusOK || replay.ID != created.ID {
		t.Fatalf("invalid create replay: status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	conflictingBody := map[string]any{"name": "different", "embedding_space_id": space.ID, "egress_policy": "deny"}
	resp, data = request(ownerClient, http.MethodPost, "/api/v1/kbs", conflictingBody, ownerIdentity.CSRF, false, "http-create-key")
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(data, []byte("idempotency_conflict")) {
		t.Fatalf("idempotency conflict status=%d body=%s", resp.StatusCode, data)
	}
	path := fmt.Sprintf("/api/v1/kbs/%d", created.ID)
	resp, data = request(outsiderClient, http.MethodGet, path, nil, "", false)
	if resp.StatusCode != http.StatusNotFound || bytes.Contains(data, []byte("private-knowledge")) {
		t.Fatalf("foreign knowledge base leaked: status=%d body=%s", resp.StatusCode, data)
	}
	var denied struct {
		Error httpapi.ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(data, &denied); err != nil || denied.Error.Code != "not_found" || denied.Error.TraceID != resp.Header.Get("X-Trace-ID") || denied.Error.TraceID == "" {
		t.Fatalf("invalid denial response: status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	resp, data = request(outsiderClient, http.MethodGet, "/api/v1/kbs", nil, "", false)
	if resp.StatusCode != http.StatusOK || bytes.Contains(data, []byte("private-knowledge")) {
		t.Fatalf("foreign listing leaked: status=%d body=%s", resp.StatusCode, data)
	}
	var listing struct {
		Items []httpapi.KnowledgeBaseBody `json:"items"`
	}
	if err := json.Unmarshal(data, &listing); err != nil || len(listing.Items) != 0 {
		t.Fatalf("foreign listing contains hidden objects: %s err=%v", data, err)
	}
	var uploaded []kb.UploadedDocument
	for i := range 3 {
		doc, err := documents.Upload(ctx, kb.Actor{TenantID: owner.TenantID, UserID: owner.UserID}, kb.UploadRequest{
			KBID: created.ID, Title: fmt.Sprintf("private-%d.csv", i), MIME: "text/csv", Content: strings.NewReader(fmt.Sprintf("column\n%d\n", i)),
		})
		if err != nil {
			t.Fatal(err)
		}
		uploaded = append(uploaded, doc)
	}
	if _, err := admin.Exec(ctx, `UPDATE document SET acl_mode='restricted' WHERE id=$1`, uploaded[2].DocumentID); err != nil {
		t.Fatal(err)
	}
	documentsPath := path + "/documents"
	resp, data = request(outsiderClient, http.MethodGet, documentsPath, nil, "", false)
	if resp.StatusCode != http.StatusNotFound || bytes.Contains(data, []byte("private-")) {
		t.Fatalf("foreign document list leaked: status=%d body=%s", resp.StatusCode, data)
	}
	resp, data = request(ownerClient, http.MethodGet, documentsPath+"?limit=1", nil, "", false)
	var first httpapi.DocumentListOutput
	if err := json.Unmarshal(data, &first.Body); err != nil || resp.StatusCode != http.StatusOK || len(first.Body.Items) != 1 ||
		first.Body.Items[0].ID != uploaded[0].DocumentID || first.Body.Items[0].LatestStatus != "queued" || first.Body.NextCursor == nil {
		t.Fatalf("document page: status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	resp, data = request(ownerClient, http.MethodGet, fmt.Sprintf("%s?limit=1&cursor=%d", documentsPath, *first.Body.NextCursor), nil, "", false)
	var second httpapi.DocumentListOutput
	if err := json.Unmarshal(data, &second.Body); err != nil || resp.StatusCode != http.StatusOK || len(second.Body.Items) != 1 ||
		second.Body.Items[0].ID != uploaded[1].DocumentID || second.Body.NextCursor != nil || bytes.Contains(data, []byte("private-2.csv")) {
		t.Fatalf("last document page: status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	if resp, _ := request(ownerClient, http.MethodGet, documentsPath+"?limit=101", nil, "", false); resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("invalid document page limit status: %d", resp.StatusCode)
	}
	upload := func(client *http.Client, csrf, key, filename, content, query string, extra bool) (*http.Response, []byte) {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, content); err != nil {
			t.Fatal(err)
		}
		if extra {
			if err := form.WriteField("unexpected", "value"); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+documentsPath+query, &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", key)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read upload response: read=%v close=%v", readErr, closeErr)
		}
		return response, data
	}
	if resp, _ := upload(ownerClient, "", "", "new.csv", "name\nvalue\n", "", false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("upload without CSRF status: %d", resp.StatusCode)
	}
	if resp, data := upload(outsiderClient, outsiderIdentity.CSRF, "", "secret.csv", "name\nvalue\n", "", false); resp.StatusCode != http.StatusNotFound || bytes.Contains(data, []byte("private-knowledge")) {
		t.Fatalf("foreign upload status=%d body=%s", resp.StatusCode, data)
	}
	if resp, data := upload(ownerClient, ownerIdentity.CSRF, "", "bad.pptx", "content", "", false); resp.StatusCode != http.StatusBadRequest || !bytes.Contains(data, []byte("unsupported_format")) {
		t.Fatalf("unsupported upload status=%d body=%s", resp.StatusCode, data)
	}
	if resp, _ := upload(ownerClient, ownerIdentity.CSRF, "", "new.csv", "name\nvalue\n", "", true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("extra multipart field status: %d", resp.StatusCode)
	}
	resp, data = upload(ownerClient, ownerIdentity.CSRF, "upload-key", "new.csv", "name\nvalue\n", "", false)
	var uploadResult httpapi.UploadedDocumentBody
	if err := json.Unmarshal(data, &uploadResult); err != nil || resp.StatusCode != http.StatusOK || uploadResult.DocumentID <= 0 || uploadResult.VersionID <= 0 || uploadResult.Status != "queued" || uploadResult.MIME != "text/csv" {
		t.Fatalf("upload status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	resp, data = upload(ownerClient, ownerIdentity.CSRF, "upload-key", "new.csv", "name\nvalue\n", "", false)
	var uploadReplay httpapi.UploadedDocumentBody
	if err := json.Unmarshal(data, &uploadReplay); err != nil || resp.StatusCode != http.StatusOK || uploadReplay.DocumentID != uploadResult.DocumentID || uploadReplay.VersionID != uploadResult.VersionID {
		t.Fatalf("upload replay status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	if resp, data := upload(ownerClient, ownerIdentity.CSRF, "upload-key", "new.csv", "different", "", false); resp.StatusCode != http.StatusConflict || !bytes.Contains(data, []byte("idempotency_conflict")) {
		t.Fatalf("upload key conflict status=%d body=%s", resp.StatusCode, data)
	}
	if resp, data := upload(ownerClient, ownerIdentity.CSRF, "another-key", "new.csv", "name\nvalue\n", "", false); resp.StatusCode != http.StatusConflict || !bytes.Contains(data, []byte("duplicate_content")) {
		t.Fatalf("duplicate upload status=%d body=%s", resp.StatusCode, data)
	}
	resp, data = upload(ownerClient, ownerIdentity.CSRF, "duplicate-key", "new.csv", "name\nvalue\n", "?allow_duplicate=true", false)
	var duplicate httpapi.UploadedDocumentBody
	if err := json.Unmarshal(data, &duplicate); err != nil || resp.StatusCode != http.StatusOK || duplicate.DocumentID == uploadResult.DocumentID {
		t.Fatalf("allowed duplicate status=%d body=%s err=%v", resp.StatusCode, data, err)
	}
	if resp, _ := request(ownerClient, http.MethodDelete, path, nil, ownerIdentity.CSRF, false); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("archive status: %d", resp.StatusCode)
	}
	if resp, _ := request(outsiderClient, http.MethodPost, "/api/v1/auth/logout", nil, outsiderIdentity.CSRF, false); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status: %d", resp.StatusCode)
	}
	if resp, _ := request(outsiderClient, http.MethodGet, "/api/v1/me", nil, "", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged-out session status: %d", resp.StatusCode)
	}
}
