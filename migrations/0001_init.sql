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
  deleted_at   INTEGER,
  builtin      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_asset_group ON asset(group_id);
CREATE INDEX idx_asset_kind  ON asset(kind);

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
CREATE INDEX idx_audit_retention ON audit_log(ts DESC, id DESC);

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
  created_at      INTEGER NOT NULL,
  seq             INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_ai_msg_conv_seq ON ai_message(conversation_id, seq);

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
CREATE INDEX idx_terminal_recording_retention
  ON terminal_recording(ended_at DESC, id DESC)
  WHERE ended_at IS NOT NULL;

CREATE TABLE outcome_record (
  idempotence_key     TEXT PRIMARY KEY,
  authorization_id    TEXT NOT NULL,
  kind                TEXT NOT NULL,
  canonical_arguments TEXT NOT NULL,
  state               TEXT NOT NULL,
  outcome             TEXT NOT NULL,
  exit_code           INTEGER,
  result_error        TEXT NOT NULL DEFAULT '',
  audit_state         TEXT NOT NULL,
  audit_error         TEXT NOT NULL DEFAULT '',
  audit_completed_at  INTEGER,
  revision            INTEGER NOT NULL,
  created_at          INTEGER NOT NULL,
  started_at          INTEGER,
  finished_at         INTEGER
);

