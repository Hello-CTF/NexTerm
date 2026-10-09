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
