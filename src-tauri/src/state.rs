//! 全局状态（§0.1 L4）：Arc 包裹的所有 manager，经 Tauri manage 注入。

use std::sync::Arc;

use crate::ipc_shim as tauri;
use crate::session::SessionManager;
use crate::store::Store;
use crate::vault::Vault;

pub struct AppState {
    pub app: tauri::AppHandle,
    pub store: Arc<Store>,
    pub vault: Arc<Vault>,
    pub sessions: Arc<SessionManager>,
    /// AI 运行时（任务表：jobId → 取消令牌）。
    pub ai: Arc<crate::ai::AiRuntime>,
    /// 数据库连接管理器。
    pub db: Arc<crate::db::DbManager>,
    /// 侦察快照缓存：session_id → (时间戳, 文本)。
    pub recon_cache: std::sync::Mutex<std::collections::HashMap<String, (u64, String)>>,
    /// 端口转发策略（监听地址 + 本平台是否允许转发）。
    ///
    /// 在 `new` 里**探测一次**就定死：它取决于编译形态与服务端启动时的 `NEXTERM_PLATFORM`，
    /// 运行期间不会变。放这里而不是让各命令各自去读环境变量，是为了让「界面看到的」
    /// 与「内核实际绑的」永远是同一个值。
    pub forward_policy: crate::transport::forward::ForwardPolicy,
}

impl AppState {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        app: tauri::AppHandle,
        store: Arc<Store>,
        vault: Arc<Vault>,
        sessions: Arc<SessionManager>,
        ai: Arc<crate::ai::AiRuntime>,
        db: Arc<crate::db::DbManager>,
    ) -> Arc<Self> {
        Arc::new(Self {
            app,
            store,
            vault,
            sessions,
            ai,
            db,
            recon_cache: std::sync::Mutex::new(std::collections::HashMap::new()),
            forward_policy: crate::transport::forward::ForwardPolicy::detect(),
        })
    }
}

/// 便捷别名：命令签名里用。
pub type ManagedState<'a> = tauri::State<'a, Arc<AppState>>;
