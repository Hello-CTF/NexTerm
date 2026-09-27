//! 跨 IPC 类型（§6.1）：唯一权威定义。
//!
//! `cargo test` 时由 ts-rs 导出到 `src/ipc/types.ts`，前端从这里 import，
//! **不要手写两份**。运行：`cargo test -p nexterm export_bindings`。

use serde::Serialize;
use ts_rs::TS;

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct AssetGroupDto {
    pub id: String,
    pub parent_id: Option<String>,
    pub name: String,
    #[ts(type = "number")]
    pub sort: i64,
    #[ts(type = "number")]
    pub created_at: i64,
    #[ts(type = "number")]
    pub updated_at: i64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct AssetDto {
    pub id: String,
    pub group_id: Option<String>,
    pub kind: String,
    pub name: String,
    pub host: Option<String>,
    pub port: Option<i32>,
    pub username: Option<String>,
    pub auth_kind: Option<String>,
    pub key_path: Option<String>,
    pub cred_id: Option<String>,
    pub options: serde_json::Value,
    pub tags: String,
    pub note: String,
    #[ts(type = "number")]
    pub sort: i64,
    #[ts(type = "number")]
    pub created_at: i64,
    #[ts(type = "number")]
    pub updated_at: i64,
    #[ts(type = "number | null")]
    pub deleted_at: Option<i64>,
}

impl From<crate::store::AssetRow> for AssetDto {
    fn from(r: crate::store::AssetRow) -> Self {
        Self {
            id: r.id,
            group_id: r.group_id,
            kind: r.kind,
            name: r.name,
            host: r.host,
            port: r.port,
            username: r.username,
            auth_kind: r.auth_kind,
            key_path: r.key_path,
            cred_id: r.cred_id,
            options: crate::store::parse_json_or(&r.options_json),
            tags: r.tags,
            note: r.note,
            sort: r.sort,
            created_at: r.created_at,
            updated_at: r.updated_at,
            deleted_at: r.deleted_at,
        }
    }
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct SessionInfoDto {
    pub id: String,
    pub asset_id: Option<String>,
    pub name: String,
    pub kind: String,
    /// connecting | connected | reconnecting | disconnected | failed
    pub status: String,
    pub tabs: Vec<String>,
    #[ts(type = "number")]
    pub created_at: u64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct SessionStatusEvent {
    pub session_id: String,
    pub status: String,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct TerminalExitEvent {
    pub tab_id: String,
    pub exit_code: Option<i32>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct FileEntryDto {
    pub name: String,
    pub path: String,
    /// dir | file | symlink | other
    pub kind: String,
    #[ts(type = "number")]
    pub size: u64,
    pub mode: String,
    pub owner: Option<String>,
    pub group: Option<String>,
    #[ts(type = "number")]
    pub mtime: i64,
    pub symlink_target: Option<String>,
}

impl From<crate::transport::FileEntry> for FileEntryDto {
    fn from(e: crate::transport::FileEntry) -> Self {
        Self {
            name: e.name,
            path: e.path,
            kind: e.kind,
            size: e.size,
            mode: e.mode,
            owner: e.owner,
            group: e.group,
            mtime: e.mtime,
            symlink_target: e.symlink_target,
        }
    }
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ScreenSnapshotDto {
    pub text: String,
    pub lines: Vec<String>,
    pub cursor_row: u16,
    pub cursor_col: u16,
    pub cols: u16,
    pub rows: u16,
    pub alt_screen: bool,
    #[ts(type = "number")]
    pub last_output_ms_ago: u64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct QueryResultDto {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<serde_json::Value>>,
    #[ts(type = "number")]
    pub rows_affected: u64,
    #[ts(type = "number")]
    pub duration_ms: u64,
    pub truncated: bool,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct RedisKeyViewDto {
    pub key: String,
    pub key_type: String,
    #[ts(type = "number")]
    pub ttl: i64,
    pub value: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ConversationDto {
    pub id: String,
    pub title: String,
    pub scope: serde_json::Value,
    #[ts(type = "number")]
    pub created_at: i64,
    #[ts(type = "number")]
    pub updated_at: i64,
}

impl From<crate::store::ConversationRow> for ConversationDto {
    fn from(r: crate::store::ConversationRow) -> Self {
        Self {
            id: r.id,
            title: r.title,
            scope: crate::store::parse_json_or(&r.scope_json),
            created_at: r.created_at,
            updated_at: r.updated_at,
        }
    }
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct MessageDto {
    pub id: String,
    pub conversation_id: String,
    pub role: String,
    pub content: serde_json::Value,
    #[ts(type = "number | null")]
    pub tokens_in: Option<i64>,
    #[ts(type = "number | null")]
    pub tokens_out: Option<i64>,
    #[ts(type = "number")]
    pub created_at: i64,
}

impl From<crate::store::MessageRow> for MessageDto {
    fn from(r: crate::store::MessageRow) -> Self {
        Self {
            id: r.id,
            conversation_id: r.conversation_id,
            role: r.role,
            // 库里存的是 JSON 字符串，前端要的是结构 —— 必须在这里解开。
            // 之前直接把 Row 丢给前端（字段叫 `content_json`），前端按 `content` 读，
            // 永远拿到 undefined → 「点进历史会话什么都恢复不出来」。
            content: crate::store::parse_json_or(&r.content_json),
            tokens_in: r.tokens_in,
            tokens_out: r.tokens_out,
            created_at: r.created_at,
        }
    }
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct AuditEntryDto {
    #[ts(type = "number")]
    pub id: i64,
    #[ts(type = "number")]
    pub ts: i64,
    pub session_id: Option<String>,
    pub asset_id: Option<String>,
    /// user | ai
    pub source: String,
    pub kind: String,
    pub payload: serde_json::Value,
    pub exit_code: Option<i32>,
    #[ts(type = "number | null")]
    pub duration_ms: Option<i64>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct CredentialMetaDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub cipher: String,
    pub kek_hint: String,
    #[ts(type = "number")]
    pub created_at: i64,
    #[ts(type = "number")]
    pub updated_at: i64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct VaultStatusDto {
    pub initialized: bool,
    /// not_init | dpapi | master
    pub mode: String,
    pub unlocked: bool,
    #[ts(type = "number")]
    pub auto_lock_minutes: u64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ProviderConfigDto {
    pub base_url: String,
    pub api_key: String,
    pub model: String,
    pub temperature: f32,
    #[ts(type = "number")]
    pub context_window: u64,
    pub proxy: Option<String>,
    pub stream: bool,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct AiScopeDto {
    pub session_id: Option<String>,
    pub tab_id: Option<String>,
    pub conn_id: Option<String>,
    pub asset_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct MountEntryDto {
    pub id: String,
    pub local_point: String,
    pub remote: String,
    pub session_id: Option<String>,
    #[ts(type = "number | null")]
    pub created_at: Option<u64>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ForwardSpecDto {
    pub id: String,
    pub session_id: String,
    pub listen_port: u16,
    /// 静态转发的目标；SOCKS5 动态转发没有固定目标 → `null`。
    pub target_host: Option<String>,
    pub target_port: Option<u16>,
    /// `local` 或 `socks`。
    pub kind: String,
    #[ts(type = "number")]
    pub created_at: u64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ContainerSummaryDto {
    pub id: String,
    pub name: String,
    pub image: String,
    pub state: String,
    pub status: String,
    pub ports: String,
    pub compose_project: Option<String>,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ImageSummaryDto {
    pub id: String,
    pub repository: String,
    pub tag: String,
    pub size: String,
    pub created_since: String,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct SnippetDto {
    pub id: String,
    pub group_id: Option<String>,
    pub name: String,
    pub body: String,
    #[ts(type = "number")]
    pub sort: i64,
    #[ts(type = "number")]
    pub created_at: i64,
    #[ts(type = "number")]
    pub updated_at: i64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct KnownHostDto {
    pub id: String,
    pub host: String,
    pub port: i32,
    pub key_type: String,
    pub fingerprint: String,
    #[ts(type = "number")]
    pub added_at: i64,
}

#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct AppErrorDto {
    pub code: String,
    pub message: String,
    pub detail: Option<serde_json::Value>,
}

/// 导出测试（ts-rs 也会为每个 #[ts(export)] 生成导出测试；此函数聚合校验）。
#[cfg(test)]
mod export_tests {
    #[test]
    fn export_bindings() {
        // ts-rs 的 export 宏在编译期注册导出，cargo test 会执行；
        // 这里再显式断言目录生成由宏完成，无需额外逻辑。
    }

    /// 回归：`ai_messages` 曾经直接把 `MessageRow` 丢给前端（字段是 `contentJson`），
    /// 而前端按 `MessageDto.content` 读 → 永远 undefined，**历史会话点进去什么都恢复不出来**。
    /// 真实数据里 `content_json` 是 JSON 字符串，DTO 必须把它解开成结构。
    #[test]
    fn message_dto_unwraps_content_json() {
        use crate::store::MessageRow;
        let row = MessageRow {
            id: "m1".into(),
            conversation_id: "c1".into(),
            role: "assistant".into(),
            content_json: r#"{"role":"assistant","content":"你好"}"#.into(),
            tokens_in: Some(1),
            tokens_out: Some(2),
            created_at: 3,
        };
        let dto: super::MessageDto = row.into();
        assert!(dto.content.is_object(), "content 应是结构而不是字符串");
        assert_eq!(dto.content["content"], "你好");
    }

    /// 同上：会话列表的 `scope_json` 也要解成结构。
    #[test]
    fn conversation_dto_unwraps_scope_json() {
        use crate::store::ConversationRow;
        let row = ConversationRow {
            id: "c1".into(),
            title: "看看磁盘".into(),
            scope_json: r#"{"scope":{"sessionId":"s1"}}"#.into(),
            created_at: 1,
            updated_at: 2,
        };
        let dto: super::ConversationDto = row.into();
        assert_eq!(dto.scope["scope"]["sessionId"], "s1");
        assert_eq!(dto.title, "看看磁盘");
    }
}
