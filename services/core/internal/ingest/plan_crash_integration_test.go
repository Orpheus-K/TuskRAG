package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/jobs"
)

func TestPlanStageCrashHelper(t *testing.T) {
	if os.Getenv("TUSKRAG_PLAN_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	var job jobs.ClaimedJob
	if err := json.Unmarshal([]byte(os.Getenv("TUSKRAG_PLAN_CRASH_JOB")), &job); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(os.Getenv("TUSKRAG_TEST_APP_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["application_name"] = os.Getenv("TUSKRAG_PLAN_CRASH_APP")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	queue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	switch os.Getenv("TUSKRAG_PLAN_CRASH_MODE") {
	case "plan":
		var plan PreparedPlan
		if err := json.Unmarshal([]byte(os.Getenv("TUSKRAG_PLAN_CRASH_PAYLOAD")), &plan); err != nil {
			t.Fatal(err)
		}
		stage, err := NewPlanStage(queue)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stage.Stage(context.Background(), job, plan); err != nil {
			t.Fatalf("subprocess plan failed before termination: %v", err)
		}
	case "vectors":
		var payload struct {
			Identity PlanIdentity
			Batch    []VectorInput
		}
		if err := json.Unmarshal([]byte(os.Getenv("TUSKRAG_PLAN_CRASH_PAYLOAD")), &payload); err != nil {
			t.Fatal(err)
		}
		stage, err := NewVectorStage(queue)
		if err != nil {
			t.Fatal(err)
		}
		if err := stage.Write(context.Background(), job, payload.Identity, payload.Batch); err != nil {
			t.Fatalf("subprocess vectors failed before termination: %v", err)
		}
	default:
		t.Fatal("unknown crash helper mode")
	}
	t.Fatal("subprocess stage unexpectedly completed")
}

func TestPlanStageProcessCrashRollsBackAndRecovers(t *testing.T) {
	f, job, uploaded, plan := preparePlanFixture(t)
	runIngestCrash(t, f, job, "plan", plan, `CREATE FUNCTION tuskrag_test_block_plan_chunk() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(9715,NEW.tenant_id::integer); RETURN NEW; END $$;
		CREATE TRIGGER tuskrag_test_block_plan_chunk BEFORE INSERT ON chunk FOR EACH ROW EXECUTE FUNCTION tuskrag_test_block_plan_chunk()`,
		`DROP TRIGGER IF EXISTS tuskrag_test_block_plan_chunk ON chunk; DROP FUNCTION IF EXISTS tuskrag_test_block_plan_chunk()`)
	var sections, chunks, plans int
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM section WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2)`, f.tenant, uploaded.VersionID).Scan(&sections, &chunks, &plans); err != nil || sections != 0 || chunks != 0 || plans != 0 {
		t.Fatalf("process crash left partial rows: sections=%d chunks=%d plans=%d err=%v", sections, chunks, plans, err)
	}
	recovered := recoverCrashedJob(t, f, job)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := stage.Stage(f.ctx, recovered, plan)
	if err != nil || result.Reused || len(result.Chunks) != len(plan.Chunks) {
		t.Fatalf("stage after crash: %+v err=%v", result, err)
	}
}

func TestVectorStageProcessCrashRollsBackAndRecovers(t *testing.T) {
	f, job, uploaded, _, staged := prepareVectorFixture(t)
	batch := []VectorInput{vectorInput(staged, 0, testVector(1, 0)), vectorInput(staged, 1, testVector(0, 1))}
	payload := struct {
		Identity PlanIdentity
		Batch    []VectorInput
	}{Identity: staged.Identity, Batch: batch}
	runIngestCrash(t, f, job, "vectors", payload, `CREATE FUNCTION tuskrag_test_block_plan_vector() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(9715,NEW.tenant_id::integer); RETURN NEW; END $$;
		CREATE TRIGGER tuskrag_test_block_plan_vector BEFORE INSERT ON chunk_vector FOR EACH ROW EXECUTE FUNCTION tuskrag_test_block_plan_vector()`,
		`DROP TRIGGER IF EXISTS tuskrag_test_block_plan_vector ON chunk_vector; DROP FUNCTION IF EXISTS tuskrag_test_block_plan_vector()`)
	var vectors, chunks, plans int
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2),
		(SELECT count(*) FROM ingest_plan WHERE tenant_id=$1 AND version_id=$2),
		(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2)`, f.tenant, uploaded.VersionID).Scan(&vectors, &chunks, &plans, &status); err != nil || vectors != 0 || chunks != 2 || plans != 1 || status != "embedding" {
		t.Fatalf("vector crash changed checkpoint: vectors=%d chunks=%d plans=%d status=%s err=%v", vectors, chunks, plans, status, err)
	}
	recovered := recoverCrashedJob(t, f, job)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, recovered, staged.Identity, batch); err != nil {
		t.Fatalf("vector write after crash: %v", err)
	}
	if err := stage.Finish(f.ctx, recovered, staged.Identity); err != nil {
		t.Fatalf("vector finish after crash: %v", err)
	}
}

