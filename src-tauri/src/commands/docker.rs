//! Docker 命令（§6.2 docker）。

use serde::Deserialize;
use tauri::ipc::Channel;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::terminal::pty::{self, ByteSource};

async fn transport_of(
    state: &ManagedState<'_>,
    session_id: &str,
) -> AppResult<std::sync::Arc<dyn crate::transport::Transport>> {
    let s = state.sessions.get(session_id).await?;
    Ok(s.transport().await)
}

#[tauri::command]
pub async fn docker_overview(
    state: ManagedState<'_>,
    session_id: String,
) -> AppResult<serde_json::Value> {
    let t = transport_of(&state, &session_id).await?;
    let containers = crate::docker::cli::ps(&*t).await?;
    let host = crate::docker::cli::overview(&*t).await?;
    Ok(serde_json::json!({ "containers": containers, "hostStats": host }))
}

#[tauri::command]
pub async fn docker_ps(
    state: ManagedState<'_>,
    session_id: String,
) -> AppResult<Vec<crate::docker::ContainerSummary>> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::ps(&*t).await
}

/// 容器日志 follow：开一个 exec channel 喂进标签（虚拟滚动在前端）。
#[tauri::command]
pub async fn docker_logs_attach(
    state: ManagedState<'_>,
    session_id: String,
    container_id: String,
    tail: Option<u64>,
    channel: Channel<Vec<u8>>,
) -> AppResult<String> {
    let s = state.sessions.get(&session_id).await?;
    let t = s.transport().await;
    let tail = tail.unwrap_or(500);
    // 走 SSH exec channel（WinRM 会话不支持 follow，回退一次性）
    let ssh = t
        .as_any()
        .downcast_ref::<crate::transport::ssh::SshTransport>()
        .ok_or_else(|| AppError::Unsupported("日志 follow 需要 SSH 会话".into()))?;
    let _ = ssh;
    // SshTransport 包了一层 session shim；用 open_exec_channel：
    let (read, write) = open_exec_for_session(
        &state,
        &session_id,
        &format!("docker logs -f --tail {tail} {container_id}"),
    )
    .await?;
    let tab_id = crate::ids::new_id();
    let tab = crate::terminal::TerminalTab::new_arc(
        tab_id.clone(),
        session_id.clone(),
        120,
        40,
        s.encoding,
    );
    tab.set_writer(crate::terminal::TerminalWriter::Ssh(std::sync::Arc::clone(
        &write,
    )))
    .await;
    tab.attach_frontend(channel, 500 * 1024).await;
    let callbacks: std::sync::Arc<dyn crate::terminal::TabCallbacks> =
        std::sync::Arc::new(crate::session::AppCallbacks {
            app: state.app.clone(),
            sessions: std::sync::Arc::clone(&state.sessions),
        });
    {
        let mut tabs = s.tabs.lock().unwrap_or_else(|e| e.into_inner());
        tabs.push(tab_id.clone());
    }
    state
        .sessions
        .tabs
        .write()
        .await
        .insert(tab_id.clone(), std::sync::Arc::clone(&tab));
    state
        .sessions
        .tab_sessions
        .write()
        .await
        .insert(tab_id.clone(), session_id.clone());
    tokio::spawn(async move {
        pty::run_pump(tab, ByteSource::Ssh(read), callbacks).await;
    });
    Ok(tab_id)
}

/// 在会话上开一个 exec channel（docker logs -f / docker exec -it）。
async fn open_exec_for_session(
    state: &ManagedState<'_>,
    session_id: &str,
    cmd: &str,
) -> AppResult<(
    russh::ChannelReadHalf,
    std::sync::Arc<russh::ChannelWriteHalf<russh::client::Msg>>,
)> {
    let s = state.sessions.get(session_id).await?;
    let t = s.transport().await;
    let ssh = t
        .as_any()
        .downcast_ref::<crate::transport::ssh::SshTransport>()
        .ok_or_else(|| AppError::Unsupported("该通道需要 SSH 会话".into()))?;
    ssh.open_exec_channel(cmd).await
}

