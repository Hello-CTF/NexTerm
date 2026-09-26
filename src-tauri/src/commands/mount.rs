//! 挂载命令（§6.2 mount）：列表以本机真实状态为准（§5.5）。

use serde::Deserialize;

use crate::error::{AppError, AppResult};
use crate::fs::mount::{self, MountEntry};
use crate::state::ManagedState;

#[tauri::command]
pub async fn mount_list(force_refresh: Option<bool>) -> AppResult<Vec<MountEntry>> {
    let _ = force_refresh;
    mount::scan().await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MountCreateArgs {
    pub session_id: String,
    /// 远端：Windows 为 \\\\host\\share，Linux 为 user@host:/path
    pub remote_path: String,
    /// Z: 或 /mnt/point
    pub local_point: String,
    pub username: Option<String>,
    pub password: Option<String>,
}

#[tauri::command]
pub async fn mount_create(state: ManagedState<'_>, args: MountCreateArgs) -> AppResult<MountEntry> {
    let _ = &state; // 凭据默认复用资产（v1 直传；后续接 vault）
    if args.local_point.is_empty() || args.remote_path.is_empty() {
        return Err(AppError::param("挂载点与远端路径不能为空"));
    }
    mount::mount(
        &args.local_point,
        &args.remote_path,
        args.username.as_deref(),
        args.password.as_deref(),
    )
    .await?;
    let _ = state
        .store
        .audit_insert(crate::store::AuditInput {
            session_id: Some(args.session_id.clone()),
            asset_id: None,
            source: "user",
            kind: "mount",
            payload: serde_json::json!({ "remote": args.remote_path, "point": args.local_point }),
            exit_code: Some(0),
            duration_ms: None,
        })
        .await;
    Ok(MountEntry {
        id: crate::ids::new_id(),
        local_point: args.local_point,
        remote: args.remote_path,
        session_id: Some(args.session_id),
        created_at: Some(crate::ids::now_ms()),
    })
}

#[tauri::command]
pub async fn mount_remove(state: ManagedState<'_>, local_point: String) -> AppResult<()> {
    mount::unmount(&local_point).await?;
    let _ = state;
    Ok(())
}
