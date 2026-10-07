CREATE TABLE transcript (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  asset_id    TEXT NOT NULL,
  asset_name  TEXT NOT NULL DEFAULT '',
  asset_kind  TEXT NOT NULL DEFAULT '',
  started_at  BIGINT NOT NULL,
  ended_at    BIGINT,
  bytes       BIGINT NOT NULL DEFAULT 0,
  chunks      BIGINT NOT NULL DEFAULT 0,
  truncated   SMALLINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_asset_started ON transcript(asset_id, started_at DESC);
CREATE INDEX idx_transcript_session ON transcript(session_id);

CREATE TABLE transcript_chunk (
  transcript_id TEXT NOT NULL REFERENCES transcript(id) ON DELETE CASCADE,
  seq         BIGINT NOT NULL,
  tab_id      TEXT NOT NULL DEFAULT '',
  ts          BIGINT NOT NULL,
  data        BYTEA NOT NULL,
  PRIMARY KEY (transcript_id, seq)
);
