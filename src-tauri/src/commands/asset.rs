//! 资产 / 分组 / 片段 / 审计 / 已知主机 命令。

use serde::Deserialize;

use crate::error::AppResult;
use crate::ipc_shim as tauri;
use crate::ipc_types::AssetDto;
use crate::state::ManagedState;
use crate::store::{AssetRow, AuditQuery, CredentialInput};

/// `AssetRow` → `AssetDto`。**必须转换**：行里是 `options_json`（字符串），
/// 前端读的是 `options`（对象）—— 直接回行的话 `options` 永远 undefined。
/// 唯一权威定义在 `ipc_types::AssetDto`（同一坑见 `ai_message` / `audit_log`）。
fn dto(row: AssetRow) -> AssetDto {
    row.into()
}

#[tauri::command]
pub async fn asset_list(state: ManagedState<'_>) -> AppResult<Vec<AssetDto>> {
    Ok(state
        .store
        .asset_list(false)
        .await?
        .into_iter()
        .map(dto)
        .collect())
}

#[tauri::command]
pub async fn asset_get(state: ManagedState<'_>, id: String) -> AppResult<AssetDto> {
    Ok(dto(state.store.asset_get(&id).await?))
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
pub async fn asset_create(state: ManagedState<'_>, args: AssetCreateArgs) -> AppResult<AssetDto> {
    Ok(dto(state
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
        .await?))
}

/// 双层 Option 的显式 null 语义：标准 serde 把 JSON `null` 反序列化成外层
/// `None`（=「不变」），`Some(None)`（=「清空这一列」）在 JSON 里根本表达
/// 不出来 —— 于是「把资产拖回未分组」「切换认证方式后清掉旧 keyPath/credId」
/// 这类更新全部静默失效。`deserialize_with` 让 `null` 落到内层：
/// 字段缺失 = 不变；`null` = 清空；有值 = 设置。
fn double_option<'de, T, D>(de: D) -> Result<Option<Option<T>>, D::Error>
where
    T: serde::Deserialize<'de>,
    D: serde::Deserializer<'de>,
{
    serde::Deserialize::deserialize(de).map(Some)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetUpdateArgs {
    pub id: String,
    #[serde(default, deserialize_with = "double_option")]
    pub group_id: Option<Option<String>>,
    pub name: Option<String>,
    #[serde(default, deserialize_with = "double_option")]
    pub host: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub port: Option<Option<i32>>,
    #[serde(default, deserialize_with = "double_option")]
    pub username: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub auth_kind: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub key_path: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub cred_id: Option<Option<String>>,
    pub options: Option<serde_json::Value>,
    pub tags: Option<String>,
    pub note: Option<String>,
}

#[tauri::command]
pub async fn asset_update(state: ManagedState<'_>, args: AssetUpdateArgs) -> AppResult<AssetDto> {
    Ok(dto(state
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
        .await?))
}

#[tauri::command]
pub async fn asset_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.asset_delete(&id).await
}

#[tauri::command]
pub async fn asset_search(state: ManagedState<'_>, q: String) -> AppResult<Vec<AssetDto>> {
    Ok(state
        .store
        .asset_search(&q)
        .await?
        .into_iter()
        .map(dto)
        .collect())
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
pub async fn audit_query(
    state: ManagedState<'_>,
    args: AuditArgs,
) -> AppResult<Vec<crate::ipc_types::AuditEntryDto>> {
    // 必须经 DTO 转换：直接回 AuditRow 的话前端拿到的是 `payloadJson` 字符串，
    // 而界面按 `payload` 读 —— 详情列永远空（与 ai_messages 那次同款 bug）。
    Ok(state
        .store
        .audit_query(AuditQuery {
            session_id: args.session_id,
            asset_id: None,
            source: args.source,
            kind: args.kind,
            limit: args.limit.unwrap_or(200),
            offset: args.offset.unwrap_or(0),
        })
        .await?
        .into_iter()
        .map(Into::into)
        .collect())
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

/// 粘贴的私钥落成本地文件：SSH 认证按路径走（`SshAuth::Key { path, .. }`），
/// 内容统一存到应用数据目录 keys/ 下。文件名用 id 而不是用户输入，避免路径注入；
/// 返回最终路径，前端拿去绑 asset.key_path。
#[tauri::command]
pub async fn asset_save_key_file(
    state: ManagedState<'_>,
    content: String,
) -> AppResult<serde_json::Value> {
    use tauri::Manager;
    let dir = state
        .app
        .path()
        .app_data_dir()
        .map_err(|e| crate::error::AppError::internal(format!("定位应用数据目录失败: {e}")))?
        .join("keys");
    std::fs::create_dir_all(&dir)?;
    let trimmed = content.trim();
    if trimmed.is_empty() {
        return Err(crate::error::AppError::param("私钥内容为空"));
    }
    let file = dir.join(format!("{}.pem", crate::ids::new_id()));
    std::fs::write(&file, format!("{trimmed}\n"))?;
    Ok(serde_json::json!({ "path": file.to_string_lossy() }))
}

/// 读取用户通过文件对话框选中的私钥内容（存入凭据库用）。
/// 路径来自原生对话框的用户选择；上限 64KB——正常私钥远小于此，防误读大文件。
#[tauri::command]
pub async fn asset_read_key_file(path: String) -> AppResult<String> {
    const MAX_LEN: u64 = 64 * 1024;
    let meta = std::fs::metadata(&path)?;
    if meta.len() > MAX_LEN {
        return Err(crate::error::AppError::param("文件超过 64KB，不是私钥"));
    }
    Ok(std::fs::read_to_string(&path)?)
}

/// 保存凭据（供资产表单组合调用：先存凭据再绑 asset.cred_id）。
#[tauri::command]
pub async fn credential_save(
    state: ManagedState<'_>,
    name: String,
    kind: String,
    secret: String,
) -> AppResult<serde_json::Value> {
    // 全新安装（未初始化）也能直接存凭据：自动落 DPAPI 免密模式。
    if !state.vault.status().await.initialized {
        state.vault.init_dpapi().await?;
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    /// 回归：`asset_update` 的双层 Option 曾用标准 serde —— JSON `null` 被当成
    /// 「字段没变」，`Some(None)`（清空）在 JSON 里表达不出来。后果：
    /// 资产拖回未分组、切换认证方式后清 keyPath/credId 全部静默失效。
    #[test]
    fn asset_update_null_clears_and_missing_keeps() {
        // null → 清空（Some(None)）
        let args: AssetUpdateArgs = serde_json::from_str(r#"{"id":"a1","groupId":null}"#).unwrap();
        assert_eq!(args.group_id, Some(None));
        // 有值 → 设置
        let args: AssetUpdateArgs = serde_json::from_str(r#"{"id":"a1","groupId":"g1"}"#).unwrap();
        assert_eq!(args.group_id, Some(Some("g1".into())));
        // 字段缺失 → 不变（None）
        let args: AssetUpdateArgs = serde_json::from_str(r#"{"id":"a1"}"#).unwrap();
        assert_eq!(args.group_id, None);
        // 其他可清空列同语义
        let args: AssetUpdateArgs =
            serde_json::from_str(r#"{"id":"a1","keyPath":null,"credId":null}"#).unwrap();
        assert_eq!(args.key_path, Some(None));
        assert_eq!(args.cred_id, Some(None));
    }
}
