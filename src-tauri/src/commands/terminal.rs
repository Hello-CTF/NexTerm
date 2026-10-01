//! 终端命令（§6.2 terminal）：attach / write / resize / 读屏 / 录制。
//!
//! # 控制权（单点模式）
//!
//! 同一个终端可以被多台设备同时看，但**同一时刻只有一个人能敲**。判据是
//! `clientId`（一台设备上一个浏览器实例的稳定身份，见
//! [`crate::terminal::TerminalTab`] 的控制权一节）：
//!
//! - `terminal_write` / `terminal_resize` 都会校验，非持有者返回
//!   [`crate::error::AppError::NotController`]（错误码 `not_controller`），
//!   界面据此弹「接管控制」而不是当报错。
//! - `clientId` 缺省为 [`crate::ipc_shim::DESKTOP_SUBSCRIBER`]。这条缺省是给
//!   **桌面版**用的（整个进程只有一个视图，缺省即正确），也让还没接 `clientId`
//!   的服务端前端保持「共享可写」的旧行为，不会因为这次改造突然敲不了字。

use crate::ipc_shim as tauri;
use serde::{Deserialize, Serialize};
use tauri::ipc::Channel;

use crate::error::{AppError, AppResult};
use crate::state::ManagedState;
use crate::terminal::transcoder::TerminalEncoding;

use super::client_or_default;

#[tauri::command]
pub async fn terminal_attach(
    state: ManagedState<'_>,
    session_id: String,
    cols: u16,
    rows: u16,
    channel: Channel<Vec<u8>>,
    client_id: Option<String>,
) -> AppResult<String> {
    let client = client_or_default(client_id);
    let s = state.sessions.get(&session_id).await?;
    match s.kind.as_str() {
        "winrm" => {
            crate::session::open_winrm_line_tab(&state, &s, cols, rows, channel, &client).await
        }
        _ => crate::session::open_terminal_tab(&state, &s, cols, rows, channel, &client).await,
    }
}

/// 接管一个**已存在**的终端标签（关掉网页再打开、第二台设备打开）。
///
/// 与 [`terminal_attach`] 的差别是**不新建 shell**：把服务端那条连接上已有的
/// 回滚内容回放回来，继续推。这是「关掉浏览器几小时再回来，日志还在、还能接着敲」
/// 的关键路径。
///
/// `replayBytes` 缺省 4 MiB：回滚缓冲上限是 32 MiB，但按这个场景的实际需要，
/// 几兆已经能看到最后几千行；再多只是让新页面白等一次大传输。
#[tauri::command]
pub async fn terminal_attach_tab(
    state: ManagedState<'_>,
    tab_id: String,
    replay_bytes: Option<usize>,
    channel: Channel<Vec<u8>>,
    client_id: Option<String>,
) -> AppResult<crate::session::AttachedTabInfo> {
    let client = client_or_default(client_id);
    let replay = replay_bytes.unwrap_or(4 * 1024 * 1024);
    crate::session::attach_existing_tab(&state, &tab_id, replay, channel, &client).await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WriteArgs {
    pub tab_id: String,
    /// 用户按键（Uint8Array 直传）。
    pub data: Vec<u8>,
    /// 谁在敲。缺省 = 桌面视图（见模块文档）。
    pub client_id: Option<String>,
}

#[tauri::command]
pub async fn terminal_write(state: ManagedState<'_>, args: WriteArgs) -> AppResult<()> {
    // 接管模式：用户按键即夺回（§8.6 steal_on_key）
    crate::ai::takeover::on_user_key(&state, &args.tab_id).await;
    let tab = state.sessions.get_tab(&args.tab_id).await?;
    let client = client_or_default(args.client_id);
    // 单点模式：只有持控制权的那端能敲。被拒返回 `not_controller`，
    // 让界面区分「别人正在操作」与真正的错误。
    tab.ensure_write_permission(&client)
        .await
        .map_err(|_| AppError::NotController)?;
    tab.write(&args.data).await
}

#[tauri::command]
pub async fn terminal_resize(
    state: ManagedState<'_>,
    tab_id: String,
    cols: u16,
    rows: u16,
    client_id: Option<String>,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    // 尺寸也只由持控制权的那端决定 —— 这就是「最后活跃者赢」的落点：
    // 谁在操作谁决定 PTY 尺寸，观察者不会因为自己窗口大小不同就把别人的画面重排。
    let client = client_or_default(client_id);
    tab.ensure_write_permission(&client)
        .await
        .map_err(|_| AppError::NotController)?;
    tab.resize(cols, rows).await
}

/// 接管输入控制权（界面上的「接管控制」）。返回被顶掉的那个人。
#[tauri::command]
pub async fn terminal_claim(
    state: ManagedState<'_>,
    tab_id: String,
    client_id: Option<String>,
) -> AppResult<Option<String>> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let client = client_or_default(client_id);
    let prev = tab.claim_controller(&client).await;
    // 控制权换了人 —— 立刻广播，让被顶掉的那一端（以及所有观察者）马上更新，
    // 而不是等它下一次敲键收到 `not_controller` 才发现。
    crate::session::notify_control_changed(&state, &tab).await;
    Ok(prev)
}

