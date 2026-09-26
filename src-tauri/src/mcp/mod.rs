//! MCP 对外（M4）：把 NexTerm 能力暴露给外部 AI 工具（Claude Code / Cursor 等）。
//!
//! - 传输：stdio（本地 CLI）+ streamable HTTP（远程/局域网）；
//! - 权限门：默认只读工具；写工具需在设置里逐个开启；
//! - token 校验：HTTP 模式必须带 `Authorization: Bearer <token>`。

use std::collections::HashMap;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::json;
use tokio::sync::RwLock;

use crate::error::{AppError, AppResult};
use crate::state::AppState;

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct McpSettings {
    pub enabled: bool,
    pub http_port: u16,
    pub token: String,
    /// 逐工具写权限门（默认全 false = 只读）。
    pub write_tools: HashMap<String, bool>,
}

/// MCP 工具定义（对外暴露面）。
pub struct McpTool {
    pub name: &'static str,
    pub description: &'static str,
    pub read_only: bool,
    pub schema: serde_json::Value,
}

pub fn tool_list() -> Vec<McpTool> {
    vec![
        McpTool {
            name: "list_assets",
            description: "列出 NexTerm 中已配置的资产（服务器/数据库）",
            read_only: true,
            schema: json!({"type":"object","properties":{}}),
        },
        McpTool {
            name: "exec_command",
            description: "在已连接的会话上执行一条命令（返回 stdout/stderr/exit_code）",
            read_only: false,
            schema: json!({
                "type":"object",
                "properties":{
                    "session_id":{"type":"string"},
                    "command":{"type":"string"},
                },
                "required":["session_id","command"]
            }),
        },
        McpTool {
            name: "read_screen",
            description: "读取某个终端标签的当前屏幕文本",
            read_only: true,
            schema: json!({
                "type":"object",
                "properties":{"tab_id":{"type":"string"}},
                "required":["tab_id"]
            }),
        },
        McpTool {
            name: "read_file",
            description: "读取已连接主机上的文件",
            read_only: true,
            schema: json!({
                "type":"object",
                "properties":{
                    "session_id":{"type":"string"},
                    "path":{"type":"string"},
                },
                "required":["session_id","path"]
            }),
        },
        McpTool {
            name: "docker_ps",
            description: "列出已连接主机上的 Docker 容器",
            read_only: true,
            schema: json!({
                "type":"object",
                "properties":{"session_id":{"type":"string"}},
                "required":["session_id"]
            }),
        },
    ]
}

/// JSON-RPC 请求（MCP 1.0 兼容子集：initialize / tools/list / tools/call）。
#[derive(Debug, Deserialize)]
struct RpcRequest {
    #[serde(default)]
    id: serde_json::Value,
    method: String,
    #[serde(default)]
    params: serde_json::Value,
}

fn rpc_ok(id: &serde_json::Value, result: serde_json::Value) -> serde_json::Value {
    json!({ "jsonrpc": "2.0", "id": id, "result": result })
}

fn rpc_err(id: &serde_json::Value, code: i32, message: &str) -> serde_json::Value {
    json!({ "jsonrpc": "2.0", "id": id, "error": { "code": code, "message": message } })
}

/// 处理一条 JSON-RPC 请求（stdio 与 HTTP 共用）。
pub async fn handle_rpc(state: &AppState, body: &str, authorized: bool) -> String {
    let req: RpcRequest = match serde_json::from_str(body) {
        Ok(r) => r,
        Err(e) => {
            return rpc_err(&serde_json::Value::Null, -32700, &format!("解析失败: {e}")).to_string()
        }
    };
    match req.method.as_str() {
        "initialize" => rpc_ok(
            &req.id,
            json!({
                "protocolVersion": "2025-06-18",
                "capabilities": { "tools": {} },
                "serverInfo": { "name": "nexterm", "version": env!("CARGO_PKG_VERSION") },
            }),
        )
        .to_string(),
        "tools/list" => {
            let tools: Vec<serde_json::Value> = tool_list()
                .iter()
                .map(|t| {
                    json!({
                        "name": t.name,
                        "description": t.description,
                        "inputSchema": t.schema,
                    })
                })
                .collect();
            rpc_ok(&req.id, json!({ "tools": tools })).to_string()
        }
        "tools/call" => {
            if !authorized {
                return rpc_err(&req.id, -32001, "未授权：token 校验失败").to_string();
            }
            let name = req
                .params
                .get("name")
                .and_then(|v| v.as_str())
                .unwrap_or("");
            let args = req.params.get("arguments").cloned().unwrap_or_default();
            match call_tool(state, name, &args).await {
                Ok(text) => rpc_ok(
                    &req.id,
                    json!({ "content": [ { "type": "text", "text": text } ] }),
                )
                .to_string(),
                Err(e) => rpc_err(&req.id, -32000, &e.to_string()).to_string(),
            }
        }
        "ping" => rpc_ok(&req.id, json!({})).to_string(),
        other => rpc_err(&req.id, -32601, &format!("未知方法 {other}")).to_string(),
    }
}

