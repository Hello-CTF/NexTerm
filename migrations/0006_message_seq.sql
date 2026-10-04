ALTER TABLE ai_message ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;

UPDATE ai_message SET seq = (
  SELECT ranked.position FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY conversation_id ORDER BY created_at, id
    ) AS position
    FROM ai_message
  ) AS ranked WHERE ranked.id = ai_message.id
);

CREATE UNIQUE INDEX idx_ai_msg_conv_seq ON ai_message(conversation_id, seq);
