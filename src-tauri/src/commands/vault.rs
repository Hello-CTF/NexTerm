//! 凭据库命令（§6.2 vault）。
//!
//! 私钥类凭据的载荷是结构化的（引用本地文件 / 正文收进库 + 可选口令），见
//! `vault::payload`。**口令不是独立凭据**：它跟私钥存在同一条记录里，
//! 所以这里的入参都带 `source` 与 `passphrase`，由本层组装成落库明文。

use serde::Deserialize;
use serde_json::json;

use crate::error::AppResult;
use crate::state::ManagedState;
use crate::vault::payload::{self, PrivateKeyPayload};
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

/// 组装落库明文。
///
/// 私钥走结构化载荷（`{"key"|"ref", "passphrase"}`）；其它类型原样存值 ——
/// 所以老类型的载荷格式一个字节都没变，不需要考虑它们的兼容。
fn build_plain(
    kind: &str,
    secret: &str,
    source: Option<&str>,
    passphrase: Option<String>,
) -> String {
    if kind != payload::KIND_PRIVATE_KEY {
        return secret.to_string();
    }
    let body = secret.trim();
    if source == Some("file") {
        PrivateKeyPayload::referenced(body, passphrase).encode()
    } else {
        PrivateKeyPayload::inline(body, passphrase).encode()
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetCredentialArgs {
    pub id: Option<String>,
    pub name: String,
    /// password | private_key | api_key
    pub kind: String,
    /// 私钥：正文或路径；其它类型：值本身
    pub secret: String,
    /// 私钥专用来源：`inline`（默认，正文收进库）| `file`（只记路径引用）
    #[serde(default)]
    pub source: Option<String>,
    /// 私钥专用口令（可选）
    #[serde(default)]
    pub passphrase: Option<String>,
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
    let plain = build_plain(
        &args.kind,
        &args.secret,
        args.source.as_deref(),
        args.passphrase,
    );
    let (nonce, blob, kek_hint) = seal(&state, &plain).await?;
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

/// 用当前 vault 的 DEK 加密明文 → (nonce, blob, kek_hint)。
/// 未初始化（全新安装）时先自动落 DPAPI 免密模式，与设置页「凭据保护」开关默认「关」一致。
async fn seal(state: &ManagedState<'_>, plain: &str) -> AppResult<(Vec<u8>, Vec<u8>, String)> {
    if !state.vault.status().await.initialized {
        state.vault.init_dpapi().await?;
    }
    let dek = state.vault.dek().await?;
    let (nonce, blob) = crate::vault::Vault::encrypt_credential(&dek, plain).await?;
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
    /// 新值（空串/缺省 = 不改值；提供则重新组装载荷并加密）
    pub secret: Option<String>,
    /// 私钥专用：新的来源（缺省 = 沿用原载荷里的来源）
    #[serde(default)]
    pub source: Option<String>,
    /// 私钥专用：新口令。**提供了就覆盖**（空串 = 清除口令）；
    /// 不提供则沿用原口令 —— 所以"只改口令"不必重新提供私钥。
    #[serde(default)]
    pub passphrase: Option<String>,
}

/// 解出私钥凭据的原载荷（用于"只改一部分"时沿用其余字段）。
/// vault 锁定或解不开时返回 `None` —— 调用方按"没有旧值"处理。
async fn load_private_payload(
    state: &ManagedState<'_>,
    row: &crate::store::models::CredentialRow,
) -> Option<PrivateKeyPayload> {
    let dek = state.vault.dek().await.ok()?;
    let raw = crate::vault::Vault::decrypt_credential(&dek, row)
        .ok()?
        .to_string();
    Some(PrivateKeyPayload::parse(&raw))
}

/// 改名 / 改值 / 改口令。
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

    let is_key = row.kind == payload::KIND_PRIVATE_KEY;

    let plain = if let Some(secret) = args.secret.as_deref().filter(|s| !s.is_empty()) {
        // 改值。**未显式提供的字段沿用原载荷** —— 改了私钥正文不该顺手把口令清掉，
        // 把引用改成正文也不该把来源悄悄换掉（这两个都很容易做成"改一次丢一半"）。
        let prev = if is_key {
            load_private_payload(&state, &row).await
        } else {
            None
        };
        let source = args.source.as_deref().or_else(|| {
            prev.as_ref()
                .and_then(|p| if p.is_ref() { Some("file") } else { None })
        });
        let pass = match args.passphrase.clone() {
            Some(p) => Some(p),
            None => prev.and_then(|p| p.passphrase),
        };
        build_plain(&row.kind, secret, source, pass)
    } else if is_key && args.passphrase.is_some() {
        // 只改口令：解出原载荷，保留私钥本体（或引用路径），只换口令
        load_private_payload(&state, &row)
            .await
            .unwrap_or_default()
            .with_passphrase(args.passphrase.clone())
            .encode()
    } else {
        // 只改名：原样保留密文
        String::new()
    };

    let (nonce, blob, kek_hint) = if plain.is_empty() {
        (row.nonce.clone(), row.blob.clone(), row.kek_hint.clone())
    } else {
        seal(&state, &plain).await?
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

/// 凭据页列表项：Meta + 引用关系 + 私钥的来源（返回 DTO 不返回 Row）。
///
/// `source` / `ref_path` / `has_passphrase` **只在解锁态有值** —— 它们藏在密文里，
/// 锁定态一律给空，免得界面显示"没有口令"这种撒谎的信息。
#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CredentialDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub created_at: i64,
    pub updated_at: i64,
    pub used_by: Vec<AssetRefDto>,
    /// "inline" | "file"（仅私钥类）
    pub source: Option<String>,
    /// 引用型私钥的本地路径
    pub ref_path: Option<String>,
    pub has_passphrase: bool,
}

#[tauri::command]
pub async fn vault_list_credentials(state: ManagedState<'_>) -> AppResult<Vec<CredentialDto>> {
    let rows = state.store.credential_list().await?;
    // 锁定态拿不到 DEK：不报错，只是不解析来源（列表本身仍可见）
    let dek = state.vault.dek().await.ok();
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

        let (source, ref_path, has_passphrase) = match &dek {
            Some(dek) if r.kind == payload::KIND_PRIVATE_KEY => {
                match crate::vault::Vault::decrypt_credential(dek, r) {
                    Ok(raw) => {
                        let p = PrivateKeyPayload::parse(&raw);
                        let src = if p.is_ref() { "file" } else { "inline" };
                        (
                            Some(src.to_string()),
                            p.file.clone(),
                            p.passphrase.is_some(),
                        )
                    }
                    // 这条解不开（换过密钥之类）：不拦整个列表，退化成"来源未知"
                    Err(_) => (None, None, false),
                }
            }
            _ => (None, None, false),
        };

        out.push(CredentialDto {
            id: r.id.clone(),
            name: r.name.clone(),
            kind: r.kind.clone(),
            created_at: r.created_at,
            updated_at: r.updated_at,
            used_by,
            source,
            ref_path,
            has_passphrase,
        });
    }
    Ok(out)
}

#[tauri::command]
pub async fn vault_delete_credential(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.credential_delete(&id).await
}

/// 凭据明文（用于界面「显示」与改值前回填；引用型私钥没有正文）。
#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RevealedCredentialDto {
    pub kind: String,
    /// 非私钥：值本身；私钥（内容型）：私钥正文；引用型：空串
    pub value: String,
    /// "inline" | "file"；非私钥为 null
    pub source: Option<String>,
    pub ref_path: Option<String>,
    pub passphrase: Option<String>,
}

/// 读取凭据明文（内部用途：连接时回填；UI 不直接展示）。
#[tauri::command]
pub async fn vault_reveal_credential(
    state: ManagedState<'_>,
    id: String,
) -> AppResult<RevealedCredentialDto> {
    let row = state.store.credential_get_row(&id).await?;
    let dek = state.vault.dek().await?;
    let raw = crate::vault::Vault::decrypt_credential(&dek, &row)?.to_string();
    if row.kind != payload::KIND_PRIVATE_KEY {
        return Ok(RevealedCredentialDto {
            kind: row.kind,
            value: raw,
            source: None,
            ref_path: None,
            passphrase: None,
        });
    }
    let p = PrivateKeyPayload::parse(&raw);
    Ok(RevealedCredentialDto {
        kind: row.kind,
        value: p.key.clone().unwrap_or_default(),
        source: Some(if p.is_ref() { "file" } else { "inline" }.to_string()),
        ref_path: p.file,
        passphrase: p.passphrase,
    })
}
