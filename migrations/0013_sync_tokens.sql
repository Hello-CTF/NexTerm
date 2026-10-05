CREATE TABLE sync_tokens (
  id           TEXT PRIMARY KEY,
  client_id    TEXT NOT NULL,
  purpose      TEXT NOT NULL,
  secret_hash  TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  revoked_at   INTEGER,
  last_used_at INTEGER
);
CREATE UNIQUE INDEX idx_sync_tokens_hash ON sync_tokens(secret_hash);
CREATE INDEX idx_sync_tokens_client ON sync_tokens(client_id);
