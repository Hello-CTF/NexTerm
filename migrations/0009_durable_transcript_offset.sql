CREATE TABLE durable_transcript_offset (
  durable_id  TEXT PRIMARY KEY,
  offset      INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
