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
