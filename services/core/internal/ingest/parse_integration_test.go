package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/storage"
)

type fixture struct {
	ctx                         context.Context
	admin                       *pgx.Conn
	pool                        *pgxpool.Pool
	queue                       *jobs.Store
	files                       *storage.Local
	root                        string
	tenant, foreign, user, kbID int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
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
		_ = admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := admin.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	var super, bypass bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil || !super {
		t.Fatalf("test admin: %t %v", super, err)
	}
	if err := pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil || bypass {
		t.Fatalf("test app bypasses RLS: %t %v", bypass, err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "ingest-test-" + hex.EncodeToString(nonce[:])
	f := &fixture{ctx: ctx, admin: admin, pool: pool, root: t.TempDir()}
	if err := admin.QueryRow(ctx, `INSERT INTO tenant(name) VALUES ($1) RETURNING id`, name).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"chunk_vector", "chunk", "ingest_plan", "section", "parse_artifact", "idempotency_record", "audit_log", "job", "document_version", "document", "kb_membership", "knowledge_base", "embedding_space", "model_provider", "app_user"} {
			if _, err := admin.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id = ANY($1)", []int64{f.tenant, f.foreign}); err != nil {
				t.Errorf("clean %s: %v", table, err)
			}
		}
		if _, err := admin.Exec(context.Background(), `DELETE FROM tenant WHERE id=ANY($1)`, []int64{f.tenant, f.foreign}); err != nil {
			t.Error(err)
		}
	})
	if err := admin.QueryRow(ctx, `INSERT INTO tenant(name) VALUES ($1) RETURNING id`, name+"-foreign").Scan(&f.foreign); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO app_user(tenant_id,display_name) VALUES ($1,'owner') RETURNING id`, f.tenant).Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	var provider, space int64
	if err := admin.QueryRow(ctx, `INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES ($1,'test','http://localhost','internal') RETURNING id`, f.tenant).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) VALUES ($1,$2,'test',64,true,'cosine') RETURNING id`, f.tenant, provider).Scan(&space); err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(pool)
	if err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Create(ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.CreateRequest{Name: "test", EmbeddingSpaceID: space, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	f.kbID = base.ID
	f.files, err = storage.NewLocal(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.files.Close(); err != nil {
			t.Error(err)
		}
	})
	f.queue, err = jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) upload(t *testing.T, content string) (kb.UploadedDocument, jobs.ClaimedJob) {
	t.Helper()
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	document, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{KBID: f.kbID, Title: "sample.csv", MIME: "text/csv", Content: strings.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "parse-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return document, job
}

type parserFunc func(context.Context, parser.ParseInput) (parser.Document, error)

func (f parserFunc) Parse(ctx context.Context, input parser.ParseInput) (parser.Document, error) {
	return f(ctx, input)
}

func document() parser.Document {
	return parser.Document{SchemaVersion: parser.ExpectedSchemaVersion, Pages: []parser.Page{{Number: 1,
		Blocks: []parser.Block{{Type: "table", Cells: [][]string{{"城市", "限额"}, {"北京", "600"}}}}, Signals: parser.Signals{CharCount: 8}}},
		Meta: parser.Metadata{ParserProfile: "test-parser"}}
}

func makeStage(t *testing.T, f *fixture, implementation DocumentParser) *ParseStage {
	t.Helper()
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	stage, err := NewParseStage(f.queue, f.files, implementation, validator, t.TempDir(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func checkNormalizedRoundTrip(t *testing.T, parsed parser.Document, normalized NormalizedDocument) {
	t.Helper()
	var expected []parser.Block
	for _, page := range parsed.Pages {
		expected = append(expected, page.Blocks...)
	}
	roundTrip := parseTextFixture(t, []byte(normalized.Markdown), "text/markdown")
	if len(roundTrip.Pages) != 1 || !reflect.DeepEqual(expected, roundTrip.Pages[0].Blocks) {
		t.Fatalf("normalized Markdown changed parsed blocks: original=%+v roundtrip=%+v", expected, roundTrip.Pages)
	}
}

func checkParsedTableChunks(t *testing.T, block NormalizedBlock, headingPath []string, mime string) {
	t.Helper()
	chunks, err := SplitTable(context.Background(), TableSplitRequest{
		Block: block, HeadingPath: headingPath, MIME: mime, HeaderRow: 1, MaxTokens: 180, CountTokens: countRunes,
	})
	if err != nil || len(chunks) == 0 {
		t.Fatalf("split parsed table: %+v err=%v", chunks, err)
	}
	var rows [][]string
	for _, chunk := range chunks {
		if chunk.Page != block.Page || chunk.TokenCount > 180 || !reflect.DeepEqual(chunk.Cells[0], block.Cells[0]) {
			t.Fatalf("parsed table chunk lost location or header: %+v", chunk)
		}
		rows = append(rows, chunk.Cells[1:]...)
	}
	if !reflect.DeepEqual(rows, block.Cells[1:]) {
		t.Fatalf("parsed table chunk lost rows: %+v", rows)
	}
}

func checkParsedTextChunks(t *testing.T, block NormalizedBlock, headingPath []string) {
	t.Helper()
	chunks, err := SplitText(context.Background(), TextSplitRequest{
		Paragraphs: []NormalizedBlock{block}, HeadingPath: headingPath,
		TargetMin: 1, TargetMax: 90, HardMax: 180, CountTokens: countRunes,
	})
	if err != nil || len(chunks) == 0 || block.Text == nil {
		t.Fatalf("split parsed text: %+v err=%v", chunks, err)
	}
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		if chunk.TokenCount > 180 || chunk.PageStart != block.Page || chunk.PageEnd != block.Page {
			t.Fatalf("parsed text chunk lost location or exceeded limit: %+v", chunk)
		}
		for _, fragment := range chunk.Fragments {
			if !fragment.Overlap {
				rebuilt.WriteString(fragment.Text)
			}
		}
	}
	if rebuilt.String() != *block.Text {
		t.Fatal("parsed text chunk lost source content")
	}
}

func TestParseCheckpointSurvivesLeaseRecovery(t *testing.T) {
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	var calls atomic.Int32
	stage := makeStage(t, f, parserFunc(func(_ context.Context, input parser.ParseInput) (parser.Document, error) {
		calls.Add(1)
		data, err := io.ReadAll(input.File)
		if err != nil || string(data) != "城市,限额\n北京,600\n" {
			return parser.Document{}, errors.New("input mismatch")
		}
		return document(), nil
	}))
	result, err := stage.Parse(f.ctx, job)
	if err != nil || result.Meta.ParserProfile != "test-parser" {
		t.Fatalf("parse result=%+v err=%v", result, err)
	}
	var status string
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT status, (SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &artifacts); err != nil || status != "embedding" || artifacts != 1 {
		t.Fatalf("checkpoint status=%s count=%d err=%v", status, artifacts, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.queue.Claim(f.ctx, f.tenant, "replacement", time.Minute)
	if err != nil || replacement.Lease.Generation <= job.Lease.Generation {
		t.Fatalf("reclaim: %+v err=%v", replacement.Lease, err)
	}
	if _, err := stage.Parse(f.ctx, replacement); err != nil || calls.Load() != 1 {
		t.Fatalf("checkpoint replay calls=%d err=%v", calls.Load(), err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, strconv.FormatInt(f.foreign, 10)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM parse_artifact WHERE tenant_id=$1`, f.tenant).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatalf("foreign artifact visible: count=%d err=%v", artifacts, err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestParseCheckpointRejectsStaleWorker(t *testing.T) {
	f := setup(t)
	_, old := f.upload(t, "城市,限额\n北京,600\n")
	started, release := make(chan struct{}), make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	var calls atomic.Int32
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return document(), nil
	}))
	done := make(chan error, 1)
	go func() { _, err := stage.Parse(f.ctx, old); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parser did not start")
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, old.Lease.JobID); err != nil {
		close(release)
		released = true
		t.Fatal(err)
	}
	replacement, err := f.queue.Claim(f.ctx, f.tenant, "replacement", time.Minute)
	if err != nil {
		close(release)
		released = true
		t.Fatal(err)
	}
	close(release)
	released = true
	if err := <-done; !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale parser committed: %v", err)
	}
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM parse_artifact WHERE tenant_id=$1`, f.tenant).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatalf("stale artifact persisted: %d %v", artifacts, err)
	}
	if _, err := stage.Parse(f.ctx, replacement); err != nil || calls.Load() != 2 {
		t.Fatalf("replacement parse calls=%d err=%v", calls.Load(), err)
	}
}

func TestCancelledParseCannotCommitLateResult(t *testing.T) {
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	started, release := make(chan struct{}), make(chan struct{})
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
		close(started)
		<-release
		return document(), nil
	}))
	done := make(chan error, 1)
	go func() { _, err := stage.Parse(f.ctx, job); done <- err }()
	select {
	case <-started:
	case <-f.ctx.Done():
		close(release)
		t.Fatal("parser did not start")
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := documents.Cancel(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, f.kbID, uploaded.DocumentID, uploaded.VersionID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("cancelled worker committed late result: %v", err)
	}
	var jobState, versionState, failureCode string
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status,v.failure_code,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.tenant_id=$1 AND j.id=$3 AND v.id=$2`, f.tenant, uploaded.VersionID, job.Lease.JobID).Scan(&jobState, &versionState, &failureCode, &artifacts); err != nil || jobState != "cancelled" || versionState != "failed" || failureCode != "cancelled" || artifacts != 0 {
		t.Fatalf("late parse outcome: job=%s version=%s failure=%s artifacts=%d err=%v", jobState, versionState, failureCode, artifacts, err)
	}
}

func TestExpiredFinalAttemptDoesNotLeaveVersionInProgress(t *testing.T) {
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
		return document(), nil
	}))
	if _, err := stage.Parse(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET attempts=max_attempts,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	check := func(wantJob, wantVersion string) {
		t.Helper()
		var jobState, versionState string
		if err := f.admin.QueryRow(f.ctx, `SELECT j.status,v.status FROM job j JOIN document_version v ON v.tenant_id=j.tenant_id WHERE j.id=$1 AND v.id=$2`, job.Lease.JobID, uploaded.VersionID).Scan(&jobState, &versionState); err != nil || jobState != wantJob || versionState != wantVersion {
			t.Fatalf("recovery states: job=%s version=%s err=%v", jobState, versionState, err)
		}
	}
	if _, err := f.queue.Claim(f.ctx, f.tenant, "replacement", time.Minute); !errors.Is(err, jobs.ErrNeedsEffects) {
		t.Fatalf("uncoordinated dead-letter: %v", err)
	}
	check("running", "embedding")
	injected := errors.New("recovery effect failed")
	if _, err := f.queue.ClaimWithRecovery(f.ctx, f.tenant, "replacement", time.Minute, func(ctx context.Context, tx jobs.EffectsTx, expired jobs.ExpiredJob) error {
		if err := stage.Expire(ctx, tx, expired); err != nil {
			return err
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("recovery failure was hidden: %v", err)
	}
	check("running", "embedding")
	if _, err := f.queue.ClaimWithRecovery(f.ctx, f.tenant, "replacement", time.Minute, stage.Expire); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatalf("final attempt recovery: %v", err)
	}
	check("dead", "failed")
	var failureCode string
	if err := f.admin.QueryRow(f.ctx, `SELECT failure_code FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&failureCode); err != nil || failureCode != string(jobs.LeaseExpired) {
		t.Fatalf("exhausted failure: code=%s err=%v", failureCode, err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, nil); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("exhausted worker committed: %v", err)
	}
}

func TestParseCheckpointSurvivesRetryableFailure(t *testing.T) {
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	var calls atomic.Int32
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
		calls.Add(1)
		return document(), nil
	}))
	if _, err := stage.Parse(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	ids := documentIDs{KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}
	var failure *jobs.ProcessingFailure
	if !errors.As(stage.failure(job, ids, jobs.ModelUnavailable, errors.New("upstream unavailable")), &failure) {
		t.Fatal("missing failure")
	}
	status, err := f.queue.FailWithEffects(f.ctx, job.Lease, failure.Code, failure.Apply)
	if err != nil || status != jobs.Queued {
		t.Fatalf("retry state=%s err=%v", status, err)
	}
	var versionStatus, failureCode string
	if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&versionStatus, &failureCode); err != nil || versionStatus != "queued" || failureCode != string(jobs.ModelUnavailable) {
		t.Fatalf("version retry state=%s failure=%s err=%v", versionStatus, failureCode, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET run_after=clock_timestamp()-interval '1 second' WHERE id=$1`, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.queue.Claim(f.ctx, f.tenant, "retry-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Parse(f.ctx, retry); err != nil || calls.Load() != 1 {
		t.Fatalf("reparsed on retry: calls=%d err=%v", calls.Load(), err)
	}
	var failureCleared bool
	if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code IS NULL FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&versionStatus, &failureCleared); err != nil || versionStatus != "embedding" || !failureCleared {
		t.Fatalf("resumed version=%s cleared=%t err=%v", versionStatus, failureCleared, err)
	}
}

func TestParseFailureUpdatesVersionAndJobAtomically(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
			key := storage.Key{TenantID: f.tenant, KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: uploaded.VersionID}
			expected := jobs.ParseError
			if mode == "missing" {
				if err := f.files.Delete(f.ctx, key); err != nil {
					t.Fatal(err)
				}
				expected = jobs.FileMissing
			}
			if mode == "corrupt" {
				name, err := key.Path()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(name)), []byte("altered"), 0600); err != nil {
					t.Fatal(err)
				}
				expected = jobs.FileCorrupt
			}
			stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
				return parser.Document{}, parser.ErrParse
			}))
			_, err := stage.Parse(f.ctx, job)
			var failure *jobs.ProcessingFailure
			if !errors.As(err, &failure) || failure.Code != expected || failure.Apply == nil {
				t.Fatalf("failure=%v", err)
			}
			status, err := f.queue.FailWithEffects(f.ctx, job.Lease, failure.Code, failure.Apply)
			if err != nil || status != jobs.Failed {
				t.Fatalf("fail status=%s err=%v", status, err)
			}
			var versionStatus, failureCode string
			var count int
			if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&versionStatus, &failureCode, &count); err != nil || versionStatus != "failed" || failureCode != string(expected) || count != 0 {
				t.Fatalf("version=%s failure=%s artifacts=%d err=%v", versionStatus, failureCode, count, err)
			}
		})
	}
}

func TestParseStageWithRealPython(t *testing.T) {
	bwrap, cgroupRoot := os.Getenv("TUSKRAG_TEST_BWRAP"), os.Getenv("TUSKRAG_TEST_CGROUP")
	runtimeDir, executable := os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"), os.Getenv("TUSKRAG_TEST_PARSER")
	if bwrap == "" || cgroupRoot == "" || runtimeDir == "" || executable == "" {
		t.Skip("isolated Python sandbox is not configured")
	}
	f := setup(t)
	_, job := f.upload(t, "城市,限额\n北京,600\n上海,600\n")
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{Bubblewrap: bwrap, CgroupRoot: cgroupRoot, RuntimeDir: runtimeDir, Executable: executable, TempDir: t.TempDir(), MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	documentParser, err := parser.NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	stage := makeStage(t, f, documentParser)
	parsed, err := stage.Parse(f.ctx, job)
	if err != nil || len(parsed.Pages) != 1 || len(parsed.Pages[0].Blocks) != 1 || len(parsed.Pages[0].Blocks[0].Cells) != 3 || len(parsed.Pages[0].Blocks[0].Cells[1]) != 2 || parsed.Pages[0].Blocks[0].Cells[1][1] != "600" {
		t.Fatalf("real parse failed: %+v err=%v", parsed, err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil || len(normalized.Sections) != 1 || normalized.Sections[0].Level != 0 ||
		len(normalized.Sections[0].Blocks) != 1 || len(normalized.Sections[0].Blocks[0].Cells) < 2 ||
		len(normalized.Sections[0].Blocks[0].Cells[1]) < 2 || normalized.Sections[0].Blocks[0].Cells[1][1] != "600" {
		t.Fatalf("CSV section assembly failed: %+v err=%v", normalized, err)
	}
	checkNormalizedRoundTrip(t, parsed, normalized)
	checkParsedTableChunks(t, normalized.Sections[0].Blocks[0], normalized.Sections[0].HeadingPath, "text/csv")
}

func TestParseStageWithRealDOCX(t *testing.T) {
	bwrap, cgroupRoot := os.Getenv("TUSKRAG_TEST_BWRAP"), os.Getenv("TUSKRAG_TEST_CGROUP")
	runtimeDir, executable := os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"), os.Getenv("TUSKRAG_TEST_PARSER")
	if bwrap == "" || cgroupRoot == "" || runtimeDir == "" || executable == "" {
		t.Skip("isolated Python sandbox is not configured")
	}
	f := setup(t)
	content, err := os.ReadFile("../../../../services/parser/tests/fixtures/docx/policy.docx")
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "policy.docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: bytes.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "docx-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{Bubblewrap: bwrap, CgroupRoot: cgroupRoot, RuntimeDir: runtimeDir, Executable: executable, TempDir: t.TempDir(), MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	documentParser, err := parser.NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	stage := makeStage(t, f, documentParser)
	parsed, err := stage.Parse(f.ctx, job)
	if err != nil || len(parsed.Pages) != 1 || len(parsed.Pages[0].Blocks) != 6 ||
		parsed.Pages[0].Blocks[0].Text == nil || *parsed.Pages[0].Blocks[0].Text != "差旅管理办法" {
		t.Fatalf("DOCX ingest parse failed: %+v err=%v", parsed, err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil || len(normalized.Sections) != 2 || len(normalized.Sections[0].Blocks) != 1 || normalized.Sections[1].ParentIndex != 0 || len(normalized.Sections[1].Blocks) != 3 {
		t.Fatalf("DOCX section assembly failed: %+v err=%v", normalized, err)
	}
	checkNormalizedRoundTrip(t, parsed, normalized)
	checkParsedTableChunks(t, normalized.Sections[1].Blocks[1], normalized.Sections[1].HeadingPath, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	checkParsedTextChunks(t, normalized.Sections[0].Blocks[0], normalized.Sections[0].HeadingPath)
	var status string
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT status,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &artifacts); err != nil || status != "embedding" || artifacts != 1 {
		t.Fatalf("DOCX checkpoint: status=%s artifacts=%d err=%v", status, artifacts, err)
	}
	encrypted, err := os.ReadFile("../../../../services/parser/tests/fixtures/docx/encrypted.docx")
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err = documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "encrypted.docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: bytes.NewReader(encrypted),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = f.queue.Claim(f.ctx, f.tenant, "docx-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stage.Parse(f.ctx, job)
	var failure *jobs.ProcessingFailure
	if !errors.As(err, &failure) || failure.Code != jobs.Encrypted {
		t.Fatalf("encrypted DOCX failure: %v", err)
	}
	if _, err := f.queue.FailWithEffects(f.ctx, job.Lease, failure.Code, failure.Apply); err != nil {
		t.Fatal(err)
	}
	var failureCode string
	if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &failureCode, &artifacts); err != nil || status != "failed" || failureCode != string(jobs.Encrypted) || artifacts != 0 {
		t.Fatalf("encrypted DOCX state: status=%s failure=%s artifacts=%d err=%v", status, failureCode, artifacts, err)
	}
}

func TestParseStageWithRealXLSX(t *testing.T) {
	bwrap, cgroupRoot := os.Getenv("TUSKRAG_TEST_BWRAP"), os.Getenv("TUSKRAG_TEST_CGROUP")
	runtimeDir, executable := os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"), os.Getenv("TUSKRAG_TEST_PARSER")
	if bwrap == "" || cgroupRoot == "" || runtimeDir == "" || executable == "" {
		t.Skip("isolated Python sandbox is not configured")
	}
	f := setup(t)
	content, err := os.ReadFile("../../../../services/parser/tests/fixtures/xlsx/travel.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "travel.xlsx", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Content: bytes.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "xlsx-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{Bubblewrap: bwrap, CgroupRoot: cgroupRoot, RuntimeDir: runtimeDir, Executable: executable, TempDir: t.TempDir(), MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	documentParser, err := parser.NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	stage := makeStage(t, f, documentParser)
	parsed, err := stage.Parse(f.ctx, job)
	if err != nil || len(parsed.Pages) != 2 || len(parsed.Pages[1].Blocks) != 2 ||
		len(parsed.Pages[1].Blocks[1].Cells) != 3 || len(parsed.Pages[1].Blocks[1].Cells[2]) != 3 || parsed.Pages[1].Blocks[1].Cells[2][0] != "华东" {
		t.Fatalf("XLSX ingest parse failed: %+v err=%v", parsed, err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil || len(normalized.Sections) != 2 || len(normalized.Sections[1].Blocks) != 1 ||
		len(normalized.Sections[1].Blocks[0].Cells) != 3 || len(normalized.Sections[1].Blocks[0].Cells[2]) == 0 ||
		normalized.Sections[1].Blocks[0].Cells[2][0] != "华东" {
		t.Fatalf("XLSX section assembly failed: %+v err=%v", normalized, err)
	}
	checkNormalizedRoundTrip(t, parsed, normalized)
	checkParsedTableChunks(t, normalized.Sections[1].Blocks[0], normalized.Sections[1].HeadingPath, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	var status string
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT status,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &artifacts); err != nil || status != "embedding" || artifacts != 1 {
		t.Fatalf("XLSX checkpoint: status=%s artifacts=%d err=%v", status, artifacts, err)
	}
}

func TestParseStageWithNativeText(t *testing.T) {
	for _, item := range []struct {
		name, mime, profile, heading string
		blocks                       int
	}{
		{"policy.md", "text/markdown", "tuskrag-core/native-markdown-v3;goldmark=1.8.6", "差旅管理办法", 5},
		{"policy.txt", "text/plain", "tuskrag-core/native-txt-v1", "第一章 总则", 4},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := setup(t)
			content, err := os.ReadFile(filepath.Join("../parser/testdata/native-text", item.name))
			if err != nil {
				t.Fatal(err)
			}
			knowledge, err := kb.NewService(f.pool)
			if err != nil {
				t.Fatal(err)
			}
			documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
			if err != nil {
				t.Fatal(err)
			}
			uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
				KBID: f.kbID, Title: item.name, MIME: item.mime, Content: bytes.NewReader(content),
			})
			if err != nil {
				t.Fatal(err)
			}
			job, err := f.queue.Claim(f.ctx, f.tenant, "native-text-worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			stage := makeStage(t, f, nativePipelineParser(t))
			parsed, err := stage.Parse(f.ctx, job)
			if err != nil || len(parsed.Pages) != 1 || len(parsed.Pages[0].Blocks) != item.blocks ||
				parsed.Pages[0].Blocks[0].Text == nil || *parsed.Pages[0].Blocks[0].Text != item.heading || parsed.Meta.ParserProfile != item.profile {
				t.Fatalf("native text parse: %+v err=%v", parsed, err)
			}
			normalized, err := AssembleMarkdown(f.ctx, parsed)
			if err != nil || len(normalized.Sections) != 2 || normalized.Sections[1].ParentIndex != 0 {
				t.Fatalf("native text section assembly: %+v err=%v", normalized, err)
			}
			if len(normalized.Sections[0].Blocks) == 0 {
				t.Fatal("native text section has no paragraph")
			}
			checkParsedTextChunks(t, normalized.Sections[0].Blocks[0], normalized.Sections[0].HeadingPath)
			var status string
			var artifacts int
			if err := f.admin.QueryRow(f.ctx, `SELECT status,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &artifacts); err != nil || status != "embedding" || artifacts != 1 {
				t.Fatalf("native text checkpoint: status=%s artifacts=%d err=%v", status, artifacts, err)
			}
			if replay, err := stage.Parse(f.ctx, job); err != nil || replay.Meta.ParserProfile != item.profile {
				t.Fatalf("native text checkpoint replay: %+v err=%v", replay, err)
			}
		})
	}
}

