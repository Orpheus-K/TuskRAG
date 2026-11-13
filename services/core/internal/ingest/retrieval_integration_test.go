package ingest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/httpapi"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/tokenizer"
)

func TestHybridRetrievalFromPublishedVersion(t *testing.T) {
	publicationDatabase(t)
	f, job, uploaded, plan, staged := prepareVectorFixture(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
			t.Errorf("clear retrieval test pointer: %v", err)
		}
	})
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, plan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	finishPublicationVectors(t, f, job, staged)
	if err := f.queue.Complete(f.ctx, job.Lease, preparePublication(t, f, job, plan, staged)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET tags=ARRAY['policy'] WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET doc_tags=ARRAY['policy'] WHERE tenant_id=$1 AND version_id=$2`, f.tenant, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	foreignActor, foreignKB, foreignDocument := setupForeignSearchFixture(t, f)
	config, err := pgxpool.ParseConfig(os.Getenv("TUSKRAG_TEST_APP_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	config.ConnConfig.RuntimeParams["application_name"] = "tuskrag-retrieval-race"
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	driver, err := retrieval.NewDriver(pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	binding, err := driver.Binding(f.ctx, actor, f.kbID)
	if err != nil || binding.SpaceID != plan.EmbeddingSpaceID || binding.Dimensions != 64 {
		t.Fatalf("retrieval binding: %+v %v", binding, err)
	}
	foreignBinding, err := driver.Binding(f.ctx, foreignActor, foreignKB)
	if err != nil {
		t.Fatal(err)
	}
	checkHybridIndexPlan(t, f, pool, binding)
	vector := testVector(1, 0)
	results, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ChunkID != staged.Chunks[0].ID || results[0].Content != "申请。" ||
		results[0].Title != "sample.csv" || results[0].LexicalRank != 1 || results[0].SemanticRank < 1 ||
		results[0].PageStart != nil || results[0].PageEnd != nil || len(results[0].HeadingPath) != 2 {
		t.Fatalf("unexpected hybrid result: %+v", results)
	}
	wantScore := 1.0/61.0 + 1.0/(60.0+float64(results[0].SemanticRank))
	if math.Abs(results[0].Score-wantScore) > 1e-12 || results[1].LexicalRank != 0 || results[1].SemanticRank < 1 {
		t.Fatalf("unexpected fusion ranks: %+v", results)
	}
	digest := sha256.Sum256([]byte("申请"))
	var auditHash, auditDetails string
	if err := f.admin.QueryRow(f.ctx, `SELECT details->>'query_sha256', details::text FROM audit_log
		WHERE tenant_id=$1 AND action='retrieval.search' ORDER BY id DESC LIMIT 1`, f.tenant).Scan(&auditHash, &auditDetails); err != nil ||
		auditHash != hex.EncodeToString(digest[:]) || strings.Contains(auditDetails, "申请") {
		t.Fatalf("search audit leaked content or has wrong digest: %q %v", auditHash, err)
	}
	testRetrievalService(t, f, pool, actor)
	for i := 0; i < 12; i++ {
		if visible, err := driver.Recall(f.ctx, foreignActor, foreignKB, foreignBinding, "申请", vector, retrieval.Filters{}, 8); err != nil || len(visible) != 1 || visible[0].DocumentID != foreignDocument {
			t.Fatalf("foreign tenant results: %d %v", len(visible), err)
		}
		if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{}, 8); err != nil || len(visible) != 2 || visible[0].DocumentID != uploaded.DocumentID || visible[1].DocumentID != uploaded.DocumentID {
			t.Fatalf("authorized recall after tenant switch: %d %v", len(visible), err)
		}
	}
	for _, filters := range []retrieval.Filters{{DocumentTags: []string{"missing"}}, {MIMEs: []string{"text/plain"}}} {
		if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, filters, 8); err != nil || len(visible) != 0 {
			t.Fatalf("unexpected filtered evidence: %d %v", len(visible), err)
		}
	}
	future := time.Now().Add(24 * time.Hour)
	if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{UpdatedAfter: &future}, 8); err != nil || len(visible) != 0 {
		t.Fatalf("future time filter: %d %v", len(visible), err)
	}
	if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{DocumentTags: []string{"policy"}, MIMEs: []string{"text/csv"}}, 8); err != nil || len(visible) != 2 {
		t.Fatalf("matching filters: %d %v", len(visible), err)
	}
	monitor, err := pgx.Connect(f.ctx, os.Getenv("TUSKRAG_TEST_ADMIN_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Close(context.Background()) }()
	testRetrievalAccessRace(t, f, driver, monitor, actor, binding, vector, retrieval.Filters{},
		`UPDATE document SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, uploaded.DocumentID, "RecheckSearchEvidence", false)
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET deleted_at=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
		t.Fatal(err)
	}
	testRetrievalAccessRace(t, f, driver, monitor, actor, binding, vector, retrieval.Filters{},
		`UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, uploaded.DocumentID, "RecheckSearchEvidence", false)
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='inherit_kb' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
		t.Fatal(err)
	}
	testRetrievalAccessRace(t, f, driver, monitor, actor, binding, vector, retrieval.Filters{DocumentTags: []string{"policy"}},
		`UPDATE document SET tags='{}' WHERE tenant_id=$1 AND id=$2`, uploaded.DocumentID, "RecheckSearchEvidence", false)
	var viewer int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'viewer') RETURNING id`, f.tenant).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := knowledge.SetMember(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, viewer, kb.Viewer); err != nil {
		t.Fatal(err)
	}
	testRetrievalAccessRace(t, f, driver, monitor, retrieval.Actor{TenantID: f.tenant, UserID: viewer}, binding, vector, retrieval.Filters{},
		`DELETE FROM kb_membership WHERE tenant_id=$1 AND kb_id=$2 AND user_id=$3`, viewer, "LockSearchBinding", true)
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
		t.Fatal(err)
	}
	if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{}, 8); err != nil || len(visible) != 0 {
		t.Fatalf("restricted document was visible: %d %v", len(visible), err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET config_revision=config_revision+1 WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
	if visible, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, retrieval.Filters{}, 8); !errors.Is(err, retrieval.ErrBindingChanged) || len(visible) != 0 {
		t.Fatalf("stale binding accepted: %d %v", len(visible), err)
	}
}

