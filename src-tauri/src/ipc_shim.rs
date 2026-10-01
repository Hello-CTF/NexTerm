//! IPC 门面：把「与 Tauri 的耦合」收束到唯一一处。
//!
//! # 为什么需要它
//!
//! NexTerm 除了桌面版（Tauri + WebView），还要有**服务端版本**（跑在懒猫微服容器里，
//! Linux、无桌面、无 WebKit，用户用浏览器访问）。而内核与命令层里到处是
//! `tauri::ipc::Channel` / `tauri::AppHandle` / `#[tauri::command]`。
//!
//! 与其改上百个调用点，不如让这些文件在顶部写一行
//!
//! ```ignore
//! use crate::ipc_shim as tauri;
//! ```
//!
//! 于是文件里原有的 `tauri::xxx` **一个字都不用改**，由本模块按 feature 决定
//! `tauri` 指的是真 Tauri（桌面）还是服务端替身。这条「use 别名遮蔽同名 extern crate」
//! 的手法已用编译实验验证（见 `docs/LAZYCAT-PORT.md` §12.2：普通路径与**属性宏路径**都会被遮蔽）。
//!
//! # 桌面侧是纯再导出
//!
//! `desktop` 分支只做 `pub use ::tauri::…`，**零行为变化**。这是硬要求：
//! 桌面版是现有发布线，服务端改造不许把它的行为挪动哪怕一个字节。
//!
//! 唯一的例外是 `command`：它不再直接再导出 `::tauri::command`，而是再导出
//! [`nexterm_ipc_macros::command`] —— 后者在桌面下**原样转发**给 `::tauri::command`，
//! 所以 122 个调用点写 `#[tauri::command]` 的写法与产物都不变，只是多了一层透明宏。
//! 这样做换来的是：同一个属性在服务端下会额外生成一份 RPC 适配器，**不需要改 122 处**。
//!
//! # 服务端侧
//!
//! `server` 分支提供接口形状与 Tauri 对齐的替身类型，使调用点在两种模式下写法一致：
//!
//! | Tauri | 服务端替身 | 语义 |
//! |---|---|---|
//! | `ipc::Channel<T>` | 同路径 | PTY 原始字节 / AI 结构化事件 → WS 帧 |
//! | `AppHandle` + `Emitter` | 同路径 | 结构化事件 → 广播给所有 WS 客户端 |
//! | `Manager::path()` | 同路径 | `app_data_dir()` → `/lzcapp/var` |
//! | `State<'r, Arc<AppState>>` | 同路径 | 由 `/rpc` 分发器构造 |
//! | `#[command]` | 见 `nexterm-ipc-macros` | 桌面转发 Tauri，服务端生成注册项 |
//!
//! # feature 判据
//!
//! **主开关是 `desktop`**：桌面模式 = `feature = "desktop"` 开启（默认）；
//! 服务端模式 = `#[cfg(not(feature = "desktop"))]`。
//!
//! 这样定的原因是 `--all-features` 必须落到**桌面**模式：CI 里跑的是
//! `clippy --all-targets --all-features` / `test --workspace`，若切成「两个 feature 互斥」，
//! `--all-features` 会同时打开两边、桌面入口与 bin 立刻编不过，**就得改 CI**。
//! 现在这样 CI 口径一行不动（见 `docs/LAZYCAT-PORT.md` §12.2）。
//!
//! `server` feature 不参与代码判据，只用来开启服务端专属可选依赖（axum 等）。
//! 服务端构建固定写法：`cargo build --no-default-features --features server`。

/// 命令属性宏。桌面下转发 `::tauri::command`，服务端下另生成 RPC 适配器模块。
///
/// 用 `pub use`（而不是 `pub use ... as command`）保持属性路径 `tauri::command`
/// —— 122 个调用点全是这么写的，一行都不用动。
pub use ::nexterm_ipc_macros::command;

/// 桌面侧的固定订阅者标识（见 [`SubscriberKey`]）。
///
/// 命令层把它当作 `clientId` 参数的缺省值：桌面前端不必为「谁在操作」这件事
/// 传任何东西 —— 整个进程只有一个视图，缺省即正确。
pub const DESKTOP_SUBSCRIBER: &str = "desktop";

