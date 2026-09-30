//! NexTerm 内核装配：Tauri Builder、状态注入、命令注册（§2）。
//!
//! 启动流程：日志 → SQLite（迁移）→ Vault → AI 运行时 → AppState →
//! 后台任务（空闲清理 / 自动锁）→ 窗口。

pub mod ai;
pub mod commands;
pub mod db;
pub mod docker;
pub mod error;
pub mod events;
pub mod fs;
pub mod ids;
pub mod ipc_shim;
pub mod ipc_types;
pub mod session;
pub mod state;
pub mod store;
pub mod sync;
pub mod terminal;
pub mod transport;
pub mod vault;

/// 服务端装配（HTTP + WS）。仅服务端模式编译；里面依赖 axum（`server` feature）。
#[cfg(not(feature = "desktop"))]
pub mod server;

// `Arc` 只被桌面装配（`run`）用到；服务端那份走 `server::serve`。
#[cfg(feature = "desktop")]
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
pub(crate) fn init_tracing(
    log_dir: &std::path::Path,
) -> Option<tracing_appender::non_blocking::WorkerGuard> {
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

/// 崩溃取证：把 panic 落到文件。
///
/// GUI（Windows 子系统）程序里 panic 的默认行为是**什么都不留下** —— 没有控制台、
/// 日志里没有、事件查看器通常也不记（Rust 是 unwind 之后以退出码 101 正常结束，
/// 不走 SEH，所以 WER 不会生成报告）。结果就是「用着用着就没了」，事后完全无从查起。
///
/// 这里挂一个 hook，把 panic 位置与 backtrace 追加到 `<data>/logs/crash.log`。
/// 刻意**不走 tracing** 落盘：那是异步 writer，panic 时未必来得及刷出去。
pub(crate) fn install_panic_hook(log_dir: std::path::PathBuf) {
    let default_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        let backtrace = std::backtrace::Backtrace::force_capture();
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis())
            .unwrap_or(0);
        let text = format!("===== PANIC @ unix_ms {stamp} =====\n{info}\n{backtrace}\n");
        eprintln!("[NexTerm] {text}");
        if let Ok(mut f) = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(log_dir.join("crash.log"))
        {
            use std::io::Write;
            let _ = f.write_all(text.as_bytes());
            let _ = f.flush();
        }
        default_hook(info);
    }));
}

