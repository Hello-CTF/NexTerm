//! 断线重连（§7 / M1-T10）：指数退避 1s→2s→4s→8s→16s→30s，最多 10 次。
//! 重连成功后在终端里插入横幅，不丢已有 scrollback（标签视图与前端通道不动）。

use std::sync::Arc;
use std::time::Duration;

use crate::error::AppResult;
use crate::session::{emit_status, notify_control_changed, SessionStatus};
use crate::state::AppState;
use crate::terminal::pty;
use crate::terminal::TerminalWriter;

const BACKOFF_SECS: [u64; 6] = [1, 2, 4, 8, 16, 30];
const MAX_ATTEMPTS: u32 = 10;
const BANNER: &str = "\r\n\x1b[33m[NexTerm] 已重新连接\x1b[0m\r\n";

/// 这个会话类型有没有「重连」这件事。
///
/// 本机会话（local）没有：底层就是本机进程，断了只能是进程没了，重建一个新
/// shell 也不是"恢复现场"。以前它落到 `_ => Err(Unsupported)`，于是 UI 会
/// **假装**重连 —— 状态切到"重连中"，按 1/2/4/8/16/30 秒退避重试十次，
/// 半分钟后报"重连次数用尽"。用户看到的是半分钟的空转加一句假故障。
pub(crate) fn is_reconnectable(kind: &str) -> bool {
    matches!(kind, "ssh" | "docker" | "winrm")
}

/// 泵退出之后该怎么办。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PumpExit {
    /// 什么都不做（用户停的 / 已经有人在处理 / 天然不可重连 / 连接还好着）
    Ignore,
    /// 连接真掉了 → 自动重连
    Reconnect,
}

/// 判定"一次泵退出"是否意味着连接掉了。
///
/// 抽成纯函数是为了能单测 —— 这里错一个分支的后果是**用户的会话被无缘无故拆掉
/// 重建**，而那种 bug 在真机上极难复现。三种结束原因长得很像，必须分开：
///
/// - **用户关标签 / 主动断开**：`stop` 已被取消 → 不管
/// - **用户在 shell 里敲了 `exit`**：`stop` 没取消，但**连接还活着** → 不管
///   （少了这条，敲一次 exit 就会把整条连接拆了重建，同会话其它标签一起断）
/// - **连接掉了**（channel 关闭 / 网络断）→ 重连
///
/// 再加一道闸：只有状态还是 `Connected` 才动手。已经是 `Disconnected` /
/// `Failed` / `Reconnecting` 说明已经有人处理过了，重复触发会把退避计数搅乱，
/// 甚至把刚建好的连接再拆一次。
pub(crate) fn pump_exit_action(
    stop_cancelled: bool,
    status: SessionStatus,
    reconnectable: bool,
    transport_alive: bool,
) -> PumpExit {
    if stop_cancelled || !reconnectable || transport_alive {
        return PumpExit::Ignore;
    }
    if status != SessionStatus::Connected {
        return PumpExit::Ignore;
    }
    PumpExit::Reconnect
}

/// 后台跑一次重连 —— 命令层（用户点「重新连接」）与掉线自动重连共用。
///
/// **必须异步**：退避最坏 1+2+4+8+16+30×5 = 181 秒。以前它是同步等在 IPC 命令里的，
/// 界面会卡在"重连中"三分钟、用户以为程序死了。结果一律走 `SESSION_STATUS` 事件：
/// 重连中 → 已连接 / 连接失败。
pub fn spawn_reconnect(state: Arc<AppState>, session_id: String) {
    tokio::spawn(async move {
        match try_reconnect(&state, &session_id).await {
            // 成功那条路自己打日志（带 attempt 明细），这里不重复
            Ok(true) => {}
            Ok(false) => tracing::info!(
                target: "session",
                session = %session_id,
                "未重连（不可重连 / 已在流程中 / 次数用尽）"
            ),
            Err(e) => tracing::warn!(
                target: "session",
                session = %session_id,
                error = %e,
                "重连出错"
            ),
        }
    });
}