/// 「谁在看这条通道」——终端标签支持多订阅者后，必须能区分订阅者身份。
///
/// # 为什么两种模式的值不一样
///
/// - **桌面**：只有一个 WebView 窗口，全部 Channel 都属于同一个视图 ⇒ 一律返回
///   固定值 `"desktop"`。语义是「同一个视图重复 attach = 替换自己」，与改造前
///   `sink: Option<Channel>` 的行为**逐字节一致**（重复 attach 只有最后一条生效）。
/// - **服务端**：前端为每条 `/ws/channel/{id}` 生成的 id 天然是唯一身份
///   ⇒ 每个浏览器标签页各算一个订阅者，于是多台设备能同时看同一个终端。
///
/// 不能在 `TerminalTab` 里自己生成这份标识：桌面侧无法从 `Channel` 反查 Tauri
/// 内部的通道号（字段私有），而服务端侧的 `channel_id` 才是前端真正连的那条 WS。
pub trait SubscriberKey {
    fn subscriber_key(&self) -> String;
}

/// 桌面：所有 Channel 归同一个视图（见 trait 文档）。
#[cfg(feature = "desktop")]
impl<T> SubscriberKey for ::tauri::ipc::Channel<T> {
    fn subscriber_key(&self) -> String {
        DESKTOP_SUBSCRIBER.to_string()
    }
}

// ── 桌面：纯再导出，零行为变化 ────────────────────────────────────────
#[cfg(feature = "desktop")]
// ⚠️ 这是**白名单**再导出，不是 `pub use ::tauri::*` —— 所以任何调用点新用到一个
// Tauri 顶层项（比如 `tauri::async_runtime`），这里必须同步补上，否则**桌面分支
// 也会编不过**（`--all-features` 就是这条路径，即 CI 口径）。
pub use ::tauri::{
    async_runtime, generate_handler, ipc, AppHandle, Builder, Emitter, Manager, Runtime, State,
};

// ── 服务端替身 ───────────────────────────────────────────────────────
#[cfg(not(feature = "desktop"))]
mod server_impl {
    use std::marker::PhantomData;
    use std::path::PathBuf;
    use std::sync::Arc;
    use std::sync::OnceLock;

    /// 替身层的错误类型。调用点只做 `let _ =` 或 `.is_ok()`，不需要更多。
    #[derive(Debug)]
    pub struct Error(pub String);

