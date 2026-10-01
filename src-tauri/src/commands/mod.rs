//! IPC 命令层（§6.2，薄）：反序列化 → 校验 → 调 service → 包装错误。
//! 业务逻辑一律在 service 模块（L4）；函数不超过 30 行（§0.3）。

pub mod ai;
pub mod asset;
pub mod db;
pub mod docker;
pub mod forward;
pub mod fs;
pub mod layout;
pub mod models;
pub mod mount;
pub mod session;
pub mod sync;
pub mod terminal;
pub mod vault;

use crate::ipc_shim as tauri;
use crate::state::ManagedState;

/// 前端平台判定（编译期常量）。WKWebView 经自定义协议加载时 UA 可能不含
/// 平台标识，前端 UA 猜测曾把 macOS 误判成 Windows（自绘三键未隐藏），
/// 所以标题栏这类平台分支一律以本命令为准。
#[tauri::command]
pub fn app_platform() -> String {
    std::env::consts::OS.to_string()
}

/// **唯一**的命令清单。
///
/// 它自己不含任何逻辑，只把命令路径交给 `$cb` 指定的宏。桌面侧交给
/// `desktop_commands!`（产出 `generate_handler!`），服务端侧交给
/// `server_commands!`（产出 `Vec<Entry>`）。
///
/// 为什么费这个劲：命令清单一旦写两份，「某条命令只在一侧注册」这种错
/// **编译期不报**，只会在运行时表现为「某个面板点了没反应」——
/// 而这类症状最难查。清单只有一份，就不存在这个类别。
///
/// ⚠️ 它和下面两个消费宏都**必须定义在 `register` / `rpc_table` 之前**：
/// `macro_rules!` 是文本作用域的，放在后面等于没定义（症状是
/// `unused macro definition` + `cannot find macro`，而不是「找不到命令」）。
macro_rules! nexterm_commands {
    ($cb:ident) => {
        $cb! {
            // app
            app_platform,
            // layout（工作区布局：服务端权威运行态的一部分）
            layout::layout_get,
            layout::layout_put,
            // session
            session::session_connect,
            session::session_connect_local,
            session::session_disconnect,
            session::session_list,
            session::session_probe,
            session::session_open_line_tab,
            session::session_line_exec,
            session::session_cwd,
            session::session_reconnect,
            // terminal
            terminal::terminal_attach,
            terminal::terminal_attach_tab,
            terminal::terminal_list,
            terminal::terminal_claim,
            terminal::terminal_release,
            terminal::terminal_write,
            terminal::terminal_resize,
            terminal::terminal_detach,
            terminal::terminal_close_tab,
            terminal::terminal_screen_text,
            terminal::terminal_snapshot,
            terminal::terminal_tail,
            terminal::terminal_set_visible,
            terminal::terminal_dump,
            terminal::terminal_switch_encoding,
            terminal::terminal_record_start,
            terminal::terminal_record_stop,
            terminal::terminal_export_log,
            // fs
            fs::fs_list,
            fs::fs_read,
            fs::fs_write,
            fs::fs_mkdir,
            fs::fs_rename,
            fs::fs_delete,
            fs::fs_chmod,
            fs::fs_checksum,
            fs::fs_upload,
            fs::fs_download,
            fs::fs_pack_download,
            fs::fs_extract,
            // mount
            mount::mount_capability,
            mount::mount_list,
            mount::mount_create,
            mount::mount_remove,
            // docker
            docker::docker_overview,
            docker::docker_ps,
            docker::docker_logs_attach,
            docker::docker_exec_attach,
            docker::docker_action,
            docker::docker_images,
            docker::docker_image_pull,
            docker::docker_image_remove,
            docker::docker_inspect,
            docker::docker_stats,
            docker::docker_container_list_dir,
            // db
            db::db_connect,
            db::db_disconnect,
            db::db_schemas,
            db::db_tables,
            db::db_columns,
            db::db_query,
            db::redis_scan,
            db::redis_inspect,
            db::redis_command,
            db::redis_set_ttl,
            // forward
            forward::forward_env,
            forward::forward_create,
            forward::forward_create_socks,
            forward::forward_list,
            forward::forward_remove,
            // ai
            ai::ai_chat,
            ai::ai_cancel,
            ai::ai_confirm,
            ai::ai_models,
            ai::ai_test_provider,
            ai::ai_set_provider,
            ai::ai_get_provider,
            ai::ai_get_permission,
            ai::ai_set_permission,
            ai::ai_presets,
            // models（多模型档案，P0-3）
            models::ai_model_profiles,
            models::ai_model_save,
            models::ai_model_delete,
            models::ai_model_activate,
            models::ai_model_refresh,
            models::ai_model_preset,
            ai::ai_takeover_enter,
            ai::ai_takeover_run,
            ai::ai_takeover_exit,
            ai::ai_conversation_create,
            ai::ai_conversation_list,
            ai::ai_conversation_delete,
            ai::ai_messages,
            // vault
            vault::vault_status,
            vault::vault_init_master,
            vault::vault_init_dpapi,
            vault::vault_unlock,
            vault::vault_lock,
            vault::vault_change_password,
            vault::vault_set_credential,
            vault::credential_update,
            vault::vault_reveal_credential,
            vault::vault_list_credentials,
            vault::vault_delete_credential,
            // asset / group / snippet / audit / known_host
            asset::asset_list,
            asset::asset_get,
            asset::asset_create,
            asset::asset_update,
            asset::asset_delete,
            asset::asset_search,
            asset::asset_save_key_file,
            asset::asset_read_key_file,
            asset::credential_save,
            asset::group_list,
            asset::group_create,
            asset::group_update,
            asset::group_delete,
            asset::snippet_list,
            asset::snippet_create,
            asset::snippet_update,
            asset::snippet_delete,
            asset::audit_query,
            asset::known_host_list,
            asset::known_host_accept,
            asset::known_host_remove,
            asset::app_info,
            // sync（两端共用三个原语 + 桌面出站五条；服务端上出站命令运行时报 unsupported）
            sync::sync_digest,
            sync::sync_origin,
            sync::sync_export,
            sync::sync_import,
            sync::sync_token,
            sync::sync_token_rotate,
            sync::sync_link_get,
            sync::sync_link_set,
            sync::sync_remote_digest,
            sync::sync_push,
            sync::sync_pull,
        }
    };
}

