//! WebSocket 总线：结构化事件广播 + 通道字节流。
//!
//! # 两条 WS 的区别
//!
//! | 路径 | 对应桌面版 | 载荷 |
//! |---|---|---|
//! | `/ws/events` | `listen()`（`tauri::Emitter`） | JSON 文本 `{event, payload}` |
//! | `/ws/channel/{id}` | `ipc::Channel<T>` | PTY 是**二进制帧**，AI 事件是 JSON 文本 |
//!
//! 分开两条而不是复用一条：PTY 是**不能走 JSON** 的（字节转义 + 膨胀，
//! 终端吞吐撑不住），而事件是结构化数据。混在一条连接里就得加一层封装，
//! 那层封装正是 Tauri 的 IPC 在桌面侧做的事 —— 服务端没必要复刻它，
//! 直接让浏览器的 WS 帧类型承担这个区分更省。
//!
//! # 通道 id 由前端生成
//!
//! 桌面下 Channel 的 id 是 Tauri 内部发的；服务端下由前端生成一个 id、
//! 先开 `/ws/channel/{id}`，再把 id 当普通参数传给命令（如 `terminal_attach`）。
//!
//! # 未认领的帧要缓存（否则会丢一屏）
//!
//! 前端是「先建通道对象 → 再调命令」，这中间那条 WS 可能还在握手。
//! [`WsHub::register_channel`] 之前到达的帧一律进 [`WsHub::pending`]，
//! 连上后一次性补发。没有这层缓存，`terminal_attach` 开头那批回滚内容会
//! **静默消失** —— 用户看到一片空屏，而日志里什么都没发生。
//!
//! 断线重连沿用同一个 id，同样靠这层缓存补齐断线期间的输出。
//!
//! # 背压
//!
//! 通道队列用 unbounded（不丢帧）—— 丢 PTY 字节会让屏幕永久花掉，比慢更糟。
//! 与桌面版 Tauri Channel 的语义一致。代价是：若浏览器标签卡住不读，
//! 队列会一直涨。单用户自托管场景下可接受，但这是**已知限制**，不是设计目标。
//! `pending` 有硬上限（[`PENDING_MAX_FRAMES`]），因为那种队列没有任何消费者兜底。

use std::collections::{HashMap, VecDeque};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use axum::extract::ws::{Message, WebSocket, WebSocketUpgrade};
use axum::extract::{Path, State as AxumState};
use axum::response::Response;
use futures::stream::{SplitSink, SplitStream};
use futures::{SinkExt, StreamExt};
use serde_json::Value;
use tokio::sync::mpsc::{self, UnboundedReceiver, UnboundedSender};

use crate::ipc_shim::Hub;
use crate::server::ServerCtx;

/// 未认领通道最多缓存多少帧。
///
/// 512 帧足够装下 `terminal_attach` 的回滚内容（PTY 泵是按块发的，
/// 一屏通常几帧到几十帧）。上限存在的意义是：一个**永远**不会被认领的
/// 通道 id 不能把内存吃光。
const PENDING_MAX_FRAMES: usize = 512;

/// 丢进 WS 的一帧。
pub enum Frame {
    Bytes(Vec<u8>),
    Text(String),
}

