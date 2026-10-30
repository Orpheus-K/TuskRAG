package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/storage"
	"tuskrag.local/core/internal/tokenizer"
)

type pipelineModel struct {
	t       *testing.T
	counter *tokenizer.BGEM3
	mu      sync.Mutex
	calls   int
	docs    int
	fail    bool
	entered chan struct{}
	release <-chan struct{}
}

func (m *pipelineModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&request) != nil ||
		request.Model != "test" || len(request.Input) != 1 {
		m.t.Error("invalid pipeline embedding request")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	text := request.Input[0]
	m.mu.Lock()
	m.calls++
	document := strings.Contains(text, "独特内容Q9")
	if document {
		m.docs++
	}
	firstDocument := document && m.docs == 1
	fail := firstDocument && m.fail
	entered, release := m.entered, m.release
	m.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if firstDocument && entered != nil {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	count, err := m.counter.Count(r.Context(), text)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	vector := make([]float32, 64)
	vector[0] = 1
	if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}},
		"usage": map[string]int{"prompt_tokens": count, "total_tokens": count}}); err != nil {
		m.t.Error(err)
	}
}

func (m *pipelineModel) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.docs
}

func pipelineStages(t *testing.T, f *fixture, implementation DocumentParser, failOnce bool) (*IngestHandler, *ParseStage, *pipelineModel) {
	t.Helper()
	var space, provider int64
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id, e.provider_id FROM knowledge_base k
		JOIN embedding_space e ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&space, &provider); err != nil {
		t.Fatal(err)
	}
	if err := indexing.Provision(f.ctx, mustTestMigratorURL(t), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(f.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	model := &pipelineModel{t: t, counter: counter, fail: failOnce}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, provider); err != nil {
		t.Fatal(err)
	}
	parseStage := makeStage(t, f, implementation)
	limits := models.ClientLimits{MaxBatch: 1, MaxRequestBytes: 1024 * 1024, MaxResponseBytes: 1024 * 1024, Timeout: 10 * time.Second, Concurrency: 1}
	embedding, err := NewEmbeddingStage(f.queue, f.pool, nil, counter.Count, limits, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewIngestHandler(f.queue, parseStage, embedding, DefaultIngestSettings())
	if err != nil {
		t.Fatal(err)
	}
	return handler, parseStage, model
}

func nativePipelineParser(t *testing.T) DocumentParser {
	t.Helper()
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return parserFunc(func(ctx context.Context, input parser.ParseInput) (parser.Document, error) {
		return parser.ParseNativeText(ctx, input, validator)
	})
}

func pipelineUpload(t *testing.T, f *fixture, mime, source string) (kb.UploadedDocument, int64) {
	t.Helper()
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "pipeline", MIME: mime, Content: strings.NewReader(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	var jobID int64
	if err := f.admin.QueryRow(f.ctx, `SELECT id FROM job WHERE tenant_id=$1 AND kind='ingest' AND (payload->>'version_id')::bigint=$2`, f.tenant, uploaded.VersionID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
			t.Errorf("clear pipeline test pointer: %v", err)
		}
	})
	return uploaded, jobID
}

func runPipelineWorker(t *testing.T, f *fixture, handler *IngestHandler, stage *ParseStage) <-chan error {
	t.Helper()
	return runJobWorker(t, f, handler, stage.Expire)
}

func runJobWorker(t *testing.T, f *fixture, handler jobs.Handler, expired jobs.ExpiredEffects) <-chan error {
	t.Helper()
	worker, err := jobs.NewWorker(f.queue, handler, jobs.WorkerConfig{TenantID: f.tenant, Kinds: []jobs.Kind{jobs.Ingest, jobs.Purge},
		Owner: "pipeline-test", LeaseTTL: 30 * time.Second, Poll: 25 * time.Millisecond, OnExpired: expired})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("pipeline worker stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("pipeline worker did not stop")
		}
	})
	return done
}

