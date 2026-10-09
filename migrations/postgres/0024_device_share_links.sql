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
