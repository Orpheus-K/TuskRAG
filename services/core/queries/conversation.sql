-- name: CreateConversation :one
INSERT INTO conversation (tenant_id, user_id, kb_id, model_id, title)
VALUES (sqlc.arg(tenant_id),sqlc.arg(user_id),sqlc.arg(kb_id),sqlc.arg(model_id),sqlc.arg(title)) RETURNING *;

-- name: ListConversations :many
SELECT c.* FROM conversation c
JOIN knowledge_base k ON k.tenant_id=c.tenant_id AND k.id=c.kb_id
JOIN kb_membership m ON m.tenant_id=k.tenant_id AND m.kb_id=k.id AND m.user_id=c.user_id
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.user_id=sqlc.arg(user_id) AND c.kb_id=sqlc.arg(kb_id)
  AND k.status='active' AND (sqlc.arg(cursor)::bigint=0 OR c.id < sqlc.arg(cursor))
ORDER BY c.id DESC LIMIT 21;

-- name: LockConversation :one
SELECT c.* FROM conversation c
JOIN knowledge_base k ON k.tenant_id=c.tenant_id AND k.id=c.kb_id
JOIN kb_membership m ON m.tenant_id=k.tenant_id AND m.kb_id=k.id AND m.user_id=c.user_id
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.user_id=sqlc.arg(user_id) AND c.id=sqlc.arg(id) AND k.status='active'
FOR UPDATE OF c FOR SHARE OF k,m;

-- name: ConversationMessages :many
SELECT m.*, f.rating, f.correction FROM answer_message m
LEFT JOIN answer_feedback f ON f.tenant_id=m.tenant_id AND f.message_id=m.id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.conversation_id=sqlc.arg(conversation_id)
ORDER BY m.ordinal;

-- name: ExpireConversationMessages :exec
UPDATE answer_message SET status='failed',content='',evidence='[]',citations='{"items":[],"invalid_count":0}',degraded='{}'
WHERE tenant_id=sqlc.arg(tenant_id) AND conversation_id=sqlc.arg(conversation_id) AND status='running' AND deadline <= now();

-- name: ConversationEvidence :many
SELECT evidence FROM answer_message
WHERE tenant_id=sqlc.arg(tenant_id) AND conversation_id=sqlc.arg(conversation_id);

-- name: StartConversationMessage :exec
INSERT INTO answer_message (tenant_id,id,conversation_id,ordinal,question)
VALUES (sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(conversation_id),sqlc.arg(ordinal),sqlc.arg(question));

-- name: AppendConversationMessage :execrows
UPDATE answer_message SET content=content || sqlc.arg(delta)::text
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND conversation_id=sqlc.arg(conversation_id)
  AND status='running' AND deadline > now();

-- name: SetMessageEvidence :execrows
UPDATE answer_message SET evidence=sqlc.arg(evidence),degraded=sqlc.arg(degraded),retrieval_revision=sqlc.arg(retrieval_revision)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND conversation_id=sqlc.arg(conversation_id)
  AND status='running' AND deadline > now();

-- name: FinishConversationMessage :execrows
UPDATE answer_message SET status='complete',citations=sqlc.arg(citations),outcome=sqlc.arg(outcome),
  prompt_version=sqlc.arg(prompt_version),usage=sqlc.narg(usage),duration_ms=sqlc.arg(duration_ms)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND conversation_id=sqlc.arg(conversation_id)
  AND status='running' AND deadline > now();

-- name: FailConversationMessage :exec
UPDATE answer_message m SET status=sqlc.arg(status),content='',evidence='[]',citations='{"items":[],"invalid_count":0}',degraded='{}'
FROM conversation c WHERE c.tenant_id=m.tenant_id AND c.id=m.conversation_id AND c.user_id=sqlc.arg(user_id)
  AND m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(id) AND m.status='running';

-- name: FeedbackConversationID :one
SELECT c.id FROM conversation c JOIN answer_message m ON m.tenant_id=c.tenant_id AND m.conversation_id=c.id
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.user_id=sqlc.arg(user_id) AND m.id=sqlc.arg(message_id) AND m.status='complete';

-- name: PutAnswerFeedback :exec
INSERT INTO answer_feedback(tenant_id,message_id,rating,correction)
VALUES(sqlc.arg(tenant_id),sqlc.arg(message_id),sqlc.arg(rating),sqlc.arg(correction))
ON CONFLICT (tenant_id,message_id) DO UPDATE SET rating=EXCLUDED.rating,correction=EXCLUDED.correction,updated_at=now();

-- name: AuditConversation :exec
INSERT INTO audit_log (tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),sqlc.arg(action),'conversation',sqlc.arg(id),'allow',
  jsonb_build_object('message_id',sqlc.arg(message_id)::text));

-- name: ListNegativeFeedbackCandidates :many
SELECT m.id AS message_id, m.conversation_id, m.question, m.content AS answer,
       m.evidence, m.outcome, f.correction, f.updated_at
FROM answer_feedback f
JOIN answer_message m ON m.tenant_id = f.tenant_id AND m.id = f.message_id
JOIN conversation c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
WHERE f.tenant_id = sqlc.arg(tenant_id)
  AND c.kb_id = sqlc.arg(kb_id)
  AND f.rating = -1
  AND m.status = 'complete'
ORDER BY f.updated_at DESC
LIMIT sqlc.arg(limit_count);
