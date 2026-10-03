-- AI 定时任务：按会话持久化 cron 作业、租约与运行状态。
-- 并发安全依赖 revision 乐观并发（CAS）；租约与运行记录在 claim 的同一次
-- CAS 写入中落库，重启后不得自动重放已认领的执行（中断由后续调度如实记录）。
CREATE TABLE IF NOT EXISTS cron_job (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,             -- 属主会话；跨会话只读，不得删除他人任务
  name         TEXT NOT NULL DEFAULT '',
  prompt       TEXT NOT NULL,
  schedule     TEXT NOT NULL,             -- 5 段 cron 表达式
  timezone     TEXT NOT NULL,             -- IANA 时区名
  enabled      INTEGER NOT NULL CHECK(enabled IN (0, 1)),
  timeout_ms   INTEGER NOT NULL CHECK(timeout_ms > 0),
  created_at   INTEGER NOT NULL,          -- 毫秒
  updated_at   INTEGER NOT NULL,          -- 毫秒
  revision     INTEGER NOT NULL CHECK(revision > 0),  -- 每次成功 CAS 后递增
  next_run_at  INTEGER NOT NULL,          -- 毫秒
  retry_at     INTEGER,                   -- 毫秒；NULL 表示无退避重试
  circuit_open_until INTEGER,             -- 毫秒；NULL 表示熔断关闭
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_run_at  INTEGER,                   -- 毫秒
  last_scheduled_for INTEGER,             -- 毫秒
  last_coalesced INTEGER NOT NULL DEFAULT 0 CHECK(last_coalesced IN (0, 1)),
  last_error   TEXT NOT NULL DEFAULT '',  -- 有界（≤2048 字符）执行错误
  lease_owner  TEXT NOT NULL DEFAULT '',  -- 空串表示无租约
  lease_expires_at INTEGER,               -- 毫秒；仅当 lease_owner 非空时有效
  run_id       TEXT NOT NULL DEFAULT '',  -- 空串表示无已认领执行
  run_scheduled_for INTEGER,              -- 毫秒
  run_started_at INTEGER,                 -- 毫秒
  run_deadline INTEGER,                   -- 毫秒
  run_coalesced INTEGER NOT NULL DEFAULT 0 CHECK(run_coalesced IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_cron_job_session ON cron_job(session_id);