func TestParseStageWithNestedOrderedList(t *testing.T) {
	f := setup(t)
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	content := "# 差旅制度\n\n12. 报销细则。\n\n    100. 第一项内容。\n    101. 第二项内容。\n    102. 第三项内容。\n"
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "ordered.md", MIME: "text/markdown", Content: strings.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "structured-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stage := makeStage(t, f, nativePipelineParser(t))
	parsed, err := stage.Parse(f.ctx, job)
	if err != nil || parsed.Meta.ParserProfile != "tuskrag-core/native-markdown-v3;goldmark=1.8.6" {
		t.Fatalf("parse ordered list: %v", err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil || len(normalized.Sections) != 1 || len(normalized.Sections[0].Blocks) != 1 {
		t.Fatalf("assemble ordered list: %+v err=%v", normalized, err)
	}
	checkNormalizedRoundTrip(t, parsed, normalized)
	section := normalized.Sections[0]
	chunks, err := SplitList(f.ctx, StructuredSplitRequest{Block: section.Blocks[0], HeadingPath: section.HeadingPath, MaxTokens: 33, CountTokens: countRunes})
	if err != nil || len(chunks) != 3 || chunks[0].FirstSubitem != 1 || chunks[2].LastSubitem != 3 {
		t.Fatalf("split checkpoint list: chunks=%d err=%v", len(chunks), err)
	}
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status); err != nil || status != "embedding" {
		t.Fatalf("checkpoint status=%s err=%v", status, err)
	}
	if replay, err := stage.Parse(f.ctx, job); err != nil || !reflect.DeepEqual(replay, parsed) {
		t.Fatalf("checkpoint replay changed: %v", err)
	}
}

func TestParseStageNativeTextFailureIsRecorded(t *testing.T) {
	f := setup(t)
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "unsupported.md", MIME: "text/markdown", Content: strings.NewReader("# 标题\n\n![图片](image.png)\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "native-text-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = makeStage(t, f, nativePipelineParser(t)).Parse(f.ctx, job)
	var failure *jobs.ProcessingFailure
	if !errors.As(err, &failure) || failure.Code != jobs.Unsupported || failure.Apply == nil {
		t.Fatalf("native text failure: %v", err)
	}
	if _, err := f.queue.FailWithEffects(f.ctx, job.Lease, failure.Code, failure.Apply); err != nil {
		t.Fatal(err)
	}
	var status, code string
	var artifacts int
	if err := f.admin.QueryRow(f.ctx, `SELECT status,failure_code,(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$2) FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&status, &code, &artifacts); err != nil || status != "failed" || code != string(jobs.Unsupported) || artifacts != 0 {
		t.Fatalf("native text failure checkpoint: status=%s code=%s artifacts=%d err=%v", status, code, artifacts, err)
	}
}
