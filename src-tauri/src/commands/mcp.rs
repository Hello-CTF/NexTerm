//! MCP 命令（§6.2 / M4）：设置读写、token 生成、工具权限门、一键写入外部 AI 工具配置。
//!
//! 说明：`enabled` / `http_port` / `token` 三项由后台 axum 服务启动时**快照**持有，
//! 改动后需重启应用才生效；`writeTools` 是每次调用现读设置，改完立即生效。

use serde::Serialize;

use crate::error::{AppError, AppResult};
use crate::mcp::{self, McpClientTarget, McpClientWriteResult, McpSettings};
use crate::state::ManagedState;

/// 对外暴露的工具描述（`McpTool` 里有 `&'static str`，不能直接序列化）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct McpToolDto {
    pub name: String,
    pub description: String,
    /// 该工具是否本来就只读（只读工具不受权限门约束）
    pub read_only: bool,
    /// 写工具是否已放闸
    pub write_enabled: bool,
}

#[tauri::command]
pub async fn mcp_get_settings(state: ManagedState<'_>) -> AppResult<McpSettings> {
    Ok(mcp::mcp_settings(&state).await)
}

/// 保存设置。返回 `restartRequired`：为 true 时 UI 应提示「重启应用生效」。
#[tauri::command]
pub async fn mcp_save_settings(state: ManagedState<'_>, settings: McpSettings) -> AppResult<bool> {
    if settings.enabled && settings.http_port == 0 {
        return Err(AppError::param("启用 MCP 时必须指定 HTTP 端口"));
    }
    if settings.enabled && settings.token.trim().is_empty() {
        return Err(AppError::param("启用 MCP 前请先生成 token"));
    }
    let before = mcp::mcp_settings(&state).await;
    let restart_required = before.enabled != settings.enabled
        || before.http_port != settings.http_port
        || before.token != settings.token;
    mcp::save_mcp_settings(&state, &settings).await?;
    Ok(restart_required)
}

/// 生成一个新的随机 token（32 位十六进制）。仅返回，不落库 —— 由 UI 调 save 落库。
#[tauri::command]
pub async fn mcp_generate_token() -> AppResult<String> {
    use rand::Rng as _;
    let mut rng = rand::thread_rng();
    Ok((0..32)
        .map(|_| char::from_digit(rng.gen_range(0..16), 16).unwrap_or('0'))
        .collect())
}

/// 列出工具与各自的权限门状态。
#[tauri::command]
pub async fn mcp_list_tools(state: ManagedState<'_>) -> AppResult<Vec<McpToolDto>> {
    let settings = mcp::mcp_settings(&state).await;
    Ok(mcp::tool_list()
        .into_iter()
        .map(|t| McpToolDto {
            read_only: t.read_only,
            write_enabled: t.read_only
                || settings.write_tools.get(t.name).copied().unwrap_or(false),
            name: t.name.to_string(),
            description: t.description.to_string(),
        })
        .collect())
}

/// M4-T2：把 nexterm 的 MCP 配置合并写入外部 AI 工具配置（保留其它 server，先备份）。
#[tauri::command]
pub async fn mcp_write_client_config(
    state: ManagedState<'_>,
    target: String,
) -> AppResult<McpClientWriteResult> {
    let t = match target.as_str() {
        "claude_code" => McpClientTarget::ClaudeCode,
        "claude_desktop" => McpClientTarget::ClaudeDesktop,
        "cursor" => McpClientTarget::Cursor,
        other => return Err(AppError::param(format!("不支持的写入目标：{other}"))),
    };
    mcp::write_client_config(&state, t).await
}

/// M4-T2 的兜底：只返回片段 JSON，由用户自行粘贴（任意工具、任意路径都适用）。
#[tauri::command]
pub async fn mcp_client_config_snippet(state: ManagedState<'_>) -> AppResult<serde_json::Value> {
    let settings = mcp::mcp_settings(&state).await;
    let snippet = mcp::client_config_snippet(&settings);
    Ok(serde_json::json!({ "mcpServers": snippet }))
}
