package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/tokenizer"
)

type fakeEmbeddingService struct {
	t            *testing.T
	counter      *tokenizer.BGEM3
	mu           sync.Mutex
	documents    map[string]int
	total        int
	failOnce     string
	mismatch     string
	badProbe     bool
	entered      chan struct{}
	release      chan struct{}
	probeEntered chan struct{}
	probeRelease chan struct{}
}

func (f *fakeEmbeddingService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&request) != nil ||
		request.Model != "test" || len(request.Input) != 1 {
		f.t.Error("invalid synthetic embedding request")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	input := request.Input[0]
	f.mu.Lock()
	f.total++
	_, document := f.documents[input]
	if document {
		f.documents[input]++
	}
	fail := document && input == f.failOnce && f.documents[input] == 1
	mismatch := (document && input == f.mismatch) || (!document && f.badProbe)
	entered, release := f.entered, f.release
	probeEntered, probeRelease := f.probeEntered, f.probeRelease
	firstProbe := !document && f.total == 1
	f.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if document && entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	if firstProbe && probeEntered != nil {
		probeEntered <- struct{}{}
		select {
		case <-probeRelease:
		case <-r.Context().Done():
			return
		}
	}
	count, err := f.counter.Count(r.Context(), input)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if mismatch {
		count++
	}
	vector := make([]float32, 64)
	vector[0] = 1
	if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}},
		"usage": map[string]int{"prompt_tokens": count, "total_tokens": count}}); err != nil {
		f.t.Error(err)
	}
}

func (f *fakeEmbeddingService) documentCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]int, 0, len(f.documents))
	for _, count := range f.documents {
		result = append(result, count)
	}
	return result
}

func (f *fakeEmbeddingService) callsFor(input string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.documents[input]
}

