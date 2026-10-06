//! 终端引擎（§4，最高优先级）：PTY + 内核 VT 状态机 + 环形缓冲 + 背压。
//!
//! 数据流（必须严格遵守）：
//! `PTY → PtyPump → ① screen.process ② scrollback.push ③ channel.send(前端)`
//!
//! 内核侧状态机是「AI 读屏 / 空闲判定 / 接管」的唯一数据来源（L2）。

pub mod keys;
pub mod pty;
pub mod scrollback;
pub mod transcoder;

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicBool, AtomicU16, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use crate::ipc_shim as tauri;
use crate::ipc_shim::SubscriberKey;
use serde::Serialize;
use tauri::ipc::Channel;
use tokio::sync::RwLock;

use crate::ids::{now_ms, TabId};
use scrollback::Scrollback;
use transcoder::{TerminalEncoding, Transcoder};

/// 默认环形缓冲容量（字节）。
pub const SCROLLBACK_BYTES: usize = 32 * 1024 * 1024;
/// vt100 网格滚动行数。
pub const SCROLLBACK_LINES: usize = 100_000;
/// 背压阈值（§4.4）：inflight 超过即暂停读 PTY。
pub const INFLIGHT_PAUSE: usize = 4 * 1024 * 1024;
pub const INFLIGHT_DROP: usize = 16 * 1024 * 1024;
/// 标签不可见时的推送间隔（毫秒）。
pub const HIDDEN_FLUSH_MS: u64 = 50;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalState {
    pub alt_screen: bool,
    pub cursor_visible: bool,
    pub bracketed_paste: bool,
    pub application_cursor: bool,
}

/// 本地 PTY 的读写句柄（writer 用于写，master 用于 resize）。
/// master 以 Mutex 包装：`dyn MasterPty` 只要求 Send，Mutex 提供跨线程共享。
pub struct LocalPtyIo {
    pub writer: Arc<Mutex<Box<dyn std::io::Write + Send>>>,
    pub master: Arc<Mutex<Box<dyn portable_pty::MasterPty + Send>>>,
}

/// 写入端抽象：本地 PTY 为阻塞 IO，SSH channel 为异步消息。
pub enum TerminalWriter {
    Local(Arc<LocalPtyIo>),
    Ssh(Arc<russh::ChannelWriteHalf<russh::client::Msg>>),
    /// WinRM 行模式：写入按「一行命令」走 exec 通道，由会话层包装。
    None,
}

impl TerminalWriter {
    pub async fn write(&self, data: &[u8]) -> crate::error::AppResult<()> {
        match self {
            Self::Local(io) => {
                let w = Arc::clone(&io.writer);
                let data = data.to_vec();
                tokio::task::spawn_blocking(move || {
                    let mut guard = w.lock().unwrap_or_else(|e| e.into_inner());
                    guard.write_all(&data).map_err(crate::error::AppError::Io)?;
                    guard.flush().map_err(crate::error::AppError::Io)
                })
                .await
                .map_err(|e| crate::error::AppError::Internal(format!("write join: {e}")))?
            }
            Self::Ssh(ch) => {
                ch.data_bytes(data.to_vec())
                    .await
                    .map_err(|e| crate::error::AppError::Ssh(format!("channel 写入失败: {e}")))?;
                Ok(())
            }
            Self::None => Err(crate::error::AppError::Unsupported(
                "该会话不支持直接写入".into(),
            )),
        }
    }
}

/// 泵回调（由会话层注入，解耦 AppHandle 便于单测）。
pub trait TabCallbacks: Send + Sync {
    fn exit(&self, tab_id: &str, exit_code: Option<i32>);
    fn throttled(&self, tab_id: &str, inflight: usize);
}

/// 永不回调的实现（测试用）。
pub struct NoopCallbacks;
impl TabCallbacks for NoopCallbacks {
    fn exit(&self, _tab_id: &str, _exit_code: Option<i32>) {}
    fn throttled(&self, _tab_id: &str, _inflight: usize) {}
}

/// 带结构的屏幕快照（§4.3）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScreenSnapshot {
    pub text: String,
    pub lines: Vec<String>,
    pub cursor_row: u16,
    pub cursor_col: u16,
    pub cols: u16,
    pub rows: u16,
    pub alt_screen: bool,
    pub last_output_ms_ago: u64,
}

/// 终端录制落盘状态。
struct Recording {
    file: tokio::fs::File,
    bytes: u64,
}

/// 一个前端订阅者。
///
/// ⚠️ `client` 与 map 的 key 是**两个不同的东西**，别合并成一个：
/// - **key** = 通道 id（`/ws/channel/{id}` 那条 WS）。一个标签页一条，页面关了就没。
/// - **`client`** = 客户端身份（一台设备上的一个浏览器实例），**跨刷新稳定**。
///   控制权认的是它 —— 否则刷新一次页面就"换了个人"，键盘控制权会在无操作的情况下
///   跑来跑去。
///
/// 一个客户端可以同时开多个标签页 ⇒ 多条通道同属一个 `client`。这就是为什么
/// 释放控制权时要检查「这个 client 还有没有别的订阅者」：一个客户端开两个标签页、
/// 关掉其中一个，不该把整台设备的输入权也一起丢掉。
struct Sink {
    client: String,
    channel: Channel<Vec<u8>>,
}

