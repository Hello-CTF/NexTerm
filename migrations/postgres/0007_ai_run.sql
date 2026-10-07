CREATE TABLE ai_run (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  attempt BIGINT NOT NULL DEFAULT 1,
  seq BIGINT NOT NULL DEFAULT 0,
  plan_mode SMALLINT NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'chat',
  answer TEXT NOT NULL DEFAULT '',
  turns BIGINT NOT NULL DEFAULT 0,
  tokens_in BIGINT NOT NULL DEFAULT 0,
  tokens_out BIGINT NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  finished_at BIGINT
);

CREATE INDEX idx_ai_run_conv ON ai_run(conversation_id, created_at);
CREATE INDEX idx_ai_run_status ON ai_run(status);

CREATE TABLE ai_run_event (
  run_id TEXT NOT NULL REFERENCES ai_run(id) ON DELETE CASCADE,
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE ai_checkpoint (
  id TEXT PRIMARY KEY,
  data BYTEA NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE ai_hitl_run (
  id TEXT PRIMARY KEY,
  data BYTEA NOT NULL,
  updated_at BIGINT NOT NULL
);