func (f *fakeEmbeddingService) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func prepareEmbeddingStage(t *testing.T) (*fixture, jobs.ClaimedJob, kb.UploadedDocument, PreparedPlan, StagedPlan, *EmbeddingStage, *fakeEmbeddingService) {
	t.Helper()
	f, job, uploaded, plan := preparePlanFixture(t)
	counter, err := tokenizer.NewBGEM3(f.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plan.Sections {
		if plan.Sections[index].BodyMarkdown != "" {
			count, err := counter.Count(f.ctx, plan.Sections[index].BodyMarkdown)
			if err != nil {
				t.Fatal(err)
			}
			plan.Sections[index].TokenCount = int32(count)
		}
	}
	for index := range plan.Chunks {
		count, err := counter.Count(f.ctx, plan.Chunks[index].SearchText)
		if err != nil {
			t.Fatal(err)
		}
		plan.Chunks[index].TokenCount = int32(count)
	}
	planStage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := planStage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeEmbeddingService{t: t, counter: counter, documents: map[string]int{}}
	for _, chunk := range plan.Chunks {
		fake.documents[chunk.SearchText] = 0
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	if _, err := f.admin.Exec(f.ctx, `UPDATE model_provider SET base_url=$1 WHERE tenant_id=$2 AND id=$3`, server.URL, f.tenant, plan.EmbeddingProfile.ProviderID); err != nil {
		t.Fatal(err)
	}
	limits := models.ClientLimits{MaxBatch: 2, MaxRequestBytes: 1024 * 1024, MaxResponseBytes: 1024 * 1024, Timeout: 10 * time.Second, Concurrency: 1}
	stage, err := NewEmbeddingStage(f.queue, f.pool, nil, counter.Count, limits, 1)
	if err != nil {
		t.Fatal(err)
	}
	otherPool, err := pgxpool.New(f.ctx, f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	if _, err := NewEmbeddingStage(f.queue, otherPool, nil, counter.Count, limits, 1); err == nil {
		t.Fatal("embedding stage accepted a different database pool")
	}
	return f, job, uploaded, plan, staged, stage, fake
}

func TestEmbeddingStageRetriesOnlyMissingVectors(t *testing.T) {
	f, job, uploaded, plan, staged, stage, fake := prepareEmbeddingStage(t)
	bad := plan
	bad.Chunks = append([]PreparedChunk(nil), plan.Chunks...)
	bad.Chunks[0].Content += "修改"
	bad.Chunks[0].SearchText += "修改"
	if err := stage.Run(f.ctx, job, bad, staged); !errors.Is(err, ErrPlanConflict) || fake.totalCalls() != 0 {
		t.Fatalf("modified plan reached model: %v", err)
	}
	forged := staged
	forged.Chunks = append([]StagedChunk(nil), staged.Chunks...)
	forged.Chunks[0].ID++
	if err := stage.Run(f.ctx, job, plan, forged); !errors.Is(err, ErrPlanCorrupt) || fake.totalCalls() != 0 {
		t.Fatalf("forged chunk reached model: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, jobs.ErrLeaseLost) || fake.totalCalls() != 0 {
		t.Fatalf("expired lease reached model: %v", err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "embedding-recovered", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.failOnce = plan.Chunks[1].SearchText
	fake.mu.Unlock()
	err = stage.Run(f.ctx, job, plan, staged)
	var upstream *models.UpstreamError
	if !errors.As(err, &upstream) || !upstream.Retryable {
		t.Fatalf("retryable model failure lost: %v", err)
	}
	var count int
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM chunk_vector WHERE tenant_id=$1), status FROM document_version WHERE tenant_id=$1 AND id=$2`,
		f.tenant, uploaded.VersionID).Scan(&count, &status); err != nil || count != 1 || status != "embedding" {
		t.Fatalf("partial batch state changed: count=%d status=%s err=%v", count, status, err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); err != nil {
		t.Fatal(err)
	}
	if fake.callsFor(plan.Chunks[0].SearchText) != 1 || fake.callsFor(plan.Chunks[1].SearchText) != 2 {
		t.Fatalf("retry resent a completed vector: counts=%v", fake.documentCalls())
	}
	var currentChunks, currentVectors int
	var noCurrent bool
	if err := f.admin.QueryRow(f.ctx, `SELECT v.status, d.current_version_id IS NULL,
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND is_current)
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &noCurrent, &currentChunks, &currentVectors); err != nil ||
		status != "indexing" || !noCurrent || currentChunks != 0 || currentVectors != 0 {
		t.Fatalf("embedding stage published data: status=%s err=%v", status, err)
	}
	before := []int{fake.callsFor(plan.Chunks[0].SearchText), fake.callsFor(plan.Chunks[1].SearchText)}
	total := fake.totalCalls()
	if err := stage.Run(f.ctx, job, plan, staged); err != nil || fake.totalCalls() != total || !reflect.DeepEqual(before, []int{fake.callsFor(plan.Chunks[0].SearchText), fake.callsFor(plan.Chunks[1].SearchText)}) {
		t.Fatalf("indexing replay called model: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET search_text=search_text || '变更' WHERE tenant_id=$1 AND id=$2`, f.tenant, staged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, ErrPlanCorrupt) || fake.totalCalls() != total {
		t.Fatalf("indexing replay accepted changed content: %v", err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE chunk_id=$1`, staged.Chunks[0].ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign tenant saw embedding: count=%d err=%v", count, err)
	}
}

func TestEmbeddingStageRejectsWrongBatchUsage(t *testing.T) {
	f, job, uploaded, plan, staged, stage, fake := prepareEmbeddingStage(t)
	fake.mu.Lock()
	fake.mismatch = plan.Chunks[0].SearchText
	fake.mu.Unlock()
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, models.ErrTokenizerMismatch) {
		t.Fatalf("batch token discrepancy accepted: %v", err)
	}
	var count int
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM chunk_vector WHERE tenant_id=$1), status FROM document_version WHERE tenant_id=$1 AND id=$2`,
		f.tenant, uploaded.VersionID).Scan(&count, &status); err != nil || count != 0 || status != "embedding" {
		t.Fatalf("mismatched batch wrote data: count=%d status=%s err=%v", count, status, err)
	}
}

func TestEmbeddingStageRejectsTokenizerMismatchBeforeDocument(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	fake.mu.Lock()
	fake.badProbe = true
	fake.mu.Unlock()
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, models.ErrTokenizerMismatch) || fake.totalCalls() != 1 ||
		fake.callsFor(plan.Chunks[0].SearchText) != 0 || fake.callsFor(plan.Chunks[1].SearchText) != 0 {
		t.Fatalf("tokenizer mismatch reached document input: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tokenizer mismatch wrote vectors: count=%d err=%v", count, err)
	}
}

func TestEmbeddingStageDeniesChangedEgressBeforeModelCall(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	if _, err := f.admin.Exec(f.ctx, `UPDATE knowledge_base SET egress_policy='allow' WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, models.ErrEgressDenied) || fake.totalCalls() != 0 {
		t.Fatalf("denied egress reached model: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE tenant_id=$1 AND actor_user_id=$2 AND action='model.call' AND result='deny'`, f.tenant, f.user).Scan(&count); err != nil || count != 1 {
		t.Fatalf("denied embedding was not audited: count=%d err=%v", count, err)
	}
}

func TestEmbeddingStageRejectsModifiedStoredText(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET search_text=search_text || '变更' WHERE tenant_id=$1 AND id=$2`, f.tenant, staged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, ErrPlanCorrupt) || fake.callsFor(plan.Chunks[0].SearchText) != 0 || fake.totalCalls() != 13 {
		t.Fatalf("modified stored text reached model: %v", err)
	}
}

func TestEmbeddingStageRejectsCorruptExistingVector(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	if err := stage.vectors.Write(f.ctx, job, staged.Identity, []VectorInput{vectorInput(staged, 0, testVector(1, 0))}); err != nil {
		t.Fatal(err)
	}
	corrupt, err := encodeHalfVector(testVector(0.5, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector SET embedding=$1::halfvec WHERE tenant_id=$2 AND chunk_id=$3`, corrupt, f.tenant, staged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := stage.Run(f.ctx, job, plan, staged); !errors.Is(err, ErrPlanCorrupt) || fake.totalCalls() != 13 ||
		fake.callsFor(plan.Chunks[0].SearchText) != 0 || fake.callsFor(plan.Chunks[1].SearchText) != 0 {
		t.Fatalf("corrupt existing vector was accepted or overwritten: %v", err)
	}
}

func TestEmbeddingStageRejectsResultAfterLeaseExpiry(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	fake.mu.Lock()
	fake.entered, fake.release = make(chan struct{}, 1), make(chan struct{})
	fake.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- stage.Run(f.ctx, job, plan, staged) }()
	select {
	case <-fake.entered:
	case err := <-done:
		t.Fatalf("embedding stopped before the document request: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		close(fake.release)
		t.Fatal(err)
	}
	close(fake.release)
	if err := <-done; !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late embedding result accepted: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late embedding wrote vectors: count=%d err=%v", count, err)
	}
}

func TestEmbeddingStageStopsProbesAfterLeaseExpiry(t *testing.T) {
	f, job, _, plan, staged, stage, fake := prepareEmbeddingStage(t)
	fake.mu.Lock()
	fake.probeEntered, fake.probeRelease = make(chan struct{}, 1), make(chan struct{})
	fake.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- stage.Run(f.ctx, job, plan, staged) }()
	select {
	case <-fake.probeEntered:
	case err := <-done:
		t.Fatalf("embedding stopped before the first probe: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		close(fake.probeRelease)
		t.Fatal(err)
	}
	close(fake.probeRelease)
	if err := <-done; !errors.Is(err, jobs.ErrLeaseLost) || fake.totalCalls() != 1 ||
		fake.callsFor(plan.Chunks[0].SearchText) != 0 || fake.callsFor(plan.Chunks[1].SearchText) != 0 {
		t.Fatalf("expired lease continued model calls: err=%v calls=%d", err, fake.totalCalls())
	}
}