func TestJobWorkerDispatchesSupersededCleanup(t *testing.T) {
	publicationDatabase(t)
	f, firstJob, uploaded, firstPlan, firstStaged := prepareVectorFixture(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
			t.Errorf("clear dispatch test pointer: %v", err)
		}
	})
	handler, _, model := pipelineStages(t, f, nativePipelineParser(t), false)
	purge, err := NewPurgeStage(f.queue, f.files)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewJobHandler(handler, purge)
	if err != nil {
		t.Fatal(err)
	}
	finishPublicationVectors(t, f, firstJob, firstStaged)
	if err := f.queue.Complete(f.ctx, firstJob.Lease, preparePublication(t, f, firstJob, firstPlan, firstStaged)); err != nil {
		t.Fatal(err)
	}
	secondJob, secondPlan, secondStaged, secondVersion := prepareRevision(t, f, uploaded.DocumentID, uploaded.VersionID, firstPlan, "dispatch")
	if err := f.queue.Complete(f.ctx, secondJob.Lease, preparePublication(t, f, secondJob, secondPlan, secondStaged)); err != nil {
		t.Fatal(err)
	}
	unsupported, err := f.queue.Enqueue(f.ctx, f.tenant, jobs.Request{Kind: jobs.Reembed, Payload: json.RawMessage(`{}`), MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	var purgeID int64
	if err := f.admin.QueryRow(f.ctx, `SELECT id FROM job WHERE tenant_id=$1 AND kind='purge' AND (payload->>'version_id')::bigint=$2`, f.tenant, uploaded.VersionID).Scan(&purgeID); err != nil {
		t.Fatal(err)
	}
	done := runJobWorker(t, f, router, router.Expire)
	waitPipelineJob(t, f, done, purgeID, "succeeded")
	assertPurgeState(t, f, uploaded.VersionID, "", false)
	assertOriginalExists(t, f, storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}, false)
	assertPublicationState(t, f, uploaded.DocumentID, secondVersion, secondVersion, "ready", "succeeded", 2)
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE tenant_id=$1 AND id=$2`, f.tenant, unsupported).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("unsupported job status=%q err=%v", status, err)
	}
	if calls, _ := model.counts(); calls != 0 {
		t.Fatalf("cleanup called the embedding model %d times", calls)
	}
}

func waitPipelineJob(t *testing.T, f *fixture, done <-chan error, jobID int64, want string) {
	t.Helper()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err := f.admin.QueryRow(f.ctx, `SELECT status FROM job WHERE tenant_id=$1 AND id=$2`, f.tenant, jobID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("pipeline worker stopped before %s: %v", want, err)
		case <-deadline.C:
			t.Fatalf("pipeline job status=%s want=%s", status, want)
		case <-ticker.C:
		}
	}
}

func TestIngestWorkerProcessesMarkdownAndRetriesModel(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	source := "# 独立制度\n\n独特内容Q9需要先申请。\n\n| 城市 | 限额 |\n| --- | --- |\n| 北京 | 600 |\n"
	uploaded, jobID := pipelineUpload(t, f, "text/markdown", source)
	handler, stage, model := pipelineStages(t, f, nativePipelineParser(t), true)
	done := runPipelineWorker(t, f, handler, stage)
	waitPipelineJob(t, f, done, jobID, "succeeded")
	var status string
	var current int64
	var attempts, chunks, vectors, tables int
	err := f.admin.QueryRow(f.ctx, `SELECT v.status,d.current_version_id,j.attempts,
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector cv JOIN chunk c ON c.tenant_id=cv.tenant_id AND c.id=cv.chunk_id WHERE c.tenant_id=$1 AND c.version_id=$2 AND cv.is_current),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND kind='table' AND is_current)
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		JOIN job j ON j.tenant_id=v.tenant_id AND j.id=$3 WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID, jobID).Scan(&status, &current, &attempts, &chunks, &vectors, &tables)
	calls, docs := model.counts()
	if err != nil || status != "ready" || current != uploaded.VersionID || attempts != 2 || chunks != 2 || vectors != 2 || tables != 1 || docs != 2 || calls < 28 {
		t.Fatalf("pipeline status=%s current=%d attempts=%d chunks=%d vectors=%d tables=%d calls=%d docs=%d err=%v", status, current, attempts, chunks, vectors, tables, calls, docs, err)
	}
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, uploaded.VersionID, "申请")
	assertVisibleVersion(t, f, f.foreign, uploaded.DocumentID, 0, "申请")
	assertOriginalExists(t, f, storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}, true)
}

