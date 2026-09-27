//! 终端命令（§6.2 terminal）：attach / write / resize / 读屏 / 录制。

use serde::Deserialize;
use tauri::ipc::Channel;

use crate::error::AppResult;
use crate::state::ManagedState;
use crate::terminal::transcoder::TerminalEncoding;

#[tauri::command]
pub async fn terminal_attach(
    state: ManagedState<'_>,
    session_id: String,
    cols: u16,
    rows: u16,
    channel: Channel<Vec<u8>>,
) -> AppResult<String> {
    let s = state.sessions.get(&session_id).await?;
    match s.kind.as_str() {
        "winrm" => crate::session::open_winrm_line_tab(&state, &s, cols, rows, channel).await,
        _ => crate::session::open_terminal_tab(&state, &s, cols, rows, channel).await,
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WriteArgs {
    pub tab_id: String,
    /// 用户按键（Uint8Array 直传）。
    pub data: Vec<u8>,
}

#[tauri::command]
pub async fn terminal_write(state: ManagedState<'_>, args: WriteArgs) -> AppResult<()> {
    // 接管模式：用户按键即夺回（§8.6 steal_on_key）
    crate::ai::takeover::on_user_key(&state, &args.tab_id).await;
    let tab = state.sessions.get_tab(&args.tab_id).await?;
    tab.write(&args.data).await
}

#[tauri::command]
pub async fn terminal_resize(
    state: ManagedState<'_>,
    tab_id: String,
    cols: u16,
    rows: u16,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    tab.resize(cols, rows).await
}

#[tauri::command]
pub async fn terminal_detach(state: ManagedState<'_>, tab_id: String) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    tab.detach_frontend().await;
    Ok(())
}

#[tauri::command]
pub async fn terminal_screen_text(state: ManagedState<'_>, tab_id: String) -> AppResult<String> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    Ok(tab.screen_text())
}

#[tauri::command]
pub async fn terminal_snapshot(
    state: ManagedState<'_>,
    tab_id: String,
) -> AppResult<crate::terminal::ScreenSnapshot> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    Ok(tab.snapshot())
}

#[tauri::command]
pub async fn terminal_tail(
    state: ManagedState<'_>,
    tab_id: String,
    n: usize,
) -> AppResult<Vec<String>> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    Ok(tab.tail_lines(n))
}

#[tauri::command]
pub async fn terminal_set_visible(
    state: ManagedState<'_>,
    tab_id: String,
    visible: bool,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    tab.set_visible(visible);
    Ok(())
}

#[tauri::command]
pub async fn terminal_dump(
    state: ManagedState<'_>,
    tab_id: String,
    max_bytes: Option<usize>,
) -> AppResult<String> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let bytes = tab.dump(max_bytes.unwrap_or(512 * 1024));
    Ok(String::from_utf8_lossy(&bytes).into_owned())
}

/// 运行中切换编码（§12.2）。
#[tauri::command]
pub async fn terminal_switch_encoding(
    state: ManagedState<'_>,
    tab_id: String,
    encoding: String,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let enc = TerminalEncoding::from_str_opt(&encoding)
        .ok_or_else(|| crate::error::AppError::param(format!("未知编码 {encoding}")))?;
    tab.switch_encoding(enc);
    Ok(())
}

#[tauri::command]
pub async fn terminal_record_start(
    state: ManagedState<'_>,
    tab_id: String,
    path: String,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let session = state.sessions.session_of_tab(&tab_id).await?;
    state
        .store
        .recording_start(&session.id, &tab_id, &path)
        .await?;
    tab.start_recording(std::path::PathBuf::from(path)).await
}

#[tauri::command]
pub async fn terminal_record_stop(state: ManagedState<'_>, tab_id: String) -> AppResult<u64> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    Ok(tab.stop_recording().await)
}

/// 关闭标签（不等于断连，§7）。
#[tauri::command]
pub async fn terminal_close_tab(state: ManagedState<'_>, tab_id: String) -> AppResult<()> {
    crate::session::close_tab(&state, &tab_id).await
}

/// 把当前回滚输出写到本地文件（右键「保存为日志」）。
///
/// 为什么走内核而不是前端：前端只装了 dialog 插件（能选路径），没有 fs 插件，
/// 落盘只能靠内核这一条路。这里就是一次 `std::fs::write`，
/// 为它单独引一个 tauri-plugin-fs 不划算。
///
/// `max_bytes` 默认 4 MiB：终端 scrollback 上限是 10 万行，
/// 不设上限时一次 dump 出十几兆写盘对用户毫无意义。
#[tauri::command]
pub async fn terminal_export_log(
    state: ManagedState<'_>,
    tab_id: String,
    path: String,
    max_bytes: Option<usize>,
) -> AppResult<u64> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let bytes = tab.dump(max_bytes.unwrap_or(4 * 1024 * 1024));
    let len = bytes.len() as u64;
    std::fs::write(&path, &bytes)
        .map_err(|e| crate::error::AppError::param(format!("写入 {path} 失败: {e}")))?;
    Ok(len)
}