/// 尝试重连一个会话：重建底层传输，替换进 Session，重开每个标签的 PTY。
pub async fn try_reconnect(state: &AppState, session_id: &str) -> AppResult<bool> {
    let session = match state.sessions.get(session_id).await {
        Ok(s) => s,
        // 会话已被回收（本机会话断开会走这条路，应用重启后前端也可能残留旧 id）：
        // 那种情况没有恢复的原料，如实回 false。
        Err(_) => return Ok(false),
    };
    // 先按类型判定，再碰状态：把状态改成「重连中」就等于对 UI 许了诺。
    if !is_reconnectable(&session.kind) {
        return Ok(false);
    }
    let asset_id = match &session.asset_id {
        Some(a) => a.clone(),
        None => return Ok(false), // 本地快速会话不重连
    };
    // 防抖：已在重连流程则退出
    if session.status_now() == SessionStatus::Reconnecting {
        return Ok(false);
    }
    let asset = state.store.asset_get(&asset_id).await?;
    let options = crate::transport::parse_options(&asset.options_json);
    let max_attempts = options
        .get("reconnectMax")
        .and_then(|v| v.as_u64())
        .unwrap_or(MAX_ATTEMPTS as u64) as u32;

    *session.status.lock().unwrap_or_else(|e| e.into_inner()) = SessionStatus::Reconnecting;
    emit_status(state, session_id, SessionStatus::Reconnecting, None);

    let mut attempt: u32 = session
        .reconnect_attempts
        .load(std::sync::atomic::Ordering::Relaxed);
    loop {
        if attempt >= max_attempts {
            *session.status.lock().unwrap_or_else(|e| e.into_inner()) = SessionStatus::Failed;
            emit_status(
                state,
                session_id,
                SessionStatus::Failed,
                Some("重连次数用尽".into()),
            );
            return Ok(false);
        }
        let wait = BACKOFF_SECS[attempt.min(BACKOFF_SECS.len() as u32 - 1) as usize];
        tokio::time::sleep(Duration::from_secs(wait)).await;
        attempt += 1;
        session
            .reconnect_attempts
            .store(attempt, std::sync::atomic::Ordering::Relaxed);

        tracing::info!(target: "session", session = %session_id, attempt, "尝试重连");
        let build = async {
            match session.kind.as_str() {
                // docker 主机就是 SSH 机器（kind 只影响前端开哪个面板）
                "ssh" | "docker" => {
                    let params = crate::session::build_ssh_params(state, &asset, &options).await?;
                    crate::transport::ssh::SshTransport::connect(
                        params,
                        Arc::clone(&state.store),
                        session_id.to_string(),
                    )
                    .await
                    .map(|t| t as Arc<dyn crate::transport::Transport>)
                }
                "winrm" => {
                    let params =
                        crate::session::build_winrm_params(state, &asset, &options).await?;
                    crate::transport::winrm::WinRmTransport::connect(params)
                        .await
                        .map(|t| t as Arc<dyn crate::transport::Transport>)
                }
                _ => Err(crate::error::AppError::Unsupported(
                    "该会话类型不重连".into(),
                )),
            }
        };
        match build.await {
            Ok(new_transport) => {
                session.replace_transport(new_transport).await;
                session
                    .reconnect_attempts
                    .store(0, std::sync::atomic::Ordering::Relaxed);
                *session.status.lock().unwrap_or_else(|e| e.into_inner()) =
                    SessionStatus::Connected;
                emit_status(state, session_id, SessionStatus::Connected, None);

                // 横幅进每个标签的 screen + scrollback + 前端（不丢 scrollback）
                let tabs = session
                    .tabs
                    .lock()
                    .unwrap_or_else(|e| e.into_inner())
                    .clone();
                for tid in tabs {
                    if let Ok(tab) = state.sessions.get_tab(&tid).await {
                        tab.feed_output(BANNER.as_bytes());
                        let _ = tab.send_to_frontend(BANNER.as_bytes().to_vec()).await;
                        reopen_pty_for_tab(state, &session, &tid).await;
                    }
                }
                tracing::info!(target: "session", session = %session_id, "重连成功");
                return Ok(true);
            }
            Err(e) => {
                tracing::warn!(target: "session", session = %session_id, attempt, error = %e, "重连失败");
            }
        }
    }
}

