-- name: ClaimedIngestJobMatches :one
SELECT EXISTS (
  SELECT 1 FROM job
  WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id)
    AND kind = 'ingest' AND payload = sqlc.arg(payload)::jsonb
);

-- name: LockIngestVersion :one
SELECT v.id, v.document_id, v.kb_id, v.content_sha256, v.byte_size, v.file_key, v.status, d.mime
FROM document_version v JOIN document d
  ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND k.status = 'active'
FOR UPDATE OF v FOR SHARE OF d, k;

-- name: LoadParseArtifact :one
SELECT content_sha256, parsed_document FROM parse_artifact
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: MarkVersionParsing :execrows
UPDATE document_version SET status = 'parsing', failure_code = NULL, failure_detail = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status IN ('queued', 'parsing');

-- name: InsertParseArtifact :exec
INSERT INTO parse_artifact (tenant_id, kb_id, document_id, version_id, content_sha256, parsed_document)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(document_id), sqlc.arg(version_id),
        sqlc.arg(content_sha256), sqlc.arg(parsed_document));

-- name: SaveParseReport :execrows
UPDATE parse_artifact SET parse_report=sqlc.arg(parse_report)::jsonb
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id)
  AND document_id=sqlc.arg(document_id) AND version_id=sqlc.arg(version_id)
  AND content_sha256=sqlc.arg(content_sha256);

-- name: MarkVersionEmbedding :execrows
UPDATE document_version SET status = 'embedding', parser_profile = sqlc.arg(parser_profile),
  page_count = sqlc.arg(page_count), failure_code = NULL, failure_detail = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status IN ('queued', 'parsing', 'embedding');

-- name: MarkVersionFailure :execrows
UPDATE document_version SET status = CASE WHEN sqlc.arg(retryable)::boolean THEN 'queued' ELSE 'failed' END,
  failure_code = sqlc.arg(failure_code), failure_detail = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status IN ('queued', 'parsing', 'embedding', 'indexing');
