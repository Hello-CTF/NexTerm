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
