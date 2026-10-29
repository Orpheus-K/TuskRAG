package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/retrieval"
)

// V08 measures storage and SQL on deterministic synthetic vectors, not model quality.
func TestCapacity200K(t *testing.T) {
	if os.Getenv("TUSKRAG_CAPACITY_EVAL") != "200000" {
		t.Skip("explicit 200000-block capacity opt-in required")
	}
	directory := os.Getenv("TUSKRAG_EVAL_REPORT_DIR")
	if directory == "" {
		t.Fatal("report directory required")
	}
	publicationDatabase(t)
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	f.ctx = ctx
	var space int64
	if err := f.admin.QueryRow(ctx, `SELECT embedding_space_id FROM knowledge_base WHERE tenant_id=$1 AND id=$2`, f.tenant, f.kbID).Scan(&space); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `UPDATE embedding_space SET dims=1024 WHERE tenant_id=$1 AND id=$2`, f.tenant, space); err != nil {
		t.Fatal(err)
	}
	prototypes := make([][]float32, 256)
	for i := range prototypes {
		v := make([]float32, 1024)
		var norm float64
		for j := range v {
			v[j] = float32(math.Sin(float64((i + 1) * (j + 3))))
			norm += float64(v[j]) * float64(v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(norm))
		}
		prototypes[i] = v
	}
	doc := insertEvaluationDocument(t, f, space, eval.Document{Key: "capacity", Title: "合成容量样本", Content: "容量样本 差旅报销 条款0"}, false, prototypes[0])
	t.Cleanup(func() {
		if _, err := f.admin.Exec(context.Background(), `UPDATE document SET current_version_id=NULL WHERE tenant_id=$1`, f.tenant); err != nil {
			t.Error(err)
		}
	})
	var version, section int64
	if err := f.admin.QueryRow(ctx, `SELECT version_id,section_id FROM chunk WHERE tenant_id=$1 AND document_id=$2`, f.tenant, doc).Scan(&version, &section); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := f.admin.Exec(ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at)
 SELECT $1,$2,$3,$4,$5,g,'text',repeat('合成制度条款，申请人提交材料，审批人审核记录。',20),'容量样本 差旅报销 条款'||(g%256)::text,512,true,ARRAY[$6]::text[],'text/markdown',now() FROM generate_series(2,200000) g`, f.tenant, f.kbID, doc, version, section, fmt.Sprintf("kb:%d:all", f.kbID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(ctx, `CREATE TEMP TABLE capacity_vectors(n int PRIMARY KEY,v halfvec(1024))`); err != nil {
		t.Fatal(err)
	}
	for i, v := range prototypes {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.admin.Exec(ctx, `INSERT INTO capacity_vectors VALUES($1,$2::halfvec)`, i, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.admin.Exec(ctx, `INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding) SELECT c.tenant_id,c.kb_id,c.id,$1,true,c.access_tags,p.v FROM chunk c JOIN capacity_vectors p ON p.n=c.ordinal%256 WHERE c.tenant_id=$2 AND c.document_id=$3 AND c.ordinal>1`, space, f.tenant, doc); err != nil {
		t.Fatal(err)
	}
	insertSeconds := time.Since(started).Seconds()
	t.Logf("200000 chunks inserted in %.1fs", insertSeconds)
	started = time.Now()
	if err := indexing.Provision(ctx, os.Getenv("TUSKRAG_TEST_MIGRATOR_URL"), f.tenant, space); err != nil {
		t.Fatal(err)
	}
	indexSeconds := time.Since(started).Seconds()
	t.Logf("indexes built in %.1fs", indexSeconds)
	if _, err := f.admin.Exec(ctx, `ANALYZE chunk; ANALYZE chunk_vector`); err != nil {
		t.Fatal(err)
	}
	driver, err := retrieval.NewDriver(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := retrieval.Actor{TenantID: f.tenant, UserID: f.user}
	binding, err := driver.Binding(ctx, actor, f.kbID)
	if err != nil {
		t.Fatal(err)
	}
	var latencies []float64
	for i := 0; i < 105; i++ {
		started = time.Now()
		rows, err := driver.Recall(ctx, actor, f.kbID, binding, fmt.Sprintf("差旅报销 条款%d", i%256), prototypes[i%256], retrieval.Filters{}, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatal("capacity query unexpectedly returned no evidence")
		}
		for _, r := range rows {
			if r.DocumentID != doc {
				t.Fatal("unauthorized capacity evidence")
			}
		}
		if i >= 5 {
			latencies = append(latencies, float64(time.Since(started).Microseconds())/1000)
		}
	}
	sort.Float64s(latencies)
	var chunkBytes, vectorBytes, bm25Bytes, hnswBytes, databaseBytes int64
	if err := f.admin.QueryRow(ctx, `SELECT pg_total_relation_size('chunk'),pg_total_relation_size('chunk_vector'),pg_relation_size('tuskrag_chunk_bm25_v1'),pg_relation_size($1::regclass),pg_database_size(current_database())`, fmt.Sprintf("tuskrag_chunk_vector_hnsw_s%d", space)).Scan(&chunkBytes, &vectorBytes, &bm25Bytes, &hnswBytes, &databaseBytes); err != nil {
		t.Fatal(err)
	}
	writeRealReport(t, directory, "v08-200k", struct {
		Blocks, Dimensions, VectorPrototypes, Queries                          int
		InsertSeconds, IndexSeconds, P50MS, P95MS                              float64
		ChunkTotalBytes, VectorTotalBytes, BM25Bytes, HNSWBytes, DatabaseBytes int64
		UnauthorizedEvidence                                                   int
		Distribution                                                           string
	}{200000, 1024, 256, 100, insertSeconds, indexSeconds, latencies[49], latencies[94], chunkBytes, vectorBytes, bm25Bytes, hnswBytes, databaseBytes, 0, "256 deterministic normalized sine vectors reused across 200000 synthetic blocks; excludes model latency"})
}
