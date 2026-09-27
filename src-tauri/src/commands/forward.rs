//! 端口转发命令（M3-T8 / §5.2）：本地静态转发 + SOCKS5 动态转发。

use std::sync::Arc;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::transport::forward::{
    spawn_local_forward, spawn_socks_forward, ForwardSpec, RunningForward,
};
use crate::transport::ssh::SshTransport;

/// 取会话底下的 SSH 传输 —— 转发只能建在 SSH 上。
///
/// 抽出来是因为两条命令都要做这同一件事，而 `as_any_arc().downcast()` 那串
/// 泛型体操在这里写两遍很容易写歪。
async fn ssh_of(state: &ManagedState<'_>, session_id: &str) -> AppResult<Arc<SshTransport>> {
    let s = state.sessions.get(session_id).await?;
    let t = s.transport().await;
    t.clone()
        .as_any_arc()
        .downcast::<SshTransport>()
        .map_err(|_| AppError::Unsupported("端口转发需要 SSH 会话".into()))
}

/// 把刚建好的转发登记进全局表。
async fn register(state: &ManagedState<'_>, running: RunningForward) -> ForwardSpec {
    let spec = running.spec.clone();
    state
        .sessions
        .forwards
        .write()
        .await
        .insert(spec.id.clone(), Arc::new(running));
    spec
}

/// 本地转发：`本地 listen_port → SSH → target_host:target_port`。
#[tauri::command]
pub async fn forward_create(
    state: ManagedState<'_>,
    session_id: String,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<ForwardSpec> {
    let ssh = ssh_of(&state, &session_id).await?;
    let running =
        spawn_local_forward(&session_id, ssh, listen_port, target_host, target_port).await?;
    Ok(register(&state, running).await)
}

/// SOCKS5 动态转发：一个本地端口当通用代理，目标由客户端当场指定。
///
/// 和 `forward_create` 的差别只在"要不要固定目标" —— 所以参数里没有 target。
#[tauri::command]
pub async fn forward_create_socks(
    state: ManagedState<'_>,
    session_id: String,
    listen_port: u16,
) -> AppResult<ForwardSpec> {
    let ssh = ssh_of(&state, &session_id).await?;
    let running = spawn_socks_forward(&session_id, ssh, listen_port).await?;
    Ok(register(&state, running).await)
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
