\set ON_ERROR_STOP on
\if :{?rows}
\else
\set rows 10000
\endif
\timing on
BEGIN;

DO $$
BEGIN
  IF current_database() NOT LIKE 'tuskrag_test_%' THEN
    RAISE EXCEPTION 'S2 requires an isolated tuskrag_test_ database';
  END IF;
END $$;

CREATE TABLE s2_vectors (
  id bigint PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  is_current boolean NOT NULL,
  access_tags text[] NOT NULL,
  embedding halfvec(1024) NOT NULL
);

INSERT INTO s2_vectors
SELECT i, 1 + (i % 5), 1 + (i % 3), i % 7 <> 0,
  ARRAY[CASE WHEN i % 100 = 1 THEN 'u:1' ELSE 'u:other' END],
  (ARRAY[sin(i * 0.013)::real, cos(i * 0.017)::real,
         sin(i * 0.019)::real, cos(i * 0.023)::real]
    || array_fill(0.01::real, ARRAY[1020]))::halfvec(1024)
FROM generate_series(1, :rows) AS i;

CREATE TEMP TABLE s2_truth AS
SELECT id FROM s2_vectors
WHERE tenant_id = 2 AND kb_id = 2 AND is_current AND access_tags && ARRAY['u:1']
ORDER BY embedding <=> (ARRAY[1, 0, 1, 0]::real[] || array_fill(0.01::real, ARRAY[1020]))::halfvec(1024)
LIMIT 20;

DO $$
BEGIN
  IF (SELECT count(*) FROM s2_truth) <> 20 THEN
    RAISE EXCEPTION 'the exact filtered result has fewer than 20 rows';
  END IF;
END $$;

SET LOCAL maintenance_work_mem = '256MB';
SET LOCAL max_parallel_maintenance_workers = 0;
CREATE INDEX s2_hnsw ON s2_vectors USING hnsw (embedding halfvec_cosine_ops)
  WITH (m = 16, ef_construction = 64) WHERE is_current;
ANALYZE s2_vectors;
SET LOCAL enable_seqscan = off;
SET LOCAL hnsw.ef_search = 40;
SET LOCAL hnsw.max_scan_tuples = 20000;

SET LOCAL hnsw.iterative_scan = 'off';
CREATE TEMP TABLE s2_without_iteration AS
SELECT id FROM s2_vectors
WHERE tenant_id = 2 AND kb_id = 2 AND is_current AND access_tags && ARRAY['u:1']
ORDER BY embedding <=> (ARRAY[1, 0, 1, 0]::real[] || array_fill(0.01::real, ARRAY[1020]))::halfvec(1024)
LIMIT 20;

SET LOCAL hnsw.iterative_scan = 'relaxed_order';
CREATE TEMP TABLE s2_with_iteration AS
SELECT id FROM s2_vectors
WHERE tenant_id = 2 AND kb_id = 2 AND is_current AND access_tags && ARRAY['u:1']
ORDER BY embedding <=> (ARRAY[1, 0, 1, 0]::real[] || array_fill(0.01::real, ARRAY[1020]))::halfvec(1024)
LIMIT 20;

SELECT 'off' AS mode, count(*) AS returned,
  count(t.id) AS exact_top20_hits, (SELECT count(*) FROM s2_truth) AS exact_count
FROM s2_without_iteration a LEFT JOIN s2_truth t USING (id)
UNION ALL
SELECT 'relaxed_order', count(*), count(t.id), (SELECT count(*) FROM s2_truth)
FROM s2_with_iteration a LEFT JOIN s2_truth t USING (id);

DO $$
DECLARE leaked integer;
BEGIN
  SELECT count(*) INTO leaked FROM s2_with_iteration a JOIN s2_vectors v USING (id)
  WHERE NOT (v.tenant_id = 2 AND v.kb_id = 2 AND v.is_current AND v.access_tags && ARRAY['u:1']);
  IF leaked <> 0 THEN RAISE EXCEPTION 'filtered vector search leaked % rows', leaked; END IF;
END $$;

DO $$
DECLARE plan_line text; used_hnsw boolean := false;
BEGIN
  FOR plan_line IN EXECUTE $query$
    EXPLAIN (COSTS OFF) SELECT id FROM s2_vectors
    WHERE tenant_id = 2 AND kb_id = 2 AND is_current AND access_tags && ARRAY['u:1']
    ORDER BY embedding <=> (ARRAY[1, 0, 1, 0]::real[] || array_fill(0.01::real, ARRAY[1020]))::halfvec(1024)
    LIMIT 20
  $query$ LOOP
    IF plan_line LIKE '%Index Scan using s2_hnsw%' THEN used_hnsw := true; END IF;
  END LOOP;
  IF NOT used_hnsw THEN RAISE EXCEPTION 'HNSW index was not selected'; END IF;
END $$;

ROLLBACK;
