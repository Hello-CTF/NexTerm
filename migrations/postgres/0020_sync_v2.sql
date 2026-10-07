DROP TABLE sync_tokens;

DELETE FROM setting WHERE key IN ('sync.token', 'sync.token.plaintext_backup', 'sync.link');

CREATE TABLE sync_tombstone (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  deleted_at BIGINT NOT NULL
);

ALTER TABLE transcript ADD COLUMN sync_opt_in SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE transcript ADD COLUMN content_omitted SMALLINT NOT NULL DEFAULT 0;

CREATE TABLE sync_state (
  user_id      TEXT NOT NULL,
  object_id    TEXT NOT NULL,
  payload_hash BYTEA,
  blob_hash    BYTEA NOT NULL,
  PRIMARY KEY (user_id, object_id)
);

ALTER TABLE user_sync_object ADD COLUMN blob_hash BYTEA;

CREATE OR REPLACE FUNCTION sync_tombstone_delete() RETURNS trigger AS $$
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at)
  VALUES (OLD.id, TG_ARGV[0], (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint)
  ON CONFLICT(id) DO NOTHING;
  RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sync_tombstone_asset_group_delete AFTER DELETE ON asset_group
FOR EACH ROW EXECUTE FUNCTION sync_tombstone_delete('group');

CREATE TRIGGER sync_tombstone_snippet_delete AFTER DELETE ON snippet
FOR EACH ROW EXECUTE FUNCTION sync_tombstone_delete('snippet');