/// 权限门 + 工具执行。
async fn call_tool(state: &AppState, name: &str, args: &serde_json::Value) -> AppResult<String> {
    let tool = tool_list()
        .into_iter()
        .find(|t| t.name == name)
        .ok_or_else(|| AppError::NotFound(format!("工具 {name}")))?;

    let settings = mcp_settings(state).await;
    if !tool.read_only && !settings.write_tools.get(name).copied().unwrap_or(false) {
        return Err(AppError::Forbidden(format!(
            "工具 {name} 是写操作，请在 NexTerm 设置里开启该 MCP 工具权限"
        )));
    }

    let sid = args
        .get("session_id")
        .and_then(|v| v.as_str())
        .unwrap_or("");
    let session = state.sessions.get(sid).await?;
    let transport = session.transport().await;

    match name {
        "list_assets" => {
            let list = state.store.asset_list(false).await?;
            Ok(serde_json::to_string_pretty(&list)?)
        }
        "exec_command" => {
            let cmd = args.get("command").and_then(|v| v.as_str()).unwrap_or("");
            let out = transport
                .exec(cmd, std::time::Duration::from_secs(120))
                .await?;
            let _ = state
                .store
                .audit_insert(crate::store::AuditInput {
                    session_id: Some(sid.to_string()),
                    asset_id: None,
                    source: "ai",
                    kind: "exec",
                    payload: json!({ "via": "mcp", "command": cmd }),
                    exit_code: out.exit_code,
                    duration_ms: Some(out.duration_ms as i64),
                })
                .await;
            Ok(serde_json::to_string_pretty(&out)?)
        }
        "read_screen" => {
            let tab_id = args.get("tab_id").and_then(|v| v.as_str()).unwrap_or("");
            let tab = state.sessions.get_tab(tab_id).await?;
            Ok(tab.screen_text())
        }
        "read_file" => {
            let path = args.get("path").and_then(|v| v.as_str()).unwrap_or("");
            let fs = transport.fs().await?;
            let data = fs.read_file(path, 120 * 1024).await?;
            Ok(String::from_utf8_lossy(&data).into_owned())
        }
        "docker_ps" => {
            let list = crate::docker::cli::ps(&*transport).await?;
            Ok(serde_json::to_string_pretty(&list)?)
        }
        _ => Err(AppError::NotFound(format!("工具 {name}"))),
    }
}

/// 读取 MCP 设置（缺省关闭）。
pub async fn mcp_settings(state: &AppState) -> McpSettings {
    state
        .store
        .setting_get("mcp.settings")
        .await
        .ok()
        .flatten()
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default()
}

/// 保存 MCP 设置。
pub async fn save_mcp_settings(state: &AppState, settings: &McpSettings) -> AppResult<()> {
    state
        .store
        .setting_set("mcp.settings", &serde_json::to_string(settings)?)
        .await
}

/// 启动 MCP 后台通道：stdio + HTTP。
pub async fn start(state: Arc<AppState>) {
    let settings = mcp_settings(&state).await;
    if !settings.enabled || settings.http_port == 0 {
        return;
    }
    let port = settings.http_port;
    let token = settings.token;
    let state2 = Arc::clone(&state);
    tokio::spawn(async move {
        if let Err(e) = run_http(state2, port, token).await {
            tracing::error!(target: "mcp", error = %e, "MCP HTTP 服务启动失败");
        }
    });
    // stdio 通道占用进程 stdio，桌面应用不启用；仅 CLI 模式（NEXTERM_MCP_STDIO=1）开启
    if std::env::var("NEXTERM_MCP_STDIO").ok().as_deref() == Some("1") {
        let state3 = Arc::clone(&state);
        tokio::spawn(async move {
            run_stdio(state3).await;
        });
    }
}

