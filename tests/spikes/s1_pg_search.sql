\set ON_ERROR_STOP on
BEGIN;

DO $$
BEGIN
  IF current_database() NOT LIKE 'tuskrag_test_%' THEN
    RAISE EXCEPTION 'S1 requires an isolated tuskrag_test_ database';
  END IF;
END $$;

CREATE TABLE s1_documents (
  id bigint PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  is_current boolean NOT NULL,
  access_tags text[] NOT NULL,
  body text NOT NULL,
  embedding halfvec(3) NOT NULL
);

INSERT INTO s1_documents VALUES
  (1, 1, 1, true,  ARRAY['u:1'], '差旅住宿标准第三条', '[1,0,0]'),
  (2, 2, 1, true,  ARRAY['u:1'], '差旅住宿标准第三条', '[1,0,0]'),
  (3, 1, 2, true,  ARRAY['u:1'], '差旅住宿标准第三条', '[1,0,0]'),
  (4, 1, 1, true,  ARRAY['u:2'], '差旅住宿标准第三条', '[1,0,0]'),
  (5, 1, 1, false, ARRAY['u:1'], '差旅住宿标准第三条', '[1,0,0]'),
  (6, 1, 1, true,  ARRAY['u:1'], '其他制度', '[0,1,0]');

CREATE INDEX s1_bm25 ON s1_documents USING bm25
  (id, (body::pdb.jieba), tenant_id, kb_id, is_current, access_tags)
  WITH (key_field = 'id');
CREATE INDEX s1_hnsw ON s1_documents USING hnsw (embedding halfvec_cosine_ops)
  WHERE is_current;

SELECT '差旅住宿标准第三条'::pdb.jieba::text[] AS jieba_tokens;
SET LOCAL enable_seqscan = off;
SET LOCAL hnsw.iterative_scan = 'relaxed_order';

DO $$
DECLARE visible_ids bigint[];
BEGIN
  SELECT array_agg(id ORDER BY id) INTO visible_ids FROM s1_documents
  WHERE body ||| '住宿' AND tenant_id = 1 AND kb_id = 1
    AND is_current AND access_tags && ARRAY['u:1'];
  IF visible_ids IS DISTINCT FROM ARRAY[1::bigint] THEN
    RAISE EXCEPTION 'unexpected visible lexical IDs: %', visible_ids;
  END IF;
END $$;

DO $$
DECLARE visible_ids bigint[];
BEGIN
  SELECT array_agg(id ORDER BY id) INTO visible_ids FROM (
    SELECT id FROM s1_documents
    WHERE tenant_id = 1 AND kb_id = 1 AND is_current AND access_tags && ARRAY['u:1']
    ORDER BY embedding <=> '[1,0,0]'::halfvec LIMIT 5
  ) AS visible;
  IF visible_ids IS DISTINCT FROM ARRAY[1::bigint, 6::bigint] THEN
    RAISE EXCEPTION 'unexpected visible vector IDs: %', visible_ids;
  END IF;
END $$;

EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
SELECT id FROM s1_documents
WHERE body ||| '住宿' AND tenant_id = 1 AND kb_id = 1
  AND is_current AND access_tags && ARRAY['u:1']
ORDER BY paradedb.score(id) DESC LIMIT 5;

EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
WITH lexical AS (
  SELECT id FROM s1_documents
  WHERE body ||| '住宿' AND tenant_id = 1 AND kb_id = 1
    AND is_current AND access_tags && ARRAY['u:1']
  ORDER BY paradedb.score(id) DESC LIMIT 5
), semantic AS (
  SELECT id FROM s1_documents
  WHERE tenant_id = 1 AND kb_id = 1
    AND is_current AND access_tags && ARRAY['u:1']
  ORDER BY embedding <=> '[1,0,0]'::halfvec LIMIT 5
)
SELECT lexical.id AS lexical_id, semantic.id AS semantic_id FROM lexical FULL JOIN semantic USING (id);

ROLLBACK;
