//! 文件系统命令（§6.2 fs）+ 传输。

use serde::Deserialize;

use crate::error::AppResult;
use crate::state::ManagedState;
use crate::transport::FileEntry;

fn sid(session_id: &str) -> AppResult<()> {
    if session_id.is_empty() {
        Err(crate::error::AppError::param("缺少 sessionId"))
    } else {
        Ok(())
    }
}

#[tauri::command]
pub async fn fs_list(
    state: ManagedState<'_>,
    session_id: String,
    path: String,
) -> AppResult<Vec<FileEntry>> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.list(&path).await
}

#[tauri::command]
pub async fn fs_read(
    state: ManagedState<'_>,
    session_id: String,
    path: String,
    max_bytes: Option<u64>,
) -> AppResult<serde_json::Value> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    let data = fs
        .read_file(&path, max_bytes.unwrap_or(5 * 1024 * 1024))
        .await?;
    // 返回 base64 + 元信息（前端按编码解码展示）
    use base64::Engine;
    Ok(serde_json::json!({
        "path": path,
        "size": data.len(),
        "contentBase64": base64::engine::general_purpose::STANDARD.encode(&data),
    }))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FsWriteArgs {
    pub session_id: String,
    pub path: String,
    pub content_base64: String,
    pub sudo: Option<bool>,
    pub backup: Option<bool>,
}

#[tauri::command]
pub async fn fs_write(state: ManagedState<'_>, args: FsWriteArgs) -> AppResult<()> {
    sid(&args.session_id)?;
    use base64::Engine;
    let data = base64::engine::general_purpose::STANDARD
        .decode(&args.content_base64)
        .map_err(|e| crate::error::AppError::param(format!("base64 解码失败: {e}")))?;
    let s = state.sessions.get(&args.session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.write_file(&args.path, &data, args.backup.unwrap_or(true))
        .await?;
    let _ = args.sudo; // sudo 写走终端提权命令，v1 对 SFTP 目标提示
    let _ = state
        .store
        .audit_insert(crate::store::AuditInput {
            session_id: Some(args.session_id.clone()),
            asset_id: None,
            source: "user",
            kind: "write_file",
            payload: serde_json::json!({ "path": args.path, "bytes": data.len() }),
            exit_code: Some(0),
            duration_ms: None,
        })
        .await;
    Ok(())
}

#[tauri::command]
pub async fn fs_mkdir(state: ManagedState<'_>, session_id: String, path: String) -> AppResult<()> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.mkdir(&path).await
}

#[tauri::command]
pub async fn fs_rename(
    state: ManagedState<'_>,
    session_id: String,
    from: String,
    to: String,
) -> AppResult<()> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.rename(&from, &to).await
}

#[tauri::command]
pub async fn fs_delete(
    state: ManagedState<'_>,
    session_id: String,
    path: String,
    is_dir: bool,
) -> AppResult<()> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.delete(&path, is_dir).await
}

#[tauri::command]
pub async fn fs_chmod(
    state: ManagedState<'_>,
    session_id: String,
    path: String,
    mode: u32,
) -> AppResult<()> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.chmod(&path, mode).await
}

#[tauri::command]
pub async fn fs_checksum(
    state: ManagedState<'_>,
    session_id: String,
    path: String,
    algo: Option<String>,
) -> AppResult<String> {
    sid(&session_id)?;
    let s = state.sessions.get(&session_id).await?;
    let fs = s.transport().await.fs().await?;
    fs.checksum(&path, algo.as_deref().unwrap_or("sha256"))
        .await
}

#[tauri::command]
pub async fn fs_upload(
    state: ManagedState<'_>,
    session_id: String,
    local_path: String,
    remote_path: String,
    resume: Option<bool>,
) -> AppResult<u64> {
    crate::fs::upload(
        &state,
        &session_id,
        local_path,
        remote_path,
        resume.unwrap_or(false),
    )
    .await
}

#[tauri::command]
pub async fn fs_download(
    state: ManagedState<'_>,
    session_id: String,
    remote_path: String,
    local_path: String,
) -> AppResult<u64> {
    crate::fs::download(&state, &session_id, remote_path, local_path).await
}
