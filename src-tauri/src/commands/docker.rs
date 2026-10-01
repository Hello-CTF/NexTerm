//! Docker 命令（§6.2 docker）。

use crate::ipc_shim as tauri;
use serde::Deserialize;
use tauri::ipc::Channel;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::terminal::pty::{self, ByteSource};
use crate::terminal::{TerminalTab, TerminalWriter};
use crate::transport::PtyHandle;

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

/// 长驻命令的执行通道（`docker logs -f` / `docker exec -it`）。
///
/// 两种形态：SSH 会话开一个 exec channel（对端天然长驻），本机会话开一个
/// **本地命令 PTY**。以前这里只有前一种，本机会话一律撞「该通道需要 SSH 会话」
/// —— 可本机跑着 Docker 恰好是最常见的用法（Docker Desktop / colima / WSL）。
///
/// 抽成枚举而不是让调用方各自 downcast：两条通道后面「注册标签 + 起泵」的
/// 逻辑一模一样，复制两遍必然有一份会落后（历史上就漏过 `tab_sessions`，
/// 那个漏了会导致关标签时找不到所属会话）。
enum ExecChannel {
    Ssh(
        russh::ChannelReadHalf,
        std::sync::Arc<russh::ChannelWriteHalf<russh::client::Msg>>,
    ),
    Local(PtyHandle),
}

/// 在会话上开一条长驻执行通道。
async fn open_exec_for_session(
    state: &ManagedState<'_>,
    session_id: &str,
    cmd: &str,
    cols: u16,
    rows: u16,
) -> AppResult<ExecChannel> {
    let s = state.sessions.get(session_id).await?;
    let t = s.transport().await;
    if let Some(ssh) = t
        .as_any()
        .downcast_ref::<crate::transport::ssh::SshTransport>()
    {
        let (read, write) = ssh.open_exec_channel(cmd).await?;
        return Ok(ExecChannel::Ssh(read, write));
    }
    if let Some(local) = t
        .as_any()
        .downcast_ref::<crate::transport::local::LocalTransport>()
    {
        return Ok(ExecChannel::Local(local.open_command_pty(cmd, cols, rows)?));
    }
    Err(AppError::Unsupported(format!(
        "{} 会话不支持长驻命令通道（容器日志跟随 / 进容器需要 SSH 或本机会话）",
        t.kind()
    )))
}

