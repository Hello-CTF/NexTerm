//! 凭据库命令（§6.2 vault）。

use serde::Deserialize;
use serde_json::json;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::store::CredentialMeta;
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

#[tauri::command]
pub async fn vault_list_credentials(state: ManagedState<'_>) -> AppResult<Vec<CredentialMeta>> {
    Ok(state
        .store
        .credential_list()
        .await?
        .iter()
        .map(CredentialMeta::from)
        .collect())
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
