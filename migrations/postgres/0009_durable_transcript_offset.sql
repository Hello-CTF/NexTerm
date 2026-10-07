CREATE TABLE durable_transcript_offset (
  durable_id  TEXT PRIMARY KEY,
  "offset"    BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);