/// 把执行通道接到一个新终端标签上：接写入端、注册进会话、起泵。
///
/// 返回新标签 id。注册那几步（`session.tabs` / `tabs` / `tab_sessions` /
/// `tab_killers`）一个都不能省 —— 漏一个的症状分别是：会话列不出标签、
/// 前端拿不到标签、关标签时找不到所属会话、关标签杀不掉进程。
async fn attach_exec_tab(
    state: &ManagedState<'_>,
    session_id: &str,
    tab: std::sync::Arc<TerminalTab>,
    channel: ExecChannel,
) -> AppResult<String> {
    let tab_id = tab.tab_id.clone();
    let source = match channel {
        ExecChannel::Ssh(read, write) => {
            tab.set_writer(TerminalWriter::Ssh(write)).await;
            ByteSource::Ssh(read)
        }
        ExecChannel::Local(handle) => {
            let PtyHandle::Local {
                io,
                reader,
                child,
                killer,
            } = handle
            else {
                return Err(AppError::internal("本地命令通道返回了非本地 PTY 句柄"));
            };
            tab.set_writer(TerminalWriter::Local(io)).await;
            let callbacks: std::sync::Arc<dyn crate::terminal::TabCallbacks> =
                std::sync::Arc::new(crate::session::AppCallbacks {
                    app: state.app.clone(),
                    sessions: std::sync::Arc::clone(&state.sessions),
                });
            pty::LocalExitWatcher { child }.spawn(
                tab_id.clone(),
                callbacks,
                std::sync::Arc::clone(&tab.stop),
            );
            state
                .sessions
                .tab_killers
                .write()
                .await
                .insert(tab_id.clone(), killer);
            ByteSource::Local(reader)
        }
    };

    if let Ok(session) = state.sessions.get(session_id).await {
        session
            .tabs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(tab_id.clone());
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
        .insert(tab_id.clone(), session_id.to_string());

    let callbacks: std::sync::Arc<dyn crate::terminal::TabCallbacks> =
        std::sync::Arc::new(crate::session::AppCallbacks {
            app: state.app.clone(),
            sessions: std::sync::Arc::clone(&state.sessions),
        });
    tokio::spawn(async move {
        pty::run_pump(tab, source, callbacks).await;
    });
    Ok(tab_id)
}

/// 容器日志 follow：开一个长驻通道喂进标签（虚拟滚动在前端）。
#[tauri::command]
pub async fn docker_logs_attach(
    state: ManagedState<'_>,
    session_id: String,
    container_id: String,
    tail: Option<u64>,
    channel: Channel<Vec<u8>>,
    client_id: Option<String>,
) -> AppResult<String> {
    let s = state.sessions.get(&session_id).await?;
    let client = super::client_or_default(client_id);
    let tail = tail.unwrap_or(500);
    let (cols, rows) = (120u16, 40u16);
    let ch = open_exec_for_session(
        &state,
        &session_id,
        &format!("docker logs -f --tail {tail} {container_id}"),
        cols,
        rows,
    )
    .await?;
    let tab_id = crate::ids::new_id();
    let tab = TerminalTab::new_arc(tab_id.clone(), session_id.clone(), cols, rows, s.encoding);
    // 日志跟随是**短命资源**：没人看（订阅者归零）时没有保留价值。
    //
    // 用户直接关掉浏览器页面时前端清不掉这个 `docker logs -f` PTY（页面卸载路径
    // 上发不出 RPC），只能由服务端在通道 WS 断开后按这个标记回收 —— 见
    // `server::serve` 里 `set_on_channel_closed` 的宽限期复查。
    //
    // ⚠️ **不要**给 `docker_exec_attach` 加这个标记：`docker exec -it` 是真交互
    // 终端，前端按普通终端标签打开，必须能跨网页关闭存活。
    tab.mark_ephemeral();
    tab.attach_frontend(channel, 500 * 1024, &client).await;
    tab.claim_if_free(&client).await;
    let tab_id = attach_exec_tab(&state, &session_id, std::sync::Arc::clone(&tab), ch).await?;
    // 新订阅（可能顺带拿到控制权）—— 广播控制权快照。
    crate::session::notify_control_changed(&state, &tab).await;
    Ok(tab_id)
}

/// 容器 exec 终端的入参。
///
/// 收成一个结构体而不是继续摊平：`docker_exec_attach` 摊平后是 8 个参数，
/// 会踩 `clippy::too_many_arguments`（CI 是 `-D warnings`），而逐条加
/// `#[allow]` 只是把问题藏起来。`channel` **必须留在签名上**：它是
/// `#[command]` 宏特殊识别的一类形参（服务端要从 WS 通道 id 构造），
/// 塞进结构体就变成普通字段、两侧都编不过。
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DockerExecArgs {
    pub session_id: String,
    pub container_id: String,
    pub cmd: Option<String>,
    pub cols: u16,
    pub rows: u16,
    /// 谁在操作（单点模式，见 `commands/terminal` 的模块文档）。
    pub client_id: Option<String>,
}

/// 容器 exec 终端（真 PTY：docker exec -it）。
#[tauri::command]
pub async fn docker_exec_attach(
    state: ManagedState<'_>,
    args: DockerExecArgs,
    channel: Channel<Vec<u8>>,
) -> AppResult<String> {
    let s = state.sessions.get(&args.session_id).await?;
    let client = super::client_or_default(args.client_id);
    let shell_cmd = args
        .cmd
        .unwrap_or_else(|| format!("docker exec -it {} sh", args.container_id));
    // docker exec 不走 request_pty（在外层命令带 -t 即可）；尺寸给标签用
    let cols = args.cols.max(20);
    let rows = args.rows.max(5);
    let ch = open_exec_for_session(&state, &args.session_id, &shell_cmd, cols, rows).await?;
    let tab_id = crate::ids::new_id();
    let tab = TerminalTab::new_arc(
        tab_id.clone(),
        args.session_id.clone(),
        cols,
        rows,
        s.encoding,
    );
    tab.attach_frontend(channel, 0, &client).await;
    tab.claim_if_free(&client).await;
    let tab_id = attach_exec_tab(&state, &args.session_id, std::sync::Arc::clone(&tab), ch).await?;
    crate::session::notify_control_changed(&state, &tab).await;
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
