package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
)

// Publication needs real global indexes. Use a new database so the test never
// creates or removes indexes in an existing database.
func publicationDatabase(t *testing.T) {
	t.Helper()
	names := []string{"TUSKRAG_TEST_ADMIN_URL", "TUSKRAG_TEST_APP_URL", "TUSKRAG_TEST_MIGRATOR_URL"}
	parsed := make([]*url.URL, len(names))
	for i, name := range names {
		raw := os.Getenv(name)
		if raw == "" {
			t.Skip("publication integration test requires isolated PostgreSQL URLs")
		}
		u, err := url.Parse(raw)
		if err != nil || u == nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
			(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") ||
			!strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tuskrag_test_") {
			t.Fatal("publication test requires local tuskrag_test_ database URLs")
		}
		parsed[i] = u
		if i > 0 && (u.Host != parsed[0].Host || u.Path != parsed[0].Path) {
			t.Fatal("publication test URLs must address the same isolated database")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	root, err := pgx.Connect(ctx, parsed[0].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	database := "tuskrag_test_publication_" + hex.EncodeToString(random[:])
	var exists bool
	if err := root.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, database).Scan(&exists); err != nil || exists {
		t.Fatalf("fresh publication test database check: exists=%t err=%v", exists, err)
	}
	owner := parsed[2].User.Username()
	if owner == "" {
		t.Fatal("test migration role is required")
	}
	if _, err := root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()+" OWNER "+pgx.Identifier{owner}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
			t.Errorf("drop owned publication test database: %v", err)
		}
	})
	for _, u := range parsed {
		u.Path = "/" + database
	}
	admin, err := pgx.Connect(ctx, parsed[0].String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"CREATE EXTENSION vector", "CREATE EXTENSION pg_search", "ALTER SCHEMA public OWNER TO " + pgx.Identifier{owner}.Sanitize(), "REVOKE CREATE ON SCHEMA public FROM PUBLIC"} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			_ = admin.Close(context.Background())
			t.Fatal(err)
		}
	}
	if err := admin.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, parsed[2].String()); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		t.Setenv(name, parsed[i].String())
	}
}

func finishPublicationVectors(t *testing.T, f *fixture, job jobs.ClaimedJob, staged StagedPlan) {
	t.Helper()
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]VectorInput, len(staged.Chunks))
	for i := range inputs {
		inputs[i] = vectorInput(staged, i, testVector(1, 0))
	}
	if err := stage.Write(f.ctx, job, staged.Identity, inputs); err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); err != nil {
		t.Fatal(err)
	}
}

func preparePublication(t *testing.T, f *fixture, job jobs.ClaimedJob, plan PreparedPlan, staged StagedPlan) func(context.Context, jobs.EffectsTx) error {
	t.Helper()
	stage, err := NewPublicationStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := stage.Prepare(f.ctx, job, plan, staged)
	if err != nil {
		t.Fatal(err)
	}
	return effects
}

