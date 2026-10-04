CREATE TABLE IF NOT EXISTS outcome_record (
  idempotence_key     TEXT PRIMARY KEY,
  authorization_id    TEXT NOT NULL,
  kind                TEXT NOT NULL,
  canonical_arguments TEXT NOT NULL,
  state               TEXT NOT NULL,
  outcome             TEXT NOT NULL,
  exit_code           INTEGER,
  result_error        TEXT NOT NULL DEFAULT '',
  audit_state         TEXT NOT NULL,
  audit_error         TEXT NOT NULL DEFAULT '',
  audit_completed_at  INTEGER,
  revision            INTEGER NOT NULL,
  created_at          INTEGER NOT NULL,
  started_at          INTEGER,
  finished_at         INTEGER
);