/// 容器 exec 终端（真 PTY：docker exec -it）。
#[tauri::command]
pub async fn docker_exec_attach(
    state: ManagedState<'_>,
    session_id: String,
    container_id: String,
    cmd: Option<String>,
    cols: u16,
    rows: u16,
    channel: Channel<Vec<u8>>,
) -> AppResult<String> {
    let s = state.sessions.get(&session_id).await?;
    let shell_cmd = cmd.unwrap_or_else(|| format!("docker exec -it {container_id} sh"));
    let (read, write) = open_exec_for_session(&state, &session_id, &shell_cmd).await?;
    // docker exec 不走 request_pty（在外层命令带 -t 即可）；尺寸固定值
    let _ = (cols, rows);
    let tab_id = crate::ids::new_id();
    let tab = crate::terminal::TerminalTab::new_arc(
        tab_id.clone(),
        session_id.clone(),
        cols.max(20),
        rows.max(5),
        s.encoding,
    );
    tab.set_writer(crate::terminal::TerminalWriter::Ssh(write))
        .await;
    tab.attach_frontend(channel, 0).await;
    let callbacks: std::sync::Arc<dyn crate::terminal::TabCallbacks> =
        std::sync::Arc::new(crate::session::AppCallbacks {
            app: state.app.clone(),
            sessions: std::sync::Arc::clone(&state.sessions),
        });
    {
        let mut tabs = s.tabs.lock().unwrap_or_else(|e| e.into_inner());
        tabs.push(tab_id.clone());
    }
    state
        .sessions
        .tabs
        .write()
        .await
        .insert(tab_id.clone(), std::sync::Arc::clone(&tab));
    state
        .sessions
        .tab_sessions
        .write()
        .await
        .insert(tab_id.clone(), session_id.clone());
    tokio::spawn(async move {
        pty::run_pump(tab, ByteSource::Ssh(read), callbacks).await;
    });
    Ok(tab_id)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DockerActionArgs {
    pub session_id: String,
    pub container_id: String,
    /// start|stop|restart|pause|unpause|remove|rename
    pub action: String,
    pub new_name: Option<String>,
}

#[tauri::command]
pub async fn docker_action(state: ManagedState<'_>, args: DockerActionArgs) -> AppResult<()> {
    let t = transport_of(&state, &args.session_id).await?;
    let r = match args.action.as_str() {
        "remove" => crate::docker::cli::remove_container(&*t, &args.container_id, false).await,
        "rename" => {
            let name = args.new_name.clone().unwrap_or_default();
            crate::docker::cli::docker_exec(
                &*t,
                &format!("rename {} {}", args.container_id, name),
                std::time::Duration::from_secs(30),
            )
            .await
            .map(|_| ())
        }
        other => crate::docker::cli::action(&*t, &args.container_id, other).await,
    };
    if r.is_ok() {
        let _ = state.store.audit_insert(crate::store::AuditInput {
            session_id: Some(args.session_id.clone()),
            asset_id: None,
            source: "user",
            kind: "docker_action",
            payload: serde_json::json!({ "container": args.container_id, "action": args.action }),
            exit_code: Some(0),
            duration_ms: None,
        })
        .await;
    }
    r
}

#[tauri::command]
pub async fn docker_images(
    state: ManagedState<'_>,
    session_id: String,
) -> AppResult<Vec<crate::docker::ImageSummary>> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::images(&*t).await
}

#[tauri::command]
pub async fn docker_image_pull(
    state: ManagedState<'_>,
    session_id: String,
    image: String,
) -> AppResult<String> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::pull(&*t, &image).await
}

#[tauri::command]
pub async fn docker_image_remove(
    state: ManagedState<'_>,
    session_id: String,
    image: String,
    force: Option<bool>,
) -> AppResult<()> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::remove_image(&*t, &image, force.unwrap_or(false)).await
}

#[tauri::command]
pub async fn docker_inspect(
    state: ManagedState<'_>,
    session_id: String,
    container_id: String,
) -> AppResult<serde_json::Value> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::inspect(&*t, &container_id).await
}

#[tauri::command]
pub async fn docker_stats(state: ManagedState<'_>, session_id: String) -> AppResult<String> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::stats(&*t).await
}

#[tauri::command]
pub async fn docker_container_list_dir(
    state: ManagedState<'_>,
    session_id: String,
    container_id: String,
    path: String,
) -> AppResult<Vec<String>> {
    let t = transport_of(&state, &session_id).await?;
    crate::docker::cli::container_list_dir(&*t, &container_id, &path).await
}
