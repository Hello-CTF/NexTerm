//! 端口转发命令（M3-T8）。

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::transport::forward::{spawn_local_forward, ForwardSpec, RunningForward};

#[tauri::command]
pub async fn forward_create(
    state: ManagedState<'_>,
    session_id: String,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<ForwardSpec> {
    let s = state.sessions.get(&session_id).await?;
    let t = s.transport().await;
    let ssh = t
        .clone()
        .as_any_arc()
        .downcast::<crate::transport::ssh::SshTransport>()
        .map_err(|_| AppError::Unsupported("端口转发需要 SSH 会话".into()))?;
    let running = std::sync::Arc::new(
        spawn_local_forward(&session_id, ssh, listen_port, target_host, target_port).await?,
    );
    let spec = running.spec.clone();
    state
        .sessions
        .forwards
        .write()
        .await
        .insert(spec.id.clone(), running);
    Ok(spec)
}

#[tauri::command]
pub async fn forward_list(state: ManagedState<'_>) -> AppResult<Vec<ForwardSpec>> {
    let forwards = state.sessions.forwards.read().await;
    Ok(forwards.values().map(|f| f.spec.clone()).collect())
}

#[tauri::command]
pub async fn forward_remove(state: ManagedState<'_>, id: String) -> AppResult<()> {
    if let Some(f) = state.sessions.forwards.write().await.remove(&id) {
        f.stop();
    }
    Ok(())
}

#[allow(dead_code)]
fn _type_anchor(_: &RunningForward) {}