/// streamable HTTP 通道（极简 POST 端点，token 校验）。
async fn run_http(state: Arc<AppState>, port: u16, token: String) -> AppResult<()> {
    let app = axum::Router::new()
        .route(
            "/mcp",
            axum::routing::post(
                move |axum::extract::State(st): axum::extract::State<Arc<AppState>>,
                      headers: axum::http::HeaderMap,
                      body: String| async move {
                    let authorized = check_token(&headers, &token);
                    let resp = handle_rpc(&st, &body, authorized).await;
                    (
                        [(axum::http::header::CONTENT_TYPE, "application/json")],
                        resp,
                    )
                },
            ),
        )
        .with_state(state);
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port))
        .await
        .map_err(|e| AppError::Internal(format!("MCP 端口 {port} 绑定失败: {e}")))?;
    tracing::info!(target: "mcp", port, "MCP HTTP 已启动");
    axum::serve(listener, app)
        .await
        .map_err(|e| AppError::Internal(format!("MCP serve 失败: {e}")))?;
    Ok(())
}

fn check_token(headers: &axum::http::HeaderMap, token: &str) -> bool {
    if token.is_empty() {
        return false;
    }
    headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .map(|v| v == format!("Bearer {token}"))
        .unwrap_or(false)
}

/// stdio 通道（JSON-RPC 行协议）。
async fn run_stdio(state: Arc<AppState>) {
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let stdin = tokio::io::stdin();
    let mut reader = BufReader::new(stdin);
    let mut stdout = tokio::io::stdout();
    let mut line = String::new();
    loop {
        line.clear();
        match reader.read_line(&mut line).await {
            Ok(0) | Err(_) => break,
            Ok(_) => {
                let resp = handle_rpc(&state, line.trim(), true).await;
                let _ = stdout.write_all(resp.as_bytes()).await;
                let _ = stdout.write_all(b"\n").await;
                let _ = stdout.flush().await;
            }
        }
    }
}

/// 供命令层使用的运行时状态占位（避免 unused）。
pub type SharedState = Arc<RwLock<()>>;

// ───────────────────── M4-T2：一键写入外部 AI 工具配置 ─────────────────────

/// 支持的写入目标。VS Code 的 MCP 配置是工作区级的（`.vscode/mcp.json`），
/// 没有稳定的用户级路径，故不在此列 —— 那类用户用「复制 JSON」按钮更好。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum McpClientTarget {
    /// Claude Code 用户级配置 `~/.claude.json`
    ClaudeCode,
    /// Claude Desktop 配置（平台各自的标准位置）
    ClaudeDesktop,
    /// Cursor 用户级配置 `~/.cursor/mcp.json`
    Cursor,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct McpClientWriteResult {
    pub target: String,
    pub path: String,
    /// 目标文件是本次新建的（此前不存在）
    pub created: bool,
    /// 覆盖前的备份路径（文件此前已存在时才有）
    pub backup: Option<String>,
    /// 实际写入的 nexterm 片段（供 UI 展示）
    pub snippet: serde_json::Value,
}

/// 目标配置文件路径。
fn client_config_path(target: McpClientTarget) -> AppResult<std::path::PathBuf> {
    let home = dirs::home_dir().ok_or_else(|| AppError::Internal("取不到用户主目录".into()))?;
    let p = match target {
        McpClientTarget::ClaudeCode => home.join(".claude.json"),
        McpClientTarget::Cursor => home.join(".cursor").join("mcp.json"),
        McpClientTarget::ClaudeDesktop => {
            if cfg!(windows) {
                dirs::config_dir()
                    .ok_or_else(|| AppError::Internal("取不到 APPDATA".into()))?
                    .join("Claude")
                    .join("claude_desktop_config.json")
            } else if cfg!(target_os = "macos") {
                home.join("Library")
                    .join("Application Support")
                    .join("Claude")
                    .join("claude_desktop_config.json")
            } else {
                dirs::config_dir()
                    .ok_or_else(|| AppError::Internal("取不到配置目录".into()))?
                    .join("Claude")
                    .join("claude_desktop_config.json")
            }
        }
    };
    Ok(p)
}

