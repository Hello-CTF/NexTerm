DROP INDEX idx_audit_ts;
CREATE INDEX idx_audit_retention ON audit_log(ts DESC, id DESC);
CREATE INDEX idx_terminal_recording_retention
  ON terminal_recording(ended_at DESC, id DESC)
  WHERE ended_at IS NOT NULL;