func runIngestCrash(t *testing.T, f *fixture, job jobs.ClaimedJob, mode string, payload any, createSQL, dropSQL string) {
	t.Helper()
	if f.tenant > int64(^uint32(0)>>1) {
		t.Fatal("test tenant ID exceeds advisory lock range")
	}
	key := int32(f.tenant)
	if _, err := f.admin.Exec(f.ctx, `SELECT pg_advisory_lock(9715,$1::integer)`, key); err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	t.Cleanup(func() {
		if lockHeld {
			if _, err := f.admin.Exec(context.Background(), `SELECT pg_advisory_unlock(9715,$1::integer)`, key); err != nil {
				t.Error(err)
			}
		}
		if _, err := f.admin.Exec(context.Background(), dropSQL); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.admin.Exec(f.ctx, createSQL); err != nil {
		t.Fatal(err)
	}
	jobJSON, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	appName := "tuskrag_crash_" + mode + "_" + strconv.FormatInt(f.tenant, 10)
	command := exec.Command(os.Args[0], "-test.run=^TestPlanStageCrashHelper$", "-test.v")
	command.Env = append(os.Environ(), "TUSKRAG_PLAN_CRASH_HELPER=1", "TUSKRAG_PLAN_CRASH_APP="+appName,
		"TUSKRAG_PLAN_CRASH_MODE="+mode, "TUSKRAG_PLAN_CRASH_JOB="+string(jobJSON), "TUSKRAG_PLAN_CRASH_PAYLOAD="+string(payloadJSON))
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var childErr error
	go func() { childErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = command.Process.Kill()
			<-done
		}
	})
	waitForPlanProcess(t, f, appName, done, &output, true)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	if childErr == nil {
		t.Fatal("terminated helper exited successfully")
	}
	if _, err := f.admin.Exec(f.ctx, `SELECT pg_advisory_unlock(9715,$1::integer)`, key); err != nil {
		t.Fatal(err)
	}
	lockHeld = false
	waitForPlanProcess(t, f, appName, nil, nil, false)
	if _, err := f.admin.Exec(f.ctx, dropSQL); err != nil {
		t.Fatal(err)
	}
}

func recoverCrashedJob(t *testing.T, f *fixture, job jobs.ClaimedJob) jobs.ClaimedJob {
	t.Helper()
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.queue.Claim(f.ctx, f.tenant, "recovery-worker", time.Minute)
	if err != nil || recovered.Lease.JobID != job.Lease.JobID || recovered.Lease.Generation <= job.Lease.Generation {
		t.Fatalf("recover job: %+v err=%v", recovered.Lease, err)
	}
	return recovered
}

func waitForPlanProcess(t *testing.T, f *fixture, appName string, done <-chan struct{}, output *bytes.Buffer, waiting bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		var count int
		query := `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`
		if waiting {
			query += ` AND wait_event='advisory'`
		}
		if err := f.admin.QueryRow(f.ctx, query, appName).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if waiting && count > 0 || !waiting && count == 0 {
			return
		}
		select {
		case <-done:
			t.Fatalf("staging subprocess exited before reaching the database lock: %s", output.String())
		case <-timeout.C:
			t.Fatalf("staging subprocess did not reach expected state (waiting=%t)", waiting)
		case <-ticker.C:
		}
	}
}
