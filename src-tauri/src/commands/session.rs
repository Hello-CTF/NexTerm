//! 会话命令（§6.2 session）。

use serde::Deserialize;
use tauri::ipc::Channel;
use tauri::State;

use crate::error::AppResult;
use crate::session::{self, SessionInfo};
use crate::state::ManagedState;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectArgs {
    pub asset_id: Option<String>,
    /// 首次指纹确认后重试时置 true（凭据已在 pending 记录中确认）。
    pub accept_host_key: Option<bool>,
}

#[tauri::command]
pub async fn session_connect(state: ManagedState<'_>, args: ConnectArgs) -> AppResult<SessionInfo> {
    let asset = state
        .store
        .asset_get(args.asset_id.as_deref().unwrap_or(""))
        .await?;
    // 首连指纹确认后重试：本次连接允许未知主机（内核会把指纹写入 known_host）
    let s = session::connect_asset(&state, &asset, args.accept_host_key.unwrap_or(false)).await?;
    let tabs = s.tabs.lock().unwrap_or_else(|e| e.into_inner()).clone();
    Ok(SessionInfo {
        id: s.id.clone(),
        asset_id: s.asset_id.clone(),
        name: s.name.clone(),
        kind: s.kind.clone(),
        status: s.status_now(),
        tabs,
        created_at: s.created_at,
    })
}

#[tauri::command]
pub async fn session_connect_local(state: ManagedState<'_>) -> AppResult<SessionInfo> {
    let s = session::connect_local_quick(&state).await?;
    Ok(SessionInfo {
        id: s.id.clone(),
        asset_id: s.asset_id.clone(),
        name: s.name.clone(),
        kind: s.kind.clone(),
        status: s.status_now(),
        tabs: vec![],
        created_at: s.created_at,
    })
}

#[tauri::command]
pub async fn session_disconnect(state: ManagedState<'_>, session_id: String) -> AppResult<()> {
    session::disconnect(&state, &session_id).await
}

#[tauri::command]
pub async fn session_list(state: ManagedState<'_>) -> AppResult<Vec<SessionInfo>> {
    Ok(state.sessions.list().await)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProbeArgs {
    pub host: String,
    pub port: u16,
    pub timeout_ms: Option<u64>,
}

#[tauri::command]
pub async fn session_probe(args: ProbeArgs) -> AppResult<serde_json::Value> {
    let timeout = std::time::Duration::from_millis(args.timeout_ms.unwrap_or(3000));
    let fut = tokio::net::TcpStream::connect((args.host.as_str(), args.port));
    match tokio::time::timeout(timeout, fut).await {
        Ok(Ok(_)) => Ok(serde_json::json!({ "open": true })),
        Ok(Err(e)) => Ok(serde_json::json!({ "open": false, "error": e.to_string() })),
        Err(_) => Ok(serde_json::json!({ "open": false, "error": "timeout" })),
    }
}

/// WinRM 行模式标签打开（非交互，§5.4）。
#[tauri::command]
pub async fn session_open_line_tab(
    state: ManagedState<'_>,
    session_id: String,
    cols: u16,
    rows: u16,
    channel: Channel<Vec<u8>>,
) -> AppResult<String> {
    let s = state.sessions.get(&session_id).await?;
    if s.kind != "winrm" {
        return Err(crate::error::AppError::param("该会话不是 WinRM"));
    }
    session::open_winrm_line_tab(&state, &s, cols, rows, channel).await
}

/// WinRM 行模式写入：一行命令 → exec → 输出进同一标签。
#[tauri::command]
pub async fn session_line_exec(
    state: ManagedState<'_>,
    tab_id: String,
    line: String,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let s = state.sessions.session_of_tab(&tab_id).await?;
    tab.feed_output(format!("{line}\r\n").as_bytes());
    let _ = tab
        .send_to_frontend(format!("{line}\r\n").into_bytes())
        .await;
    let t = s.transport().await;
    let out = t.exec(&line, std::time::Duration::from_secs(120)).await;
    let mut text = String::new();
    match &out {
        Ok(o) => {
            text.push_str(&o.stdout);
            if !o.stderr.is_empty() {
                text.push_str(&o.stderr);
            }
            if !text.ends_with('\n') {
                text.push('\n');
            }
        }
        Err(e) => text.push_str(&format!("[错误] {e}\n")),
    }
    tab.feed_output(text.as_bytes());
    let _ = tab.send_to_frontend(text.into_bytes()).await;
    tab.feed_output(b"PS> ");
    let _ = tab.send_to_frontend(b"PS> ".to_vec()).await;
    Ok(())
}

/// 状态查询用（AI context 面板）。
#[tauri::command]
pub async fn session_cwd(state: ManagedState<'_>, session_id: String) -> AppResult<Option<String>> {
    let s = state.sessions.get(&session_id).await?;
    Ok(s.transport().await.cwd())
}

/// 重连一个会话（§7）：重建底层传输、替换进 Session、为每个标签重开 PTY。
///
/// `session::reconnect::try_reconnect` 在核心里一直存在，只是从没暴露成命令 ——
/// 所以之前断开之后只能关掉工作区重开。返回 true 表示重连成功。
#[tauri::command]
pub async fn session_reconnect(state: ManagedState<'_>, session_id: String) -> AppResult<bool> {
    session::reconnect::try_reconnect(&state, &session_id).await
}

/// 屏蔽未使用告警。
#[allow(dead_code)]
fn _unused(_s: State<'_, ()>) {}
