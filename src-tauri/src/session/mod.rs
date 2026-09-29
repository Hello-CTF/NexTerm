//! 会话层（§7）：会话池、标签生命周期、复用连接。
//!
//! 规则：
//! - 一个资产 → 一个 Session → 一条底层连接；多标签复用（再开 channel）。
//! - 标签 ≠ 连接：关闭最后一个标签不断连（idle 30 分钟后自动断）。
//! - 断线自动重连（指数退避），成功后在终端里插入横幅且不丢 scrollback。

pub mod reconnect;

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Serialize;
use tokio::sync::RwLock;

use crate::error::{AppError, AppResult};
use crate::events::SessionStatusPayload;
use crate::ids::{new_id, now_ms, SessionId, TabId};
use crate::state::AppState;
use crate::store::models::AssetRow;
use crate::terminal::transcoder::TerminalEncoding;
use crate::terminal::{pty, TabCallbacks, TerminalTab, TerminalWriter};
use crate::transport::ssh::{SshConnectParams, SshTransport};
use crate::transport::winrm::WinRmTransport;
use crate::transport::{PtyHandle, Transport};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SessionStatus {
    Connecting,
    Connected,
    Reconnecting,
    Disconnected,
    Failed,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionInfo {
    pub id: SessionId,
    pub asset_id: Option<String>,
    pub name: String,
    pub kind: String,
    pub status: SessionStatus,
    pub tabs: Vec<TabId>,
    pub created_at: u64,
}

pub struct Session {
    pub id: SessionId,
    pub asset_id: Option<String>,
    pub name: String,
    pub kind: String,
    /// 底层传输；重连时整体替换（RwLock 支持运行期更换）。
    transport: RwLock<Arc<dyn Transport>>,
    pub tabs: Mutex<Vec<TabId>>,
    pub status: Mutex<SessionStatus>,
    /// 非交互会话 cwd（WinRM）。
    pub cwd: Mutex<String>,
    pub created_at: u64,
    pub encoding: TerminalEncoding,
    /// 最后一个标签关闭时间（空闲断连计时）。
    pub last_tab_closed_at: Mutex<Option<u64>>,
    pub reconnect_attempts: std::sync::atomic::AtomicU32,
}

impl Session {
    pub async fn transport(&self) -> Arc<dyn Transport> {
        Arc::clone(&*self.transport.read().await)
    }

    async fn replace_transport(&self, t: Arc<dyn Transport>) {
        let old = {
            let mut guard = self.transport.write().await;
            std::mem::replace(&mut *guard, t)
        };
        old.close().await;
    }

    pub fn status_now(&self) -> SessionStatus {
        *self.status.lock().unwrap_or_else(|e| e.into_inner())
    }
}

pub struct SessionManager {
    pub sessions: RwLock<HashMap<SessionId, Arc<Session>>>,
    pub tabs: RwLock<HashMap<TabId, Arc<TerminalTab>>>,
    /// 标签 → 会话 索引。
    pub tab_sessions: RwLock<HashMap<TabId, SessionId>>,
    /// 运行中的端口转发。
    pub forwards:
        RwLock<HashMap<String, std::sync::Arc<crate::transport::forward::RunningForward>>>,
    /// 本地 PTY 的杀手（close_tab 时杀进程）。
    pub tab_killers: RwLock<HashMap<TabId, Box<dyn portable_pty::ChildKiller + Send + Sync>>>,
}

impl Default for SessionManager {
    fn default() -> Self {
        Self::new()
    }
}

impl SessionManager {
    pub fn new() -> Self {
        Self {
            sessions: RwLock::new(HashMap::new()),
            tabs: RwLock::new(HashMap::new()),
            tab_sessions: RwLock::new(HashMap::new()),
            forwards: RwLock::new(HashMap::new()),
            tab_killers: RwLock::new(HashMap::new()),
        }
    }

    pub async fn list(&self) -> Vec<SessionInfo> {
        let sessions = self.sessions.read().await;
        sessions
            .values()
            .map(|s| SessionInfo {
                id: s.id.clone(),
                asset_id: s.asset_id.clone(),
                name: s.name.clone(),
                kind: s.kind.to_string(),
                status: s.status_now(),
                tabs: s.tabs.lock().unwrap_or_else(|e| e.into_inner()).clone(),
                created_at: s.created_at,
            })
            .collect()
    }

    pub async fn get(&self, session_id: &str) -> AppResult<Arc<Session>> {
        self.sessions
            .read()
            .await
            .get(session_id)
            .cloned()
            .ok_or_else(|| AppError::NotFound(format!("会话 {session_id}")))
    }

    pub async fn get_tab(&self, tab_id: &str) -> AppResult<Arc<TerminalTab>> {
        self.tabs
            .read()
            .await
            .get(tab_id)
            .cloned()
            .ok_or_else(|| AppError::NotFound(format!("终端标签 {tab_id}")))
    }

    pub async fn session_of_tab(&self, tab_id: &str) -> AppResult<Arc<Session>> {
        let sid = self
            .tab_sessions
            .read()
            .await
            .get(tab_id)
            .cloned()
            .ok_or_else(|| AppError::NotFound(format!("标签 {tab_id} 无所属会话")))?;
        self.get(&sid).await
    }
}

/// 依据资产建立会话（§7：一资产一连接）。
/// `accept_unknown`：首连指纹确认后重试时放行未知主机（内核写入 known_host）。
pub async fn connect_asset(
    state: &AppState,
    asset: &AssetRow,
    accept_unknown: bool,
) -> AppResult<Arc<Session>> {
    // 复用：§7 写的是「一个资产 → 一个 Session」，但实现一直是"每次调用新建一个"。
    // 后果在本地资产上最刺眼 —— 双击两下「当前设备」就冒出两个同名工作区、
    // 两条互不相干的会话，关掉一个另一个还在。已 Failed / Disconnected 的不复用
    // （那种本来就是要重建）。
    if let Some(existing) = live_session_of_asset(state, &asset.id).await {
        return Ok(existing);
    }

    let options = crate::transport::parse_options(&asset.options_json);
    let encoding = options
        .get("encoding")
        .and_then(|v| v.as_str())
        .and_then(TerminalEncoding::from_str_opt)
        .unwrap_or_default();
    let session_id = new_id();

    let transport: Arc<dyn Transport> = match asset.kind.as_str() {
        "local" => Arc::new(local_transport_from(&options)),
        // docker 主机 = 一台跑着 Docker 的 SSH 机器（前端连上后直接开容器面板）。
        // 以前 docker 落到下面的 `other` 分支 → 建得出资产、点连接必报
        // 「资产类型 docker 不支持会话」，属于半成品入口。
        "ssh" | "docker" => {
            let mut params = build_ssh_params(state, asset, &options).await?;
            if accept_unknown {
                params.auto_accept_unknown = true;
            }
            SshTransport::connect(params, Arc::clone(&state.store), session_id.clone()).await?
        }
        "winrm" => {
            let params = build_winrm_params(state, asset, &options).await?;
            WinRmTransport::connect(params).await?
        }
        other => return Err(AppError::param(format!("资产类型 {other} 不支持会话"))),
    };

    // 初始命令（§5.2 连接配置项：连接后自动执行）
    if let Some(init_cmd) = options.get("initialCommand").and_then(|v| v.as_str()) {
        let _ = transport.exec(init_cmd, Duration::from_secs(10)).await;
    }

    let session = Arc::new(Session {
        id: session_id.clone(),
        asset_id: Some(asset.id.clone()),
        name: asset.name.clone(),
        kind: asset.kind.clone(),
        transport: RwLock::new(transport),
        tabs: Mutex::new(Vec::new()),
        status: Mutex::new(SessionStatus::Connected),
        cwd: Mutex::new(String::new()),
        created_at: now_ms(),
        encoding,
        last_tab_closed_at: Mutex::new(None),
        reconnect_attempts: std::sync::atomic::AtomicU32::new(0),
    });
    state
        .sessions
        .sessions
        .write()
        .await
        .insert(session_id.clone(), Arc::clone(&session));

    state
        .store
        .audit_insert(crate::store::AuditInput {
            session_id: Some(session_id),
            asset_id: Some(asset.id.clone()),
            source: "user",
            kind: "connect",
            payload: serde_json::json!({ "asset": asset.name, "kind": asset.kind }),
            exit_code: None,
            duration_ms: None,
        })
        .await
        .ok();

    Ok(session)
}

/// 找出某资产上仍然活着的会话（用于复用）。
///
/// 「活着」= Connected / Connecting / Reconnecting：这三种状态下连接是好的或正在恢复，
/// 可以直接拿来开新标签。Failed / Disconnected 不复用 —— 那种会话的底层传输已经废了。
async fn live_session_of_asset(state: &AppState, asset_id: &str) -> Option<Arc<Session>> {
    let sessions = state.sessions.sessions.read().await;
    sessions
        .values()
        .find(|s| {
            s.asset_id.as_deref() == Some(asset_id)
                && matches!(
                    s.status_now(),
                    SessionStatus::Connected
                        | SessionStatus::Connecting
                        | SessionStatus::Reconnecting
                )
        })
        .cloned()
}

/// 本机传输：把资产的 `options` 落成 shell / 起始目录。
///
/// 以前 `local` 分支直接 `LocalTransport::new()`，而选项在**更前面**就已经按
/// 合成资产丢了 —— 本地资产「配置了也不生效」。现在两个 key 有明确语义：
/// `shell`（默认取 `$SHELL` / Windows 上的 pwsh）、`cwd`（默认家目录）。
fn local_transport_from(
    options: &std::collections::HashMap<String, serde_json::Value>,
) -> crate::transport::local::LocalTransport {
    let mut t = crate::transport::local::LocalTransport::new();
    let pick = |key: &str| {
        options
            .get(key)
            .and_then(|v| v.as_str())
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
    };
    if let Some(shell) = pick("shell") {
        t.shell = Some(shell);
    }
    if let Some(cwd) = pick("cwd") {
        if let Ok(mut cur) = t.initial_cwd.lock() {
            *cur = cwd;
        }
    }
    t
}

/// 为本地快速终端（无资产）建立会话。
pub async fn connect_local_quick(state: &AppState) -> AppResult<Arc<Session>> {
    // 落到内置的「当前设备」资产上，而不是现造一个 id 并不存在于库里的合成资产。
    // 合成资产的两个后遗症：审计的 `asset_id` 指向查不到的归属；重连时
    // `asset_get` 直接报错（看着像"重连失败"，其实是身份不存在）。
    let asset = state.store.asset_ensure_builtin_local().await?;
    connect_asset(state, &asset, false).await
}

/// 打开一个终端标签（复用会话连接，§7）。
pub async fn open_terminal_tab(
    state: &AppState,
    session: &Arc<Session>,
    cols: u16,
    rows: u16,
    channel: tauri::ipc::Channel<Vec<u8>>,
) -> AppResult<TabId> {
    let tab_id = new_id();
    let tab = TerminalTab::new_arc(
        tab_id.clone(),
        session.id.clone(),
        cols,
        rows,
        session.encoding,
    );
    let handle = session.transport().await.open_pty(cols, rows).await?;
    tab.attach_frontend(channel, 0).await;
    match handle {
        PtyHandle::Ssh { read, write } => {
            tab.set_writer(TerminalWriter::Ssh(Arc::clone(&write)))
                .await;
            register_and_pump(state, session, tab, pty::ByteSource::Ssh(read)).await?;
        }
        PtyHandle::Local {
            io,
            reader,
            child,
            killer,
        } => {
            tab.set_writer(TerminalWriter::Local(io)).await;
            let callbacks = AppCallbacks {
                app: state.app.clone(),
                sessions: Arc::clone(&state.sessions),
            };
            pty::LocalExitWatcher { child }.spawn(
                tab_id.clone(),
                Arc::new(callbacks),
                Arc::clone(&tab.stop),
            );
            state
                .sessions
                .tab_killers
                .write()
                .await
                .insert(tab_id.clone(), killer);
            register_and_pump(state, session, tab, pty::ByteSource::Local(reader)).await?;
        }
    }
    Ok(tab_id)
}

/// WinRM 行模式标签（非交互，§5.4）。
pub async fn open_winrm_line_tab(
    state: &AppState,
    session: &Arc<Session>,
    cols: u16,
    rows: u16,
    channel: tauri::ipc::Channel<Vec<u8>>,
) -> AppResult<TabId> {
    let tab_id = new_id();
    let tab = TerminalTab::new_arc(
        tab_id.clone(),
        session.id.clone(),
        cols,
        rows,
        session.encoding,
    );
    tab.attach_frontend(channel, 0).await;
    tab.feed_output(
        "[NexTerm] WinRM 非交互模式（每条命令新 shell，不支持 vim/top）\r\nPS> ".as_bytes(),
    );
    register_and_pump_quiet(state, session, tab).await
}

async fn register_and_pump(
    state: &AppState,
    session: &Arc<Session>,
    tab: Arc<TerminalTab>,
    source: pty::ByteSource,
) -> AppResult<()> {
    let tab_id = tab.tab_id.clone();
    {
        let mut tabs = session.tabs.lock().unwrap_or_else(|e| e.into_inner());
        tabs.push(tab_id.clone());
    }
    state
        .sessions
        .tabs
        .write()
        .await
        .insert(tab_id.clone(), Arc::clone(&tab));
    state
        .sessions
        .tab_sessions
        .write()
        .await
        .insert(tab_id.clone(), session.id.clone());
    *session
        .last_tab_closed_at
        .lock()
        .unwrap_or_else(|e| e.into_inner()) = None;
    *session.status.lock().unwrap_or_else(|e| e.into_inner()) = SessionStatus::Connected;

    let callbacks: Arc<dyn TabCallbacks> = Arc::new(AppCallbacks {
        app: state.app.clone(),
        sessions: Arc::clone(&state.sessions),
    });
    tokio::spawn(async move {
        pty::run_pump(tab, source, callbacks).await;
    });
    Ok(())
}

/// WinRM 行模式：只注册，无泵（输出由 exec 包装器 feed）。
async fn register_and_pump_quiet(
    state: &AppState,
    session: &Arc<Session>,
    tab: Arc<TerminalTab>,
) -> AppResult<TabId> {
    let tab_id = tab.tab_id.clone();
    session
        .tabs
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .push(tab_id.clone());
    state
        .sessions
        .tabs
        .write()
        .await
        .insert(tab_id.clone(), Arc::clone(&tab));
    state
        .sessions
        .tab_sessions
        .write()
        .await
        .insert(tab_id.clone(), session.id.clone());
    Ok(tab_id)
}

/// 关闭标签：停泵杀进程；会话保留（§7 标签 ≠ 连接）。
pub async fn close_tab(state: &AppState, tab_id: &str) -> AppResult<()> {
    let tab = state.sessions.get_tab(tab_id).await?;
    tab.stop.cancel();
    tab.detach_frontend().await;
    if let Some(mut killer) = state.sessions.tab_killers.write().await.remove(tab_id) {
        let _ = killer.kill();
    }
    state.sessions.tabs.write().await.remove(tab_id);
    if let Ok(session) = state.sessions.session_of_tab(tab_id).await {
        {
            let mut tabs = session.tabs.lock().unwrap_or_else(|e| e.into_inner());
            tabs.retain(|t| t != tab_id);
        }
        if session
            .tabs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .is_empty()
        {
            *session
                .last_tab_closed_at
                .lock()
                .unwrap_or_else(|e| e.into_inner()) = Some(now_ms());
        }
    }
    state.sessions.tab_sessions.write().await.remove(tab_id);
    Ok(())
}

/// 断开会话（显式操作或空闲超时）。
pub async fn disconnect(state: &AppState, session_id: &str) -> AppResult<()> {
    let session = state.sessions.get(session_id).await?;
    {
        let tabs = session
            .tabs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .clone();
        for t in tabs {
            if let Ok(tab) = state.sessions.get_tab(&t).await {
                tab.stop.cancel();
            }
            if let Some(mut killer) = state.sessions.tab_killers.write().await.remove(&t) {
                let _ = killer.kill();
            }
            state.sessions.tabs.write().await.remove(&t);
            state.sessions.tab_sessions.write().await.remove(&t);
        }
    }
    session.transport().await.close().await;
    *session.status.lock().unwrap_or_else(|e| e.into_inner()) = SessionStatus::Disconnected;
    state.sessions.sessions.write().await.remove(session_id);
    emit_status(state, session_id, SessionStatus::Disconnected, None);
    Ok(())
}

pub fn emit_status(
    state: &AppState,
    session_id: &str,
    status: SessionStatus,
    error: Option<String>,
) {
    use tauri::Emitter;
    let status_str = match status {
        SessionStatus::Connecting => "connecting",
        SessionStatus::Connected => "connected",
        SessionStatus::Reconnecting => "reconnecting",
        SessionStatus::Disconnected => "disconnected",
        SessionStatus::Failed => "failed",
    };
    let _ = state.app.emit(
        crate::events::SESSION_STATUS,
        SessionStatusPayload {
            session_id: session_id.to_string(),
            status: status_str.to_string(),
            error,
        },
    );
}

/// 泵回调：exit → 通知前端 / 触发重连；throttled → 通知前端。
pub struct AppCallbacks {
    pub app: tauri::AppHandle,
    pub sessions: Arc<SessionManager>,
}

impl TabCallbacks for AppCallbacks {
    fn exit(&self, tab_id: &str, exit_code: Option<i32>) {
        use tauri::Emitter;
        let _ = self.app.emit(
            crate::events::TERMINAL_EXIT,
            crate::events::TerminalExitPayload {
                tab_id: tab_id.to_string(),
                exit_code,
            },
        );
    }

    fn throttled(&self, tab_id: &str, inflight: usize) {
        use tauri::Emitter;
        let _ = self.app.emit(
            crate::events::TERMINAL_THROTTLED,
            crate::events::TerminalThrottledPayload {
                tab_id: tab_id.to_string(),
                inflight_bytes: inflight,
            },
        );
    }
}

/// 从资产 + 凭据库组装 SSH 连接参数。
pub async fn build_ssh_params(
    state: &AppState,
    asset: &AssetRow,
    options: &HashMap<String, serde_json::Value>,
) -> AppResult<SshConnectParams> {
    let host = asset
        .host
        .clone()
        .ok_or_else(|| AppError::param("SSH 资产缺少 host"))?;
    let port = asset.port.map(|p| p as u16).unwrap_or(22);
    let username = asset.username.clone().unwrap_or_else(|| "root".into());
    let auth = match asset.auth_kind.as_deref() {
        Some("password") => {
            SshAuth::Password(asset_credential(state, asset).await?.unwrap_or_default())
        }
        Some("key") => {
            // 私钥两个来源，不加新列、按凭据类型区分：
            // - key_path 有值 → 本地文件私钥；cred_id 若绑定且是 passphrase 类型则当口令
            // - key_path 为空 → cred_id 必须指向 private_key 凭据（私钥本体在凭据库里）
            let key_path = asset.key_path.clone().unwrap_or_default();
            if !key_path.is_empty() {
                let passphrase = match asset.cred_id.as_deref() {
                    Some(id) => {
                        let row = state.store.credential_get_row(id).await?;
                        if row.kind == "private_key" {
                            // 私钥本体凭据对文件私钥没有意义，不当口令用
                            None
                        } else {
                            let dek = state.vault.dek().await?;
                            Some(Vault::decrypt_credential(&dek, &row)?.to_string())
                        }
                    }
                    None => None,
                };
                SshAuth::Key {
                    path: key_path,
                    passphrase,
                }
            } else {
                match asset.cred_id.as_deref() {
                    Some(id) => {
                        let row = state.store.credential_get_row(id).await?;
                        if row.kind != "private_key" {
                            return Err(AppError::param(
                                "该资产未绑定私钥：请在凭据库里选择 private_key 类型凭据，或填写私钥文件路径",
                            ));
                        }
                        let dek = state.vault.dek().await?;
                        let content = Vault::decrypt_credential(&dek, &row)?.to_string();
                        SshAuth::KeyContent {
                            content,
                            passphrase: None,
                        }
                    }
                    None => {
                        return Err(AppError::param("私钥资产缺少私钥文件或私钥凭据"));
                    }
                }
            }
        }
        Some("agent") => SshAuth::Agent,
        _ => return Err(AppError::param("SSH 资产缺少认证方式")),
    };
    Ok(SshConnectParams {
        host,
        port,
        username,
        auth,
        proxy: options
            .get("proxy")
            .and_then(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .map(|s| s.to_string()),
        connect_timeout: Duration::from_secs(
            options
                .get("connectTimeout")
                .and_then(|v| v.as_u64())
                .unwrap_or(15),
        ),
        auto_accept_unknown: options
            .get("autoAcceptUnknownHost")
            .and_then(|v| v.as_bool())
            .unwrap_or(false),
    })
}

use crate::transport::ssh::SshAuth;

/// 取资产凭据明文（vault 解密）。未绑定凭据返回 None。
pub async fn asset_credential(state: &AppState, asset: &AssetRow) -> AppResult<Option<String>> {
    let cred_id = match &asset.cred_id {
        Some(id) => id.clone(),
        None => return Ok(None),
    };
    let row = state.store.credential_get_row(&cred_id).await?;
    let dek = state.vault.dek().await?;
    Ok(Some(Vault::decrypt_credential(&dek, &row)?.to_string()))
}

use crate::vault::Vault;

/// 组装 WinRM 连接参数。
pub async fn build_winrm_params(
    state: &AppState,
    asset: &AssetRow,
    options: &HashMap<String, serde_json::Value>,
) -> AppResult<crate::transport::winrm::WinRmConnectParams> {
    let host = asset
        .host
        .clone()
        .ok_or_else(|| AppError::param("WinRM 资产缺少 host"))?;
    let port = asset.port.map(|p| p as u16).unwrap_or(5985);
    let username = asset
        .username
        .clone()
        .unwrap_or_else(|| "Administrator".into());
    let password = asset_credential(state, asset).await?.unwrap_or_default();
    let (domain, user_only) = match username.split_once('\\') {
        Some((d, u)) => (d.to_string(), u.to_string()),
        None => (String::new(), username),
    };
    Ok(crate::transport::winrm::WinRmConnectParams {
        host,
        port,
        username: user_only,
        password,
        domain,
        auth: options
            .get("auth")
            .and_then(|v| v.as_str())
            .unwrap_or("ntlm")
            .to_string(),
        use_tls: port == 5986
            || options
                .get("tls")
                .and_then(|v| v.as_bool())
                .unwrap_or(false),
        accept_invalid_certs: options
            .get("acceptInvalidCerts")
            .and_then(|v| v.as_bool())
            .unwrap_or(true),
        proxy: None,
    })
}

/// 空闲会话清理（默认 30 分钟，§7）。
pub async fn idle_sweeper(state: std::sync::Arc<AppState>, idle_close: Duration) {
    loop {
        tokio::time::sleep(Duration::from_secs(60)).await;
        let candidates: Vec<String> = {
            let sessions = state.sessions.sessions.read().await;
            sessions
                .values()
                .filter_map(|s| {
                    let closed_at = *s
                        .last_tab_closed_at
                        .lock()
                        .unwrap_or_else(|e| e.into_inner());
                    let empty = s.tabs.lock().unwrap_or_else(|e| e.into_inner()).is_empty();
                    match (empty, closed_at) {
                        (true, Some(t)) if now_ms() - t > idle_close.as_millis() as u64 => {
                            Some(s.id.clone())
                        }
                        _ => None,
                    }
                })
                .collect()
        };
        for sid in candidates {
            let _ = disconnect(&state, &sid).await;
        }
    }
}

#[cfg(test)]
mod local_option_tests {
    use super::local_transport_from;
    use serde_json::json;

    /// 本机资产的 options 必须**真的**落到传输上。
    ///
    /// 以前 `local` 分支直接 `LocalTransport::new()`，而 options 在更前面就被
    /// 合成资产丢掉了 —— 「配了 Shell 却还是起 $SHELL」正是这类静默失效：
    /// 保存成功、界面显示已配置、行为完全没变。
    #[test]
    fn local_options_become_transport_settings() {
        let mut o = std::collections::HashMap::new();
        o.insert("shell".to_string(), json!("/bin/zsh"));
        o.insert("cwd".to_string(), json!("/tmp"));
        let t = local_transport_from(&o);
        assert_eq!(t.shell.as_deref(), Some("/bin/zsh"));
        assert_eq!(*t.initial_cwd.lock().unwrap(), "/tmp");
    }

    /// 空串 / 纯空白 / 缺键一律算「没配」，保持默认（$SHELL、家目录）。
    ///
    /// 不放行的话，前端「清空输入框」会变成一个空 shell 路径或空 cwd ——
    /// 症状是「编辑过资产之后终端起不来」，而报错会指向 shell 启动失败，
    /// 跟"清空了输入框"这件事看起来毫无关系。
    #[test]
    fn blank_local_options_keep_defaults() {
        let mut o = std::collections::HashMap::new();
        o.insert("shell".to_string(), json!("   "));
        o.insert("cwd".to_string(), json!(""));
        let t = local_transport_from(&o);
        assert!(t.shell.is_none(), "空白 shell 应视为未配置");
        assert!(
            !t.initial_cwd.lock().unwrap().is_empty(),
            "cwd 应保持默认家目录"
        );
    }
}