impl Frame {
    fn into_message(self) -> Message {
        match self {
            Frame::Bytes(b) => Message::Binary(b.into()),
            Frame::Text(t) => Message::Text(t.into()),
        }
    }
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    // 别的线程 panic 过不该把整个服务端拖死；中毒后继续用这份数据是安全的
    // （里面只是「谁在连」这类可重建的表）。
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// 通道断开回调：hub 只负责「告诉装配方某条通道断了」，不关心断开后干什么。
///
/// 抽成别名既是可读性，也让 `clippy::type_complexity` 闭嘴；`Option` 包一层后
/// 仍满足 `Default`（`#[derive(Default)]` 不受影响）。
type ChannelClosedHook = Arc<dyn Fn(&str) + Send + Sync>;

/// 连接表。
#[derive(Default)]
pub struct WsHub {
    next_event_id: AtomicU64,
    events: Mutex<HashMap<u64, UnboundedSender<Frame>>>,
    channels: Mutex<HashMap<String, UnboundedSender<Frame>>>,
    /// 还没有 WS 认领的帧（见模块文档）。
    pending: Mutex<HashMap<String, VecDeque<Frame>>>,
    /// 通道 WS 断开时的回调。
    ///
    /// # 为什么必须有这个（不能省）
    ///
    /// 终端标签现在持有**订阅者表**（`TerminalTab::sinks`），键就是通道 id。
    /// 浏览器关掉页面时那条 `/ws/channel/{id}` 会断，但**没人会告诉终端标签** ——
    /// 于是表里留下一个已经死掉的 `Channel`，之后每一帧 PTY 输出都要白跑一遍。
    ///
    /// 这里**不能指望 `Channel::send` 失败来自动剔除**：服务端门面的
    /// `Channel::send` 永远返回 `Ok(())`（它只是往 hub 投一帧，投递结果由 hub
    /// 自己消化）。实测口径是「`WsHub::deliver` 发现发送失败会摘掉通道并**直接
    /// return，不落 pending**」—— 也就是说断连这件事**只在这里知道**。
    ///
    /// 用回调而不是让 hub 直接持有 `AppState`：本模块的边界是「连接表 + 通道路由」，
    /// 一旦让它认识会话与标签，hub 就得跟着内核的每次重构走。回调把「断开之后
    /// 该干什么」留给装配方（`server::serve`）。
    on_channel_closed: Mutex<Option<ChannelClosedHook>>,
}

impl WsHub {
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    fn subscribe_events(&self) -> (u64, UnboundedReceiver<Frame>) {
        let id = self.next_event_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = mpsc::unbounded_channel();
        lock(&self.events).insert(id, tx);
        (id, rx)
    }

    fn unsubscribe_events(&self, id: u64) {
        lock(&self.events).remove(&id);
    }

    fn register_channel(&self, id: String, tx: UnboundedSender<Frame>) {
        // 顺序：先把老的补掉，再上表；上表之后再扫一次，堵住这两步之间
        // 新落到 pending 的帧（否则它们要等下一次认领，而可能永远不来了）。
        let first = lock(&self.pending).remove(&id);
        // 同 id 重连（浏览器刷新 / 网络抖动后沿用同一 id）：旧的那条直接顶掉。
        lock(&self.channels).insert(id.clone(), tx.clone());
        let mut restored = 0usize;
        for queue in [first, lock(&self.pending).remove(&id)]
            .into_iter()
            .flatten()
        {
            restored += queue.len();
            for frame in queue {
                if tx.send(frame).is_err() {
                    break;
                }
            }
        }
        if restored > 0 {
            tracing::debug!(target: "server", channel = %id, frames = restored, "补齐未认领期间缓存的帧");
        }
    }

    fn unregister_channel(&self, id: &str) {
        // 只摘表，**不清 pending**：重连时还要靠它补上断线期间的输出。
        lock(&self.channels).remove(id);
    }

    /// 投递一帧：有连接就直发，没有就先缓存。
    fn deliver(&self, channel_id: &str, frame: Frame) {
        {
            let mut guard = lock(&self.channels);
            if let Some(tx) = guard.get(channel_id) {
                if tx.send(frame).is_err() {
                    guard.remove(channel_id);
                }
                return;
            }
        }
        let mut pend = lock(&self.pending);
        let queue = pend.entry(channel_id.to_string()).or_default();
        if queue.len() >= PENDING_MAX_FRAMES {
            // 满了说明这个 id 长期没人认领。丢最老的、留最新的：
            // 屏幕可以往上翻，而「最后几屏」才是用户当下要看的。
            queue.pop_front();
        }
        queue.push_back(frame);
    }

    /// 当前事件订阅者数量（`/healthz` 用）。
    pub fn event_subscribers(&self) -> usize {
        lock(&self.events).len()
    }

    /// 当前活跃通道数量（`/healthz` 用）。
    pub fn live_channels(&self) -> usize {
        lock(&self.channels).len()
    }

    /// 注册「通道 WS 断开」回调（装配方在 `serve` 里调一次）。
    pub fn set_on_channel_closed(&self, cb: ChannelClosedHook) {
        *lock(&self.on_channel_closed) = Some(cb);
    }

