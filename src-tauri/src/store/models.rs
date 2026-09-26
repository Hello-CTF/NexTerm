//! 数据行模型（与 §3 的 SQLite 表一一对应）。
//! 时间戳为 i64 Unix 毫秒；ID 为 ULID 字符串。

use serde::Serialize;
use sqlx::FromRow;

#[derive(Debug, Clone, FromRow, Serialize)]
pub struct AssetGroupRow {
    pub id: String,
    pub parent_id: Option<String>,
    pub name: String,
    pub sort: i64,
    pub created_at: i64,
    pub updated_at: i64,
}

#[derive(Debug, Clone, FromRow, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetRow {
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
    pub options_json: String,
    pub tags: String,
    pub note: String,
    pub sort: i64,
    pub created_at: i64,
    pub updated_at: i64,
    pub deleted_at: Option<i64>,
}

#[derive(Debug, Clone, FromRow)]
pub struct CredentialRow {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub cipher: String,
    pub nonce: Vec<u8>,
    pub blob: Vec<u8>,
    pub kek_hint: String,
    pub created_at: i64,
    pub updated_at: i64,
}

/// 凭据元数据（不含密文）——暴露给前端的形态。
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CredentialMeta {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub cipher: String,
    pub kek_hint: String,
    pub created_at: i64,
    pub updated_at: i64,
}

impl From<&CredentialRow> for CredentialMeta {
    fn from(r: &CredentialRow) -> Self {
        Self {
            id: r.id.clone(),
            name: r.name.clone(),
            kind: r.kind.clone(),
            cipher: r.cipher.clone(),
            kek_hint: r.kek_hint.clone(),
            created_at: r.created_at,
            updated_at: r.updated_at,
        }
    }
}

#[derive(Debug, Clone, FromRow, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditRow {
    pub id: i64,
    pub ts: i64,
    pub session_id: Option<String>,
    pub asset_id: Option<String>,
    pub source: String,
    pub kind: String,
    pub payload_json: String,
    pub exit_code: Option<i32>,
    pub duration_ms: Option<i64>,
}

#[derive(Debug, Clone, FromRow, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConversationRow {
    pub id: String,
    pub title: String,
    pub scope_json: String,
    pub created_at: i64,
    pub updated_at: i64,
}

#[derive(Debug, Clone, FromRow, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MessageRow {
    pub id: String,
    pub conversation_id: String,
    pub role: String,
    pub content_json: String,
    pub tokens_in: Option<i64>,
    pub tokens_out: Option<i64>,
    pub created_at: i64,
}

#[derive(Debug, Clone, FromRow, Serialize)]
pub struct SnippetRow {
    pub id: String,
    pub group_id: Option<String>,
    pub name: String,
    pub body: String,
    pub sort: i64,
    pub created_at: i64,
    pub updated_at: i64,
}

#[derive(Debug, Clone, FromRow, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct KnownHostRow {
    pub id: String,
    pub host: String,
    pub port: i32,
    pub key_type: String,
    pub fingerprint: String,
    pub added_at: i64,
}

#[derive(Debug, Clone, FromRow, Serialize)]
pub struct RecordingRow {
    pub id: String,
    pub session_id: String,
    pub tab_id: String,
    pub path: String,
    pub bytes: i64,
    pub started_at: i64,
    pub ended_at: Option<i64>,
}