/// 主动交出控制权（只在自己持权时生效）。
#[tauri::command]
pub async fn terminal_release(
    state: ManagedState<'_>,
    tab_id: String,
    client_id: Option<String>,
) -> AppResult<bool> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    let client = client_or_default(client_id);
    let released = tab.release_controller(&client).await;
    crate::session::notify_control_changed(&state, &tab).await;
    Ok(released)
}

/// 内核里存活着的终端标签（**后台会话面板**的数据源）。
///
/// 判据不是「有几个人在看」，而是「进程还在不在」：`subscribers == 0 且 !exited`
/// 就是用户关掉页面后仍在后台跑着的那个任务。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LiveTabInfo {
    pub tab_id: String,
    pub session_id: String,
    /// 所属会话/资产名（界面上的标题）。
    pub session_name: String,
    pub session_kind: String,
    pub cols: u16,
    pub rows: u16,
    /// 当前持控制权的人；`None` = 无人持权（谁都不能敲，需先接管）。
    pub controller: Option<String>,
    /// 有几个前端在看（0 = 没人在看，也就是「后台运行中」）。
    ///
    /// ⚠️ **通道数**，不是设备数：同一台设备开两个页面就是 2。"后台是否还有人在看"
    /// 用这个字段（有推送管道就说明有人在看）；界面上的「N 个设备正在观看」用 `viewers`。
    pub subscribers: usize,
    /// **观看设备数**（按 `client` 去重）—— 界面「N 个设备正在观看」的唯一数据源。
    /// 同一台设备开两个页面时 `subscribers == 2` 而 `viewers == 1`。
    pub viewers: usize,
    /// 进程已结束（只能看最后一屏）。
    pub exited: bool,
    /// 距最近一次输出多久（毫秒）。
    pub last_output_ms_ago: u64,
}

#[tauri::command]
pub async fn terminal_list(state: ManagedState<'_>) -> AppResult<Vec<LiveTabInfo>> {
    let sessions = state.sessions.sessions.read().await;
    let mut out = Vec::new();
    for session in sessions.values() {
        let tab_ids = session
            .tabs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .clone();
        for tid in tab_ids {
            let Ok(tab) = state.sessions.get_tab(&tid).await else {
                continue;
            };
            out.push(LiveTabInfo {
                tab_id: tab.tab_id.clone(),
                session_id: session.id.clone(),
                session_name: session.name.clone(),
                session_kind: session.kind.clone(),
                cols: tab.cols(),
                rows: tab.rows(),
                controller: tab.controller().await,
                subscribers: tab.subscriber_count().await,
                viewers: tab.viewer_count().await,
                exited: tab.has_exited(),
                last_output_ms_ago: tab.last_output_ms_ago(),
            });
        }
    }
    Ok(out)
}

/// 摘掉自己的订阅。
///
/// `channelId` 给出来就只摘那一条（服务端：一次只摘自己的 WS 通道），
/// 不给就清空全部（桌面语义 —— 整个进程只有一个视图，"摘自己"与"清空"等价）。
///
/// ⚠️ 多端同看时**必须传 `channelId`**：否则一台设备切走标签会把所有其他设备的
/// 推送一起掐掉，而它们那边看起来只是"画面不动了"，极难排查。
#[tauri::command]
pub async fn terminal_detach(
    state: ManagedState<'_>,
    tab_id: String,
    channel_id: Option<String>,
) -> AppResult<()> {
    let tab = state.sessions.get_tab(&tab_id).await?;
    match channel_id {
        Some(cid) if !cid.is_empty() => tab.detach_subscriber(&cid).await,
        _ => tab.detach_frontend().await,
    }
    // 订阅数（可能还有控制权）变了 —— 让还在看的那一端立刻更新。
    crate::session::notify_control_changed(&state, &tab).await;
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

/// 关闭标签。
///
/// `mode` 决定它是哪一种「关」：
/// - `"detach"`：**只从视图里拿走**。泵继续跑、进程不动，输出照旧进回滚缓冲，
///   之后可以在「后台会话」里重新接管。用户跑着长任务时该选这个。
///   多端同看时只摘 `clientId` 那一端 —— 别的设备照旧在看（缺省 `clientId` =
///   桌面视图，整个进程只有一个视图，"摘自己"与"清空"等价）。
/// - 其它（含不传）：真的结束 —— 停泵、杀进程、摘记录，所有观看端都会收到
///   `terminal://control`（`exited: true`）。
///
/// 缺省是「真结束」而不是「detach」：不能因为"关标签"这个动作看着轻，就默默
/// 把用户可能正等着结果的任务留成后台僵尸。要后台运行，得由用户显式选。
#[tauri::command]
pub async fn terminal_close_tab(
    state: ManagedState<'_>,
    tab_id: String,
    mode: Option<String>,
    client_id: Option<String>,
) -> AppResult<()> {
    match mode.as_deref() {
        // detach 只摘这一端（缺省 client = 桌面视图）：绝不无差别清掉别人的订阅。
        Some("detach") => {
            let client = client_or_default(client_id);
            crate::session::detach_tab_for_client(&state, &tab_id, &client).await
        }
        _ => crate::session::close_tab(&state, &tab_id).await,
    }
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