CREATE TABLE cron_job (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  prompt       TEXT NOT NULL,
  schedule     TEXT NOT NULL,
  timezone     TEXT NOT NULL,
  enabled      INTEGER NOT NULL CHECK(enabled IN (0, 1)),
  timeout_ms   INTEGER NOT NULL CHECK(timeout_ms > 0),
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  revision     INTEGER NOT NULL CHECK(revision > 0),
  next_run_at  INTEGER NOT NULL,
  retry_at     INTEGER,
  circuit_open_until INTEGER,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_run_at  INTEGER,
  last_scheduled_for INTEGER,
  last_coalesced INTEGER NOT NULL DEFAULT 0 CHECK(last_coalesced IN (0, 1)),
  last_error   TEXT NOT NULL DEFAULT '',
  lease_owner  TEXT NOT NULL DEFAULT '',
  lease_expires_at INTEGER,
  run_id       TEXT NOT NULL DEFAULT '',
  run_scheduled_for INTEGER,
  run_started_at INTEGER,
  run_deadline INTEGER,
  run_coalesced INTEGER NOT NULL DEFAULT 0 CHECK(run_coalesced IN (0, 1)),
  model_profile_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_cron_job_session ON cron_job(session_id);

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
  finished_at INTEGER,
  profile_id TEXT NOT NULL DEFAULT '',
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_ai_run_conv ON ai_run(conversation_id, created_at);
CREATE INDEX idx_ai_run_status ON ai_run(status);
CREATE INDEX idx_ai_run_profile ON ai_run(profile_id, created_at);

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

CREATE TABLE transcript (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  asset_name  TEXT NOT NULL DEFAULT '',
  asset_kind  TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,
  bytes       INTEGER NOT NULL DEFAULT 0,
  chunks      INTEGER NOT NULL DEFAULT 0,
  truncated   INTEGER NOT NULL DEFAULT 0,
  sync_opt_in INTEGER NOT NULL DEFAULT 0,
  content_omitted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_asset_started ON transcript(asset_id, started_at DESC);
CREATE INDEX idx_transcript_session ON transcript(session_id);

CREATE TABLE transcript_chunk (
  transcript_id TEXT NOT NULL REFERENCES transcript(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  tab_id      TEXT NOT NULL DEFAULT '',
  ts          INTEGER NOT NULL,
  data        BLOB NOT NULL,
  kind        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (transcript_id, seq)
);

CREATE TABLE durable_transcript_offset (
  durable_id  TEXT PRIMARY KEY,
  offset      INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE credential_tombstone (
  id         TEXT PRIMARY KEY,
  deleted_at INTEGER NOT NULL
);

CREATE TABLE app_user (
  id                  TEXT PRIMARY KEY,
  username            TEXT NOT NULL COLLATE NOCASE UNIQUE,
  display_name        TEXT NOT NULL DEFAULT '',
  role                TEXT NOT NULL CHECK(role IN ('superadmin','user')),
  password_hash       TEXT NOT NULL,
  state               TEXT NOT NULL CHECK(state IN ('active','disabled','reset_required')),
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  last_login_at       INTEGER
);

CREATE TABLE user_dek (
  user_id           TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  dek_envelope      BLOB NOT NULL,
  kdf_salt          BLOB NOT NULL,
  kdf_params        TEXT NOT NULL,
  recovery_envelope BLOB NOT NULL,
  recovery_hash     TEXT NOT NULL,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);

CREATE TABLE user_device (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'desktop',
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER,
  revoked_at   INTEGER
);
CREATE INDEX idx_user_device_user ON user_device(user_id);

CREATE TABLE user_session (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id  TEXT REFERENCES user_device(id) ON DELETE SET NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  touched_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER
);
CREATE INDEX idx_user_session_user ON user_session(user_id);
CREATE INDEX idx_user_session_device ON user_session(device_id);

CREATE TABLE sync_credential (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  purpose      TEXT NOT NULL,
  secret_hash  TEXT NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  revoked_at   INTEGER,
  last_used_at INTEGER
);
CREATE INDEX idx_sync_credential_user ON sync_credential(user_id);
CREATE INDEX idx_sync_credential_device ON sync_credential(device_id);

CREATE TABLE user_setting (
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(user_id, key)
);

CREATE TABLE device_enroll_code (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  code_hash   TEXT NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  consumed_at INTEGER
);
CREATE INDEX idx_device_enroll_code_user ON device_enroll_code(user_id);

CREATE TABLE user_sync_object (
  user_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  id      TEXT NOT NULL,
  seq     INTEGER NOT NULL,
  blob    BLOB NOT NULL,
  blob_hash BLOB,
  PRIMARY KEY (user_id, id)
);
CREATE INDEX idx_user_sync_object_seq ON user_sync_object(user_id, seq);

CREATE TABLE user_sync_head (
  user_id   TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  head_hash TEXT NOT NULL
);

CREATE TABLE device_agent (
  device_id          TEXT PRIMARY KEY REFERENCES user_device(id) ON DELETE CASCADE,
  credential_id      TEXT NOT NULL REFERENCES sync_credential(id) ON DELETE CASCADE,
  platform           TEXT NOT NULL DEFAULT '',
  app_version        TEXT NOT NULL DEFAULT '',
  desired_autostart  INTEGER NOT NULL DEFAULT 1,
  terminal_enabled   INTEGER NOT NULL DEFAULT 1,
  current_url        TEXT NOT NULL DEFAULT '',
  service_state_json TEXT NOT NULL DEFAULT '{}',
  created_at         INTEGER NOT NULL,
  last_seen_at       INTEGER
);

CREATE TABLE device_metrics (
  id         TEXT PRIMARY KEY,
  device_id  TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  ts         INTEGER NOT NULL,
  cpu_pct    REAL,
  mem_used   INTEGER,
  mem_total  INTEGER,
  disk_used  INTEGER,
  disk_total INTEGER,
  uptime_s   INTEGER
);
CREATE INDEX idx_device_metrics_device_ts ON device_metrics(device_id, ts);

CREATE TABLE device_metrics_hourly (
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  bucket_ts    INTEGER NOT NULL,
  cpu_pct      REAL,
  mem_used     INTEGER,
  mem_total    INTEGER,
  disk_used    INTEGER,
  disk_total   INTEGER,
  uptime_s     INTEGER,
  sample_count INTEGER NOT NULL,
  PRIMARY KEY (device_id, bucket_ts)
);

CREATE TABLE sync_tombstone (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  deleted_at INTEGER NOT NULL
);

CREATE TABLE sync_state (
  user_id      TEXT NOT NULL,
  object_id    TEXT NOT NULL,
  payload_hash BLOB,
  blob_hash    BLOB NOT NULL,
  PRIMARY KEY (user_id, object_id)
);

CREATE TABLE share_link (
  id               TEXT PRIMARY KEY,
  owner_id         TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id        TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  session_id       TEXT NOT NULL,
  token_hash       TEXT NOT NULL UNIQUE,
  permission       TEXT NOT NULL DEFAULT 'read',
  created_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,
  revoked_at       INTEGER,
  last_accessed_at INTEGER
);
CREATE INDEX idx_share_link_owner ON share_link(owner_id);
CREATE INDEX idx_share_link_device ON share_link(device_id);

CREATE TABLE host_share (
  id           TEXT PRIMARY KEY,
  owner_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  recipient_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  permission   TEXT NOT NULL DEFAULT 'read_write',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  revoked_at   INTEGER,
  UNIQUE(owner_id, device_id, recipient_id)
);
CREATE INDEX idx_host_share_device ON host_share(device_id);
CREATE INDEX idx_host_share_recipient ON host_share(recipient_id);

CREATE TABLE command_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL,
  tab_id      TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  user_id     TEXT,
  command     TEXT NOT NULL,
  source      TEXT NOT NULL,
  exit_code   INTEGER,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER NOT NULL
);
CREATE INDEX idx_command_log_started ON command_log(started_at DESC);
CREATE INDEX idx_command_log_session ON command_log(session_id);
CREATE INDEX idx_command_log_asset ON command_log(asset_id);
CREATE INDEX idx_command_log_user ON command_log(user_id);

CREATE TABLE device_share_link (
  id               TEXT PRIMARY KEY,
  owner_id         TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id        TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  token_hash       TEXT NOT NULL UNIQUE,
  permission       TEXT NOT NULL DEFAULT 'read',
  created_at       INTEGER NOT NULL,
  not_before       INTEGER NOT NULL DEFAULT 0,
  expires_at       INTEGER NOT NULL,
  revoked_at       INTEGER,
  last_accessed_at INTEGER
);
CREATE INDEX idx_device_share_link_owner ON device_share_link(owner_id);
CREATE INDEX idx_device_share_link_device ON device_share_link(device_id);

CREATE TABLE user_totp (
  user_id                 TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  secret_envelope         BLOB,
  pending_secret_envelope BLOB,
  created_at              INTEGER NOT NULL,
  updated_at              INTEGER NOT NULL
);

CREATE TABLE user_totp_recovery_code (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  used_at    INTEGER
);
CREATE INDEX idx_user_totp_recovery_code_user ON user_totp_recovery_code(user_id);

CREATE TRIGGER sync_tombstone_asset_group_delete AFTER DELETE ON asset_group
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES (old.id, 'group', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER))
  ON CONFLICT(id) DO NOTHING;
END;

CREATE TRIGGER sync_tombstone_snippet_delete AFTER DELETE ON snippet
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES (old.id, 'snippet', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER))
  ON CONFLICT(id) DO NOTHING;
END;
