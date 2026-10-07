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
  PRIMARY KEY (user_id, id)
);
CREATE INDEX idx_user_sync_object_seq ON user_sync_object(user_id, seq);

CREATE TABLE user_sync_head (
  user_id   TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  head_hash TEXT NOT NULL
);
