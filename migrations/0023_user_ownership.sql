ALTER TABLE asset_group ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE asset ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE credential ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE snippet ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
CREATE TABLE known_host_ownership (
  id TEXT PRIMARY KEY,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  key_type TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  added_at INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  UNIQUE(owner_id, host, port, key_type)
);
INSERT INTO known_host_ownership(id, host, port, key_type, fingerprint, added_at, owner_id)
SELECT id, host, port, key_type, fingerprint, added_at, '' FROM known_host;
DROP TABLE known_host;
ALTER TABLE known_host_ownership RENAME TO known_host;
ALTER TABLE ai_conversation ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_message ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_run ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_run_event ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_checkpoint ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_hitl_run ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE transcript ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE transcript_chunk ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE durable_transcript_offset ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE terminal_recording ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
CREATE TABLE outcome_record_ownership (
  idempotence_key TEXT NOT NULL,
  authorization_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  canonical_arguments TEXT NOT NULL,
  state TEXT NOT NULL,
  outcome TEXT NOT NULL,
  exit_code INTEGER,
  result_error TEXT NOT NULL DEFAULT '',
  audit_state TEXT NOT NULL,
  audit_error TEXT NOT NULL DEFAULT '',
  audit_completed_at INTEGER,
  revision INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(owner_id, idempotence_key)
);
INSERT INTO outcome_record_ownership(idempotence_key, authorization_id, kind, canonical_arguments, state, outcome,
  exit_code, result_error, audit_state, audit_error, audit_completed_at, revision, created_at, started_at, finished_at, owner_id)
SELECT idempotence_key, authorization_id, kind, canonical_arguments, state, outcome,
  exit_code, result_error, audit_state, audit_error, audit_completed_at, revision, created_at, started_at, finished_at, '' FROM outcome_record;
DROP TABLE outcome_record;
ALTER TABLE outcome_record_ownership RENAME TO outcome_record;
ALTER TABLE cron_job ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
CREATE TABLE sync_tombstone_ownership (
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  deleted_at INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(owner_id, id)
);
INSERT INTO sync_tombstone_ownership(id, kind, deleted_at, owner_id)
SELECT id, kind, deleted_at, '' FROM sync_tombstone;
DROP TABLE sync_tombstone;
CREATE TABLE sync_tombstone (
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  deleted_at INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(owner_id, id)
);
INSERT INTO sync_tombstone(id, kind, deleted_at, owner_id)
SELECT id, kind, deleted_at, owner_id FROM sync_tombstone_ownership;
DROP TABLE sync_tombstone_ownership;
CREATE TABLE credential_tombstone_ownership (
  id TEXT NOT NULL,
  deleted_at INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(owner_id, id)
);
INSERT INTO credential_tombstone_ownership(id, deleted_at, owner_id)
SELECT id, deleted_at, '' FROM credential_tombstone;
DROP TABLE credential_tombstone;
CREATE TABLE credential_tombstone (
  id TEXT NOT NULL,
  deleted_at INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(owner_id, id)
);
INSERT INTO credential_tombstone(id, deleted_at, owner_id)
SELECT id, deleted_at, owner_id FROM credential_tombstone_ownership;
DROP TABLE credential_tombstone_ownership;

DROP TRIGGER IF EXISTS sync_tombstone_asset_group_delete;
CREATE TRIGGER sync_tombstone_asset_group_delete AFTER DELETE ON asset_group
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at, owner_id)
  VALUES (old.id, 'group', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER), old.owner_id)
  ON CONFLICT(owner_id, id) DO NOTHING;
END;

DROP TRIGGER IF EXISTS sync_tombstone_snippet_delete;
CREATE TRIGGER sync_tombstone_snippet_delete AFTER DELETE ON snippet
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at, owner_id)
  VALUES (old.id, 'snippet', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER), old.owner_id)
  ON CONFLICT(owner_id, id) DO NOTHING;
END;

UPDATE asset_group SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE asset SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE credential SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE snippet SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE known_host SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_conversation SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_message SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_run SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_run_event SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_checkpoint SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE ai_hitl_run SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE transcript SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE transcript_chunk SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE durable_transcript_offset SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE terminal_recording SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE outcome_record SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE cron_job SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE audit_log SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE sync_tombstone SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE credential_tombstone SET owner_id = COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '');
UPDATE setting SET key = CASE WHEN EXISTS(SELECT 1 FROM app_user WHERE role = 'superadmin') THEN 'user.ai.models.' || COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '') ELSE key END WHERE key = 'ai.models';
UPDATE setting SET key = CASE WHEN EXISTS(SELECT 1 FROM app_user WHERE role = 'superadmin') THEN 'user.layout.' || COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '') ELSE key END WHERE key = 'layout.workspace';
UPDATE setting SET key = CASE WHEN EXISTS(SELECT 1 FROM app_user WHERE role = 'superadmin') THEN 'ai.permission.user.' || COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '') ELSE key END WHERE key = 'ai.permission';
UPDATE setting SET key = CASE WHEN EXISTS(SELECT 1 FROM app_user WHERE role = 'superadmin') THEN 'ai.device_permission.user.' || COALESCE((SELECT id FROM app_user WHERE role = 'superadmin' ORDER BY created_at, id LIMIT 1), '') ELSE key END WHERE key = 'ai.device_permission';

CREATE INDEX idx_asset_group_owner ON asset_group(owner_id);
CREATE INDEX idx_asset_owner ON asset(owner_id);
CREATE INDEX idx_credential_owner ON credential(owner_id);
CREATE INDEX idx_snippet_owner ON snippet(owner_id);
CREATE INDEX idx_known_host_owner ON known_host(owner_id);
CREATE INDEX idx_ai_conversation_owner ON ai_conversation(owner_id);
CREATE INDEX idx_ai_message_owner ON ai_message(owner_id);
CREATE INDEX idx_ai_run_owner ON ai_run(owner_id);
CREATE INDEX idx_ai_run_event_owner ON ai_run_event(owner_id);
CREATE INDEX idx_ai_checkpoint_owner ON ai_checkpoint(owner_id);
CREATE INDEX idx_ai_hitl_run_owner ON ai_hitl_run(owner_id);
CREATE INDEX idx_transcript_owner ON transcript(owner_id);
CREATE INDEX idx_transcript_chunk_owner ON transcript_chunk(owner_id);
CREATE INDEX idx_durable_transcript_offset_owner ON durable_transcript_offset(owner_id);
CREATE INDEX idx_terminal_recording_owner ON terminal_recording(owner_id);
CREATE INDEX idx_outcome_record_owner ON outcome_record(owner_id);
CREATE INDEX idx_cron_job_owner ON cron_job(owner_id);
CREATE INDEX idx_audit_log_owner ON audit_log(owner_id);
CREATE INDEX idx_sync_tombstone_owner ON sync_tombstone(owner_id);
CREATE INDEX idx_credential_tombstone_owner ON credential_tombstone(owner_id);
