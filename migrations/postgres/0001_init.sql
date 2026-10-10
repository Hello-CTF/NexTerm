CREATE TABLE asset_group (
  id          TEXT PRIMARY KEY,
  parent_id   TEXT REFERENCES asset_group(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  sort        BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);

CREATE TABLE credential (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  cipher     TEXT NOT NULL,
  nonce      BYTEA NOT NULL,
  blob       BYTEA NOT NULL,
  kek_hint   TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE asset (
  id           TEXT PRIMARY KEY,
  group_id     TEXT REFERENCES asset_group(id) ON DELETE SET NULL,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  host         TEXT,
  port         BIGINT,
  username     TEXT,
  auth_kind    TEXT,
  key_path     TEXT,
  cred_id      TEXT REFERENCES credential(id) ON DELETE SET NULL,
  options_json TEXT NOT NULL DEFAULT '{}',
  tags         TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  sort         BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL,
  deleted_at   BIGINT,
  builtin      SMALLINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_asset_group ON asset(group_id);
CREATE INDEX idx_asset_kind  ON asset(kind);

CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE audit_log (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ts           BIGINT NOT NULL,
  session_id   TEXT,
  asset_id     TEXT,
  source       TEXT NOT NULL,
  kind         TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  exit_code    BIGINT,
  duration_ms  BIGINT
);
CREATE INDEX idx_audit_retention ON audit_log(ts DESC, id DESC);

CREATE TABLE ai_conversation (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '{}',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE ai_message (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
  role            TEXT NOT NULL,
  content_json    TEXT NOT NULL,
  tokens_in       BIGINT,
  tokens_out      BIGINT,
  created_at      BIGINT NOT NULL,
  seq             BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_ai_msg_conv_seq ON ai_message(conversation_id, seq);

CREATE TABLE snippet (
  id         TEXT PRIMARY KEY,
  group_id   TEXT,
  name       TEXT NOT NULL,
  body       TEXT NOT NULL,
  sort       BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE known_host (
  id          TEXT PRIMARY KEY,
  host        TEXT NOT NULL,
  port        BIGINT NOT NULL,
  key_type    TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  added_at    BIGINT NOT NULL,
  UNIQUE(host, port, key_type)
);

CREATE TABLE terminal_recording (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  tab_id     TEXT NOT NULL,
  path       TEXT NOT NULL,
  bytes      BIGINT NOT NULL DEFAULT 0,
  started_at BIGINT NOT NULL,
  ended_at   BIGINT
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
  exit_code           BIGINT,
  result_error        TEXT NOT NULL DEFAULT '',
  audit_state         TEXT NOT NULL,
  audit_error         TEXT NOT NULL DEFAULT '',
  audit_completed_at  BIGINT,
  revision            BIGINT NOT NULL,
  created_at          BIGINT NOT NULL,
  started_at          BIGINT,
  finished_at         BIGINT
);

CREATE TABLE cron_job (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  prompt       TEXT NOT NULL,
  schedule     TEXT NOT NULL,
  timezone     TEXT NOT NULL,
  enabled      SMALLINT NOT NULL CHECK(enabled IN (0, 1)),
  timeout_ms   BIGINT NOT NULL CHECK(timeout_ms > 0),
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL,
  revision     BIGINT NOT NULL CHECK(revision > 0),
  next_run_at  BIGINT NOT NULL,
  retry_at     BIGINT,
  circuit_open_until BIGINT,
  consecutive_failures BIGINT NOT NULL DEFAULT 0,
  last_run_at  BIGINT,
  last_scheduled_for BIGINT,
  last_coalesced SMALLINT NOT NULL DEFAULT 0 CHECK(last_coalesced IN (0, 1)),
  last_error   TEXT NOT NULL DEFAULT '',
  lease_owner  TEXT NOT NULL DEFAULT '',
  lease_expires_at BIGINT,
  run_id       TEXT NOT NULL DEFAULT '',
  run_scheduled_for BIGINT,
  run_started_at BIGINT,
  run_deadline BIGINT,
  run_coalesced SMALLINT NOT NULL DEFAULT 0 CHECK(run_coalesced IN (0, 1)),
  model_profile_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_cron_job_session ON cron_job(session_id);

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
  finished_at BIGINT,
  profile_id TEXT NOT NULL DEFAULT '',
  cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
  latency_ms BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX idx_ai_run_conv ON ai_run(conversation_id, created_at);
CREATE INDEX idx_ai_run_status ON ai_run(status);
CREATE INDEX idx_ai_run_profile ON ai_run(profile_id, created_at);

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

CREATE TABLE transcript (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  asset_name  TEXT NOT NULL DEFAULT '',
  asset_kind  TEXT NOT NULL DEFAULT '',
  started_at  BIGINT NOT NULL,
  ended_at    BIGINT,
  bytes       BIGINT NOT NULL DEFAULT 0,
  chunks      BIGINT NOT NULL DEFAULT 0,
  truncated   SMALLINT NOT NULL DEFAULT 0,
  sync_opt_in SMALLINT NOT NULL DEFAULT 0,
  content_omitted SMALLINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_asset_started ON transcript(asset_id, started_at DESC);
CREATE INDEX idx_transcript_session ON transcript(session_id);

CREATE TABLE transcript_chunk (
  transcript_id TEXT NOT NULL REFERENCES transcript(id) ON DELETE CASCADE,
  seq         BIGINT NOT NULL,
  tab_id      TEXT NOT NULL DEFAULT '',
  ts          BIGINT NOT NULL,
  data        BYTEA NOT NULL,
  kind        SMALLINT NOT NULL DEFAULT 0,
  PRIMARY KEY (transcript_id, seq)
);

CREATE TABLE durable_transcript_offset (
  durable_id  TEXT PRIMARY KEY,
  "offset"    BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);

CREATE TABLE credential_tombstone (
  id         TEXT PRIMARY KEY,
  deleted_at BIGINT NOT NULL
);

CREATE TABLE app_user (
  id                  TEXT PRIMARY KEY,
  username            TEXT NOT NULL,
  display_name        TEXT NOT NULL DEFAULT '',
  role                TEXT NOT NULL CHECK(role IN ('superadmin','user')),
  password_hash       TEXT NOT NULL,
  state               TEXT NOT NULL CHECK(state IN ('active','disabled','reset_required')),
  must_change_password SMALLINT NOT NULL DEFAULT 0,
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL,
  last_login_at       BIGINT
);
CREATE UNIQUE INDEX idx_app_user_username ON app_user(lower(username));

CREATE TABLE user_dek (
  user_id           TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  dek_envelope      BYTEA NOT NULL,
  kdf_salt          BYTEA NOT NULL,
  kdf_params        TEXT NOT NULL,
  recovery_envelope BYTEA NOT NULL,
  recovery_hash     TEXT NOT NULL,
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL
);

CREATE TABLE user_device (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'desktop',
  created_at   BIGINT NOT NULL,
  last_seen_at BIGINT,
  revoked_at   BIGINT
);
CREATE INDEX idx_user_device_user ON user_device(user_id);

CREATE TABLE user_session (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id  TEXT REFERENCES user_device(id) ON DELETE SET NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at BIGINT NOT NULL,
  touched_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  revoked_at BIGINT
);
CREATE INDEX idx_user_session_user ON user_session(user_id);
CREATE INDEX idx_user_session_device ON user_session(device_id);

CREATE TABLE sync_credential (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  purpose      TEXT NOT NULL,
  secret_hash  TEXT NOT NULL UNIQUE,
  created_at   BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL DEFAULT 0,
  revoked_at   BIGINT,
  last_used_at BIGINT
);
CREATE INDEX idx_sync_credential_user ON sync_credential(user_id);
CREATE INDEX idx_sync_credential_device ON sync_credential(device_id);

CREATE TABLE user_setting (
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY(user_id, key)
);

CREATE TABLE device_enroll_code (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  code_hash   TEXT NOT NULL UNIQUE,
  created_at  BIGINT NOT NULL,
  expires_at  BIGINT NOT NULL,
  consumed_at BIGINT
);
CREATE INDEX idx_device_enroll_code_user ON device_enroll_code(user_id);

CREATE TABLE user_sync_object (
  user_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  id      TEXT NOT NULL,
  seq     BIGINT NOT NULL,
  blob    BYTEA NOT NULL,
  blob_hash BYTEA,
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
  desired_autostart  SMALLINT NOT NULL DEFAULT 1,
  terminal_enabled   SMALLINT NOT NULL DEFAULT 1,
  current_url        TEXT NOT NULL DEFAULT '',
  service_state_json TEXT NOT NULL DEFAULT '{}',
  created_at         BIGINT NOT NULL,
  last_seen_at       BIGINT
);

CREATE TABLE device_metrics (
  id         TEXT PRIMARY KEY,
  device_id  TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  ts         BIGINT NOT NULL,
  cpu_pct    DOUBLE PRECISION,
  mem_used   BIGINT,
  mem_total  BIGINT,
  disk_used  BIGINT,
  disk_total BIGINT,
  uptime_s   BIGINT
);
CREATE INDEX idx_device_metrics_device_ts ON device_metrics(device_id, ts);

CREATE TABLE device_metrics_hourly (
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  bucket_ts    BIGINT NOT NULL,
  cpu_pct      DOUBLE PRECISION,
  mem_used     BIGINT,
  mem_total    BIGINT,
  disk_used    BIGINT,
  disk_total   BIGINT,
  uptime_s     BIGINT,
  sample_count BIGINT NOT NULL,
  PRIMARY KEY (device_id, bucket_ts)
);

CREATE TABLE sync_tombstone (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  deleted_at BIGINT NOT NULL
);

CREATE TABLE sync_state (
  user_id      TEXT NOT NULL,
  object_id    TEXT NOT NULL,
  payload_hash BYTEA,
  blob_hash    BYTEA NOT NULL,
  PRIMARY KEY (user_id, object_id)
);

CREATE TABLE share_link (
  id               TEXT PRIMARY KEY,
  owner_id         TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id        TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  session_id       TEXT NOT NULL,
  token_hash       TEXT NOT NULL UNIQUE,
  permission       TEXT NOT NULL DEFAULT 'read',
  created_at       BIGINT NOT NULL,
  expires_at       BIGINT NOT NULL,
  revoked_at       BIGINT,
  last_accessed_at BIGINT
);
CREATE INDEX idx_share_link_owner ON share_link(owner_id);
CREATE INDEX idx_share_link_device ON share_link(device_id);

CREATE TABLE host_share (
  id           TEXT PRIMARY KEY,
  owner_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  device_id    TEXT NOT NULL REFERENCES user_device(id) ON DELETE CASCADE,
  recipient_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  permission   TEXT NOT NULL DEFAULT 'read_write',
  created_at   BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL,
  revoked_at   BIGINT,
  UNIQUE(owner_id, device_id, recipient_id)
);
CREATE INDEX idx_host_share_device ON host_share(device_id);
CREATE INDEX idx_host_share_recipient ON host_share(recipient_id);

CREATE TABLE command_log (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  session_id  TEXT NOT NULL,
  tab_id      TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  user_id     TEXT,
  command     TEXT NOT NULL,
  source      TEXT NOT NULL,
  exit_code   BIGINT,
  started_at  BIGINT NOT NULL,
  finished_at BIGINT NOT NULL
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
  created_at       BIGINT NOT NULL,
  not_before       BIGINT NOT NULL DEFAULT 0,
  expires_at       BIGINT NOT NULL,
  revoked_at       BIGINT,
  last_accessed_at BIGINT
);
CREATE INDEX idx_device_share_link_owner ON device_share_link(owner_id);
CREATE INDEX idx_device_share_link_device ON device_share_link(device_id);

CREATE TABLE user_totp (
  user_id                 TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  secret_envelope         BYTEA,
  pending_secret_envelope BYTEA,
  created_at              BIGINT NOT NULL,
  updated_at              BIGINT NOT NULL
);

CREATE TABLE user_totp_recovery_code (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  used_at    BIGINT
);
CREATE INDEX idx_user_totp_recovery_code_user ON user_totp_recovery_code(user_id);

CREATE OR REPLACE FUNCTION sync_tombstone_delete() RETURNS trigger AS $$
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at)
  VALUES (OLD.id, TG_ARGV[0], (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint)
  ON CONFLICT(id) DO NOTHING;
  RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sync_tombstone_asset_group_delete AFTER DELETE ON asset_group
FOR EACH ROW EXECUTE FUNCTION sync_tombstone_delete('group');

CREATE TRIGGER sync_tombstone_snippet_delete AFTER DELETE ON snippet
FOR EACH ROW EXECUTE FUNCTION sync_tombstone_delete('snippet');