    /// 通知断开。回调里**只能做立即返回的事**（取锁、`tokio::spawn`），
    /// 不能 `.await` —— 它在关闭路径上，被它拖住就等于连接关不掉。
    fn notify_channel_closed(&self, id: &str) {
        let cb = lock(&self.on_channel_closed).clone();
        if let Some(cb) = cb {
            cb(id);
        }
    }

    /// 还没被认领的通道数（`/healthz` 用；长期 > 0 说明前端没把 WS 连上）。
    pub fn pending_channels(&self) -> usize {
        lock(&self.pending).len()
    }
}

impl Hub for WsHub {
    fn broadcast_event(&self, event: &str, payload: Value) {
        let text = serde_json::json!({ "event": event, "payload": payload }).to_string();
        // `retain` 顺手清掉已经关掉的连接 —— 否则断线记录会一直堆积。
        lock(&self.events).retain(|_, tx| tx.send(Frame::Text(text.clone())).is_ok());
    }

    fn send_bytes(&self, channel_id: &str, data: Vec<u8>) {
        self.deliver(channel_id, Frame::Bytes(data));
    }

    fn send_json(&self, channel_id: &str, payload: Value) {
        self.deliver(channel_id, Frame::Text(payload.to_string()));
    }
}

// ── axum 入口 ────────────────────────────────────────────────────────
//
// 两个处理器都从 `ServerCtx` 里取 hub（而不是直接以 `Arc<WsHub>` 作 state）：
// Router 只有一个 state 类型，混用两种就得 `.with_state` 两次或包一层。

/// `GET /ws/events`
pub async fn ws_events(
    ws: WebSocketUpgrade,
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
) -> Response {
    let hub = Arc::clone(&ctx.hub);
    ws.on_upgrade(move |socket| pump_events(socket, hub))
}

/// `GET /ws/channel/{id}`
pub async fn ws_channel(
    ws: WebSocketUpgrade,
    Path(channel_id): Path<String>,
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
) -> Response {
    let hub = Arc::clone(&ctx.hub);
    ws.on_upgrade(move |socket| pump_channel(socket, hub, channel_id))
}

async fn pump_events(socket: WebSocket, hub: Arc<WsHub>) {
    let (id, rx) = hub.subscribe_events();
    let (sink, stream) = socket.split();
    pump(sink, stream, rx).await;
    hub.unsubscribe_events(id);
    tracing::debug!(target: "server", id, "事件 WS 断开");
}

async fn pump_channel(socket: WebSocket, hub: Arc<WsHub>, channel_id: String) {
    let (tx, rx) = mpsc::unbounded_channel();
    hub.register_channel(channel_id.clone(), tx);
    tracing::debug!(target: "server", channel = %channel_id, "通道 WS 已连");
    let (sink, stream) = socket.split();
    pump(sink, stream, rx).await;
    hub.unregister_channel(&channel_id);
    // 告诉终端标签「这个订阅者走了」—— 它会把 sinks 里那条删掉（见 set_on_channel_closed）。
    hub.notify_channel_closed(&channel_id);
    tracing::debug!(target: "server", channel = %channel_id, "通道 WS 断开");
}

/// 收/发两条方向各自跑到断开。
///
/// 客户端→服务端目前**不使用**（终端输入走 `/rpc terminal_write`，与桌面版一致），
/// 但仍然要读：只有持续的 `read` 才能感知到对端关闭，
/// 否则连接断了以后这边的发送任务会一直挂在队列上。
async fn pump(
    mut sink: SplitSink<WebSocket, Message>,
    mut stream: SplitStream<WebSocket>,
    mut rx: UnboundedReceiver<Frame>,
) {
    let writer = tokio::spawn(async move {
        while let Some(frame) = rx.recv().await {
            if sink.send(frame.into_message()).await.is_err() {
                break;
            }
        }
    });

    // 读侧：只用于感知关闭。
    while let Some(Ok(msg)) = stream.next().await {
        if matches!(msg, Message::Close(_)) {
            break;
        }
    }

    writer.abort();
}
