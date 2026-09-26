PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- 资产分组（树）
CREATE TABLE asset_group (
  id          TEXT PRIMARY KEY,
  parent_id   TEXT REFERENCES asset_group(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  sort        INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- 资产（服务器 / 数据库 / Docker 主机 / 本地终端）
CREATE TABLE asset (
  id           TEXT PRIMARY KEY,
  group_id     TEXT REFERENCES asset_group(id) ON DELETE SET NULL,
  kind         TEXT NOT NULL,        -- ssh | winrm | local | docker | mysql | redis
  name         TEXT NOT NULL,
  host         TEXT,                 -- 本地终端为 NULL
  port         INTEGER,
  username     TEXT,
  auth_kind    TEXT,                 -- password | key | agent | ntlm | none
  key_path     TEXT,                 -- 私钥文件路径（不复制内容）
  cred_id      TEXT REFERENCES credential(id) ON DELETE SET NULL,
  options_json TEXT NOT NULL DEFAULT '{}',  -- 按 kind 不同：encoding/keepalive/proxy/初始命令/tls...
  tags         TEXT NOT NULL DEFAULT '',    -- 逗号分隔
  note         TEXT NOT NULL DEFAULT '',
  sort         INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  deleted_at   INTEGER                -- 墓碑（为将来的同步预留）
);
CREATE INDEX idx_asset_group ON asset(group_id);
CREATE INDEX idx_asset_kind  ON asset(kind);

-- 凭据（密文）
CREATE TABLE credential (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,          -- password | private_key | passphrase | api_key
  cipher     TEXT NOT NULL,          -- 加密算法标识，如 "xchacha20poly1305-v1"
  nonce      BLOB NOT NULL,
  blob       BLOB NOT NULL,          -- 密文
  kek_hint   TEXT NOT NULL,          -- "dpapi" | "master:<salt_id>" —— 用哪个 KEK 解
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- 设置（键值）
CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- 审计日志（用户与 AI 的所有关键动作）
CREATE TABLE audit_log (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           INTEGER NOT NULL,
  session_id   TEXT,
  asset_id     TEXT,
  source       TEXT NOT NULL,        -- user | ai
  kind         TEXT NOT NULL,        -- exec | write_file | docker_action | db_execute | mount | connect | takeover
  payload_json TEXT NOT NULL,
  exit_code    INTEGER,
  duration_ms  INTEGER
);
CREATE INDEX idx_audit_ts ON audit_log(ts DESC);

-- AI 会话
CREATE TABLE ai_conversation (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '{}',  -- 绑定的 asset/session/tab
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE ai_message (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
  role            TEXT NOT NULL,     -- system | user | assistant | tool
  content_json    TEXT NOT NULL,     -- 原样保存 provider 的 message 结构
  tokens_in       INTEGER,
  tokens_out      INTEGER,
  created_at      INTEGER NOT NULL
);
CREATE INDEX idx_ai_msg_conv ON ai_message(conversation_id, created_at);

-- 快捷命令片段
CREATE TABLE snippet (
  id         TEXT PRIMARY KEY,
  group_id   TEXT,
  name       TEXT NOT NULL,
  body       TEXT NOT NULL,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- 已知主机指纹（SSH known_hosts）
CREATE TABLE known_host (
  id          TEXT PRIMARY KEY,
  host        TEXT NOT NULL,
  port        INTEGER NOT NULL,
  key_type    TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  added_at    INTEGER NOT NULL,
  UNIQUE(host, port, key_type)
);

-- 终端录制（可选功能，默认关）
CREATE TABLE terminal_recording (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  tab_id     TEXT NOT NULL,
  path       TEXT NOT NULL,          -- 落盘文件路径
  bytes      INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  ended_at   INTEGER
);
