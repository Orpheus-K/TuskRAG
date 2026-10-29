package ingest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
)

func prepareVectorFixture(t *testing.T) (*fixture, jobs.ClaimedJob, kb.UploadedDocument, PreparedPlan, StagedPlan) {
	t.Helper()
	f, job, uploaded, plan := preparePlanFixture(t)
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	return f, job, uploaded, plan, staged
}

func vectorInput(staged StagedPlan, index int, values []float32) VectorInput {
	return VectorInput{ChunkID: staged.Chunks[index].ID, Ordinal: staged.Chunks[index].Ordinal, Values: values}
}

func TestVectorStageWritesBatchesAndFinishesWithoutPublishing(t *testing.T) {
	f, job, uploaded, _, staged := prepareVectorFixture(t)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, ErrVectorsIncomplete) {
		t.Fatalf("incomplete vectors accepted: %v", err)
	}
	first := vectorInput(staged, 0, testVector(3, 4))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{first}); err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{first}); err != nil {
		t.Fatalf("replay vector batch: %v", err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, ErrVectorsIncomplete) {
		t.Fatalf("partially embedded plan accepted: %v", err)
	}
	second := vectorInput(staged, 1, testVector(1, 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{second}); err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); err != nil {
		t.Fatalf("finish replay: %v", err)
	}
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{first}); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("vector write after indexing accepted: %v", err)
	}
	var status, jobStatus string
	var noCurrent bool
	var total, current int
	var norm float32
	var tags []string
	if err := f.admin.QueryRow(f.ctx, `SELECT v.status, d.current_version_id IS NULL,
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$3),
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$3 AND is_current),
		(SELECT l2_norm(embedding) FROM chunk_vector WHERE tenant_id=$1 AND chunk_id=$4 AND space_id=$3),
		(SELECT access_tags FROM chunk_vector WHERE tenant_id=$1 AND chunk_id=$4 AND space_id=$3),
		(SELECT status FROM job WHERE tenant_id=$1 AND id=$5)
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID, staged.Identity.EmbeddingSpaceID, first.ChunkID, job.Lease.JobID).Scan(
		&status, &noCurrent, &total, &current, &norm, &tags, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if status != "indexing" || !noCurrent || total != 2 || current != 0 || jobStatus != "running" ||
		math.Abs(float64(norm)-1) > 0.001 || !reflect.DeepEqual(tags, []string{fmt.Sprintf("kb:%d:all", f.kbID)}) {
		t.Fatalf("vectors were published or malformed: status=%s total=%d current=%d job=%s norm=%f", status, total, current, jobStatus, norm)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE chunk_id=$1`, first.ChunkID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign tenant saw vector: count=%d err=%v", visible, err)
	}
}

func TestVectorStageRejectsWrongPlanChunkAndDimension(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	other, _, _, _, foreign := prepareVectorFixture(t)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	identity := staged.Identity
	identity.Digest[0] ^= 1
	if err := stage.Write(f.ctx, job, identity, []VectorInput{vectorInput(staged, 0, testVector(1, 0))}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("wrong plan digest accepted: %v", err)
	}
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{vectorInput(foreign, 0, testVector(1, 0))}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("foreign chunk accepted: %v", err)
	}
	wrong := vectorInput(staged, 0, append(testVector(1, 0), 1))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{wrong}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("wrong dimensions accepted: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 OR tenant_id=$2`, f.tenant, other.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid batch wrote vectors: count=%d err=%v", count, err)
	}
}

func TestVectorStageRejectsChangedEmbeddingProfile(t *testing.T) {
	for name, mutation := range map[string]string{
		"dimensions": `UPDATE embedding_space SET dims=65 WHERE tenant_id=$1 AND id=$2`,
		"normalized": `UPDATE embedding_space SET normalized=false WHERE tenant_id=$1 AND id=$2`,
		"distance":   `UPDATE embedding_space SET distance='ip' WHERE tenant_id=$1 AND id=$2`,
		"model":      `UPDATE embedding_space SET model='changed-model' WHERE tenant_id=$1 AND id=$2`,
	} {
		t.Run(name, func(t *testing.T) {
			f, job, _, _, staged := prepareVectorFixture(t)
			if _, err := f.admin.Exec(f.ctx, mutation, f.tenant, staged.Identity.EmbeddingSpaceID); err != nil {
				t.Fatal(err)
			}
			stage, err := NewVectorStage(f.queue)
			if err != nil {
				t.Fatal(err)
			}
			if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{vectorInput(staged, 0, testVector(1, 0))}); !errors.Is(err, ErrPlanConflict) {
				t.Fatalf("changed embedding profile accepted: %v", err)
			}
			if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, ErrPlanConflict) {
				t.Fatalf("changed embedding profile finished: %v", err)
			}
			forged := staged.Identity
			switch name {
			case "dimensions":
				forged.EmbeddingProfile.Dimensions = 65
			case "normalized":
				forged.EmbeddingProfile.Normalized = false
			case "distance":
				forged.EmbeddingProfile.Distance = "ip"
			case "model":
				forged.EmbeddingProfile.Model = "changed-model"
			}
			if err := stage.Write(f.ctx, job, forged, []VectorInput{vectorInput(staged, 0, testVector(1, 0))}); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("forged profile with old digest accepted: %v", err)
			}
			forged.Digest = planIdentityDigest(forged)
			if err := stage.Write(f.ctx, job, forged, []VectorInput{vectorInput(staged, 0, testVector(1, 0))}); !errors.Is(err, ErrPlanConflict) {
				t.Fatalf("forged profile with new digest accepted: %v", err)
			}
		})
	}
}

