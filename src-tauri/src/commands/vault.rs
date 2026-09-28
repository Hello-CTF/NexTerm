//! 凭据库命令（§6.2 vault）。

use serde::Deserialize;
use serde_json::json;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::vault::VaultStatus;

#[tauri::command]
pub async fn vault_status(state: ManagedState<'_>) -> AppResult<VaultStatus> {
    Ok(state.vault.status().await)
}

#[tauri::command]
pub async fn vault_init_master(state: ManagedState<'_>, password: String) -> AppResult<()> {
    state.vault.init_master(&password).await
}

#[tauri::command]
pub async fn vault_init_dpapi(state: ManagedState<'_>) -> AppResult<()> {
    state.vault.init_dpapi().await
}

#[tauri::command]
pub async fn vault_unlock(state: ManagedState<'_>, password: String) -> AppResult<()> {
    state.vault.unlock_master(&password).await
}

#[tauri::command]
pub async fn vault_lock(state: ManagedState<'_>) -> AppResult<()> {
    state.vault.lock().await;
    Ok(())
}

#[tauri::command]
pub async fn vault_change_password(
    state: ManagedState<'_>,
    old_password: String,
    new_password: String,
) -> AppResult<()> {
    state
        .vault
        .change_master_password(&old_password, &new_password)
        .await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetCredentialArgs {
    pub id: Option<String>,
    pub name: String,
    /// password | private_key | passphrase | api_key
    pub kind: String,
    pub secret: String,
}

/// 存凭据：vault 解锁态下加密写入。
#[tauri::command]
pub async fn vault_set_credential(
    state: ManagedState<'_>,
    args: SetCredentialArgs,
) -> AppResult<serde_json::Value> {
    // 全新安装（未初始化）也能直接存凭据：自动落 DPAPI 免密模式。
    if !state.vault.status().await.initialized {
        state.vault.init_dpapi().await?;
    }
    let dek = state.vault.dek().await?;
    let (nonce, blob) = crate::vault::Vault::encrypt_credential(&dek, &args.secret).await?;
    let kek_hint = match state.vault.status().await.mode.as_str() {
        "dpapi" => "dpapi".to_string(),
        _ => "master:0".to_string(),
    };
    let id = state
        .store
        .credential_put(crate::store::CredentialInput {
            id: args.id,
            name: args.name,
            kind: args.kind,
            nonce,
            blob,
            kek_hint,
        })
        .await?;
    Ok(json!({ "id": id }))
}

/// 用当前 vault 的 DEK 重加密明文 → (nonce, blob, kek_hint)。
/// 未初始化（全新安装）时先自动落 DPAPI 免密模式，与设置页「凭据保护」开关默认「关」一致。
async fn reencrypt(
    state: &ManagedState<'_>,
    secret: &str,
) -> AppResult<(Vec<u8>, Vec<u8>, String)> {
    if !state.vault.status().await.initialized {
        state.vault.init_dpapi().await?;
    }
    let dek = state.vault.dek().await?;
    let (nonce, blob) = crate::vault::Vault::encrypt_credential(&dek, secret).await?;
    let kek_hint = match state.vault.status().await.mode.as_str() {
        "dpapi" => "dpapi".to_string(),
        _ => "master:0".to_string(),
    };
    Ok((nonce, blob, kek_hint))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateCredentialArgs {
    pub id: String,
    /// 新名称（空串/缺省 = 不改名）
    pub name: Option<String>,
    /// 新值（空串/缺省 = 不改值；提供则重加密）
    pub secret: Option<String>,
}

/// 改名 / 改值。改值走重加密；未初始化时自动落 DPAPI（免密），与开关默认「关」一致。
#[tauri::command]
pub async fn credential_update(
    state: ManagedState<'_>,
    args: UpdateCredentialArgs,
) -> AppResult<()> {
    let row = state.store.credential_get_row(&args.id).await?;
    let name = args
        .name
        .map(|n| n.trim().to_string())
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| row.name.clone());
    let (nonce, blob, kek_hint) = match args.secret.filter(|s| !s.is_empty()) {
        Some(secret) => reencrypt(&state, &secret).await?,
        None => (row.nonce.clone(), row.blob.clone(), row.kek_hint.clone()),
    };
    state
        .store
        .credential_put(crate::store::CredentialInput {
            id: Some(row.id.clone()),
            name,
            kind: row.kind.clone(),
            nonce,
            blob,
            kek_hint,
        })
        .await?;
    Ok(())
}

/// used_by 元素：引用该凭据的资产摘要。
/// store::repo 是私有模块，repo::AssetRef 无法在本层按路径命名（store/mod.rs 未再导出），
/// 故定义同形 DTO，经字段映射承接（serde 输出一致：id/name/kind）。
#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetRefDto {
    pub id: String,
    pub name: String,
    pub kind: String,
}

/// 凭据页列表项：Meta + 引用关系（返回 DTO 不返回 Row）。
#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CredentialDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub created_at: i64,
    pub updated_at: i64,
    pub used_by: Vec<AssetRefDto>,
}

#[tauri::command]
pub async fn vault_list_credentials(state: ManagedState<'_>) -> AppResult<Vec<CredentialDto>> {
    let rows = state.store.credential_list().await?;
    let mut out = Vec::with_capacity(rows.len());
    for r in &rows {
        let used_by = state
            .store
            .credential_usage(&r.id)
            .await?
            .into_iter()
            .map(|a| AssetRefDto {
                id: a.id,
                name: a.name,
                kind: a.kind,
            })
            .collect();
        out.push(CredentialDto {
            id: r.id.clone(),
            name: r.name.clone(),
            kind: r.kind.clone(),
            created_at: r.created_at,
            updated_at: r.updated_at,
            used_by,
        });
    }
    Ok(out)
}

#[tauri::command]
pub async fn vault_delete_credential(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.credential_delete(&id).await
}

/// 读取凭据明文（内部用途：连接时回填；UI 不直接展示）。
#[tauri::command]
pub async fn vault_reveal_credential(state: ManagedState<'_>, id: String) -> AppResult<String> {
    let row = state.store.credential_get_row(&id).await?;
    let dek = state.vault.dek().await?;
    Ok(crate::vault::Vault::decrypt_credential(&dek, &row)?.to_string())
}

#[allow(dead_code)]
fn _unused() -> Result<(), AppError> {
    Ok(())
}
