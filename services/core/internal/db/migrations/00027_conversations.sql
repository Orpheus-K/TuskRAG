-- +goose Up
CREATE TABLE conversation (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  user_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  model_id bigint NOT NULL,
  title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 120),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id),
  FOREIGN KEY (tenant_id, model_id) REFERENCES chat_model(tenant_id, id)
);
CREATE INDEX conversation_owner ON conversation (tenant_id, user_id, kb_id, id DESC);

CREATE TABLE answer_message (
  id text NOT NULL CHECK (length(id) BETWEEN 16 AND 64),
  tenant_id bigint NOT NULL,
  conversation_id bigint NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 50),
  question text NOT NULL CHECK (octet_length(question) BETWEEN 1 AND 4096),
  content text NOT NULL DEFAULT '' CHECK (octet_length(content) <= 65536),
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','complete','failed','cancelled')),
  evidence jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence) = 'array'),
  citations jsonb NOT NULL DEFAULT '{"items":[],"invalid_count":0}' CHECK (jsonb_typeof(citations) = 'object'),
  degraded text[] NOT NULL DEFAULT '{}',
  outcome text CHECK (outcome IN ('answered','refused','unsupported','truncated')),
  prompt_version text,
  retrieval_revision integer,
  usage jsonb CHECK (jsonb_typeof(usage) = 'object'),
  duration_ms bigint CHECK (duration_ms >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  deadline timestamptz NOT NULL DEFAULT (now() + interval '125 seconds'),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, conversation_id, ordinal),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversation(tenant_id, id),
  CHECK ((status = 'complete') = (outcome IS NOT NULL AND prompt_version IS NOT NULL AND duration_ms IS NOT NULL)),
  CHECK (status NOT IN ('failed','cancelled') OR (content = '' AND evidence = '[]'::jsonb))
);
CREATE UNIQUE INDEX answer_one_running ON answer_message (tenant_id, conversation_id) WHERE status = 'running';

CREATE TABLE answer_feedback (
  tenant_id bigint NOT NULL,
  message_id text NOT NULL,
  rating smallint NOT NULL CHECK (rating IN (-1,1)),
  correction text NOT NULL DEFAULT '' CHECK (octet_length(correction) <= 4000),
  status text NOT NULL DEFAULT 'pending' CHECK (status = 'pending'),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, message_id),
  FOREIGN KEY (tenant_id, message_id) REFERENCES answer_message(tenant_id, id)
);

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['conversation','answer_message','answer_feedback'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd
GRANT SELECT, INSERT ON conversation TO tuskrag_app;
-- Row locking serializes turns without holding a transaction during generation.
GRANT UPDATE (title) ON conversation TO tuskrag_app;
GRANT SELECT, INSERT, UPDATE ON answer_message, answer_feedback TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE conversation_id_seq TO tuskrag_app;
