package models

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/secrets"
)

func TestCatalogEncryptsCredentialsAndEnforcesAdministrator(t *testing.T) {
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
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "model-test-" + hex.EncodeToString(nonce[:])
	var tenants []int64
	t.Cleanup(func() {
		for _, table := range []string{"login_session", "local_account", "audit_log", "chat_model", "embedding_space", "model_provider", "app_user"} {
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
	first, other := bootstrap("-a"), bootstrap("-b")
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewVault(key[:])
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var input struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&input) != nil ||
			len(input.Input) != 1 || input.Input[0] != "synthetic" {
			t.Error("unexpected synthetic embedding request")
		}
		if (input.Model == "test-embedding" && r.Header.Get("Authorization") != "Bearer test-model-secret") ||
			(input.Model == "test-anonymous" && r.Header.Get("Authorization") != "") ||
			(input.Model != "test-embedding" && input.Model != "test-anonymous") {
			t.Error("model identity or authorization is incorrect")
		}
		vector := make([]float32, 64)
		vector[0] = 3
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	request := NewSpace{ProviderName: "internal", BaseURL: server.URL, Locality: Internal,
		APIKey: []byte("test-model-secret"), Model: "test-embedding", Dimensions: 64, Normalized: true, Distance: "cosine"}
	created, err := catalog.CreateSpace(ctx, first.TenantID, first.UserID, request)
	if err != nil || created.ID <= 0 || !created.HasCredential {
		t.Fatalf("create model space: %+v err=%v", created, err)
	}
	var encrypted secrets.Encrypted
	if err := admin.QueryRow(ctx, `SELECT api_key_id, api_key_nonce, api_key_ciphertext FROM model_provider WHERE tenant_id = $1 AND id = $2`, first.TenantID, created.ProviderID).Scan(&encrypted.KeyID, &encrypted.Nonce, &encrypted.Ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Ciphertext, request.APIKey) {
		t.Fatal("credential persisted as plaintext")
	}
	plaintext, err := vault.Open(ctx, first.TenantID, created.ProviderID, encrypted)
	if err != nil || !bytes.Equal(plaintext, request.APIKey) {
		t.Fatalf("stored credential could not be decrypted: %v", err)
	}
	clear(plaintext)
	list, err := catalog.ListSpaces(ctx, first.TenantID, first.UserID)
	if err != nil || len(list) != 1 || list[0].ID != created.ID || !list[0].HasCredential {
		t.Fatalf("admin list: %+v err=%v", list, err)
	}
	if list, err := catalog.ListSpaces(ctx, other.TenantID, other.UserID); err != nil || len(list) != 0 {
		t.Fatalf("foreign tenant saw model spaces: %+v err=%v", list, err)
	}
	limits := ClientLimits{MaxBatch: 2, MaxRequestBytes: 4096, MaxResponseBytes: 16384, Timeout: 5 * time.Second, Concurrency: 1}
	auditor := &testAuditor{}
	resolver := embeddingScopeResolverFunc(func(ctx context.Context, binding EmbeddingBinding) (EgressScope, error) {
		return EgressScope{TenantID: binding.TenantID, KnowledgeBaseID: binding.KnowledgeBaseID, TenantPolicy: Deny, KnowledgeBasePolicy: Deny}, ctx.Err()
	})
	binding := EmbeddingBinding{TenantID: first.TenantID, KnowledgeBaseID: 1, EmbeddingSpaceID: created.ID, ConfigRevision: 1}
	client, runtimeSpace, err := catalog.OpenEmbeddingClient(ctx, binding, limits, resolver, auditor)
	if err != nil || client == nil || runtimeSpace != created {
		t.Fatalf("open runtime model: %+v err=%v", runtimeSpace, err)
	}
	t.Cleanup(client.Close)
	scope := EgressScope{TenantID: first.TenantID, KnowledgeBaseID: 1, TenantPolicy: Deny, KnowledgeBasePolicy: Deny}
	result, err := client.Embed(ctx, scope, []string{"synthetic"})
	if err != nil || len(result.Vectors) != 1 || result.Vectors[0][0] != 1 || modelCalls.Load() != 1 {
		t.Fatalf("runtime model request failed: err=%v calls=%d", err, modelCalls.Load())
	}
	foreignScope := scope
	foreignScope.TenantID = other.TenantID
	if _, err := client.Embed(ctx, foreignScope, []string{"synthetic"}); !errors.Is(err, ErrScopeMismatch) || modelCalls.Load() != 1 {
		t.Fatalf("runtime client accepted a foreign scope: err=%v calls=%d", err, modelCalls.Load())
	}
	countCalls := 0
	if _, err := client.CheckTokenizer(ctx, foreignScope, func(context.Context, string) (int, error) {
		countCalls++
		return 1, nil
	}); !errors.Is(err, ErrScopeMismatch) || modelCalls.Load() != 1 || countCalls != 0 {
		t.Fatalf("runtime token check accepted a foreign scope: err=%v calls=%d", err, modelCalls.Load())
	}
	foreignBinding := binding
	foreignBinding.TenantID = other.TenantID
	if client, space, err := catalog.OpenEmbeddingClient(ctx, foreignBinding, limits, resolver, auditor); !errors.Is(err, ErrRuntimeUnavailable) || client != nil || space.ID != 0 || modelCalls.Load() != 1 {
		t.Fatalf("foreign tenant opened model configuration: err=%v calls=%d", err, modelCalls.Load())
	}
	noVault, err := NewCatalog(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client, _, err := noVault.OpenEmbeddingClient(ctx, binding, limits, resolver, auditor); !errors.Is(err, secrets.ErrKeyUnavailable) || client != nil {
		t.Fatalf("encrypted credential opened without its master key: %v", err)
	}
	corrupt := bytes.Clone(encrypted.Ciphertext)
	corrupt[0] ^= 1
	if _, err := admin.Exec(ctx, `UPDATE model_provider SET api_key_ciphertext=$1 WHERE tenant_id=$2 AND id=$3`, corrupt, first.TenantID, created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(ctx, scope, []string{"synthetic"}); !errors.Is(err, ErrRuntimeChanged) || modelCalls.Load() != 1 {
		t.Fatalf("changed credential reached the model: err=%v calls=%d", err, modelCalls.Load())
	}
	if client, _, err := catalog.OpenEmbeddingClient(ctx, binding, limits, resolver, auditor); !errors.Is(err, secrets.ErrInvalidSecret) || client != nil || strings.Contains(err.Error(), "test-model-secret") {
		t.Fatalf("tampered credential was accepted or leaked: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE model_provider SET api_key_ciphertext=$1 WHERE tenant_id=$2 AND id=$3`, encrypted.Ciphertext, first.TenantID, created.ProviderID); err != nil {
		t.Fatal(err)
	}
	client.Close()
	anonymous := request
	anonymous.ProviderName, anonymous.APIKey, anonymous.Model = "anonymous", nil, "test-anonymous"
	anonymousSpace, err := noVault.CreateSpace(ctx, first.TenantID, first.UserID, anonymous)
	if err != nil {
		t.Fatal(err)
	}
	anonymousBinding := binding
	anonymousBinding.EmbeddingSpaceID = anonymousSpace.ID
	client, runtimeSpace, err = noVault.OpenEmbeddingClient(ctx, anonymousBinding, limits, resolver, auditor)
	if err != nil || client == nil || runtimeSpace.HasCredential {
		t.Fatalf("open model without credentials: %+v err=%v", runtimeSpace, err)
	}
	result, err = client.Embed(ctx, scope, []string{"synthetic"})
	client.Close()
	if err != nil || len(result.Vectors) != 1 || modelCalls.Load() != 2 {
		t.Fatalf("anonymous model request failed: err=%v calls=%d", err, modelCalls.Load())
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(other.TenantID)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_provider WHERE id = $1`, created.ProviderID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign provider visible: count=%d err=%v", visible, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var nonAdmin int64
	if err := admin.QueryRow(ctx, `INSERT INTO app_user (tenant_id, display_name) VALUES ($1, 'user') RETURNING id`, first.TenantID).Scan(&nonAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ListSpaces(ctx, first.TenantID, nonAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-administrator listed providers: %v", err)
	}
	if _, err := catalog.CreateSpace(ctx, first.TenantID, nonAdmin, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-administrator configured provider: %v", err)
	}
	if _, err := catalog.CreateSpace(ctx, first.TenantID, first.UserID, request); !errors.Is(err, ErrProviderNameUsed) {
		t.Fatalf("duplicate provider accepted: %v", err)
	}
	request.ProviderName = "without-key"
	if _, err := noVault.CreateSpace(ctx, first.TenantID, first.UserID, request); !errors.Is(err, secrets.ErrKeyUnavailable) {
		t.Fatalf("credential accepted without master key: %v", err)
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM model_provider WHERE tenant_id = $1 AND name = 'without-key'`, first.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("provider persisted after missing key: count=%d err=%v", count, err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO model_provider (tenant_id, name, base_url, locality, api_key_id) VALUES ($1, 'partial', 'http://localhost', 'internal', 'key')`, first.TenantID)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.Code != "23514" {
		t.Fatalf("partial ciphertext accepted by database: %v", err)
	}
	var denied int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action IN ('model.list', 'model.create') AND result = 'deny'`, first.TenantID).Scan(&denied); err != nil || denied != 2 {
		t.Fatalf("administrator denials not audited: count=%d err=%v", denied, err)
	}
	chat, err := catalog.CreateChatModel(ctx, first.TenantID, first.UserID, NewChatModel{Name: "chat", BaseURL: server.URL, Locality: Internal, Model: "test-chat", APIKey: "chat-secret"})
	if err != nil || chat.ID <= 0 {
		t.Fatalf("create chat: %+v %v", chat, err)
	}
	chatModels, err := catalog.ListChatModels(ctx, first.TenantID, first.UserID)
	if err != nil || len(chatModels) != 1 || chatModels[0] != chat {
		t.Fatalf("list chat: %+v %v", chatModels, err)
	}
	foreignModels, err := catalog.ListChatModels(ctx, other.TenantID, other.UserID)
	if err != nil || len(foreignModels) != 0 {
		t.Fatalf("chat tenant leak: %+v %v", foreignModels, err)
	}
	var chatCiphertext []byte
	if err := admin.QueryRow(ctx, `SELECT p.api_key_ciphertext FROM chat_model c JOIN model_provider p ON p.tenant_id=c.tenant_id AND p.id=c.provider_id WHERE c.tenant_id=$1 AND c.id=$2`, first.TenantID, chat.ID).Scan(&chatCiphertext); err != nil || len(chatCiphertext) == 0 || bytes.Contains(chatCiphertext, []byte("chat-secret")) {
		t.Fatalf("chat key not encrypted: %v", err)
	}
	if _, err := catalog.CreateChatModel(ctx, first.TenantID, other.UserID, NewChatModel{Name: "forbidden-chat", BaseURL: server.URL, Locality: Internal, Model: "chat"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("chat authorization: %v", err)
	}
	trigger := fmt.Sprintf("tuskrag_test_deny_model_%d", first.TenantID)
	if _, err := admin.Exec(ctx, "CREATE FUNCTION "+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+trigger+" ON audit_log"); err != nil {
			t.Errorf("remove test audit trigger: %v", err)
		}
		if _, err := admin.Exec(context.Background(), "DROP FUNCTION "+trigger+"()"); err != nil {
			t.Errorf("remove test audit function: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.tenant_id = %d) EXECUTE FUNCTION %s()", trigger, first.TenantID, trigger)); err != nil {
		t.Fatal(err)
	}
	request.ProviderName = "rollback"
	if _, err := catalog.CreateSpace(ctx, first.TenantID, first.UserID, request); err == nil {
		t.Fatal("model configuration succeeded without an audit record")
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM model_provider WHERE tenant_id = $1 AND name = 'rollback'`, first.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited model configuration persisted: count=%d err=%v", count, err)
	}
}