/// 桌面版启动。服务端不走这里（见 `server::serve`）。
///
/// 整个函数门控在 `desktop` 下：它直接使用 `tauri::` 与两个 Tauri 插件，
/// 而这些在服务端是不存在的依赖（`tauri` 是 optional）。
#[cfg(feature = "desktop")]
pub fn run() {
    let log_dir = data_dir().join("logs");
    let _ = std::fs::create_dir_all(&log_dir);
    let _log_guard = init_tracing(&log_dir);
    install_panic_hook(log_dir.clone());

    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_global_shortcut::Builder::new().build());
    let builder = commands::register(builder);
    builder
        .on_window_event(|window, event| {
            // 「应用整体退出」最常见的直接原因就是窗口被关/被销毁（Tauri 默认最后一个
            // 窗口关闭即退出），而它默认不留任何痕迹。
            //
            // ⚠️ 实测（2026-09-27）：**`.run()` 之后的代码不会执行** —— Tauri 在最后一个
            // 窗口销毁后直接 `process::exit()`，不走返回到调用方那条路。所以"是否正常退出"
            // 只能靠这两条日志判断：**两条都有 = 窗口被正常关掉（应用退出）；两条都没有
            // 也没 crash.log = 进程被外部强杀**。别再指望在 `.run()` 后面补日志。
            match event {
                tauri::WindowEvent::CloseRequested { .. } => {
                    tracing::warn!(target: "lifecycle", label = window.label(), "窗口收到关闭请求");
                }
                tauri::WindowEvent::Destroyed => {
                    tracing::warn!(target: "lifecycle", label = window.label(), "窗口已销毁");
                }
                _ => {}
            }
        })
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
                // 凭据库是可选组件：加载**不会失败**，坏状态只在内存里降级为「未初始化」。
                // 以前这里是个 `match ... Err => return Err(...)`，而 setup 返回 Err 会让
                // tauri 直接 panic —— 一个可选组件能把整个应用变成"双击一闪就没"。
                let vault = vault::Vault::load(Arc::clone(&store)).await;
                // 内置「当前设备」资产：装上就有的一台"机器"，本地终端 / 文件树 /
                // 容器面板都挂在它上面。幂等（固定 ID），重复启动只是一次主键查询。
                // 失败只记日志、不拦启动 —— 少了它应用照样能用，只是首屏没有本机入口。
                match store.asset_ensure_builtin_local().await {
                    Ok(a) => tracing::info!(target: "boot", asset = %a.name, "内置本地资产就绪"),
                    Err(e) => tracing::warn!(target: "boot", error = %e, "内置本地资产创建失败"),
                }
                let sessions = Arc::new(session::SessionManager::new());
                // AI 提供方配置：从「多模型档案」里取当前激活的那一份。
                // 首次启动顺带做一次旧 `ai.provider` → `ai.models` 的迁移（会落库）。
                // 读失败就退回空配置 —— 模型配置坏掉不该拦着应用启动。
                let model_store = ai::profiles::ModelProfileStore::load(&store)
                    .await
                    .unwrap_or_default();
                let provider = model_store
                    .active()
                    .map(|p| p.to_provider())
                    .unwrap_or_default();
                let ai = Arc::new(ai::AiRuntime::new(provider));
                // AI 权限配置（档位 + 自定义危险规则）：同样从 setting 读，缺省「读写」档。
                // 读不到就用默认值 —— 权限配置坏掉不该拦着应用启动。
                if let Some(cfg) = store
                    .setting_get("ai.permission")
                    .await
                    .ok()
                    .flatten()
                    .and_then(|s| serde_json::from_str::<ai::guard::PermissionConfig>(&s).ok())
                {
                    *ai.permission.write().await = cfg;
                }
                let dbmgr = Arc::new(db::DbManager::default());
                let app_state =
                    state::AppState::new(handle.clone(), store, vault, sessions, ai, dbmgr);
                tauri::Manager::manage(&handle, Arc::clone(&app_state));

                // macOS：原生红绿灯标题栏。tao/wry 的运行时 API 靠不住 ——
                // set_decorations 只翻转内部原子标志（is_decorated 读的也是它，
                // 不是 NSWindow 真实状态），wry 的 Overlay 处理又不补 Titled 位；
                // 红绿灯只有样式掩码真含 Titled 才会渲染。所以这里直接用
                // AppKit 配 NSWindow：Titled 全家桶 + 全尺寸内容视图 + 透明
                // 标题栏 + 隐藏标题。真实掩码前后值都写进启动日志。
                #[cfg(target_os = "macos")]
                {
                    use objc2_app_kit::{NSWindow, NSWindowTitleVisibility};
                    if let Some(win) = tauri::Manager::get_webview_window(&handle, "main") {
                        match win.ns_window() {
                            Ok(ptr) => {
                                let ns: &NSWindow = unsafe { &*(ptr as *const NSWindow) };
                                let before = ns.styleMask();
                                ns.setStyleMask(
                                    objc2_app_kit::NSWindowStyleMask::Titled
                                        | objc2_app_kit::NSWindowStyleMask::Closable
                                        | objc2_app_kit::NSWindowStyleMask::Miniaturizable
                                        | objc2_app_kit::NSWindowStyleMask::Resizable
                                        | objc2_app_kit::NSWindowStyleMask::FullSizeContentView,
                                );
                                ns.setTitlebarAppearsTransparent(true);
                                ns.setTitleVisibility(NSWindowTitleVisibility::Hidden);
                                let after = ns.styleMask();
                                tracing::info!(
                                    target: "boot",
                                    before = ?before,
                                    after = ?after,
                                    "macOS 标题栏已直接配置（AppKit：红绿灯 + 透明标题栏 + 全尺寸内容）"
                                );
                            }
                            Err(e) => {
                                tracing::warn!(target: "boot", error = %e, "取 NSWindow 失败，跳过标题栏配置")
                            }
                        }
                    } else {
                        tracing::warn!(target: "boot", "未找到主窗口，跳过标题栏配置");
                    }
                }

                // 后台任务：空闲会话清理（30 分钟，§7）+ vault 自动锁
                let bg_state = Arc::clone(&app_state);
                tokio::spawn(async move {
                    session::idle_sweeper(bg_state, std::time::Duration::from_secs(30 * 60)).await;
                });
                let vault2 = Arc::clone(&app_state.vault);
                tokio::spawn(async move {
                    vault::auto_lock_task(vault2).await;
                });

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

#[cfg(test)]
mod tests {
    use super::*;

    /// panic hook 必须**真的**把内容写进 crash.log。
    ///
    /// 值得单测，是因为写文件那段用了 `if let Ok(..)`：路径不可写时它会**静默什么都不做** ——
    /// 那样这套取证就成了摆设，而我们还以为有保底。下次真出问题照样查不出来。
    #[test]
    fn panic_hook_writes_crash_log() {
        let dir = std::env::temp_dir().join(format!("nexterm-crash-hook-{}", std::process::id()));
        let file = dir.join("crash.log");
        std::fs::create_dir_all(&dir).expect("建临时目录");
        let _ = std::fs::remove_file(&file);

        let prev = std::panic::take_hook();
        install_panic_hook(dir.clone());
        // 这个 panic 是**故意**的：hook 会在 unwind 之前把现场写下来
        let panicked = std::panic::catch_unwind(|| panic!("取证自检用的假 panic"));
        std::panic::set_hook(prev); // 还原，别影响别的测试的 panic 输出

        assert!(panicked.is_err(), "catch_unwind 应捕获到 panic");
        let text = std::fs::read_to_string(&file).expect("crash.log 应被写入");
        assert!(
            text.contains("取证自检用的假 panic"),
            "应含 panic 信息：{text}"
        );
        assert!(text.contains("PANIC @ unix_ms"), "应带时间戳头：{text}");

        let _ = std::fs::remove_file(&file);
        let _ = std::fs::remove_dir(&dir);
    }
}
