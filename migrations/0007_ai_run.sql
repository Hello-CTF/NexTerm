CREATE TABLE ai_run (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 1,
  seq INTEGER NOT NULL DEFAULT 0,
  plan_mode INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'chat',
  answer TEXT NOT NULL DEFAULT '',
  turns INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  finished_at INTEGER
);

CREATE INDEX idx_ai_run_conv ON ai_run(conversation_id, created_at);
CREATE INDEX idx_ai_run_status ON ai_run(status);

CREATE TABLE ai_run_event (
  run_id TEXT NOT NULL REFERENCES ai_run(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE ai_checkpoint (
  id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE ai_hitl_run (
  id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