/// 一个终端标签页 = 一个 PTY + 一份状态机 + 一条到前端的通道（§4.2）。
pub struct TerminalTab {
    pub tab_id: TabId,
    pub session_id: String,
    cols: AtomicU16,
    rows: AtomicU16,
    /// 终端类型名。
    pub term: String,
    /// 内核侧状态机：AI 读屏的唯一数据来源。
    screen: Mutex<vt100::Parser>,
    transcoder: Mutex<Transcoder>,
    /// 原始字节环形缓冲（重放/导出用）。
    scrollback: Mutex<Scrollback>,
    /// 前端订阅者表（**多订阅，不是一个 sink**）。key = 通道 id，见 [`Sink`]。
    ///
    /// 没有订阅者时 `send_to_frontend` 什么都不做（返回 false），这正是
    /// 「浏览器关掉后」该有的行为：字节继续进 scrollback，不再往 hub 里堆。
    /// 改造前是单 sink 且一直指着那个已断开的通道 id，帧会全部落进
    /// `WsHub::pending`（上限 512 帧，超出丢最老的）—— 既漏内容又吃内存。
    sinks: RwLock<HashMap<String, Sink>>,
    /// 前端标签是否可见。
    visible: AtomicBool,
    /// 背压：已读出但尚未送达前端的字节数。
    inflight: Arc<AtomicUsize>,
    /// 写入端（前端按键 / AI 发送）。
    writer: RwLock<Arc<TerminalWriter>>,
    /// 最近一次输出时间（Unix 毫秒）。
    last_output_at: AtomicU64,
    /// 终端模式位（由 vt100 状态推导）。
    state: Mutex<TerminalState>,
    /// 终端录制（可选）。
    recorder: tokio::sync::Mutex<Option<Recording>>,
    /// 泵停止信号。
    pub stop: Arc<tokio_util::sync::CancellationToken>,
    /// 输入控制权持有者（订阅者标识，见 `SubscriberKey`）。
    ///
    /// **单点模式**：所有设备都能看，但同一时刻只有一个人能敲。`None` = 无人
    /// 持权 —— 这时**写入会被拒绝**（不是"谁先写谁拿"），因为用户明确要的是
    /// 「观看可以一起看，但只能有一个操作」。取权只有两条路：新接管者
    /// 走 `claim_if_free`（先到者自动持权），或任何人显式 `claim_controller` 夺权。
    controller: RwLock<Option<String>>,
    /// 泵已结束（shell 自己 exit、连接掉线、进程被杀）。
    ///
    /// 存在的意义是**接管时要能说实话**：`sessions.tabs` 里那条记录不会因为
    /// pump 结束而消失，所以光凭"查得到这个 tab"会让人以为它还活着，接管回来
    /// 却是一片死屏。有了这个标记，接管方能明确回「这个终端已经结束了」。
    exited: AtomicBool,
    /// 「短命资源」标记：没人看就可以回收的标签（目前只有日志跟随）。
    ///
    /// # 为什么需要这个标记
    ///
    /// 内核**分不清**「日志跟随（`docker logs -f`）」和「普通交互终端」——
    /// 两者的标签都由同一个 [`TerminalTab::new_arc`] 造出来，字段完全一样。
    /// 但服务端的「订阅者归零即回收」策略**只对日志跟随成立**：普通终端标签
    /// 绝不能因为浏览器关掉就被回收，用户的核心预期就是「关掉网页、换个设备
    /// 回来，终端还在、还在跑」。
    ///
    /// 所以回收策略必须由一个**显式标记**决定，且默认值是 `false`（不回收）：
    /// 只有明确知道自己是短命资源的调用点（`commands/docker.rs` 的
    /// `docker_logs_attach`）才主动 [`Self::mark_ephemeral`]，其余所有构造点
    /// 行为零变化。
    ephemeral: AtomicBool,
}

impl TerminalTab {
    pub fn new_arc(
        tab_id: TabId,
        session_id: String,
        cols: u16,
        rows: u16,
        encoding: TerminalEncoding,
    ) -> Arc<Self> {
        let parser = vt100::Parser::new(rows, cols, SCROLLBACK_LINES);
        Arc::new(Self {
            tab_id,
            session_id,
            cols: AtomicU16::new(cols),
            rows: AtomicU16::new(rows),
            term: "xterm-256color".into(),
            screen: Mutex::new(parser),
            transcoder: Mutex::new(Transcoder::new(encoding)),
            scrollback: Mutex::new(Scrollback::new(SCROLLBACK_BYTES)),
            sinks: RwLock::new(HashMap::new()),
            visible: AtomicBool::new(true),
            inflight: Arc::new(AtomicUsize::new(0)),
            writer: RwLock::new(Arc::new(TerminalWriter::None)),
            last_output_at: AtomicU64::new(now_ms()),
            state: Mutex::new(TerminalState {
                cursor_visible: true,
                ..Default::default()
            }),
            recorder: tokio::sync::Mutex::new(None),
            stop: Arc::new(tokio_util::sync::CancellationToken::new()),
            controller: RwLock::new(None),
            exited: AtomicBool::new(false),
            // 默认「不回收」——所有既有构造点（本地 shell、SSH、docker exec…）
            // 行为零变化；只有显式 mark_ephemeral() 的日志跟随才会被自动回收。
            ephemeral: AtomicBool::new(false),
        })
    }

    pub fn cols(&self) -> u16 {
        self.cols.load(Ordering::Relaxed)
    }

    pub fn rows(&self) -> u16 {
        self.rows.load(Ordering::Relaxed)
    }

    /// 设置写入端（PTY 打开后调用）。
    pub async fn set_writer(&self, writer: TerminalWriter) {
        *self.writer.write().await = Arc::new(writer);
    }