    impl std::fmt::Display for Error {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.write_str(&self.0)
        }
    }
    impl std::error::Error for Error {}

    /// 事件总线：所有 WS 连接注册在它上面。
    ///
    /// 具体的 hub 实现（连接表 + 通道路由）在 `server` 模块里，本模块只定义契约，
    /// 这样门面层不依赖 axum。
    ///
    /// 三个方法刻意分开而不是合成一个「发点什么」：
    /// - `broadcast_event` 面向**所有人**（会话状态、FS 进度这类）；
    /// - `send_bytes` / `send_json` 面向**某一条通道**（PTY 输出、AI 事件流）。
    ///   PTY 字节**不能**走 JSON（转义 + 膨胀，终端吞吐撑不住），所以必须分开。
    pub trait Hub: Send + Sync + 'static {
        /// 结构化事件 → 广播给所有 `/ws/events` 连接。
        fn broadcast_event(&self, event: &str, payload: serde_json::Value);
        /// 原始字节 → 发给某条通道（前端 `/ws/channel/{id}` 那条）。
        fn send_bytes(&self, channel_id: &str, data: Vec<u8>);
        /// 结构化载荷 → 发给某条通道。
        fn send_json(&self, channel_id: &str, payload: serde_json::Value);
    }

    /// 什么都不做的总线：兜住「前端还没 attach」的情形。
    pub struct NullHub;

    impl Hub for NullHub {
        fn broadcast_event(&self, _event: &str, _payload: serde_json::Value) {}
        fn send_bytes(&self, _channel_id: &str, _data: Vec<u8>) {}
        fn send_json(&self, _channel_id: &str, _payload: serde_json::Value) {}
    }

    /// 事件出口（对齐 `tauri::Emitter`）。
    pub trait Emitter {
        fn emit<S: serde::Serialize + Clone>(&self, event: &str, payload: S) -> Result<(), Error>;
    }

    /// 路径解析（对齐 `tauri::Manager::path()`）。
    pub struct PathResolver {
        data_dir: PathBuf,
    }

    impl PathResolver {
        pub fn app_data_dir(&self) -> Result<PathBuf, Error> {
            Ok(self.data_dir.clone())
        }
    }

    /// 路径 / 状态（对齐 `tauri::Manager`）。
    ///
    /// Tauri 的 `Manager` 是个大 trait（`path` / `manage` / `state` / `try_state`…）。
    /// 我们只补**调用点真的用到**的那些 —— 每多一个方法都要有真实现，否则就是
    /// 又一个「编译过、跑起来静默失效」的坑。
    pub trait Manager {
        fn path(&self) -> &PathResolver;

        /// 对齐 `tauri::Manager::try_state`：取当前应用状态。
        ///
        /// 调用点是 `session::AppCallbacks::exit` 里的
        /// `self.app.try_state::<Arc<AppState>>()` —— 泵退出后要判定「连接掉了还是
        /// shell 正常退出」，据此决定要不要自动重连。
        ///
        /// 泛型只为对上 Tauri 的调用形状；服务端只有一种状态，其余 T 一律 `None`
        /// （与 Tauri 行为一致：没 `manage` 过的类型就是取不到）。
        fn try_state<T: 'static>(&self) -> Option<State<'_, T>>;
    }

    /// 服务端「应用句柄」：数据目录 + 事件总线。
    #[derive(Clone)]
    pub struct AppHandle {
        paths: Arc<PathResolver>,
        hub: Arc<dyn Hub>,
        /// 回填的应用状态（对齐 Tauri 的「`manage` 过什么就能 `try_state` 到什么」）。
        ///
        /// ⚠️ 只能在 `AppState` 造好之后回填：`AppState` 自己就持有 `AppHandle`，
        /// 从构造函数传会绕成鸡生蛋。`OnceLock` 让「先建句柄、后挂状态」这件事安全
        /// 且只做一次；`Arc` 是为了让 `Clone` 出来的句柄共享同一个槽。
        state: Arc<OnceLock<Arc<crate::state::AppState>>>,
    }

    impl AppHandle {
        pub fn new(data_dir: PathBuf, hub: Arc<dyn Hub>) -> Self {
            Self {
                paths: Arc::new(PathResolver { data_dir }),
                hub,
                state: Arc::new(OnceLock::new()),
            }
        }

        /// 把 `AppState` 挂到这个句柄上，使 `Manager::try_state` 能取到它。
        ///
        /// 幂等：重复调用只有第一次生效（`OnceLock` 语义）。bootstrap 在
        /// `AppState::new` 之后调一次即可。
        pub fn attach_state(&self, state: Arc<crate::state::AppState>) {
            let _ = self.state.set(state);
        }

        pub fn hub(&self) -> &Arc<dyn Hub> {
            &self.hub
        }
    }

    impl Emitter for AppHandle {
        fn emit<S: serde::Serialize + Clone>(&self, event: &str, payload: S) -> Result<(), Error> {
            let value = serde_json::to_value(payload).map_err(|e| Error(e.to_string()))?;
            self.hub.broadcast_event(event, value);
            Ok(())
        }
    }

    impl Manager for AppHandle {
        fn path(&self) -> &PathResolver {
            &self.paths
        }

        fn try_state<T: 'static>(&self) -> Option<State<'_, T>> {
            // 先 `get()`（没挂就 `None`），再把 `&Arc<AppState>` 当作 `&dyn Any`
            // 下转成调用点要的 `T`。这样不需要在门面层给每种状态开一个字段。
            let any: &dyn std::any::Any = self.state.get()?;
            any.downcast_ref::<T>().map(State::new)
        }
    }

    /// 命令状态（对齐 `tauri::State<'r, T>`）。
    ///
    /// Tauri 那个版本内部是 `&'r T` 且字段私有、**无法凭空构造**（只有框架能造）。
    /// 服务端没有框架，所以这里给一个公开构造器 —— 这正是「服务端不可能直接复用
    /// Tauri 的命令包装函数」的原因，也是本门面必须存在的原因。
    pub struct State<'r, T>(&'r T);

    impl<'r, T> State<'r, T> {
        pub fn new(inner: &'r T) -> Self {
            Self(inner)
        }
        pub fn inner(&self) -> &'r T {
            self.0
        }
    }

    impl<T> Clone for State<'_, T> {
        fn clone(&self) -> Self {
            *self
        }
    }
    impl<T> Copy for State<'_, T> {}

    impl<T> std::ops::Deref for State<'_, T> {
        type Target = T;
        fn deref(&self) -> &T {
            self.0
        }
    }

    /// 通道载荷。只有两种：PTY 原始字节、AI 结构化事件。
    pub enum Payload {
        Bytes(Vec<u8>),
        Json(serde_json::Value),
    }

    /// 能被送进通道的类型。
    pub trait IntoPayload {
        fn into_payload(self) -> Payload;
    }

    impl IntoPayload for Vec<u8> {
        fn into_payload(self) -> Payload {
            Payload::Bytes(self)
        }
    }

    impl IntoPayload for &[u8] {
        fn into_payload(self) -> Payload {
            Payload::Bytes(self.to_vec())
        }
    }

    impl IntoPayload for crate::ai::AiEvent {
        fn into_payload(self) -> Payload {
            Payload::Json(serde_json::to_value(self).unwrap_or(serde_json::Value::Null))
        }
    }

    /// 前端通道（对齐 `tauri::ipc::Channel<T>`）。
    ///
    /// 桌面下 Channel 由 Tauri 自己托管并把消息经自定义协议推给 WebView；
    /// 服务端下由前端先开一条 `/ws/channel/{id}`，再把 id 作为普通参数传上来。
    pub struct Channel<T> {
        hub: Arc<dyn Hub>,
        channel_id: String,
        _t: PhantomData<T>,
    }

    impl<T> Clone for Channel<T> {
        fn clone(&self) -> Self {
            Self {
                hub: Arc::clone(&self.hub),
                channel_id: self.channel_id.clone(),
                _t: PhantomData,
            }
        }
    }

    impl<T> std::fmt::Debug for Channel<T> {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(f, "Channel({})", self.channel_id)
        }
    }

    impl<T: IntoPayload> Channel<T> {
        pub fn new(hub: Arc<dyn Hub>, channel_id: impl Into<String>) -> Self {
            Self {
                hub,
                channel_id: channel_id.into(),
                _t: PhantomData,
            }
        }

        pub fn send(&self, data: T) -> Result<(), Error> {
            match data.into_payload() {
                Payload::Bytes(b) => self.hub.send_bytes(&self.channel_id, b),
                Payload::Json(v) => self.hub.send_json(&self.channel_id, v),
            }
            Ok(())
        }
    }

    /// 服务端：订阅者身份 = 前端那条 `/ws/channel/{id}` 的 id（见 `SubscriberKey`）。
    ///
    /// 写在这里而不是门面顶层，是因为 `channel_id` 是私有字段 —— 同模块才能读。
    impl<T> super::SubscriberKey for Channel<T> {
        fn subscriber_key(&self) -> String {
            self.channel_id.clone()
        }
    }

    /// 与 Tauri 同路径的子模块：`ipc::Channel`。
    pub mod ipc {
        pub use super::Channel;
    }

    /// 与 Tauri 同路径的子模块：`async_runtime`。
    ///
    /// 服务端本来就跑在 tokio 上（axum 的运行时），而 Tauri 的 `async_runtime`
    /// 底层也是 tokio —— 直接映射，不是"凑合能过"。
    ///
    /// 刻意**不返回** `JoinHandle`：唯一调用点（`AppCallbacks::exit` 里的重连触发器）
    /// 是即发即忘，返回句柄没人接只会招来 `unused_must_use`（CI 是 `-D warnings`）。
    pub mod async_runtime {
        use std::future::Future;

        /// 对齐 `tauri::async_runtime::spawn`（即发即忘式后台任务）。
        pub fn spawn<F>(future: F)
        where
            F: Future<Output = ()> + Send + 'static,
        {
            tokio::spawn(future);
        }
    }
}

#[cfg(not(feature = "desktop"))]
pub use server_impl::*;