/// 重连后为标签重开 PTY：前端通道不变（tab.sink 仍在），只换底层泵。
/// 注意：tab.stop 只在用户关标签时才取消（close_tab），
/// 连接断开只让旧泵因 channel 关闭自然退出，令牌保持可用 —— 新泵直接复用。
///
/// # 为什么这里要清 `exited`
///
/// 旧泵退出时 `AppCallbacks::exit` 会无条件 `mark_exited()` 并把 `exited: true`
/// 推给前端，而 `exited` 从来没有地方设回 `false` —— 用户敲 `reboot` 后重连成功、
/// 新 shell 提示符都出来了、终端实际能操作，前端的「终端进程已结束」遮罩却一直
/// 挂着，正是因为没人清这个标记。这里是**唯一**该清的地方：底层传输已重建
/// （[`try_reconnect`] 里 `replace_transport` 成功），本函数的调用就是"这个标签
/// 拿到了新会话"的判据。
///
/// ⚠️ **必须等真的拿到新 PTY 再清**：`open_pty` 失败就说明这个标签没复活，
/// 标记保持 `exited` 才是对的。也正因为清标记只发生在重连路径上，用户敲 `exit`
/// 结束 shell（连接仍活、`pump_exit_action` 判 `Ignore`、根本不会调到这里）不会被
/// 误复活。
async fn reopen_pty_for_tab(
    state: &AppState,
    session: &Arc<crate::session::Session>,
    tab_id: &str,
) {
    let Ok(tab) = state.sessions.get_tab(tab_id).await else {
        return;
    };
    // WinRM 行模式标签没有 PTY（每条命令新 shell），但 `try_reconnect` 已经把
    // 底层传输换成功了 —— 那就是"拿到了新会话"，同样要清 `exited` 并广播，
    // 否则 winrm 行模式标签重连后前端会一直停在"进程已结束"。
    if session.kind != "ssh" && session.kind != "docker" {
        tab.mark_live();
        notify_control_changed(state, &tab).await;
        return;
    }
    let cols = tab.cols();
    let rows = tab.rows();
    if let Ok(crate::transport::PtyHandle::Ssh { read, write }) =
        session.transport().await.open_pty(cols, rows).await
    {
        tab.set_writer(TerminalWriter::Ssh(write)).await;
        // 新 PTY 到手 = 这个标签真的复活了：清 `exited` 并把 `exited: false`
        // 广播给所有观看端，让前端把遮罩摘掉。
        tab.mark_live();
        notify_control_changed(state, &tab).await;
        let tab2 = Arc::clone(&tab);
        let callbacks: Arc<dyn crate::terminal::TabCallbacks> =
            Arc::new(crate::session::AppCallbacks {
                app: state.app.clone(),
                sessions: Arc::clone(&state.sessions),
            });
        tokio::spawn(async move {
            pty::run_pump(tab2, pty::ByteSource::Ssh(read), callbacks).await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::{is_reconnectable, pump_exit_action, PumpExit};
    use crate::session::SessionStatus;
    use crate::terminal::transcoder::TerminalEncoding;
    use crate::terminal::TerminalTab;
    use std::sync::Arc;

    fn fresh_tab() -> Arc<TerminalTab> {
        TerminalTab::new_arc(
            "tab-1".into(),
            "sess-1".into(),
            80,
            24,
            TerminalEncoding::Utf8,
        )
    }

    /// 复刻一条完整的「泵退出」路径，断言最终 `exited` 取值。
    ///
    /// 与生产代码一一对应：
    /// 1. `AppCallbacks::exit` 无条件 `mark_exited()`；
    /// 2. 由 `pump_exit_action` 判是否重连；
    /// 3. 只有走重连（且真的拿到新 PTY）时才会 `mark_live()`。
    ///
    /// 这样三条语义（重连复活 / 主动关闭保持 / shell exit 保持）用同一个模型
    /// 就能全部锁住，避免"只有重连路径会清标记"这条不变量被将来的改动破坏。
    fn exit_flow_exited(stop_cancelled: bool, reconnectable: bool, transport_alive: bool) -> bool {
        let tab = fresh_tab();
        tab.mark_exited(); // AppCallbacks::exit 的第一步
        let action = pump_exit_action(
            stop_cancelled,
            SessionStatus::Connected,
            reconnectable,
            transport_alive,
        );
        if action == PumpExit::Reconnect {
            // 模拟 reopen_pty_for_tab 在拿到新 PTY 之后的动作
            tab.mark_live();
        }
        tab.has_exited()
    }

    /// ① 真掉线 ⇒ 重连 ⇒ 标签复活（`exited` 被清除，前端遮罩才会消失）。
    ///
    /// 回归：以前 reconnect 重建了 PTY 却不清 `exited`，用户敲 `reboot` 重连成功、
    /// 终端能操作，遮罩却一直挂着。
    #[test]
    fn successful_reconnect_revives_the_tab() {
        assert!(
            !exit_flow_exited(false, true, false),
            "真掉线并重连成功后，标签必须复活（exited=false）"
        );
    }

    /// ② 主动 close_tab / reap：`stop` 已被取消 ⇒ 不重连 ⇒ 保持已结束。
    ///
    /// 这两个动作本来就是要结束进程，绝不能把它们复活。
    #[test]
    fn deliberate_close_keeps_the_tab_exited() {
        assert!(
            exit_flow_exited(true, true, false),
            "主动关闭的标签必须保持 exited"
        );
    }

    /// ③ shell exit（连接仍活）⇒ 不触发重连 ⇒ 保持已结束。
    ///
    /// 锁死「不能因为重连就把真正结束的进程复活」：用户敲 `exit` 时连接还是好的，
    /// `pump_exit_action` 判 `Ignore`，走不到 `mark_live`。
    #[test]
    fn shell_exit_keeps_the_tab_exited() {
        assert!(
            exit_flow_exited(false, true, true),
            "shell 正常退出的标签不得被误复活"
        );
    }

    /// 回归：本机会话**不得**进入重连循环。
    ///
    /// 以前 local 落到 `_ => Err(Unsupported)`，于是 UI 会假装重连：
    /// 状态切「重连中」，按 1/2/4/8/16/30 秒退避试十次，半分钟后报
    /// 「重连次数用尽」。用户看到的是半分钟空转 + 一句假故障。
    #[test]
    fn local_sessions_are_not_reconnectable() {
        assert!(!is_reconnectable("local"), "本机没有重连语义");
        assert!(!is_reconnectable("mysql"), "数据库会话走自己的连接管理");
        assert!(!is_reconnectable("redis"));
    }

    #[test]
    fn network_sessions_are_reconnectable() {
        assert!(is_reconnectable("ssh"));
        assert!(is_reconnectable("docker"), "docker 主机底层就是 SSH");
        assert!(is_reconnectable("winrm"));
    }

    /// 真掉线 —— 这是"持久运维不能掉"的底线场景，必须重连。
    ///
    /// 三个条件同时成立：没被主动停、连接已死、状态还是 Connected。
    #[test]
    fn a_dead_connection_triggers_reconnect() {
        assert_eq!(
            pump_exit_action(false, SessionStatus::Connected, true, false),
            PumpExit::Reconnect
        );
    }

    /// 用户关标签 / 主动断开：`stop` 已取消 → 绝不能顺手重连。
    ///
    /// 少了这条，"关一个标签"会把整条连接拆掉重建。
    #[test]
    fn stopping_a_tab_never_triggers_reconnect() {
        assert_eq!(
            pump_exit_action(true, SessionStatus::Connected, true, false),
            PumpExit::Ignore
        );
    }

    /// 用户在 shell 里敲了 `exit`：`stop` 没取消，但**连接还活着** → 不重连。
    ///
    /// 这条最容易被漏掉：只看"泵为什么结束"会把正常退出 shell 误判成掉线，
    /// 于是敲一次 exit 就把整条连接拆了重建，同会话里其它标签一起断。
    #[test]
    fn shell_exit_does_not_tear_down_a_healthy_connection() {
        assert_eq!(
            pump_exit_action(false, SessionStatus::Connected, true, true),
            PumpExit::Ignore
        );
    }

    /// 已经在处理中（断开 / 失败 / 重连中 / 连接中）：不叠加第二次。
    ///
    /// 重复触发会把退避计数搅乱，最坏把刚建好的连接再拆一次。
    #[test]
    fn an_in_flight_state_suppresses_reconnect() {
        for st in [
            SessionStatus::Disconnected,
            SessionStatus::Failed,
            SessionStatus::Reconnecting,
            SessionStatus::Connecting,
        ] {
            assert_eq!(
                pump_exit_action(false, st, true, false),
                PumpExit::Ignore,
                "{st:?} 状态不该触发自动重连"
            );
        }
    }

    /// 天然不可重连的会话（本机 / 数据库）：连接"死了"也不重连。
    #[test]
    fn non_reconnectable_kinds_never_auto_reconnect() {
        assert_eq!(
            pump_exit_action(false, SessionStatus::Connected, false, false),
            PumpExit::Ignore
        );
    }
}