func TestIngestWorkerStopsBeforeModelOnLowQuality(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	uploaded, jobID := pipelineUpload(t, f, "text/plain", "正常内容。\n"+strings.Repeat("�", 8))
	handler, stage, model := pipelineStages(t, f, nativePipelineParser(t), false)
	done := runPipelineWorker(t, f, handler, stage)
	waitPipelineJob(t, f, done, jobID, "failed")
	var status, failure string
	var current bool
	var vectors int
	err := f.admin.QueryRow(f.ctx, `SELECT v.status,v.failure_code,d.current_version_id IS NULL,
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1) FROM document_version v
		JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &failure, &current, &vectors)
	calls, _ := model.counts()
	if err != nil || status != "failed" || failure != string(jobs.QualityReview) || !current || vectors != 0 || calls != 0 {
		t.Fatalf("quality failure status=%s code=%s noCurrent=%t vectors=%d modelCalls=%d err=%v", status, failure, current, vectors, calls, err)
	}
}

func TestIngestWorkerRetriesWhenIndexDisappearsBeforeCommit(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	uploaded, jobID := pipelineUpload(t, f, "text/markdown", "# 独立制度\n\n独特内容Q9需要先申请。\n")
	handler, stage, model := pipelineStages(t, f, nativePipelineParser(t), false)
	entered, release := make(chan struct{}), make(chan struct{})
	model.entered, model.release = entered, release
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	done := runPipelineWorker(t, f, handler, stage)
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("pipeline worker stopped before embedding: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	migrator, err := pgx.Connect(f.ctx, mustTestMigratorURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close(context.Background()) }()
	if _, err := migrator.Exec(f.ctx, `DROP INDEX CONCURRENTLY public.tuskrag_chunk_bm25_v2`); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	waitPipelineJob(t, f, done, jobID, "queued")
	var lastError string
	var space int64
	if err := f.admin.QueryRow(f.ctx, `SELECT j.last_error,k.embedding_space_id FROM job j JOIN knowledge_base k ON k.tenant_id=j.tenant_id
		WHERE j.tenant_id=$1 AND j.id=$2 AND k.id=$3`, f.tenant, jobID, f.kbID).Scan(&lastError, &space); err != nil || lastError != string(jobs.IndexUnavailable) {
		t.Fatalf("index failure code=%q err=%v", lastError, err)
	}
	if err := indexing.Provision(f.ctx, mustTestMigratorURL(t), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	waitPipelineJob(t, f, done, jobID, "succeeded")
	var attempts, chunks, vectors int
	if err := f.admin.QueryRow(f.ctx, `SELECT j.attempts,
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector cv JOIN chunk c ON c.tenant_id=cv.tenant_id AND c.id=cv.chunk_id WHERE c.tenant_id=$1 AND c.version_id=$2 AND cv.is_current)
		FROM job j WHERE j.tenant_id=$1 AND j.id=$3`, f.tenant, uploaded.VersionID, jobID).Scan(&attempts, &chunks, &vectors); err != nil {
		t.Fatal(err)
	}
	_, docs := model.counts()
	if attempts != 2 || chunks != 1 || vectors != 1 || docs != 1 {
		t.Fatalf("index retry attempts=%d chunks=%d vectors=%d documentCalls=%d", attempts, chunks, vectors, docs)
	}
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, uploaded.VersionID, "申请")
}

func TestIngestRequiresConfirmedTableHeader(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	handler, _, model := pipelineStages(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) { return document(), nil }), false)
	_, err := handler.Prepare(f.ctx, job)
	var failure *jobs.ProcessingFailure
	if !errors.As(err, &failure) || failure.Code != jobs.TableHeaderRequired || failure.Apply == nil {
		t.Fatalf("unconfirmed table accepted: %v", err)
	}
	if status, err := f.queue.FailWithEffects(f.ctx, job.Lease, failure.Code, failure.Apply); err != nil || status != jobs.Failed {
		t.Fatalf("record table failure status=%s err=%v", status, err)
	}
	var code string
	if err := f.admin.QueryRow(f.ctx, `SELECT failure_code FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&code); err != nil || code != string(jobs.TableHeaderRequired) {
		t.Fatalf("table failure code=%s err=%v", code, err)
	}
	if calls, _ := model.counts(); calls != 0 {
		t.Fatalf("unconfirmed table reached model: %d calls", calls)
	}
}

func TestIngestRequiresHeaderConfirmationGuard(t *testing.T) {
	publicationDatabase(t)
	f := setup(t)
	if err := db.CheckVersionGuards(f.ctx, f.pool); err != nil {
		t.Fatalf("initial version guards: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `ALTER TABLE document_version DISABLE TRIGGER document_header_confirmation_guard`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), `ALTER TABLE document_version ENABLE TRIGGER document_header_confirmation_guard`); err != nil {
			t.Errorf("restore table header guard: %v", err)
		}
	}()
	if err := db.CheckVersionGuards(f.ctx, f.pool); !errors.Is(err, db.ErrVersionGuards) {
		t.Fatalf("disabled table header guard accepted: %v", err)
	}
}

func TestVerifyOriginalRejectsTamperingAndMissingFile(t *testing.T) {
	f := setup(t)
	source := "# 测试\n\n原始内容。\n"
	uploaded, jobID := pipelineUpload(t, f, "text/markdown", source)
	job, err := f.queue.Claim(f.ctx, f.tenant, "original-test", time.Minute)
	if err != nil || job.Lease.JobID != jobID {
		t.Fatalf("claim original job: %v", err)
	}
	stage := makeStage(t, f, nativePipelineParser(t))
	if _, err := stage.Parse(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := stage.VerifyOriginal(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	key := storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}
	keyPath, err := key.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(keyPath)), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	var failure *jobs.ProcessingFailure
	if err := stage.VerifyOriginal(f.ctx, job); !errors.As(err, &failure) || failure.Code != jobs.FileCorrupt {
		t.Fatalf("changed original accepted: %v", err)
	}
	if err := f.files.Delete(f.ctx, key); err != nil {
		t.Fatal(err)
	}
	failure = nil
	if err := stage.VerifyOriginal(f.ctx, job); !errors.As(err, &failure) || failure.Code != jobs.FileMissing {
		t.Fatalf("missing original accepted: %v", err)
	}
	if err := f.queue.WithLease(f.ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		return failure.Apply(ctx, tx, jobs.Failed)
	}); !errors.Is(err, jobs.ErrFailureRequired) {
		t.Fatalf("unfenced failure effects accepted: %v", err)
	}
}
