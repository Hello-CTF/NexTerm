//! IPC 命令层（§6.2，薄）：反序列化 → 校验 → 调 service → 包装错误。
//! 业务逻辑一律在 service 模块（L4）；函数不超过 30 行（§0.3）。

pub mod ai;
pub mod asset;
pub mod db;
pub mod docker;
pub mod forward;
pub mod fs;
pub mod mcp;
pub mod mount;
pub mod session;
pub mod terminal;
pub mod vault;

use crate::state::ManagedState;

/// 注册全部命令到 Builder。
pub fn register<R: tauri::Runtime>(builder: tauri::Builder<R>) -> tauri::Builder<R> {
    builder.invoke_handler(tauri::generate_handler![
        // session
        session::session_connect,
        session::session_connect_local,
        session::session_disconnect,
        session::session_list,
        session::session_probe,
        session::session_open_line_tab,
        session::session_line_exec,
        session::session_cwd,
        // terminal
        terminal::terminal_attach,
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
        // mount
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
        forward::forward_create,
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
        ai::ai_presets,
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
        vault::vault_reveal_credential,
        vault::vault_list_credentials,
        vault::vault_delete_credential,
        // mcp（M4-T1 设置 + M4-T2 写入外部 AI 工具配置）
        mcp::mcp_get_settings,
        mcp::mcp_save_settings,
        mcp::mcp_generate_token,
        mcp::mcp_list_tools,
        mcp::mcp_write_client_config,
        mcp::mcp_client_config_snippet,
        // asset / group / snippet / audit / known_host
        asset::asset_list,
        asset::asset_get,
        asset::asset_create,
        asset::asset_update,
        asset::asset_delete,
        asset::asset_search,
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
    ])
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

#[allow(dead_code)]
fn _typecheck_state(_: ManagedState<'_>) {}
