//! NexTerm 内核装配：Tauri Builder、状态注入、命令注册（§2）。
//!
//! 启动流程：日志 → SQLite（迁移）→ Vault → AI 运行时 → AppState →
//! 后台任务（空闲清理 / 自动锁）→ MCP → 窗口。

pub mod ai;
pub mod commands;
pub mod db;
pub mod docker;
pub mod error;
pub mod events;
pub mod fs;
pub mod ids;
pub mod ipc_types;
pub mod mcp;
pub mod session;
pub mod state;
pub mod store;
pub mod terminal;
pub mod transport;
pub mod vault;

use std::sync::Arc;

/// 应用数据目录（%APPDATA%/NexTerm）。
pub fn data_dir() -> std::path::PathBuf {
    dirs::data_dir()
        .unwrap_or_else(|| std::env::current_dir().unwrap_or_default())
        .join("NexTerm")
}

pub fn run() {
    // 日志：按天滚动到 data/logs（§1 tracing + tracing-appender）
    let log_dir = data_dir().join("logs");
    let _ = std::fs::create_dir_all(&log_dir);
    let file_appender = tracing_appender::rolling::daily(&log_dir, "nexterm.log");
    let (writer, _guard) = tracing_appender::non_blocking(file_appender);
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .with_writer(writer)
        .with_ansi(false)
        .init();

    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_global_shortcut::Builder::new().build());
    let builder = commands::register(builder);
    builder
        .setup(|app| {
            let handle = app.handle().clone();
            tauri::async_runtime::block_on(async move {
                let db_path = data_dir().join("data.db");
                let store = match store::Store::open(&db_path).await {
                    Ok(s) => Arc::new(s),
                    Err(e) => {
                        tracing::error!(target: "boot", error = %e, "SQLite 打开失败");
                        return Err(e.to_string().into());
                    }
                };
                let vault = match vault::Vault::load(Arc::clone(&store)).await {
                    Ok(v) => v,
                    Err(e) => {
                        tracing::error!(target: "boot", error = %e, "凭据库加载失败");
                        return Err(e.to_string().into());
                    }
                };
                let sessions = Arc::new(session::SessionManager::new());
                // AI 提供方配置：从 setting 读取，缺省空配置
                let provider = store
                    .setting_get("ai.provider")
                    .await
                    .ok()
                    .flatten()
                    .and_then(|s| serde_json::from_str::<ai::ProviderConfig>(&s).ok())
                    .unwrap_or_default();
                let ai = Arc::new(ai::AiRuntime::new(provider));
                let dbmgr = Arc::new(db::DbManager::default());
                let app_state =
                    state::AppState::new(handle.clone(), store, vault, sessions, ai, dbmgr);
                tauri::Manager::manage(&handle, Arc::clone(&app_state));

                // 后台任务：空闲会话清理（30 分钟，§7）+ vault 自动锁
                let bg_state = Arc::clone(&app_state);
                tokio::spawn(async move {
                    session::idle_sweeper(bg_state, std::time::Duration::from_secs(30 * 60)).await;
                });
                let vault2 = Arc::clone(&app_state.vault);
                tokio::spawn(async move {
                    vault::auto_lock_task(vault2).await;
                });

                // MCP 对外（M4，设置开启时启动）
                mcp::start(Arc::clone(&app_state)).await;

                tracing::info!(target: "boot", "NexTerm 内核就绪");
                Ok(())
            })
        })
        .run(tauri::generate_context!())
        .unwrap_or_else(|e| {
            eprintln!("NexTerm 启动失败: {e}");
            std::process::exit(1);
        });
}