func TestPublicationAtomicSwitchPostgres(t *testing.T) {
	publicationDatabase(t)
	f, firstJob, uploaded, firstPlan, firstStaged := prepareVectorFixture(t)
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.DocumentID); err != nil {
			t.Errorf("clear publication test pointer: %v", err)
		}
	})
	finishPublicationVectors(t, f, firstJob, firstStaged)
	firstEffects := preparePublication(t, f, firstJob, firstPlan, firstStaged)
	checkPublicationTransaction(t, f, firstJob, firstEffects)
	if err := f.queue.Complete(f.ctx, firstJob.Lease, firstEffects); !errors.Is(err, indexing.ErrIndexMissing) {
		t.Fatalf("publication without indexes: %v", err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, uploaded.VersionID, 0, "indexing", "running", 0)
	if err := indexing.Provision(f.ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, firstPlan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	checkPublicationTampering(t, f, firstJob, uploaded.VersionID, firstPlan, firstStaged, firstEffects)
	checkPublicationAuditFailure(t, f, firstJob, uploaded.DocumentID, uploaded.VersionID, firstEffects)
	checkPublicationLeaseExpiry(t, f, firstJob, uploaded.DocumentID, uploaded.VersionID, firstEffects)
	oldHeading, oldContent, oldChunkID := firstPlan.Sections[1].HeadingPath[0], firstPlan.Chunks[0].Content, firstStaged.Chunks[0].ID
	firstPlan.Sections[1].HeadingPath[0], firstPlan.Chunks[0].Content, firstStaged.Chunks[0].ID = "mutated", "mutated", -1
	err := f.queue.Complete(f.ctx, firstJob.Lease, firstEffects)
	firstPlan.Sections[1].HeadingPath[0], firstPlan.Chunks[0].Content, firstStaged.Chunks[0].ID = oldHeading, oldContent, oldChunkID
	if err != nil {
		t.Fatalf("publish first version: %v", err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, uploaded.VersionID, uploaded.VersionID, "ready", "succeeded", 2)
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, uploaded.VersionID, "申请")
	assertVisibleVersion(t, f, f.foreign, uploaded.DocumentID, 0, "申请")
	assertVisibleVectorVersion(t, f, f.tenant, uploaded.DocumentID, firstPlan.EmbeddingSpaceID, uploaded.VersionID)

	secondJob, secondPlan, secondStaged, secondVersion := prepareRevision(t, f, uploaded.DocumentID, uploaded.VersionID, firstPlan, "second")
	secondEffects := preparePublication(t, f, secondJob, secondPlan, secondStaged)
	if _, err := f.admin.Exec(f.ctx, `UPDATE ingest_plan SET chunk_count=chunk_count+1 WHERE tenant_id=$1 AND version_id=$2`, f.tenant, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, secondJob.Lease, secondEffects); !errors.Is(err, ErrPlanCorrupt) {
		t.Fatalf("corrupt current version was accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE ingest_plan SET chunk_count=chunk_count-1 WHERE tenant_id=$1 AND version_id=$2`, f.tenant, uploaded.VersionID); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected publication rollback")
	err = f.queue.Complete(f.ctx, secondJob.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := secondEffects(ctx, tx); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("publication rollback: %v", err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, secondVersion, uploaded.VersionID, "indexing", "running", 2)
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, uploaded.VersionID, "申请")
	assertVisibleVectorVersion(t, f, f.tenant, uploaded.DocumentID, firstPlan.EmbeddingSpaceID, uploaded.VersionID)
	assertCleanupJobs(t, f, uploaded.DocumentID, uploaded.VersionID, 0)

	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	done := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	go func() {
		defer close(done)
		result <- f.queue.Complete(f.ctx, secondJob.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
			if err := secondEffects(ctx, tx); err != nil {
				return err
			}
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("publication failed before switch: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, uploaded.VersionID, "申请")
	assertVisibleVectorVersion(t, f, f.tenant, uploaded.DocumentID, firstPlan.EmbeddingSpaceID, uploaded.VersionID)
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("commit publication: %v", err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, secondVersion, secondVersion, "ready", "succeeded", 2)
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, secondVersion, "审批")
	assertVisibleVersion(t, f, f.foreign, uploaded.DocumentID, 0, "审批")
	assertVisibleVectorVersion(t, f, f.tenant, uploaded.DocumentID, firstPlan.EmbeddingSpaceID, secondVersion)
	assertVisibleVectorVersion(t, f, f.foreign, uploaded.DocumentID, firstPlan.EmbeddingSpaceID, 0)
	assertCleanupJobs(t, f, uploaded.DocumentID, uploaded.VersionID, 1)
	var previousStatus string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, uploaded.VersionID).Scan(&previousStatus); err != nil || previousStatus != "superseded" {
		t.Fatalf("previous version status=%q err=%v", previousStatus, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET run_after=clock_timestamp()+interval '1 hour' WHERE tenant_id=$1 AND kind='purge'`, f.tenant); err != nil {
		t.Fatal(err)
	}
	lateJob, latePlan, lateStaged, lateVersion := prepareRevision(t, f, uploaded.DocumentID, secondVersion, firstPlan, "late")
	newestJob, newestPlan, newestStaged, newestVersion := prepareRevision(t, f, uploaded.DocumentID, secondVersion, firstPlan, "newest")
	if err := f.queue.Complete(f.ctx, newestJob.Lease, preparePublication(t, f, newestJob, newestPlan, newestStaged)); err != nil {
		t.Fatalf("publish newer pending version: %v", err)
	}
	if err := f.queue.Complete(f.ctx, lateJob.Lease, preparePublication(t, f, lateJob, latePlan, lateStaged)); !errors.Is(err, ErrVersionSuperseded) {
		t.Fatalf("late older version was accepted: %v", err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, lateVersion, newestVersion, "indexing", "running", 2)
	lateHandler := &IngestHandler{queue: f.queue}
	var lateFailure *jobs.ProcessingFailure
	if err := lateHandler.supersededFailure(lateJob, documentIDs{KBID: f.kbID, DocumentID: uploaded.DocumentID, VersionID: lateVersion}, ErrVersionSuperseded); !errors.As(err, &lateFailure) {
		t.Fatalf("missing late-version failure: %v", err)
	}
	if status, err := f.queue.FailWithEffects(f.ctx, lateJob.Lease, lateFailure.Code, lateFailure.Apply); err != nil || status != jobs.Failed {
		t.Fatalf("record late version failure status=%s err=%v", status, err)
	}
	assertPublicationState(t, f, uploaded.DocumentID, lateVersion, newestVersion, "superseded", "failed", 2)
	assertCleanupJobs(t, f, uploaded.DocumentID, lateVersion, 1)
	assertVisibleVersion(t, f, f.tenant, uploaded.DocumentID, newestVersion, "审批")
}

func checkPublicationTransaction(t *testing.T, f *fixture, job jobs.ClaimedJob, effects func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	if err := f.queue.WithLease(f.ctx, job.Lease, effects); !errors.Is(err, jobs.ErrCompletionRequired) {
		t.Fatalf("checkpoint transaction published a version: %v", err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := effects(f.ctx, tx); !errors.Is(err, jobs.ErrCompletionRequired) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("unfenced transaction published a version: %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.Enqueue(f.ctx, f.tenant, jobs.Request{Kind: jobs.EvalRun, Payload: json.RawMessage(`{}`), MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	other, err := f.queue.Claim(f.ctx, f.tenant, "unrelated-publication-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, other.Lease, effects); !errors.Is(err, jobs.ErrCompletionRequired) {
		t.Fatalf("unrelated job completion published a version: %v", err)
	}
}

func checkPublicationTampering(t *testing.T, f *fixture, job jobs.ClaimedJob, version int64, plan PreparedPlan, staged StagedPlan, effects func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	mutations := []struct {
		name, change, restore string
		id                    int64
		original              string
	}{
		{"section", `UPDATE section SET body_md='tampered' WHERE tenant_id=$1 AND version_id=$2 AND ordinal=2`, `UPDATE section SET body_md=$3 WHERE tenant_id=$1 AND version_id=$2 AND ordinal=2`, version, plan.Sections[1].BodyMarkdown},
		{"chunk", `UPDATE chunk SET content='tampered' WHERE tenant_id=$1 AND id=$2`, `UPDATE chunk SET content=$3 WHERE tenant_id=$1 AND id=$2`, staged.Chunks[0].ID, plan.Chunks[0].Content},
		{"vector", `UPDATE chunk_vector SET embedding=('[2' || repeat(',0',63) || ']')::halfvec WHERE tenant_id=$1 AND chunk_id=$2`, `UPDATE chunk_vector SET embedding=$3::halfvec WHERE tenant_id=$1 AND chunk_id=$2`, staged.Chunks[0].ID, "[1" + strings.Repeat(",0", 63) + "]"},
	}
	for _, mutation := range mutations {
		if _, err := f.admin.Exec(f.ctx, mutation.change, f.tenant, mutation.id); err != nil {
			t.Fatalf("tamper %s: %v", mutation.name, err)
		}
		if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, ErrPlanCorrupt) {
			t.Fatalf("tampered %s accepted: %v", mutation.name, err)
		}
		if _, err := f.admin.Exec(f.ctx, mutation.restore, f.tenant, mutation.id, mutation.original); err != nil {
			t.Fatalf("restore %s: %v", mutation.name, err)
		}
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM chunk_vector WHERE tenant_id=$1 AND chunk_id=$2`, f.tenant, staged.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, ErrVectorsIncomplete) {
		t.Fatalf("missing vector accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector (tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding)
		SELECT tenant_id,kb_id,id,$3,false,access_tags,$4::halfvec FROM chunk WHERE tenant_id=$1 AND id=$2`,
		f.tenant, staged.Chunks[0].ID, staged.Identity.EmbeddingSpaceID, "[1"+strings.Repeat(",0", 63)+"]"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Complete(f.ctx, job.Lease, effects); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired lease accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()+interval '1 minute' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
}

func checkPublicationAuditFailure(t *testing.T, f *fixture, job jobs.ClaimedJob, document, version int64, effects func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `REVOKE INSERT ON audit_log FROM tuskrag_app`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.admin.Exec(context.Background(), `GRANT INSERT ON audit_log TO tuskrag_app`); err != nil {
			t.Errorf("restore audit permission: %v", err)
		}
	}()
	if err := f.queue.Complete(f.ctx, job.Lease, effects); err == nil || !strings.Contains(err.Error(), "audit publication") {
		t.Fatalf("audit failure did not roll back publication: %v", err)
	}
	assertPublicationState(t, f, document, version, 0, "indexing", "running", 0)
}

func checkPublicationLeaseExpiry(t *testing.T, f *fixture, job jobs.ClaimedJob, document, version int64, effects func(context.Context, jobs.EffectsTx) error) {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()+interval '2 seconds' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	reached := false
	err := f.queue.Complete(f.ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := effects(ctx, tx); err != nil {
			return err
		}
		reached = true
		_, err := tx.Exec(ctx, `SELECT pg_sleep(2.2)`)
		return err
	})
	if !reached || !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("lease expiry inside publication was not fenced: reached=%t err=%v", reached, err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()+interval '1 minute' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	assertPublicationState(t, f, document, version, 0, "indexing", "running", 0)
}

func prepareRevision(t *testing.T, f *fixture, document, previous int64, original PreparedPlan, suffix string) (jobs.ClaimedJob, PreparedPlan, StagedPlan, int64) {
	t.Helper()
	var version int64
	digest := sha256.Sum256([]byte("synthetic revision " + suffix))
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO document_version (tenant_id,kb_id,document_id,content_sha256,byte_size,file_key,status,created_by)
		VALUES ($1,$2,$3,$4,1,$5,'embedding',$6) RETURNING id`, f.tenant, f.kbID, document, digest[:], "revision-test-"+suffix, f.user).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO parse_artifact (tenant_id,kb_id,document_id,version_id,content_sha256,parsed_document)
		SELECT tenant_id,kb_id,document_id,$1,$4,parsed_document FROM parse_artifact WHERE tenant_id=$2 AND version_id=$3`, version, f.tenant, previous, digest[:]); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(documentIDs{KBID: f.kbID, DocumentID: document, VersionID: version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.Enqueue(f.ctx, f.tenant, jobs.Request{Kind: jobs.Ingest, Payload: payload, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "publication-revision-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	plan := samplePreparedPlan(original.EmbeddingSpaceID)
	plan.ConfigRevision, plan.EmbeddingProfile = original.ConfigRevision, original.EmbeddingProfile
	plan.Sections[1].BodyMarkdown = "审批。\n\n核对。"
	plan.Chunks[0].Content, plan.Chunks[0].SearchText = "审批。", "制度 › 差旅\n审批。"
	plan.Chunks[1].Content, plan.Chunks[1].SearchText = "核对。", "制度 › 差旅\n核对。"
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	finishPublicationVectors(t, f, job, staged)
	return job, plan, staged, version
}

func assertPublicationState(t *testing.T, f *fixture, document, version, current int64, status, jobStatus string, currentChunks int) {
	t.Helper()
	var gotCurrent int64
	var gotStatus, gotJob string
	var chunks, vectors int
	err := f.admin.QueryRow(f.ctx, `SELECT COALESCE(d.current_version_id,0),v.status,
		(SELECT status FROM job WHERE tenant_id=$1 AND kind='ingest' AND (payload->>'version_id')::bigint=$3 ORDER BY id DESC LIMIT 1),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND document_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector cv JOIN chunk c ON c.tenant_id=cv.tenant_id AND c.id=cv.chunk_id
		 WHERE c.tenant_id=$1 AND c.document_id=$2 AND cv.is_current)
		FROM document d JOIN document_version v ON v.tenant_id=d.tenant_id AND v.document_id=d.id
		WHERE d.tenant_id=$1 AND d.id=$2 AND v.id=$3`, f.tenant, document, version).Scan(&gotCurrent, &gotStatus, &gotJob, &chunks, &vectors)
	if err != nil || gotCurrent != current || gotStatus != status || gotJob != jobStatus || chunks != currentChunks || vectors != currentChunks {
		t.Fatalf("publication state: current=%d status=%s job=%s chunks=%d vectors=%d err=%v", gotCurrent, gotStatus, gotJob, chunks, vectors, err)
	}
}

func assertCleanupJobs(t *testing.T, f *fixture, document, version int64, want int) {
	t.Helper()
	var count int
	err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM job WHERE tenant_id=$1 AND kind='purge' AND status='queued'
		AND (payload->>'document_id')::bigint=$2 AND (payload->>'version_id')::bigint=$3`, f.tenant, document, version).Scan(&count)
	if err != nil || count != want {
		t.Fatalf("cleanup jobs=%d want=%d err=%v", count, want, err)
	}
}

func assertVisibleVersion(t *testing.T, f *fixture, tenant, document, want int64, term string) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"SET LOCAL plan_cache_mode=force_custom_plan", "SET LOCAL enable_seqscan=off"} {
		if _, err := tx.Exec(f.ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(f.ctx, `SELECT version_id FROM chunk WHERE tenant_id=$1 AND document_id=$2 AND is_current AND search_text ||| $3`, tenant, document, term)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != want {
			t.Fatalf("unexpected visible version=%d want=%d", version, want)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if (want == 0 && count != 0) || (want != 0 && count != 1) {
		t.Fatalf("visible versions=%d want version=%d", count, want)
	}
}

func assertVisibleVectorVersion(t *testing.T, f *fixture, tenant, document, space, want int64) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(tenant)); err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"SET LOCAL plan_cache_mode=force_custom_plan", "SET LOCAL enable_seqscan=off", "SET LOCAL enable_sort=off"} {
		if _, err := tx.Exec(f.ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	vector := "[1" + strings.Repeat(",0", 63) + "]"
	rows, err := tx.Query(f.ctx, `SELECT c.version_id FROM chunk_vector v JOIN chunk c
		ON c.tenant_id=v.tenant_id AND c.kb_id=v.kb_id AND c.id=v.chunk_id
		WHERE v.tenant_id=$1 AND c.document_id=$2 AND v.space_id=$3 AND v.is_current AND c.is_current
		ORDER BY (v.embedding::halfvec(64)) <=> $4::halfvec(64) LIMIT 5`, tenant, document, space, vector)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != want {
			t.Fatalf("unexpected visible vector version=%d want=%d", version, want)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if (want == 0 && count != 0) || (want != 0 && count != 2) {
		t.Fatalf("visible vectors=%d want version=%d", count, want)
	}
}
