CREATE TABLE transcript (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  asset_name  TEXT NOT NULL DEFAULT '',
  asset_kind  TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,
  bytes       INTEGER NOT NULL DEFAULT 0,
  chunks      INTEGER NOT NULL DEFAULT 0,
  truncated   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_asset_started ON transcript(asset_id, started_at DESC);
CREATE INDEX idx_transcript_session ON transcript(session_id);

CREATE TABLE transcript_chunk (
  transcript_id TEXT NOT NULL REFERENCES transcript(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  tab_id      TEXT NOT NULL DEFAULT '',
  ts          INTEGER NOT NULL,
  data        BLOB NOT NULL,
  PRIMARY KEY (transcript_id, seq)
);
