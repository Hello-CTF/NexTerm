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

/// 初始化日志：按天滚动写入 `<data>/logs`；**写不了就降级到控制台，绝不 panic**。
///
/// 背景：`tracing_appender::rolling::daily` 在无法创建日志文件时**会 panic**
/// （源文件 `rolling.rs` 里写死的 `expect`），表现是应用启动即崩、只留一行难懂的
/// Rust panic。日志不可用不该让整个应用起不来 —— 只读配置目录、杀软占用、
/// 磁盘满、上一个实例句柄未释放，都可能触发。
///
/// 返回的 guard 必须活到进程结束，否则非阻塞写入线程会被提前掐掉。
fn init_tracing(log_dir: &std::path::Path) -> Option<tracing_appender::non_blocking::WorkerGuard> {
    let env_filter = || {
        tracing_subscriber::EnvFilter::try_from_default_env()
            .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info"))
    };
    let appender = tracing_appender::rolling::RollingFileAppender::builder()
        .rotation(tracing_appender::rolling::Rotation::DAILY)
        .filename_prefix("nexterm.log")
        .build(log_dir);
    match appender {
        Ok(appender) => {
            let (writer, guard) = tracing_appender::non_blocking(appender);
            tracing_subscriber::fmt()
                .with_env_filter(env_filter())
                .with_writer(writer)
                .with_ansi(false)
                .init();
            Some(guard)
        }
        Err(e) => {
            eprintln!("[NexTerm] 日志文件不可用（{e}），本次运行日志仅输出到控制台");
            tracing_subscriber::fmt()
                .with_env_filter(env_filter())
                .init();
            None
        }
    }
}

pub fn run() {
    let log_dir = data_dir().join("logs");
    let _ = std::fs::create_dir_all(&log_dir);
    let _log_guard = init_tracing(&log_dir);

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
