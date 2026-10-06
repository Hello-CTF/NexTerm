-- SYNC117 v2 全量载荷对象协议(0020): 令牌时代 sync_tokens 退役, 通用删除墓碑、会话记录同步列与对象对账簿。
DROP TABLE sync_tokens;

DELETE FROM setting WHERE key IN ('sync.token', 'sync.token.plaintext_backup', 'sync.link');

CREATE TABLE sync_tombstone (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  deleted_at INTEGER NOT NULL
);

ALTER TABLE transcript ADD COLUMN sync_opt_in INTEGER NOT NULL DEFAULT 0;
ALTER TABLE transcript ADD COLUMN content_omitted INTEGER NOT NULL DEFAULT 0;

CREATE TABLE sync_state (
  user_id      TEXT NOT NULL,
  object_id    TEXT NOT NULL,
  payload_hash BLOB,
  blob_hash    BLOB NOT NULL,
  PRIMARY KEY (user_id, object_id)
);

ALTER TABLE user_sync_object ADD COLUMN blob_hash BLOB;

-- asset_group/snippet 的删除入口不在本迁移所有者的改动范围内, 用触发器保证任何删除路径都留下墓碑;
-- transcript 的删除与保留清理在 store 层显式写墓碑, 不经过触发器。
-- strftime 精确到毫秒, 保证 deleted_at 与同秒 updated_at 的比较不颠倒修订顺序。
CREATE TRIGGER sync_tombstone_asset_group_delete AFTER DELETE ON asset_group
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES (old.id, 'group', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER))
  ON CONFLICT(id) DO NOTHING;
END;

CREATE TRIGGER sync_tombstone_snippet_delete AFTER DELETE ON snippet
BEGIN
  INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES (old.id, 'snippet', CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER))
  ON CONFLICT(id) DO NOTHING;
END;