    pub async fn writer_arc(&self) -> Arc<TerminalWriter> {
        Arc::clone(&*self.writer.read().await)
    }

    /// 前端按键 / AI 发送（§4.5）。
    pub async fn write(&self, data: &[u8]) -> crate::error::AppResult<()> {
        self.writer_arc().await.write(data).await
    }

    /// attach 一个前端订阅者：先回放已有 scrollback 尾部，再持续推送。
    ///
    /// `client` 是**控制权身份**（见 [`Sink`]），与通道 id 分开传。
    ///
    /// 同一个通道 id 重复 attach（浏览器刷新、WS 断线重连沿用同一 id）会**顶掉**
    /// 自己那条旧记录 —— 否则刷新几次就会攒出几个已经死掉的通道，每次推送都要
    /// 白跑一遍。
    pub async fn attach_frontend(
        &self,
        channel: Channel<Vec<u8>>,
        replay_bytes: usize,
        client: &str,
    ) {
        let key = channel.subscriber_key();
        let backlog = {
            let sb = self.scrollback.lock().unwrap_or_else(|e| e.into_inner());
            sb.dump(replay_bytes)
        };
        if !backlog.is_empty() {
            let _ = channel.send(backlog);
        }
        self.sinks.write().await.insert(
            key,
            Sink {
                client: client.to_string(),
                channel,
            },
        );
    }

    /// 摘掉**一个**订阅者（服务端的通道 WS 断开时调）。
    ///
    /// 顺带做一件事：**如果走的人正是控制者，并且他没有别的订阅者了**，就释放
    /// 控制权。不做这一步的话，控制者关掉页面后控制权会永远卡在一个已经不在场的
    /// 人手里（`terminal_write` 一律报 `not_controller`，谁也敲不了）。
    pub async fn detach_subscriber(&self, key: &str) {
        let mut sinks = self.sinks.write().await;
        let Some(gone) = sinks.remove(key) else {
            return;
        };
        // 同一个客户端还开着别的标签页 ⇒ 控制权照旧归他。
        if sinks.values().any(|s| s.client == gone.client) {
            return;
        }
        let mut c = self.controller.write().await;
        if c.as_deref() == Some(gone.client.as_str()) {
            *c = None;
        }
    }

    /// 摘掉**某个客户端**在该标签上的全部订阅（`terminal_close_tab` 的 detach 分支用）。
    ///
    /// 与 [`Self::detach_subscriber`] 的分工是「摘谁」：
    /// - `detach_subscriber` 摘**一条通道** —— 服务端某条 WS 断开时，精确摘掉它；
    /// - `detach_client` 摘**一台设备的全部通道** —— 用户在一台设备上关掉这个标签，
    ///   语义是「我这一端不看了」，而不是「所有人都别看了」。后者（无差别清空）正是
    ///   本方法要修的缺陷：A、B 同看时 A 关标签，B 的订阅会被一起摘掉。
    ///
    /// 返回实际摘掉的通道条数（0 = 该客户端本来就没订阅这个标签）。
    ///
    /// 控制权只在「控制权本来就是被摘的这个 client 的」时释放 —— 别的人持权时一律
    /// 不动。⚠️ 这里不需要 `detach_subscriber` 里那种「同 client 还有没有别的通道」
    /// 的残留判断：本方法一次摘掉该 client 的**全部**通道，走到释放那一步时他在本
    /// 标签上必然已经没有任何 sink 了。真正要守住的不变量是「只释放属于自己的控制
    /// 权」。
    pub async fn detach_client(&self, client: &str) -> usize {
        let mut sinks = self.sinks.write().await;
        let before = sinks.len();
        sinks.retain(|_, s| s.client != client);
        let removed = before - sinks.len();
        if removed == 0 {
            return 0;
        }
        let mut c = self.controller.write().await;
        if c.as_deref() == Some(client) {
            *c = None;
        }
        removed
    }

    /// 清空所有订阅者（关标签时用），**同时释放控制权** —— 人都散了，留着控制者
    /// 没有意义。
    pub async fn detach_frontend(&self) {
        self.sinks.write().await.clear();
        *self.controller.write().await = None;
    }

    /// 当前订阅者数量（`/healthz`、界面上的「几个人在看」用）。
    ///
    /// ⚠️ 这是**通道数**，不是设备数 —— `sinks` 的键是通道 id，同一台设备开两个
    /// 页面/标签就是两条 sink。界面上「N 个设备正在观看」请用 [`Self::viewer_count`]。
    pub async fn subscriber_count(&self) -> usize {
        self.sinks.read().await.len()
    }

    /// 当前**观看设备数**（按 `client` 去重）——「N 个设备正在观看」用这个。
    ///
    /// # 为什么与 `subscriber_count` 并存（两个都要留着）
    ///
    /// 两者口径不同，各自服务不同的判据，任何一方都替代不了另一方：
    /// - `subscriber_count()` = **通道数**。它的语义是「还有没有字节管道连着我」，
    ///   被用作「后端是否还有人在看」的判据（`LiveTabInfo.subscribers`、
    ///   `terminal_list` 的「后台运行中」）。一台设备多开一个页面确实会让它 +1，
    ///   但那恰恰是真实的"多一条推送管道"。
    /// - `viewer_count()` = **设备数**（`client` 去重）。界面上的
    ///   「N 个设备正在观看」必须用它，否则同一台设备多开一个标签就会虚报成 2 台。
    ///
    /// 实测依据：同一个浏览器（同一 `localStorage.nexterm.client`）开两个页面看同一个
    /// 标签，`subscriber_count() == 2` 而 `viewer_count() == 1`。
    pub async fn viewer_count(&self) -> usize {
        self.sinks
            .read()
            .await
            .values()
            .map(|s| s.client.as_str())
            .collect::<HashSet<_>>()
            .len()
    }

