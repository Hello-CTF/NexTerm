-- ai_message 增加每会话单调插入序号：MsgList 以此反映真实插入顺序。
-- created_at 只有毫秒精度，同一毫秒内的行曾退化为按 ULID 随机尾排序，
--  steered 补充行与回答行同毫秒落库时顺序由随机数决定（竞态）。
-- seq 在插入事务内以 MAX(seq)+1 分配（_txlock=immediate 串行化写者），
-- 唯一索引把"每会话 seq 严格递增"固化为数据库约束。
ALTER TABLE ai_message ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;

-- 存量行按旧语义（created_at, id）回填，保证迁移前后读序一致。
UPDATE ai_message SET seq = (
  SELECT ranked.position FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY conversation_id ORDER BY created_at, id
    ) AS position
    FROM ai_message
  ) AS ranked WHERE ranked.id = ai_message.id
);

CREATE UNIQUE INDEX idx_ai_msg_conv_seq ON ai_message(conversation_id, seq);
