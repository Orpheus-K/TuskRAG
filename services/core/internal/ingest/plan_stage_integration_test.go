package ingest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/parser"
)

func preparePlanFixture(t *testing.T) (*fixture, jobs.ClaimedJob, kb.UploadedDocument, PreparedPlan) {
	t.Helper()
	f := setup(t)
	uploaded, job := f.upload(t, "城市,限额\n北京,600\n")
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) { return document(), nil }))
	if _, err := stage.Parse(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	var space, provider int64
	var revision int32
	var model, distance string
	var dimensions int32
	var normalized bool
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id, k.config_revision, e.provider_id, e.model, e.dims, e.normalized, e.distance
		FROM knowledge_base k JOIN embedding_space e ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id
		WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&space, &revision, &provider, &model, &dimensions, &normalized, &distance); err != nil {
		t.Fatal(err)
	}
	plan := samplePreparedPlan(space)
	plan.ConfigRevision = revision
	plan.EmbeddingProfile = EmbeddingProfile{ProviderID: provider, Model: model, Dimensions: dimensions, Normalized: normalized, Distance: distance}
	return f, job, uploaded, plan
}

func TestPlanStageWritesNonCurrentRowsAndReplays(t *testing.T) {
	f, job, uploaded, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.Stage(f.ctx, job, plan)
	if err != nil || result.Reused || len(result.Chunks) != 2 || result.Chunks[0].Ordinal != 1 || result.Chunks[1].Ordinal != 2 || result.Chunks[0].ID <= 0 {
		t.Fatalf("stage result=%+v err=%v", result, err)
	}
	var sectionCount, chunkCount, planCount, currentCount, vectorCount int
	var status string
	var noCurrent bool
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1),
		v.status, d.current_version_id IS NULL
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID).Scan(&sectionCount, &chunkCount, &planCount, &currentCount, &vectorCount, &status, &noCurrent); err != nil {
		t.Fatal(err)
	}
	if sectionCount != 2 || chunkCount != 2 || planCount != 1 || currentCount != 0 || vectorCount != 0 || status != "embedding" || !noCurrent {
		t.Fatalf("stage published or lost rows: sections=%d chunks=%d plans=%d current=%d vectors=%d status=%s", sectionCount, chunkCount, planCount, currentCount, vectorCount, status)
	}
	var childParent, rootID int64
	var accessTags []string
	var mime, content, search string
	if err := f.admin.QueryRow(f.ctx, `SELECT child.parent_section_id, root.id, c.access_tags, c.doc_mime, c.content, c.search_text
		FROM section child JOIN section root ON root.id=child.parent_section_id
		JOIN chunk c ON c.section_id=child.id AND c.ordinal=1
		WHERE child.tenant_id=$1 AND child.version_id=$2 AND child.ordinal=2`, f.tenant, uploaded.VersionID).Scan(&childParent, &rootID, &accessTags, &mime, &content, &search); err != nil {
		t.Fatal(err)
	}
	if childParent != rootID || !reflect.DeepEqual(accessTags, []string{fmt.Sprintf("kb:%d:all", f.kbID)}) || mime != "text/csv" ||
		content != plan.Chunks[0].Content || search != plan.Chunks[0].SearchText {
		t.Fatal("staged section relationship or filtering fields changed")
	}
	replay, err := stage.Stage(f.ctx, job, plan)
	if err != nil || !replay.Reused || !reflect.DeepEqual(replay.Chunks, result.Chunks) {
		t.Fatalf("replay result=%+v err=%v", replay, err)
	}
	changed := samplePreparedPlan(plan.EmbeddingSpaceID)
	changed.EmbeddingProfile = plan.EmbeddingProfile
	changed.Chunks[0].Content = "修改。"
	changed.Chunks[0].SearchText = "制度 › 差旅\n修改。"
	if conflict, err := stage.Stage(f.ctx, job, changed); !errors.Is(err, ErrPlanConflict) || len(conflict.Chunks) != 0 {
		t.Fatalf("different replay accepted: %+v err=%v", conflict, err)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id', $1, true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM ingest_plan WHERE version_id=$1`, uploaded.VersionID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign tenant saw staged plan: count=%d err=%v", visible, err)
	}
}

