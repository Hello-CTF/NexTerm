-- 外部副作用结果账本：按幂等键持久化执行状态、结果与审计状态。
-- 状态迁移使用 revision 乐观并发（CAS）；账本不伪造 exactly-once，
-- 未知结果（unknown）不得自动重试，也不得显示为确定失败。
CREATE TABLE IF NOT EXISTS outcome_record (
  idempotence_key     TEXT PRIMARY KEY,
  authorization_id    TEXT NOT NULL,        -- 授权本次执行的 guard 决策标识
  kind                TEXT NOT NULL,        -- command | file | database
  canonical_arguments TEXT NOT NULL,        -- 规范化后的 JSON 参数（键有序、空白规整）
  state               TEXT NOT NULL,        -- pending | running | finished
  outcome             TEXT NOT NULL,        -- rejected | not_attempted | failed | accepted | unknown
  exit_code           INTEGER,              -- 进程式结果的退出码；无进程语义为 NULL
  result_error        TEXT NOT NULL DEFAULT '',
  audit_state         TEXT NOT NULL,        -- pending | persisted | failed
  audit_error         TEXT NOT NULL DEFAULT '',
  audit_completed_at  INTEGER,              -- 审计终态完成时间（毫秒）
  revision            INTEGER NOT NULL,     -- 每次成功迁移后递增
  created_at          INTEGER NOT NULL,     -- 毫秒
  started_at          INTEGER,              -- 毫秒
  finished_at         INTEGER               -- 毫秒
);
