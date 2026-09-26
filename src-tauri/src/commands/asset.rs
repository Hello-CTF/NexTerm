//! 资产 / 分组 / 片段 / 审计 / 已知主机 命令。

use serde::Deserialize;

use crate::error::AppResult;
use crate::state::ManagedState;
use crate::store::{AssetRow, AuditQuery, AuditRow, CredentialInput};

#[tauri::command]
pub async fn asset_list(state: ManagedState<'_>) -> AppResult<Vec<AssetRow>> {
    state.store.asset_list(false).await
}

#[tauri::command]
pub async fn asset_get(state: ManagedState<'_>, id: String) -> AppResult<AssetRow> {
    state.store.asset_get(&id).await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetCreateArgs {
    pub group_id: Option<String>,
    pub kind: String,
    pub name: String,
    pub host: Option<String>,
    pub port: Option<i32>,
    pub username: Option<String>,
    pub auth_kind: Option<String>,
    pub key_path: Option<String>,
    pub cred_id: Option<String>,
    pub options: Option<serde_json::Value>,
    pub tags: Option<String>,
    pub note: Option<String>,
}

#[tauri::command]
pub async fn asset_create(state: ManagedState<'_>, args: AssetCreateArgs) -> AppResult<AssetRow> {
    state
        .store
        .asset_create(crate::store::AssetInput {
            group_id: args.group_id,
            kind: args.kind,
            name: args.name,
            host: args.host,
            port: args.port,
            username: args.username,
            auth_kind: args.auth_kind,
            key_path: args.key_path,
            cred_id: args.cred_id,
            options_json: args
                .options
                .map(|o| o.to_string())
                .unwrap_or_else(|| "{}".into()),
            tags: args.tags.unwrap_or_default(),
            note: args.note.unwrap_or_default(),
            sort: 0,
        })
        .await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetUpdateArgs {
    pub id: String,
    pub group_id: Option<Option<String>>,
    pub name: Option<String>,
    pub host: Option<Option<String>>,
    pub port: Option<Option<i32>>,
    pub username: Option<Option<String>>,
    pub auth_kind: Option<Option<String>>,
    pub key_path: Option<Option<String>>,
    pub cred_id: Option<Option<String>>,
    pub options: Option<serde_json::Value>,
    pub tags: Option<String>,
    pub note: Option<String>,
}

#[tauri::command]
pub async fn asset_update(state: ManagedState<'_>, args: AssetUpdateArgs) -> AppResult<AssetRow> {
    state
        .store
        .asset_update(
            &args.id,
            args.group_id,
            args.name,
            args.host,
            args.port,
            args.username,
            args.auth_kind,
            args.key_path,
            args.cred_id,
            args.options.map(|o| o.to_string()),
            args.tags,
            args.note,
            None,
        )
        .await
}

#[tauri::command]
pub async fn asset_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.asset_delete(&id).await
}

#[tauri::command]
pub async fn asset_search(state: ManagedState<'_>, q: String) -> AppResult<Vec<AssetRow>> {
    state.store.asset_search(&q).await
}

// ── 分组 ──

#[tauri::command]
pub async fn group_list(state: ManagedState<'_>) -> AppResult<Vec<crate::store::AssetGroupRow>> {
    state.store.group_list().await
}

#[tauri::command]
pub async fn group_create(
    state: ManagedState<'_>,
    parent_id: Option<String>,
    name: String,
) -> AppResult<crate::store::AssetGroupRow> {
    state
        .store
        .group_create(crate::store::GroupInput {
            parent_id,
            name,
            sort: 0,
        })
        .await
}

#[tauri::command]
pub async fn group_update(
    state: ManagedState<'_>,
    id: String,
    name: Option<String>,
    parent_id: Option<Option<String>>,
) -> AppResult<crate::store::AssetGroupRow> {
    state.store.group_update(&id, name, parent_id, None).await
}

#[tauri::command]
pub async fn group_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.group_delete(&id).await
}

// ── 片段 ──

#[tauri::command]
pub async fn snippet_list(state: ManagedState<'_>) -> AppResult<Vec<crate::store::SnippetRow>> {
    state.store.snippet_list().await
}

#[tauri::command]
pub async fn snippet_create(
    state: ManagedState<'_>,
    name: String,
    body: String,
) -> AppResult<crate::store::SnippetRow> {
    state.store.snippet_create(&name, &body, None, 0).await
}

#[tauri::command]
pub async fn snippet_update(
    state: ManagedState<'_>,
    id: String,
    name: String,
    body: String,
) -> AppResult<()> {
    state.store.snippet_update(&id, &name, &body).await
}

#[tauri::command]
pub async fn snippet_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.snippet_delete(&id).await
}

// ── 审计 ──

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditArgs {
    pub session_id: Option<String>,
    pub source: Option<String>,
    pub kind: Option<String>,
    pub limit: Option<i64>,
    pub offset: Option<i64>,
}

#[tauri::command]
pub async fn audit_query(state: ManagedState<'_>, args: AuditArgs) -> AppResult<Vec<AuditRow>> {
    state
        .store
        .audit_query(AuditQuery {
            session_id: args.session_id,
            asset_id: None,
            source: args.source,
            kind: args.kind,
            limit: args.limit.unwrap_or(200),
            offset: args.offset.unwrap_or(0),
        })
        .await
}

// ── 已知主机 ──

#[tauri::command]
pub async fn known_host_list(
    state: ManagedState<'_>,
) -> AppResult<Vec<crate::store::KnownHostRow>> {
    state.store.known_host_list().await
}

#[tauri::command]
pub async fn known_host_accept(
    state: ManagedState<'_>,
    host: String,
    port: i32,
    key_type: String,
    fingerprint: String,
) -> AppResult<()> {
    state
        .store
        .known_host_accept(&host, port, &key_type, &fingerprint)
        .await
}

#[tauri::command]
pub async fn known_host_remove(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.known_host_remove(&id).await
}

/// 保存凭据（供资产表单组合调用：先存凭据再绑 asset.cred_id）。
#[tauri::command]
pub async fn credential_save(
    state: ManagedState<'_>,
    name: String,
    kind: String,
    secret: String,
) -> AppResult<serde_json::Value> {
    let dek = state.vault.dek().await?;
    let (nonce, blob) = crate::vault::Vault::encrypt_credential(&dek, &secret).await?;
    let kek_hint = if state.vault.status().await.mode == "dpapi" {
        "dpapi".into()
    } else {
        "master:0".into()
    };
    let id = state
        .store
        .credential_put(CredentialInput {
            id: None,
            name,
            kind,
            nonce,
            blob,
            kek_hint,
        })
        .await?;
    Ok(serde_json::json!({ "id": id }))
}

/// 内核信息。
#[tauri::command]
pub async fn app_info(state: ManagedState<'_>) -> AppResult<serde_json::Value> {
    let vault = state.vault.status().await;
    Ok(serde_json::json!({
        "name": "NexTerm",
        "version": env!("CARGO_PKG_VERSION"),
        "vault": vault,
    }))
}