func TestPlanStageConcurrentReplayUsesSameRows(t *testing.T) {
	f, job, _, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result StagedPlan
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := stage.Stage(f.ctx, job, plan)
			results <- outcome{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var outcomes []outcome
	for result := range results {
		outcomes = append(outcomes, result)
	}
	if len(outcomes) != 2 || outcomes[0].err != nil || outcomes[1].err != nil || outcomes[0].result.Reused == outcomes[1].result.Reused ||
		!reflect.DeepEqual(outcomes[0].result.Chunks, outcomes[1].result.Chunks) {
		t.Fatalf("concurrent stage did not converge: %+v", outcomes)
	}
}

func TestPlanStagePreservesUntrustedText(t *testing.T) {
	f, job, _, plan := preparePlanFixture(t)
	content := "引号'、反斜线\\、<script>、& 和 🙂\n第二行"
	plan.Chunks[0].Content = content
	plan.Chunks[0].SearchText = "制度 › 差旅\n" + content
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.Stage(f.ctx, job, plan)
	if err != nil || len(result.Chunks) != 2 {
		t.Fatalf("stage untrusted text: %v", err)
	}
	var gotContent, gotSearch string
	if err := f.admin.QueryRow(f.ctx, `SELECT content, search_text FROM chunk WHERE tenant_id=$1 AND id=$2`, f.tenant, result.Chunks[0].ID).Scan(&gotContent, &gotSearch); err != nil {
		t.Fatal(err)
	}
	if gotContent != content || gotSearch != plan.Chunks[0].SearchText {
		t.Fatal("staged text changed during JSON and PostgreSQL round trip")
	}
}

func TestPlanStageRejectsStaleLeaseAndConfiguration(t *testing.T) {
	f, job, uploaded, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.ConfigRevision++
	if _, err := stage.Stage(f.ctx, job, changed); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("stale configuration accepted: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	newJob, err := f.queue.Claim(f.ctx, f.tenant, "new-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stage(f.ctx, job, plan); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale lease accepted: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2`, f.tenant, uploaded.VersionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale lease wrote sections: count=%d err=%v", count, err)
	}
	if _, err := stage.Stage(f.ctx, newJob, plan); err != nil {
		t.Fatalf("current lease could not stage plan: %v", err)
	}
}

func TestIngestRejectsForgedJobPayload(t *testing.T) {
	f := setup(t)
	_, first := f.upload(t, "城市,限额\n北京,600\n")
	secondUpload, second := f.upload(t, "城市,限额\n上海,500\n")
	called := false
	stage := makeStage(t, f, parserFunc(func(context.Context, parser.ParseInput) (parser.Document, error) {
		called = true
		return document(), nil
	}))
	forged := first
	forged.Payload = second.Payload
	if _, err := stage.Parse(f.ctx, forged); !errors.Is(err, ErrJobMismatch) || called {
		t.Fatalf("forged parse accepted: called=%t err=%v", called, err)
	}
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenant, secondUpload.VersionID).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("forged parse changed target: status=%s err=%v", status, err)
	}
	if _, err := stage.Parse(f.ctx, second); err != nil {
		t.Fatal(err)
	}
	var space int64
	if err := f.admin.QueryRow(f.ctx, `SELECT embedding_space_id FROM knowledge_base WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID).Scan(&space); err != nil {
		t.Fatal(err)
	}
	planStage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planStage.Stage(f.ctx, forged, samplePreparedPlan(space)); !errors.Is(err, ErrJobMismatch) {
		t.Fatalf("forged staging accepted: %v", err)
	}
	var rows int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2`, f.tenant, secondUpload.VersionID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("forged staging wrote a plan: rows=%d err=%v", rows, err)
	}
}

func TestPlanStageRollsBackDatabaseFailureWithoutLeakingContent(t *testing.T) {
	f, job, uploaded, plan := preparePlanFixture(t)
	if _, err := f.admin.Exec(f.ctx, `CREATE FUNCTION tuskrag_test_reject_plan_chunk() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'private document content'; END $$;
		CREATE TRIGGER tuskrag_test_reject_plan_chunk BEFORE INSERT ON chunk FOR EACH ROW EXECUTE FUNCTION tuskrag_test_reject_plan_chunk()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS tuskrag_test_reject_plan_chunk ON chunk; DROP FUNCTION IF EXISTS tuskrag_test_reject_plan_chunk()`); err != nil {
			t.Error(err)
		}
	})
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stage(f.ctx, job, plan); err == nil || strings.Contains(err.Error(), "private document content") {
		t.Fatalf("database failure hidden or leaked content: %v", err)
	}
	var sections, chunks, plans int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2)`, f.tenant, uploaded.VersionID).Scan(&sections, &chunks, &plans); err != nil || sections != 0 || chunks != 0 || plans != 0 {
		t.Fatalf("partial stage persisted: sections=%d chunks=%d plans=%d err=%v", sections, chunks, plans, err)
	}
	if _, err := f.admin.Exec(f.ctx, `DROP TRIGGER tuskrag_test_reject_plan_chunk ON chunk; DROP FUNCTION tuskrag_test_reject_plan_chunk()`); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stage(f.ctx, job, plan); err != nil {
		t.Fatalf("retry after rollback failed: %v", err)
	}
}

