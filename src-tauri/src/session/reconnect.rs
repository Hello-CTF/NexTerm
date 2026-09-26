//! 断线重连（§7 / M1-T10）：指数退避 1s→2s→4s→8s→16s→30s，最多 10 次。
//! 重连成功后在终端里插入横幅，不丢已有 scrollback（标签视图与前端通道不动）。

use std::sync::Arc;
use std::time::Duration;

use crate::error::AppResult;
use crate::session::{emit_status, SessionStatus};
use crate::state::AppState;
use crate::terminal::pty;
use crate::terminal::TerminalWriter;

const BACKOFF_SECS: [u64; 6] = [1, 2, 4, 8, 16, 30];
const MAX_ATTEMPTS: u32 = 10;
const BANNER: &str = "\r\n\x1b[33m[NexTerm] 已重新连接\x1b[0m\r\n";

/// 尝试重连一个会话：重建底层传输，替换进 Session，重开每个标签的 PTY。
pub async fn try_reconnect(state: &AppState, session_id: &str) -> AppResult<bool> {
    let session = match state.sessions.get(session_id).await {
        Ok(s) => s,
        Err(_) => return Ok(false), // 会话已被显式断开，不重连
    };
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
                "ssh" => {
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
async fn reopen_pty_for_tab(
    state: &AppState,
    session: &Arc<crate::session::Session>,
    tab_id: &str,
) {
    if session.kind != "ssh" {
        return; // WinRM 行模式标签无需 PTY
    }
    let Ok(tab) = state.sessions.get_tab(tab_id).await else {
        return;
    };
    let cols = tab.cols();
    let rows = tab.rows();
    if let Ok(crate::transport::PtyHandle::Ssh { read, write }) =
        session.transport().await.open_pty(cols, rows).await
    {
        tab.set_writer(TerminalWriter::Ssh(write)).await;
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
