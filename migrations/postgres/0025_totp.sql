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