func setupForeignSearchFixture(t *testing.T, f *fixture) (retrieval.Actor, int64, int64) {
	t.Helper()
	var user, provider, space int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'foreign owner') RETURNING id`, f.foreign).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES ($1,'foreign','http://localhost','internal') RETURNING id`, f.foreign).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) VALUES ($1,$2,'test',64,true,'cosine') RETURNING id`, f.foreign, provider).Scan(&space); err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Create(f.ctx, kb.Actor{TenantID: f.foreign, UserID: user}, kb.CreateRequest{Name: "foreign", EmbeddingSpaceID: space, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	var document, version, section, chunk int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document(tenant_id,kb_id,title,mime) VALUES ($1,$2,'foreign','text/plain') RETURNING id`, f.foreign, base.ID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("foreign synthetic search fixture"))
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version(tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by)
		VALUES ($1,$2,$3,$4,1,'foreign-search-original','ready',$5) RETURNING id`, f.foreign, base.ID, document, digest[:], user).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET current_version_id=$1 WHERE tenant_id=$2 AND id=$3`, version, f.foreign, document); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.foreign, document); err != nil {
			t.Errorf("clear foreign search pointer: %v", err)
		}
	})
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO section(tenant_id,kb_id,document_id,version_id,ordinal,level,heading,heading_path,body_md,token_count)
		VALUES ($1,$2,$3,$4,1,1,'隔离',ARRAY['隔离'],'异租户申请。',3) RETURNING id`, f.foreign, base.ID, document, version).Scan(&section); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("kb:%d:all", base.ID)
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at)
		VALUES ($1,$2,$3,$4,$5,1,'text','异租户申请。','异租户申请。',3,true,ARRAY[$6]::text[],'text/plain',now()) RETURNING id`, f.foreign, base.ID, document, version, section, tag).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding)
		VALUES ($1,$2,$3,$4,true,ARRAY[$5]::text[],$6::halfvec)`, f.foreign, base.ID, chunk, space, tag, "[1"+strings.Repeat(",0", 63)+"]"); err != nil {
		t.Fatal(err)
	}
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.foreign, space); err != nil {
		t.Fatal(err)
	}
	return retrieval.Actor{TenantID: f.foreign, UserID: user}, base.ID, document
}

func testRetrievalService(t *testing.T, f *fixture, pool *pgxpool.Pool, actor retrieval.Actor) {
	t.Helper()
	counter, err := tokenizer.NewBGEM3(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var calls, mode atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&input) != nil ||
			input.Model != "test" || len(input.Input) != 1 {
			t.Error("invalid search embedding request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		current := mode.Load()
		if current == 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if current == 4 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		count, err := counter.Count(r.Context(), input.Input[0])
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body := map[string]any{"data": []any{map[string]any{"index": 0, "embedding": testVector(1, 0)}}}
		if current != 1 {
			if current == 2 {
				count++
			}
			body["usage"] = map[string]int{"prompt_tokens": count, "total_tokens": count}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	var provider int64
	if err := f.admin.QueryRow(f.ctx, `SELECT e.provider_id FROM knowledge_base k JOIN embedding_space e
		ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	service, err := retrieval.NewService(pool, nil, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	request := retrieval.Request{KnowledgeBaseID: f.kbID, Query: "申请", Limit: 8}
	result, err := service.Search(f.ctx, actor, request)
	if err != nil || len(result.Candidates) != 2 || len(result.Degraded) != 1 || result.Degraded[0] != "rerank" || calls.Load() != 1 {
		t.Fatalf("search service: candidates=%d degraded=%v calls=%d err=%v", len(result.Candidates), result.Degraded, calls.Load(), err)
	}
	testSearchHTTP(t, f, service, actor, http.StatusOK, `"score_kind":"rrf"`)
	testRerankService(t, f, service, actor, request)
	baseline := calls.Load()
	if result, err := service.Search(f.ctx, retrieval.Actor{TenantID: f.foreign, UserID: f.user}, request); !errors.Is(err, retrieval.ErrNotFound) || len(result.Candidates) != 0 || calls.Load() != baseline {
		t.Fatalf("unauthorized search reached model: candidates=%d calls=%d err=%v", len(result.Candidates), calls.Load(), err)
	}
	testSearchHTTP(t, f, service, retrieval.Actor{TenantID: f.foreign, UserID: f.user}, http.StatusNotFound, `"code":"not_found"`)
	if calls.Load() != baseline {
		t.Fatal("unauthorized HTTP search called the model")
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET distance='ip' WHERE tenant_id=$1 AND id=(SELECT embedding_space_id FROM knowledge_base WHERE tenant_id=$1 AND id=$2)`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Search(f.ctx, actor, request); !errors.Is(err, indexing.ErrUnsupportedDistance) || len(result.Candidates) != 0 || calls.Load() != baseline {
		t.Fatalf("unsupported distance reached model: candidates=%d calls=%d err=%v", len(result.Candidates), calls.Load(), err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET distance='cosine' WHERE tenant_id=$1 AND id=(SELECT embedding_space_id FROM knowledge_base WHERE tenant_id=$1 AND id=$2)`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET locality='external',base_url='https://127.0.0.1:9' WHERE tenant_id=$1 AND id=$2`, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Search(f.ctx, actor, request); !errors.Is(err, models.ErrEgressDenied) || len(result.Candidates) != 0 || calls.Load() != baseline {
		t.Fatalf("denied model call: candidates=%d calls=%d err=%v", len(result.Candidates), calls.Load(), err)
	}
	var denied int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND action='model.call' AND result='deny'`, f.tenant).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("denied model call audit=%d err=%v", denied, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET locality='internal',base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode int32
		want error
	}{{1, models.ErrTokenizerUsageMissing}, {2, models.ErrTokenizerMismatch}} {
		mode.Store(tc.mode)
		if result, err := service.Search(f.ctx, actor, request); !errors.Is(err, tc.want) || len(result.Candidates) != 0 {
			t.Fatalf("token usage mode %d: candidates=%d err=%v", tc.mode, len(result.Candidates), err)
		}
	}
	mode.Store(3)
	var upstream *models.UpstreamError
	if result, err := service.Search(f.ctx, actor, request); !errors.As(err, &upstream) || !upstream.Retryable || len(result.Candidates) != 0 {
		t.Fatalf("model failure was hidden: candidates=%d err=%v", len(result.Candidates), err)
	}
	testSearchHTTP(t, f, service, actor, http.StatusServiceUnavailable, `"code":"model_unavailable"`)
	mode.Store(0)
	if _, err := f.admin.Exec(f.ctx, `REVOKE INSERT ON audit_log FROM tuskrag_app`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), `GRANT INSERT ON audit_log TO tuskrag_app`); err != nil {
			t.Errorf("restore audit permission: %v", err)
		}
	}()
	if result, err := service.Search(f.ctx, actor, request); err == nil || len(result.Candidates) != 0 {
		t.Fatalf("audit failure returned evidence: candidates=%d err=%v", len(result.Candidates), err)
	}
	if _, err := f.admin.Exec(f.ctx, `GRANT INSERT ON audit_log TO tuskrag_app`); err != nil {
		t.Fatal(err)
	}
	var viewer int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'search viewer') RETURNING id`, f.tenant).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	owner := kb.Actor{TenantID: f.tenant, UserID: f.user}
	if err := knowledge.SetMember(f.ctx, owner, f.kbID, viewer, kb.Viewer); err != nil {
		t.Fatal(err)
	}
	mode.Store(4)
	type outcome struct {
		result retrieval.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.Search(f.ctx, retrieval.Actor{TenantID: f.tenant, UserID: viewer}, request)
		done <- outcome{result: result, err: err}
	}()
	defer releaseOnce.Do(func() { close(release) })
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal("search model call did not start")
	}
	if err := knowledge.RemoveMember(f.ctx, owner, f.kbID, viewer); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, retrieval.ErrNotFound) || len(outcome.result.Candidates) != 0 {
			t.Fatalf("revoked search returned evidence: candidates=%d err=%v", len(outcome.result.Candidates), outcome.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked search did not stop")
	}
}

func testRerankService(t *testing.T, f *fixture, service *retrieval.Service, actor retrieval.Actor, request retrieval.Request) {
	t.Helper()
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Error("unexpected rerank path")
			w.WriteHeader(404)
			return
		}
		var input struct {
			Documents []string `json:"documents"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Documents) != 2 {
			t.Error("unexpected candidate batch")
			w.WriteHeader(400)
			return
		}
		if mode.Load() == 1 {
			w.WriteHeader(503)
			return
		}
		if mode.Load() == 4 {
			w.WriteHeader(401)
			return
		}
		if mode.Load() == 2 {
			if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND kb_id=$2`, f.tenant, f.kbID); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
		}
		if mode.Load() == 3 {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"index": 1, "relevance_score": 0.9}, map[string]any{"index": 0, "relevance_score": 0.1}}})
	}))
	defer server.Close()
	var provider int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES($1,'reranker',$2,'internal') RETURNING id`, f.tenant, server.URL).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	request.Debug = true
	baseline, err := service.Search(f.ctx, actor, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Rerank = &retrieval.RerankOptions{ProviderID: provider, Model: "rank", TopN: 30}
	result, err := service.Search(f.ctx, actor, request)
	if err != nil || len(result.Degraded) != 0 || len(result.Candidates) != 2 || result.Candidates[0].ChunkID != baseline.Candidates[1].ChunkID || result.Candidates[0].Score != 0.9 || result.Trace == nil || len(result.Trace.Candidates) != 2 {
		t.Fatalf("rerank result: %+v %v", result, err)
	}
	testStoredRerankConfiguration(t, f, service, actor, request.Rerank, result.Candidates[0].ChunkID)
	mode.Store(1)
	if result, err = service.Search(f.ctx, actor, request); err == nil || len(result.Candidates) != 0 {
		t.Fatal("rerank failure masked")
	}
	request.Rerank.Fallback = true
	result, err = service.Search(f.ctx, actor, request)
	if err != nil || len(result.Degraded) != 1 || result.Trace == nil || len(result.Trace.Degraded) != 1 || result.Candidates[0].ChunkID != baseline.Candidates[0].ChunkID {
		t.Fatalf("fallback: %+v %v", result, err)
	}
	mode.Store(3)
	if result, err = service.Search(f.ctx, actor, request); !errors.Is(err, models.ErrInvalidResponse) || len(result.Candidates) != 0 {
		t.Fatal("malformed rerank hidden by fallback")
	}
	mode.Store(4)
	if result, err = service.Search(f.ctx, actor, request); err == nil || len(result.Candidates) != 0 {
		t.Fatal("authentication failure hidden by fallback")
	}
	mode.Store(2)
	result, err = service.Search(f.ctx, actor, request)
	if err != nil || len(result.Candidates) != 0 || result.Trace == nil || len(result.Trace.Candidates) != 0 {
		t.Fatalf("revoked evidence escaped through rerank/debug: %+v %v", result, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document SET acl_mode='inherit_kb' WHERE tenant_id=$1 AND kb_id=$2`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
}

type retrievalTestIdentity struct{ actor retrieval.Actor }

func (retrievalTestIdentity) Login(context.Context, int64, string, []byte) (identity.Session, error) {
	return identity.Session{}, errors.New("unexpected login")
}

func (i retrievalTestIdentity) Authenticate(context.Context, string) (identity.Session, error) {
	return identity.Session{Principal: identity.Principal{TenantID: i.actor.TenantID, UserID: i.actor.UserID}}, nil
}

func (retrievalTestIdentity) Logout(context.Context, string) error {
	return errors.New("unexpected logout")
}

func testSearchHTTP(t *testing.T, f *fixture, service *retrieval.Service, actor retrieval.Actor, status int, fragment string) {
	t.Helper()
	app := httpapi.New(httpapi.Dependencies{Identity: retrievalTestIdentity{actor: actor}, Search: service, InsecureLocalCookies: true})
	body := fmt.Sprintf(`{"query":"申请","kb_ids":[%d],"options":{"rerank":false}}`, f.kbID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(body))
	secret := make([]byte, 32)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("tuskrag.session.csrf.v1"))
	req.AddCookie(&http.Cookie{Name: "tuskrag_dev_session", Value: "1." + base64.RawURLEncoding.EncodeToString(secret)})
	req.Header.Set("X-CSRF-Token", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	if w.Code != status || !strings.Contains(w.Body.String(), fragment) || (status != http.StatusOK && strings.Contains(w.Body.String(), "申请。")) {
		t.Fatalf("search HTTP status=%d body=%s", w.Code, w.Body.String())
	}
}

var errCapturedQuery = errors.New("captured query")

type retrievalQueryCapture struct {
	sql  string
	args []any
}

func (*retrievalQueryCapture) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errCapturedQuery
}

func (c *retrievalQueryCapture) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return nil, errCapturedQuery
}

func (*retrievalQueryCapture) QueryRow(context.Context, string, ...any) pgx.Row {
	return capturedRow{}
}

type capturedRow struct{}

func (capturedRow) Scan(...any) error { return errCapturedQuery }

func checkHybridIndexPlan(t *testing.T, f *fixture, pool *pgxpool.Pool, binding retrieval.Binding) {
	t.Helper()
	capture := &retrievalQueryCapture{}
	_, err := dbquery.New(capture).RecallHybrid(f.ctx, dbquery.RecallHybridParams{
		TenantID: f.tenant, KbID: f.kbID, UserID: f.user, SpaceID: binding.SpaceID,
		PrincipalTags: []string{fmt.Sprintf("kb:%d:all", f.kbID)}, Query: "申请", DocTags: []string{}, Mimes: []string{},
		Dimensions: binding.Dimensions, Vector: "[1" + strings.Repeat(",0", 63) + "]",
	})
	if !errors.Is(err, errCapturedQuery) || capture.sql == "" {
		t.Fatalf("capture generated recall query: %v", err)
	}
	tx, err := pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, setting := range []string{"SET LOCAL plan_cache_mode=force_custom_plan", "SET LOCAL enable_seqscan=off", "SET LOCAL enable_sort=off", "SET LOCAL hnsw.ef_search=100", "SET LOCAL hnsw.iterative_scan=relaxed_order"} {
		if _, err := tx.Exec(f.ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.tenant)); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(f.ctx, "EXPLAIN (COSTS OFF) "+capture.sql, capture.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "tuskrag_chunk_bm25_v2") ||
		!strings.Contains(plan.String(), fmt.Sprintf("tuskrag_chunk_vector_hnsw_s%d", binding.SpaceID)) {
		t.Fatalf("hybrid index plan missing: %s", plan.String())
	}
}

func testRetrievalAccessRace(t *testing.T, f *fixture, driver *retrieval.Driver, monitor *pgx.Conn, actor retrieval.Actor,
	binding retrieval.Binding, vector []float32, filters retrieval.Filters, mutation string, id int64, blockedQuery string, membership bool) {
	t.Helper()
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	args := []any{f.tenant, id}
	if membership {
		args = []any{f.tenant, f.kbID, id}
	}
	if _, err := tx.Exec(f.ctx, mutation, args...); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	type outcome struct {
		results []retrieval.Candidate
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := driver.Recall(f.ctx, actor, f.kbID, binding, "申请", vector, filters, 8)
		done <- outcome{results: results, err: err}
	}()
	pending := true
	defer func() {
		_ = tx.Rollback(context.Background())
		if pending {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("retrieval race did not stop after rollback")
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := monitor.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE application_name='tuskrag-retrieval-race' AND state='active' AND wait_event_type='Lock' AND query LIKE $1)`, "%"+blockedQuery+"%").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case result := <-done:
			pending = false
			t.Fatalf("retrieval did not reach blocked recheck: results=%d err=%v", len(result.results), result.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("retrieval recheck did not wait for concurrent access change")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		pending = false
		if len(result.results) != 0 || (membership && !errors.Is(result.err, retrieval.ErrNotFound)) || (!membership && result.err != nil) {
			t.Fatalf("revoked evidence was returned: results=%d err=%v", len(result.results), result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retrieval did not finish after access change")
	}
}