    // ───────── 输入控制权（单点模式，§4.6）─────────

    /// 现在谁持控制权（订阅者标识）。
    pub async fn controller(&self) -> Option<String> {
        self.controller.read().await.clone()
    }

    /// 无人持权时自动取权；已有人持权则返回 false。
    ///
    /// 用在「新接管 / 新开标签」这一刻：先来的人自然成为操作者，后面进来的是观察者。
    pub async fn claim_if_free(&self, client: &str) -> bool {
        let mut c = self.controller.write().await;
        if c.is_none() {
            *c = Some(client.to_string());
            true
        } else {
            false
        }
    }

    /// 显式夺权（界面上的「接管控制」）。返回被顶掉的那个人。
    pub async fn claim_controller(&self, client: &str) -> Option<String> {
        let mut c = self.controller.write().await;
        let prev = c.clone();
        *c = Some(client.to_string());
        prev
    }

    /// 释放控制权。**只在自己持权时生效** —— 否则会变成"任何人一句 release
    /// 就能把别人的键盘抢掉"。返回是否真的释放了。
    pub async fn release_controller(&self, client: &str) -> bool {
        let mut c = self.controller.write().await;
        if c.as_deref() == Some(client) {
            *c = None;
            true
        } else {
            false
        }
    }

    /// 写入前的准入判定。`Err(holder)` = 别人正持着权，把持有者一并带回去。
    ///
    /// ⚠️ **无人持权时一律拒绝**，不做"谁先写谁拿"：用户的验收口径是
    /// 「观看可以一起看，但只能有一个操作」，而"谁先敲谁拿"会让两台设备抢键盘
    /// 变成无声的拉锯。取权必须走一次显式动作（`claim_if_free` 或 `claim_controller`）。
    pub async fn ensure_write_permission(&self, client: &str) -> Result<(), String> {
        match self.controller.read().await.as_deref() {
            Some(cur) if cur == client => Ok(()),
            Some(cur) => Err(cur.to_string()),
            None => Err(String::new()),
        }
    }

    /// 标记这个标签的进程已结束（泵自然退出时 `AppCallbacks::exit` 里调；
    /// `close_tab` / `reap` 主动杀进程时也会调，否则等泵回调时 tab 已被摘掉、
    /// 那一步就补不上了）。
    pub fn mark_exited(&self) {
        self.exited.store(true, Ordering::Relaxed);
    }

    /// 把这个标签标记为**重新存活**，与 [`Self::mark_exited`] 对称。
    ///
    /// # 语义
    ///
    /// 清除 `exited` 标记，让「接管 / 控制权推送」重新把这个标签报成「可操作」。
    ///
    /// # 什么时候才允许调用
    ///
    /// **仅当这个标签确实拿到了新的 PTY / 新会话之后** —— 目前唯一的调用点是
    /// `session::reconnect::reopen_pty_for_tab`，它在底层传输已重建、
    /// 新 PTY 已 `open_pty` 成功（或 WinRM 会话已重连）之后才调。
    ///
    /// ⚠️ 绝不能因为「重连成功」就无条件复活：那会把**真正结束**的进程（用户
    /// 敲了 `exit`、`close_tab` / `reap` 主动杀掉的标签）也一并唤醒。判据是
    /// 「这个标签有没有拿到新东西」，而不是「会话有没有重连」—— 前者由调用点
    /// 用 `open_pty` 的返回值把关。用户敲 `exit` 时连接仍活着，`pump_exit_action`
    /// 判为 `Ignore`，根本不会走重连，因此不会误复活（见 reconnect.rs 的单测）。
    pub fn mark_live(&self) {
        self.exited.store(false, Ordering::Relaxed);
    }

    /// 这个标签的进程还在跑吗。
    pub fn has_exited(&self) -> bool {
        self.exited.load(Ordering::Relaxed)
    }

    pub fn set_visible(&self, visible: bool) {
        self.visible.store(visible, Ordering::Relaxed);
    }

    pub fn is_visible(&self) -> bool {
        self.visible.load(Ordering::Relaxed)
    }

    /// 把这个标签标记为「短命资源」：订阅者归零后服务端可以回收它。
    ///
    /// # 为什么必须显式标记（而不是按 `kind` 或命令名猜）
    ///
    /// 内核分不清「日志跟随」和「普通交互终端」（见字段文档）。回收普通终端
    /// 会直接违背用户核心预期「关掉网页，终端还在跑」，所以这个决定权只交给
    /// **明确知道自己是短命资源的调用点**：目前只有 `commands/docker.rs` 的
    /// `docker_logs_attach`（`docker logs -f`）会调用它。
    ///
    /// ⚠️ **不要**给 `docker_exec_attach`（`docker exec -it`）打这个标记：
    /// 那是真交互终端，前端按普通终端标签打开，必须能跨网页关闭存活。
    pub fn mark_ephemeral(&self) {
        self.ephemeral.store(true, Ordering::Relaxed);
    }

    /// 是否是可回收的短命资源（见字段文档）。默认 `false`。
    pub fn is_ephemeral(&self) -> bool {
        self.ephemeral.load(Ordering::Relaxed)
    }

