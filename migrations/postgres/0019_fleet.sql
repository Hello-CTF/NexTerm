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