func TestVectorStageRejectsUnsafeExistingVectors(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	first := vectorInput(staged, 0, testVector(1, 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{first}); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		`UPDATE chunk_vector SET is_current=true WHERE tenant_id=$1 AND chunk_id=$2`,
		`UPDATE chunk_vector SET access_tags=ARRAY['u:999'] WHERE tenant_id=$1 AND chunk_id=$2`,
	} {
		if _, err := f.admin.Exec(f.ctx, mutation, f.tenant, first.ChunkID); err != nil {
			t.Fatal(err)
		}
		if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{first}); !errors.Is(err, ErrPlanCorrupt) {
			t.Fatalf("unsafe vector overwritten: %v", err)
		}
		if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, ErrPlanCorrupt) {
			t.Fatalf("unsafe vector accepted for indexing: %v", err)
		}
		if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector SET is_current=false, access_tags=ARRAY[$3] WHERE tenant_id=$1 AND chunk_id=$2`,
			f.tenant, first.ChunkID, fmt.Sprintf("kb:%d:all", f.kbID)); err != nil {
			t.Fatal(err)
		}
	}
	badNorm := "[2," + strings.Repeat("0,", 62) + "0]"
	if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector SET embedding=$3::halfvec WHERE tenant_id=$1 AND chunk_id=$2`, f.tenant, first.ChunkID, badNorm); err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, ErrPlanCorrupt) {
		t.Fatalf("unnormalized vector accepted in normalized space: %v", err)
	}
}

func TestVectorStageChecksNormalizedTolerance(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	batch := []VectorInput{vectorInput(staged, 0, testVector(1, 0)), vectorInput(staged, 1, testVector(0, 1))}
	if err := stage.Write(f.ctx, job, staged.Identity, batch); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value   string
		wantErr error
	}{
		{value: "1.006", wantErr: ErrPlanCorrupt},
		{value: "1.003"},
	} {
		vector := "[" + tc.value + "," + strings.Repeat("0,", 62) + "0]"
		if _, err := f.admin.Exec(f.ctx, `UPDATE chunk_vector SET embedding=$3::halfvec WHERE tenant_id=$1 AND chunk_id=$2`, f.tenant, batch[0].ChunkID, vector); err != nil {
			t.Fatal(err)
		}
		if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, tc.wantErr) {
			t.Fatalf("normalization tolerance for %s: %v", tc.value, err)
		}
	}
}

func TestVectorStageRollsBackBatchFailure(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	if _, err := f.admin.Exec(f.ctx, `CREATE FUNCTION tuskrag_test_reject_second_vector() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF (SELECT ordinal FROM chunk WHERE tenant_id=NEW.tenant_id AND id=NEW.chunk_id)=2
		THEN RAISE EXCEPTION 'private vector'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER tuskrag_test_reject_second_vector BEFORE INSERT ON chunk_vector FOR EACH ROW EXECUTE FUNCTION tuskrag_test_reject_second_vector()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS tuskrag_test_reject_second_vector ON chunk_vector; DROP FUNCTION IF EXISTS tuskrag_test_reject_second_vector()`); err != nil {
			t.Error(err)
		}
	})
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	batch := []VectorInput{vectorInput(staged, 0, testVector(1, 0)), vectorInput(staged, 1, testVector(0, 1))}
	err = stage.Write(f.ctx, job, staged.Identity, batch)
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "P0001" || strings.Contains(err.Error(), "private vector") {
		t.Fatalf("vector failure lost its cause or leaked content: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial vector batch persisted: count=%d err=%v", count, err)
	}
	if _, err := f.admin.Exec(f.ctx, `DROP TRIGGER tuskrag_test_reject_second_vector ON chunk_vector; DROP FUNCTION tuskrag_test_reject_second_vector()`); err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, job, staged.Identity, batch); err != nil {
		t.Fatalf("retry vector batch: %v", err)
	}
}