    /// 调整尺寸：内核状态机 + 写入端同步（§4.2 / §5.2）。
    pub async fn resize(&self, cols: u16, rows: u16) -> crate::error::AppResult<()> {
        if cols == 0 || rows == 0 || cols > 1024 || rows > 1024 {
            return Err(crate::error::AppError::param("非法终端尺寸"));
        }
        self.screen
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .screen_mut()
            .set_size(rows, cols);
        self.cols.store(cols, Ordering::Relaxed);
        self.rows.store(rows, Ordering::Relaxed);
        match &*self.writer_arc().await {
            TerminalWriter::Local(io) => {
                let master = io.master.lock().unwrap_or_else(|e| e.into_inner());
                let _ = master.resize(portable_pty::PtySize {
                    rows,
                    cols,
                    pixel_width: 0,
                    pixel_height: 0,
                });
            }
            TerminalWriter::Ssh(ch) => {
                let _ = ch
                    .window_change(u32::from(cols), u32::from(rows), 0, 0)
                    .await;
            }
            TerminalWriter::None => {}
        }
        Ok(())
    }

    /// 向引擎喂输出（泵调用；也用于重连横幅、WinRM 行模式注入）。
    /// 返回值：检测到 DSR(ESC[6n) 请求时应答的光标报告字节（写入 PTY）。
    /// ConPTY 会阻塞等待该应答，不回应则整个 PTY 输出停摆。
    pub fn feed_output(&self, raw: &[u8]) -> Option<Vec<u8>> {
        self.last_output_at.store(now_ms(), Ordering::Relaxed);
        self.scrollback
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(raw);
        let utf8 = {
            let mut tr = self.transcoder.lock().unwrap_or_else(|e| e.into_inner());
            tr.feed(raw)
        };
        let (alt, cursor_visible, app_cursor, bracketed) = {
            let mut screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            screen.process(&utf8);
            let s = screen.screen();
            (
                s.alternate_screen(),
                !s.hide_cursor(),
                s.application_cursor(),
                self.transcoder
                    .lock()
                    .unwrap_or_else(|e| e.into_inner())
                    .bracketed_paste(),
            )
        };
        *self.state.lock().unwrap_or_else(|e| e.into_inner()) = TerminalState {
            alt_screen: alt,
            cursor_visible,
            bracketed_paste: bracketed,
            application_cursor: app_cursor,
        };

        // DSR 应答：以内核状态机的真实光标位置回应（真实终端行为）
        if utf8.windows(4).any(|w| w == b"[6n") {
            let screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            let (row, col) = screen.screen().cursor_position();
            return Some(
                format!("[{};{}R", row.saturating_add(1), col.saturating_add(1)).into_bytes(),
            );
        }
        None
    }

    /// 运行中切换编码（§12.2）。
    pub fn switch_encoding(&self, encoding: TerminalEncoding) {
        self.transcoder
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .switch(encoding);
    }

    pub fn encoding(&self) -> TerminalEncoding {
        self.transcoder
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .encoding()
    }