/// 生成写入外部工具的 `mcpServers.nexterm` 片段。
///
/// 用 HTTP 传输而非 stdio：NexTerm 是常驻 GUI，MCP 客户端若用 stdio 会另起一个
/// 空进程，反而连不到正在跑的会话；HTTP 才能连上当前实例。
pub fn client_config_snippet(settings: &McpSettings) -> serde_json::Value {
    json!({
        "nexterm": {
            "type": "http",
            "url": format!("http://127.0.0.1:{}/mcp", settings.http_port),
            "headers": { "Authorization": format!("Bearer {}", settings.token) }
        }
    })
}

/// 合并写入目标配置文件：保留其它 server，先备份，绝不整文件覆盖。
///
/// 若目标文件存在但**不是合法 JSON**，直接报错返回，不做任何写入 ——
/// 宁可失败也不能破坏用户已有的配置。
pub async fn write_client_config(
    state: &AppState,
    target: McpClientTarget,
) -> AppResult<McpClientWriteResult> {
    let settings = mcp_settings(state).await;
    if settings.token.is_empty() || settings.http_port == 0 {
        return Err(AppError::param(
            "MCP 尚未配置：请先启用并生成 token（端口与 token 都必需）",
        ));
    }
    let snippet = client_config_snippet(&settings);
    let path = client_config_path(target)?;

    let existed = path.exists();
    let mut root: serde_json::Value = if existed {
        let raw = tokio::fs::read_to_string(&path)
            .await
            .map_err(|e| AppError::Internal(format!("读取 {} 失败: {e}", path.display())))?;
        if raw.trim().is_empty() {
            json!({})
        } else {
            serde_json::from_str(&raw).map_err(|e| {
                AppError::param(format!(
                    "{} 不是合法 JSON（{e}），为避免破坏已有配置已中止写入",
                    path.display()
                ))
            })?
        }
    } else {
        json!({})
    };

    // 备份原文件（存在才备份）
    let backup = if existed {
        let b = path.with_extension("nexterm.bak");
        tokio::fs::copy(&path, &b)
            .await
            .map_err(|e| AppError::Internal(format!("备份失败: {e}")))?;
        Some(b.to_string_lossy().to_string())
    } else {
        None
    };

    let obj = root
        .as_object_mut()
        .ok_or_else(|| AppError::param("目标配置的顶层不是 JSON 对象，已中止写入"))?;
    let servers = obj
        .entry("mcpServers")
        .or_insert_with(|| json!({}))
        .as_object_mut()
        .ok_or_else(|| AppError::param("目标配置的 mcpServers 不是对象，已中止写入"))?;
    servers.insert("nexterm".into(), snippet.clone());

    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent)
            .await
            .map_err(|e| AppError::Internal(format!("创建目录失败: {e}")))?;
    }
    let text = serde_json::to_string_pretty(&root)?;
    tokio::fs::write(&path, text)
        .await
        .map_err(|e| AppError::Internal(format!("写入 {} 失败: {e}", path.display())))?;

    tracing::info!(target: "mcp", path = %path.display(), "已写入 MCP 客户端配置");
    Ok(McpClientWriteResult {
        target: match target {
            McpClientTarget::ClaudeCode => "claude_code",
            McpClientTarget::ClaudeDesktop => "claude_desktop",
            McpClientTarget::Cursor => "cursor",
        }
        .into(),
        path: path.to_string_lossy().to_string(),
        created: !existed,
        backup,
        snippet,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snippet_shape_is_http_with_bearer() {
        let s = McpSettings {
            enabled: true,
            http_port: 8799,
            token: "abc123".into(),
            write_tools: HashMap::new(),
        };
        let v = client_config_snippet(&s);
        assert_eq!(v["nexterm"]["type"], "http");
        assert_eq!(v["nexterm"]["url"], "http://127.0.0.1:8799/mcp");
        assert_eq!(v["nexterm"]["headers"]["Authorization"], "Bearer abc123");
    }

    #[test]
    fn client_paths_are_under_home() {
        for t in [
            McpClientTarget::ClaudeCode,
            McpClientTarget::ClaudeDesktop,
            McpClientTarget::Cursor,
        ] {
            let p = client_config_path(t).expect("path");
            assert!(p.is_absolute(), "{:?} 应为绝对路径", t);
        }
    }
}
