PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE asset_group (
  id          TEXT PRIMARY KEY,
  parent_id   TEXT REFERENCES asset_group(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  sort        INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE asset (
  id           TEXT PRIMARY KEY,
  group_id     TEXT REFERENCES asset_group(id) ON DELETE SET NULL,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  host         TEXT,
  port         INTEGER,
  username     TEXT,
  auth_kind    TEXT,
  key_path     TEXT,
  cred_id      TEXT REFERENCES credential(id) ON DELETE SET NULL,
  options_json TEXT NOT NULL DEFAULT '{}',
  tags         TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  sort         INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  deleted_at   INTEGER
);
CREATE INDEX idx_asset_group ON asset(group_id);
CREATE INDEX idx_asset_kind  ON asset(kind);

CREATE TABLE credential (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  cipher     TEXT NOT NULL,
  nonce      BLOB NOT NULL,
  blob       BLOB NOT NULL,
  kek_hint   TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE audit_log (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           INTEGER NOT NULL,
  session_id   TEXT,
  asset_id     TEXT,
  source       TEXT NOT NULL,
  kind         TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  exit_code    INTEGER,
  duration_ms  INTEGER
);
CREATE INDEX idx_audit_ts ON audit_log(ts DESC);

CREATE TABLE ai_conversation (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE ai_message (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
  role            TEXT NOT NULL,
  content_json    TEXT NOT NULL,
  tokens_in       INTEGER,
  tokens_out      INTEGER,
  created_at      INTEGER NOT NULL
);
CREATE INDEX idx_ai_msg_conv ON ai_message(conversation_id, created_at);

CREATE TABLE snippet (
  id         TEXT PRIMARY KEY,
  group_id   TEXT,
  name       TEXT NOT NULL,
  body       TEXT NOT NULL,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE known_host (
  id          TEXT PRIMARY KEY,
  host        TEXT NOT NULL,
  port        INTEGER NOT NULL,
  key_type    TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  added_at    INTEGER NOT NULL,
  UNIQUE(host, port, key_type)
);

CREATE TABLE terminal_recording (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  tab_id     TEXT NOT NULL,
  path       TEXT NOT NULL,
  bytes      INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  ended_at   INTEGER
);
