ALTER TABLE ai_run ADD COLUMN profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_run ADD COLUMN cache_creation_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE ai_run ADD COLUMN latency_ms BIGINT NOT NULL DEFAULT 0;

CREATE INDEX idx_ai_run_profile ON ai_run(profile_id, created_at);
