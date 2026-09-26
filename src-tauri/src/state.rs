//! 全局状态（§0.1 L4）：Arc 包裹的所有 manager，经 Tauri manage 注入。

use std::sync::Arc;

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
        })
    }
}

/// 便捷别名：命令签名里用。
pub type ManagedState<'a> = tauri::State<'a, Arc<AppState>>;