/// 取路径的最后一段作为命令名。
///
/// 需要它是因为 `stringify!(a :: b)` 会原样带空格（`"a :: b"`），
/// 而 RPC 名必须恰好是 `b`。
///
/// 注意：更直观的写法 `$($mods:ident ::)* $leaf:ident` **不能用** ——
/// rustc 会报 `local ambiguity ... identical NTs`（已实测）。递归展开是可行的替代。
#[cfg(not(feature = "desktop"))]
macro_rules! __nexterm_leaf {
    ($leaf:ident) => {
        stringify!($leaf)
    };
    ($head:ident :: $($rest:ident)::+) => {
        __nexterm_leaf!($($rest)::+)
    };
}

#[cfg(feature = "desktop")]
macro_rules! desktop_commands {
    ($($($seg:ident)::+),* $(,)?) => {
        tauri::generate_handler![$($($seg)::+),*]
    };
}

/// 每条命令取 `<模块路径>::call` —— 那个 `call` 由 `#[command]` 生成。
#[cfg(not(feature = "desktop"))]
macro_rules! server_commands {
    ($($($seg:ident)::+),* $(,)?) => {
        vec![
            $(
                crate::server::rpc::Entry {
                    name: __nexterm_leaf!($($seg)::+),
                    call: $($seg)::+::call as crate::server::rpc::RpcFn,
                }
            ),*
        ]
    };
}

/// 注册全部命令到 Builder。（仅桌面：服务端没有 Builder，走 `rpc_table`。）
#[cfg(feature = "desktop")]
pub fn register<R: tauri::Runtime>(builder: tauri::Builder<R>) -> tauri::Builder<R> {
    builder.invoke_handler(nexterm_commands!(desktop_commands))
}

/// 服务端 RPC 分发表。（仅服务端。）
///
/// 表由 `nexterm_commands!` 这份**唯一清单**生成 —— 与桌面侧的
/// `generate_handler!` 用的是同一份。增删命令只改一处，两边不可能漂移。
#[cfg(not(feature = "desktop"))]
pub fn rpc_table() -> Vec<crate::server::rpc::Entry> {
    nexterm_commands!(server_commands)
}

/// 通用入参形状：会话级命令都带 sessionId。
pub trait SessionArgs {
    fn session_id(&self) -> &str;
}

/// app_info 用（前端探测内核版本）。
#[derive(serde::Serialize)]
pub struct AppInfo {
    pub name: &'static str,
    pub version: &'static str,
}

/// `clientId` 参数的缺省值：桌面视图。
///
/// 「谁在操作」这件事在桌面版上只有一个答案（整个进程一个窗口），所以缺省即正确。
/// 服务端侧同一条缺省还承担一个兼容作用：**还没接 `clientId` 的前端不会被这次
/// 改造锁死**（控制权归 `desktop`，写入也报 `desktop`，两边一致 ⇒ 照旧能敲）。
/// 详见 `commands/terminal` 的模块文档。
pub(crate) fn client_or_default(client_id: Option<String>) -> String {
    client_id.unwrap_or_else(|| crate::ipc_shim::DESKTOP_SUBSCRIBER.to_string())
}

#[allow(dead_code)]
fn _typecheck_state(_: ManagedState<'_>) {}
