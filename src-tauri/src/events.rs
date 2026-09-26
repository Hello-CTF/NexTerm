//! Rust → 前端事件名常量与 payload 结构（§6.3）。
//! 控制信息走事件，二进制数据走 Channel —— 两者绝不混用（§6.4）。

use serde::Serialize;

pub const SESSION_STATUS: &str = "session://status";
pub const TERMINAL_EXIT: &str = "terminal://exit";
pub const TERMINAL_THROTTLED: &str = "terminal://throttled";
pub const FS_PROGRESS: &str = "fs://progress";
pub const DOCKER_STATS: &str = "docker://stats";
pub const AI_EVENT: &str = "ai://event";
/// M5 预留，v1 不发。
pub const SYNC_STATUS: &str = "sync://status";
pub const APP_ERROR: &str = "app://error";

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionStatusPayload {
    pub session_id: String,
    /// connecting | connected | reconnecting | disconnected | failed
    pub status: String,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalExitPayload {
    pub tab_id: String,
    pub exit_code: Option<i32>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalThrottledPayload {
    pub tab_id: String,
    pub inflight_bytes: usize,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FsProgressPayload {
    pub task_id: String,
    pub transferred: u64,
    pub total: u64,
    pub done: bool,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AppErrorPayload {
    pub code: String,
    pub message: String,
    pub detail: Option<String>,
}