    /// 录制开关。
    pub async fn start_recording(&self, path: std::path::PathBuf) -> crate::error::AppResult<()> {
        let file = tokio::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&path)
            .await?;
        *self.recorder.lock().await = Some(Recording { file, bytes: 0 });
        Ok(())
    }

    pub async fn stop_recording(&self) -> u64 {
        use tokio::io::AsyncWriteExt;
        let mut rec = self.recorder.lock().await;
        match rec.take() {
            Some(mut r) => {
                let _ = r.file.flush().await;
                r.bytes
            }
            None => 0,
        }
    }

    pub(crate) async fn record(&self, data: &[u8]) {
        let mut guard = self.recorder.lock().await;
        if let Some(rec) = guard.as_mut() {
            use tokio::io::AsyncWriteExt;
            if rec.file.write_all(data).await.is_ok() {
                rec.bytes += data.len() as u64;
            }
        }
    }

    pub async fn recording_bytes(&self) -> u64 {
        self.recorder
            .lock()
            .await
            .as_ref()
            .map(|r| r.bytes)
            .unwrap_or(0)
    }

    // ───────── 读屏 API（§4.3）：AI 接管的唯一入口 ─────────

    /// 屏幕当前可见内容（纯文本，无 ANSI 残留）。
    pub fn screen_text(&self) -> String {
        self.screen
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .screen()
            .contents()
    }

    pub fn snapshot(&self) -> ScreenSnapshot {
        let (text, rows, cols, cursor_row, cursor_col, alt) = {
            let screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            let s = screen.screen();
            let (rows, cols) = s.size();
            let (cursor_row, cursor_col) = s.cursor_position();
            (
                s.contents(),
                rows,
                cols,
                cursor_row,
                cursor_col,
                s.alternate_screen(),
            )
        };
        ScreenSnapshot {
            lines: text.lines().map(|l| l.trim_end().to_string()).collect(),
            cursor_row,
            cursor_col,
            cols,
            rows,
            alt_screen: alt,
            last_output_ms_ago: now_ms()
                .saturating_sub(self.last_output_at.load(Ordering::Relaxed)),
            text,
        }
    }

    /// 空闲判定：最近 quiet_ms 内无输出。
    pub fn is_idle(&self, quiet_ms: u64) -> bool {
        now_ms().saturating_sub(self.last_output_at.load(Ordering::Relaxed)) >= quiet_ms
    }

    /// 距最近一次输出过了多久（后台会话面板用：「3 分钟前还有输出」）。
    pub fn last_output_ms_ago(&self) -> u64 {
        now_ms().saturating_sub(self.last_output_at.load(Ordering::Relaxed))
    }

    /// 最后 N 行（含滚动缓冲）。
    pub fn tail_lines(&self, n: usize) -> Vec<String> {
        let mut all: Vec<String> = Vec::new();
        {
            let sb = self.scrollback.lock().unwrap_or_else(|e| e.into_inner());
            let bytes = sb.dump(256 * 1024);
            let text = String::from_utf8_lossy(&bytes);
            all.extend(
                text.split('\n')
                    .map(|l| l.trim_end_matches('\r').to_string()),
            );
        }
        all.extend(self.snapshot().lines);
        if all.len() > n {
            all.split_off(all.len() - n)
        } else {
            all
        }
    }

    pub fn dump(&self, max_bytes: usize) -> Vec<u8> {
        self.scrollback
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .dump(max_bytes)
    }

    pub fn inflight(&self) -> usize {
        self.inflight.load(Ordering::Relaxed)
    }

    pub fn inflight_arc(&self) -> Arc<AtomicUsize> {
        Arc::clone(&self.inflight)
    }

    /// 把字节推给**所有**订阅者。
    ///
    /// 返回「是否至少有一个人收到」。**没有订阅者时返回 false 且什么都不做** ——
    /// 这是浏览器关掉后的正常状态（字节照旧进 scrollback，等下次 attach 回放），
    /// 不是错误。
    ///
    /// 发送失败的订阅者会被就地摘掉：`Channel::send` 失败意味着那条 WS 已经没了
    /// （关页面 / 断网），留着他只会让之后每一帧都白跑一遍。
    pub async fn send_to_frontend(&self, data: Vec<u8>) -> bool {
        let mut sinks = self.sinks.write().await;
        if sinks.is_empty() {
            return false;
        }
        // 常见路径是「只有一个订阅者」（桌面固定一个；服务端多数时候也只有一个），
        // 这条分支直接 move，省掉整块 PTY 字节的拷贝。
        if sinks.len() == 1 {
            let key = sinks.keys().next().cloned().unwrap_or_default();
            if let Some(s) = sinks.get(&key) {
                if s.channel.send(data).is_ok() {
                    return true;
                }
            }
            sinks.remove(&key);
            return false;
        }
        let mut delivered = false;
        sinks.retain(|_, s| {
            let ok = s.channel.send(data.clone()).is_ok();
            delivered |= ok;
            ok
        });
        delivered
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tab() -> Arc<TerminalTab> {
        TerminalTab::new_arc("t1".into(), "s1".into(), 80, 24, TerminalEncoding::Utf8)
    }

    #[test]
    fn new_tab_is_not_ephemeral_by_default() {
        // 默认「不回收」是关键不变量：普通交互终端绝不能被「订阅者归零」回收，
        // 否则用户关掉网页后想保留的终端会被一起杀掉。
        let t = tab();
        assert!(!t.is_ephemeral());
    }

    #[test]
    fn mark_ephemeral_flips_flag() {
        // 只有显式标记的短命资源（docker logs -f）才允许订阅归零即回收。
        let t = tab();
        t.mark_ephemeral();
        assert!(t.is_ephemeral());
    }

    #[test]
    fn screen_text_has_no_ansi_residue() {
        let t = tab();
        t.feed_output(b"\x1b[2J\x1b[H");
        t.feed_output(b"hello \x1b[31;1mworld\x1b[0m\r\n");
        t.feed_output(b"$ \x1b[?25lhidden");
        let text = t.screen_text();
        assert!(text.contains("hello world"), "文本: {text:?}");
        assert!(text.contains("$"), "提示符: {text:?}");
        assert!(!text.contains("\x1b["), "残留 ANSI: {text:?}");
    }

    #[test]
    fn cursor_position_reported() {
        let t = tab();
        t.feed_output(b"abc");
        let snap = t.snapshot();
        assert_eq!(snap.rows, 24);
        assert_eq!(snap.cols, 80);
        assert_eq!(snap.cursor_row, 0);
        assert_eq!(snap.cursor_col, 3);
    }

    #[test]
    fn alt_screen_detection() {
        let t = tab();
        t.feed_output(b"\x1b[?1049h"); // 进入备用屏幕（vim 类）
        assert!(t.snapshot().alt_screen);
        t.feed_output(b"\x1b[?1049l");
        assert!(!t.snapshot().alt_screen);
    }

    #[test]
    fn idle_detection() {
        let t = tab();
        t.feed_output(b"boom\r\n");
        std::thread::sleep(std::time::Duration::from_millis(5));
        assert!(!t.is_idle(5_000));
        assert!(t.is_idle(1));
    }

    #[test]
    fn tail_lines_with_scrollback() {
        let t = tab();
        for i in 0..100 {
            t.feed_output(format!("line-{i}\r\n").as_bytes());
        }
        let tail = t.tail_lines(10);
        assert_eq!(tail.len(), 10);
        assert!(tail.iter().any(|l| l.contains("line-99")));
    }

    #[test]
    fn high_volume_feed_memory_bounded() {
        // 喂 100MB 伪随机字节：滚动缓冲按容量截断，vt100 网格有界 → 内存有界
        let t = tab();
        let chunk: Vec<u8> = (0..65_536u32).map(|i| (i % 251) as u8 + 1).collect();
        for _ in 0..1600 {
            t.feed_output(&chunk);
        }
        assert!(t.dump(1024).len() <= 1024);
        let _ = t.screen_text();
    }

    #[test]
    fn resize_updates_state_machine() {
        let t = tab();
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            t.resize(120, 40).await.unwrap();
        });
        t.feed_output(b"x");
        let snap = t.snapshot();
        assert_eq!(snap.cols, 120);
        assert_eq!(snap.rows, 40);
    }

    // ───────── 多端同看：输入控制权（单点模式）─────────

    fn rt() -> tokio::runtime::Runtime {
        tokio::runtime::Runtime::new().unwrap()
    }

    /// 先到者持权，后来者是观察者。
    #[test]
    fn first_subscriber_takes_control() {
        let t = tab();
        rt().block_on(async {
            assert!(t.claim_if_free("device-a").await);
            assert!(!t.claim_if_free("device-b").await, "已有持有者时不该被抢");
            assert_eq!(t.controller().await.as_deref(), Some("device-a"));
        });
    }

    /// 无人持权时**谁都写不了**。
    ///
    /// 这条是刻意的：若改成"谁先写谁拿"，两台设备抢键盘就成了无声拉锯 ——
    /// 用户在 A 上打字、字却进了 B 的窗口，而他没有任何线索知道为什么。
    /// 取权必须是一次显式动作。
    #[test]
    fn nobody_writes_without_a_controller() {
        let t = tab();
        rt().block_on(async {
            assert!(t.controller().await.is_none());
            assert!(t.ensure_write_permission("device-a").await.is_err());
        });
    }

    /// 显式接管：控制权易主，原持有者随即失去写权限。
    #[test]
    fn claim_steals_control_from_previous_holder() {
        let t = tab();
        rt().block_on(async {
            t.claim_if_free("device-a").await;
            assert_eq!(
                t.claim_controller("device-b").await.as_deref(),
                Some("device-a"),
                "应把被顶掉的那个人报回来"
            );
            assert!(t.ensure_write_permission("device-a").await.is_err());
            assert!(t.ensure_write_permission("device-b").await.is_ok());
        });
    }

    /// 释放只能由持有者发起 —— 否则任何人都能一句话把别人的键盘抢掉。
    #[test]
    fn release_only_works_for_the_holder() {
        let t = tab();
        rt().block_on(async {
            t.claim_if_free("device-a").await;
            assert!(!t.release_controller("device-b").await, "非持有者不能释放");
            assert_eq!(t.controller().await.as_deref(), Some("device-a"));
            assert!(t.release_controller("device-a").await);
            assert!(t.controller().await.is_none());
        });
    }

    /// 服务端专属：两条通道同看一个标签，**两边都收到**同一份输出。
    ///
    /// 只能在不带 `desktop` feature 时跑 —— 桌面门面把 Channel 的订阅者身份固定
    /// 成同一个视图，构造不出"两条不同的通道"（Tauri 的 Channel 也没法凭空造）。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn two_subscribers_receive_the_same_output() {
        use crate::ipc_shim::{Channel, Hub};

        /// 把发出去的每一帧记下来，好断言"两个通道各收到一份"。
        #[derive(Default)]
        struct RecHub {
            sent: std::sync::Mutex<Vec<(String, Vec<u8>)>>,
        }
        impl Hub for RecHub {
            fn broadcast_event(&self, _event: &str, _payload: serde_json::Value) {}
            fn send_bytes(&self, channel_id: &str, data: Vec<u8>) {
                self.sent
                    .lock()
                    .unwrap()
                    .push((channel_id.to_string(), data));
            }
            fn send_json(&self, _channel_id: &str, _payload: serde_json::Value) {}
        }

        let hub = std::sync::Arc::new(RecHub::default());
        let t = tab();
        rt().block_on(async {
            let as_hub = || std::sync::Arc::clone(&hub) as std::sync::Arc<dyn Hub>;
            t.attach_frontend(Channel::new(as_hub(), "ws-a"), 0, "device-a")
                .await;
            t.attach_frontend(Channel::new(as_hub(), "ws-b"), 0, "device-b")
                .await;
            assert_eq!(t.subscriber_count().await, 2);

            t.feed_output(b"job is still running\r\n");
            assert!(
                t.send_to_frontend(b"job is still running\r\n".to_vec())
                    .await
            );

            let sent = hub.sent.lock().unwrap();
            let a = sent
                .iter()
                .find(|(id, _)| id == "ws-a")
                .map(|(_, d)| d.clone());
            let b = sent
                .iter()
                .find(|(id, _)| id == "ws-b")
                .map(|(_, d)| d.clone());
            assert_eq!(
                a.as_deref(),
                Some(&b"job is still running\r\n"[..]),
                "A 没收到"
            );
            assert_eq!(b, a, "两端拿到的必须是同一份字节");
        });
    }

    /// 同一台设备开两个标签页、关掉其中一个 —— **不该把整台设备的输入权也丢掉**。
    ///
    /// 这是 `detach_subscriber` 里那段"还有没有同 client 的其它订阅者"判断的
    /// 存在理由。少了它，用户在第二个标签页里关掉窗口，另一台设备就能把他的键盘
    /// 抢走，而他还开着页面。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn detach_keeps_control_while_client_has_other_channels() {
        use crate::ipc_shim::{Channel, Hub, NullHub};

        let hub = std::sync::Arc::new(NullHub) as std::sync::Arc<dyn Hub>;
        let t = tab();
        rt().block_on(async {
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-1"),
                0,
                "device-a",
            )
            .await;
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-2"),
                0,
                "device-a",
            )
            .await;
            t.claim_if_free("device-a").await;

            t.detach_subscriber("ws-1").await;
            assert_eq!(
                t.controller().await.as_deref(),
                Some("device-a"),
                "同一 client 还有一条通道，控制权不该丢"
            );

            t.detach_subscriber("ws-2").await;
            assert!(
                t.controller().await.is_none(),
                "人都走了，控制权必须释放 —— 否则控制权永远卡在一个不在场的人手里"
            );
        });
    }

    /// 摘一个客户端**只会**摘掉他的订阅，另一个客户端的订阅必须原样保留。
    ///
    /// 这是 `terminal_close_tab` 那个缺陷的回归测试：以前 detach 分支走的是
    /// `detach_frontend`（清空全部），A 关标签会把 B 的推送一起掐掉，而 B 那边
    /// 看起来只是"画面不动了"，极难排查。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn detach_client_only_removes_that_client() {
        use crate::ipc_shim::{Channel, Hub, NullHub};

        let hub = std::sync::Arc::new(NullHub) as std::sync::Arc<dyn Hub>;
        let t = tab();
        rt().block_on(async {
            // A 开两个标签页（两条通道），B 开一个。
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-a1"),
                0,
                "device-a",
            )
            .await;
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-a2"),
                0,
                "device-a",
            )
            .await;
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-b1"),
                0,
                "device-b",
            )
            .await;
            assert_eq!(t.subscriber_count().await, 3);

            assert_eq!(t.detach_client("device-a").await, 2, "应摘掉 A 的两条通道");
            assert_eq!(
                t.subscriber_count().await,
                1,
                "B 的订阅不该被一起摘掉（只该剩 B 这一条）"
            );

            // B 仍然收得到输出 —— 证明留下的那条是真订阅，不是残留计数。
            // 直接摘 B 后归零，作为"剩下那条确实是 B"的补充证据。
            assert_eq!(t.detach_client("device-b").await, 1);
            assert_eq!(t.subscriber_count().await, 0);
        });
    }

    /// `viewer_count` 按 `client` 去重：**通道数 ≠ 设备数**。
    ///
    /// 场景来自实测：同一个浏览器开两个页面看同一个标签，`subscribers` 报 2，
    /// 而那只应当是**一台**设备（两个页面共享同一个 `clientId`）。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn viewer_count_dedups_clients() {
        use crate::ipc_shim::{Channel, Hub, NullHub};

        let hub = std::sync::Arc::new(NullHub) as std::sync::Arc<dyn Hub>;
        let t = tab();
        rt().block_on(async {
            // A 开两个页面（两条通道），B 开一个：共 3 条 sink、2 台设备。
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-a1"),
                0,
                "device-a",
            )
            .await;
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-a2"),
                0,
                "device-a",
            )
            .await;
            t.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-b1"),
                0,
                "device-b",
            )
            .await;
            assert_eq!(t.subscriber_count().await, 3, "通道口径 = 3");
            assert_eq!(
                t.viewer_count().await,
                2,
                "设备口径 = 2（A 的两条通道去重）"
            );

            // 摘掉 A 的全部通道后只剩 B 一台设备。
            assert_eq!(t.detach_client("device-a").await, 2);
            assert_eq!(t.subscriber_count().await, 1);
            assert_eq!(t.viewer_count().await, 1, "A 走了，设备数该降到 1");

            // 全摘掉：两个口径都归零。
            assert_eq!(t.detach_client("device-b").await, 1);
            assert_eq!(t.subscriber_count().await, 0);
            assert_eq!(t.viewer_count().await, 0);
        });
    }

    /// `detach_client` 释放控制权是有条件的：
    /// ① 走的正是控制者，且他在本标签上的通道被全部摘掉 ⇒ 释放；
    /// ② 走的**不是**控制者 ⇒ 控制者的控制权不能被顺手释放。
    ///
    /// ⚠️ 与 `detach_subscriber` 不同，`detach_client` 一次摘掉该 client 的**全部**
    /// 通道，所以「A 持权、本标签还留着第二条 A 的通道」这个情形对本方法不成立 ——
    /// 那条通道也会被一起摘掉。真正要守的不变量是「只释放属于自己的控制权」，
    /// 断言 ② 用「摘另一台设备」来表达它。同 client 残留通道那个微妙点由
    /// `detach_keeps_control_while_client_has_other_channels` 单独覆盖。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn detach_client_releases_control_only_when_it_was_the_controller_and_has_no_other_sink() {
        use crate::ipc_shim::{Channel, Hub, NullHub};

        let hub = std::sync::Arc::new(NullHub) as std::sync::Arc<dyn Hub>;

        // ① 控制者只有一条通道：摘掉 = 他在这个标签上彻底走了 ⇒ 必须释放，
        //    否则控制权永远卡在一个不在场的人手里（谁也敲不了）。
        let t1 = tab();
        rt().block_on(async {
            t1.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-1"),
                0,
                "device-a",
            )
            .await;
            t1.claim_if_free("device-a").await;
            assert_eq!(t1.detach_client("device-a").await, 1);
            assert!(
                t1.controller().await.is_none(),
                "控制者在本标签已无任何订阅，控制权必须释放"
            );
        });

        // ② 控制者是 A，但摘的是另一台设备 B ⇒ A 的控制权不受影响。
        let t2 = tab();
        rt().block_on(async {
            t2.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-a"),
                0,
                "device-a",
            )
            .await;
            t2.attach_frontend(
                Channel::new(std::sync::Arc::clone(&hub), "ws-b"),
                0,
                "device-b",
            )
            .await;
            t2.claim_if_free("device-a").await;
            assert_eq!(t2.detach_client("device-b").await, 1);
            assert_eq!(
                t2.controller().await.as_deref(),
                Some("device-a"),
                "摘的不是控制者，控制权不该被动"
            );
            assert_eq!(t2.subscriber_count().await, 1);
        });
    }
}