func TestVectorStageRejectsStaleLease(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, f.tenant, job.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.queue.Claim(f.ctx, f.tenant, "vector-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	batch := []VectorInput{vectorInput(staged, 0, testVector(1, 0))}
	if err := stage.Write(f.ctx, job, staged.Identity, batch); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale vector write accepted: %v", err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale vector completion accepted: %v", err)
	}
	var count int
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM chunk_vector WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale vector wrote data: count=%d err=%v", count, err)
	}
	if err := stage.Write(f.ctx, recovered, staged.Identity, batch); err != nil {
		t.Fatalf("current lease could not write: %v", err)
	}
}

func TestVectorStageRollsBackWhenLeaseExpiresDuringWrite(t *testing.T) {
	f, job, _, _, staged := prepareVectorFixture(t)
	if _, err := f.admin.Exec(f.ctx, `CREATE FUNCTION tuskrag_test_expire_vector_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN UPDATE job SET lease_until=clock_timestamp()-interval '1 second'
		WHERE tenant_id=NEW.tenant_id AND kind='ingest' AND status='running'; RETURN NEW; END $$;
		CREATE TRIGGER tuskrag_test_expire_vector_lease BEFORE INSERT ON chunk_vector FOR EACH ROW EXECUTE FUNCTION tuskrag_test_expire_vector_lease()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS tuskrag_test_expire_vector_lease ON chunk_vector; DROP FUNCTION IF EXISTS tuskrag_test_expire_vector_lease()`); err != nil {
			t.Error(err)
		}
	})
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	batch := []VectorInput{vectorInput(staged, 0, testVector(1, 0)), vectorInput(staged, 1, testVector(0, 1))}
	if err := stage.Write(f.ctx, job, staged.Identity, batch); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired vector transaction committed: %v", err)
	}
	var count int
	var leaseValid bool
	if err := f.admin.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1),
		(SELECT lease_until > clock_timestamp() FROM job WHERE tenant_id=$1 AND id=$2)`, f.tenant, job.Lease.JobID).Scan(&count, &leaseValid); err != nil || count != 0 || !leaseValid {
		t.Fatalf("expired batch did not roll back: count=%d lease=%t err=%v", count, leaseValid, err)
	}
	if _, err := f.admin.Exec(f.ctx, `DROP TRIGGER tuskrag_test_expire_vector_lease ON chunk_vector; DROP FUNCTION tuskrag_test_expire_vector_lease()`); err != nil {
		t.Fatal(err)
	}
	if err := stage.Write(f.ctx, job, staged.Identity, batch); err != nil {
		t.Fatalf("retry vector batch after rollback: %v", err)
	}
}

func TestVectorStageChecksHalfPrecisionForUnnormalizedSpace(t *testing.T) {
	f, job, _, plan := preparePlanFixture(t)
	if _, err := f.admin.Exec(f.ctx, `UPDATE embedding_space SET normalized=false WHERE tenant_id=$1 AND id=$2`, f.tenant, plan.EmbeddingSpaceID); err != nil {
		t.Fatal(err)
	}
	plan.EmbeddingProfile.Normalized = false
	planStage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := planStage.Stage(f.ctx, job, plan)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	tooSmall := vectorInput(staged, 0, testVector(float32(math.Ldexp(1, -25)), 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{tooSmall}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("zero after half conversion accepted: %v", err)
	}
	tooLarge := vectorInput(staged, 0, testVector(65505, 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{tooLarge}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("out-of-range half vector accepted: %v", err)
	}
	justAboveZero := vectorInput(staged, 0, testVector(math.Nextafter32(float32(math.Ldexp(1, -25)), 1), 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{justAboveZero}); err != nil {
		t.Fatalf("half precision rounding boundary rejected: %v", err)
	}
	small := vectorInput(staged, 0, testVector(float32(math.Ldexp(1, -24)), 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{small}); err != nil {
		t.Fatalf("representable half subnormal rejected: %v", err)
	}
	var norm float32
	if err := f.admin.QueryRow(f.ctx, `SELECT l2_norm(embedding) FROM chunk_vector WHERE tenant_id=$1 AND chunk_id=$2`, f.tenant, small.ChunkID).Scan(&norm); err != nil || norm <= 0 {
		t.Fatalf("half subnormal was lost: norm=%g err=%v", norm, err)
	}
	maximum := vectorInput(staged, 0, testVector(65504, 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{maximum}); err != nil {
		t.Fatalf("maximum finite half vector rejected: %v", err)
	}
	second := vectorInput(staged, 1, testVector(2, 0))
	if err := stage.Write(f.ctx, job, staged.Identity, []VectorInput{second}); err != nil {
		t.Fatal(err)
	}
	if err := stage.Finish(f.ctx, job, staged.Identity); err != nil {
		t.Fatalf("unnormalized space incorrectly required unit vectors: %v", err)
	}
}