func TestPlanStageRollsBackWhenLeaseExpiresDuringWrite(t *testing.T) {
	f, job, uploaded, plan := preparePlanFixture(t)
	if _, err := f.admin.Exec(f.ctx, `CREATE FUNCTION tuskrag_test_expire_plan_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN UPDATE job SET lease_until=clock_timestamp()-interval '1 second'
		WHERE tenant_id=NEW.tenant_id AND kind='ingest' AND status='running'; RETURN NEW; END $$;
		CREATE TRIGGER tuskrag_test_expire_plan_lease BEFORE INSERT ON section FOR EACH ROW EXECUTE FUNCTION tuskrag_test_expire_plan_lease()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS tuskrag_test_expire_plan_lease ON section; DROP FUNCTION IF EXISTS tuskrag_test_expire_plan_lease()`); err != nil {
			t.Error(err)
		}
	})
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stage(f.ctx, job, plan); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired transaction committed: %v", err)
	}
	var sections, chunks, plans int
	var validLease bool
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2),
		(SELECT lease_until > clock_timestamp() FROM job WHERE tenant_id=$1 AND id=$3)`, f.tenant, uploaded.VersionID, job.Lease.JobID).Scan(&sections, &chunks, &plans, &validLease); err != nil || sections != 0 || chunks != 0 || plans != 0 || !validLease {
		t.Fatalf("expired write did not roll back: sections=%d chunks=%d plans=%d lease=%t err=%v", sections, chunks, plans, validLease, err)
	}
	if _, err := f.admin.Exec(f.ctx, `DROP TRIGGER tuskrag_test_expire_plan_lease ON section; DROP FUNCTION tuskrag_test_expire_plan_lease()`); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stage(f.ctx, job, plan); err != nil {
		t.Fatalf("retry after expiry rollback failed: %v", err)
	}
}

func TestPlanStageDetectsMissingStagedRows(t *testing.T) {
	f, job, _, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM chunk WHERE tenant_id=$1 AND id=$2`, f.tenant, result.Chunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if replay, err := stage.Stage(f.ctx, job, plan); !errors.Is(err, ErrPlanCorrupt) || len(replay.Chunks) != 0 {
		t.Fatalf("incomplete checkpoint accepted: %+v err=%v", replay, err)
	}
}

func TestPlanStageDetectsUnsafeStagedFlags(t *testing.T) {
	f, job, _, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE chunk SET is_current=true WHERE tenant_id=$1 AND id=$2`,
		`UPDATE chunk SET disabled=true WHERE tenant_id=$1 AND id=$2`,
		`UPDATE chunk SET access_tags=ARRAY['u:999'] WHERE tenant_id=$1 AND id=$2`,
	} {
		if _, err := f.admin.Exec(f.ctx, query, f.tenant, result.Chunks[0].ID); err != nil {
			t.Fatal(err)
		}
		if replay, err := stage.Stage(f.ctx, job, plan); !errors.Is(err, ErrPlanCorrupt) || len(replay.Chunks) != 0 {
			t.Fatalf("unsafe staged chunk accepted: %+v err=%v", replay, err)
		}
		if _, err := f.admin.Exec(f.ctx, `UPDATE chunk SET is_current=false, disabled=false, access_tags=ARRAY[$3] WHERE tenant_id=$1 AND id=$2`,
			f.tenant, result.Chunks[0].ID, fmt.Sprintf("kb:%d:all", f.kbID)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStageRejectsUnavailableTargets(t *testing.T) {
	for name, query := range map[string]string{
		"restricted": `UPDATE document SET acl_mode='restricted' WHERE tenant_id=$1 AND id=$2`,
		"deleted":    `UPDATE document SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`,
		"archived":   `UPDATE knowledge_base SET status='archived' WHERE tenant_id=$1 AND id=$2`,
	} {
		t.Run(name, func(t *testing.T) {
			f, job, uploaded, plan := preparePlanFixture(t)
			id := uploaded.DocumentID
			if name == "archived" {
				id = f.kbID
			}
			if _, err := f.admin.Exec(f.ctx, query, f.tenant, id); err != nil {
				t.Fatal(err)
			}
			stage, err := NewPlanStage(f.queue)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := stage.Stage(f.ctx, job, plan); !errors.Is(err, errTarget) || len(result.Chunks) != 0 {
				t.Fatalf("unavailable target accepted: %+v err=%v", result, err)
			}
			var count int
			if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2`, f.tenant, uploaded.VersionID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unavailable target produced plan: count=%d err=%v", count, err)
			}
		})
	}
}
