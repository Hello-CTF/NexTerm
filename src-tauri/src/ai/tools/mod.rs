//! 工具注册与分发（§8.2）：每个工具的返回都带 exit_code 与 truncated 标记。

pub mod db;
pub mod docker;
pub mod meta;
pub mod server;

use std::sync::Arc;

use crate::state::AppState;

use super::{AiJob, AiScope};

/// 工具执行输出。
#[derive(Debug, Clone)]
pub struct ToolOutput {
    pub ok: bool,
    pub text: String,
    pub exit_code: Option<i32>,
    pub truncated: bool,
}

impl ToolOutput {
    pub fn ok(text: impl Into<String>) -> Self {
        Self {
            ok: true,
            text: text.into(),
            exit_code: Some(0),
            truncated: false,
        }
    }
    pub fn fail(text: impl Into<String>) -> Self {
        Self {
            ok: false,
            text: text.into(),
            exit_code: Some(1),
            truncated: false,
        }
    }
}

/// v1 工具 schema 集（server 8 个 + docker 4 + db 4 + meta 2）。
pub fn all_tools() -> Vec<super::provider::ToolSchema> {
    let mut v = server::schemas();
    v.extend(docker::schemas());
    v.extend(db::schemas());
    v.extend(meta::schemas());
    v
}

/// 按名称分发执行。
pub async fn execute(
    state: &AppState,
    scope: &AiScope,
    job: &Arc<AiJob>,
    name: &str,
    args: &serde_json::Value,
) -> ToolOutput {
    match name {
        // server.rs
        "exec_commands" => server::exec_commands(state, scope, args).await,
        "read_file" => server::read_file(state, scope, args).await,
        "write_file" => server::write_file(state, scope, args).await,
        "list_dir" => server::list_dir(state, scope, args).await,
        "search_files" => server::search_files(state, scope, args).await,
        "read_screen" => server::read_screen(state, scope, args).await,
        "send_keys" => server::send_keys(state, scope, args).await,
        "wait_for" => server::wait_for(state, scope, args, job).await,
        // docker.rs
        "docker_ps" => docker::docker_ps(state, scope).await,
        "docker_logs" => docker::docker_logs(state, scope, args).await,
        "docker_exec" => docker::docker_exec(state, scope, args).await,
        "docker_control" => docker::docker_control(state, scope, args).await,
        // db.rs
        "db_list_tables" => db::db_list_tables(state, scope, args).await,
        "db_describe" => db::db_describe(state, scope, args).await,
        "db_query" => db::db_query(state, scope, args).await,
        "redis_scan" => db::redis_scan_tool(state, scope, args).await,
        // meta.rs
        "list_assets" => meta::list_assets(state).await,
        "ask_user" => meta::ask_user(args),
        _ => ToolOutput::fail(format!("未知工具 {name}")),
    }
}

/// 取作用域内的 transport（带断连显式错误，§6.3）。
pub async fn scoped_transport(
    state: &AppState,
    scope: &AiScope,
) -> Result<std::sync::Arc<dyn crate::transport::Transport>, ToolOutput> {
    let sid = scope.session_id.as_ref().ok_or_else(|| {
        ToolOutput::fail("当前没有已连接的会话。请先在资产树连接一台机器，或让用户指定目标。")
    })?;
    let session = state
        .sessions
        .get(sid)
        .await
        .map_err(|e| ToolOutput::fail(format!("会话不存在: {e}")))?;
    let t = session.transport().await;
    Ok(t)
}

/// AI 执行的命令实时汇入用户终端（§6.1 transparency 铁律）。
pub async fn echo_to_tab(state: &AppState, scope: &AiScope, text: &str) {
    if let Some(tid) = &scope.tab_id {
        if let Ok(tab) = state.sessions.get_tab(tid).await {
            let payload = format!("\x1b[36m[AI] {text}\x1b[0m");
            tab.feed_output(payload.as_bytes());
            let _ = tab.send_to_frontend(payload.into_bytes()).await;
        }
    }
}

/// DB 连接引用解析。
pub async fn scoped_db(state: &AppState, scope: &AiScope) -> Result<crate::db::DbRef, ToolOutput> {
    let cid = scope
        .conn_id
        .as_ref()
        .ok_or_else(|| ToolOutput::fail("当前没有数据库连接。请先在数据库面板建立连接。"))?;
    state
        .db
        .get(cid)
        .await
        .map_err(|e| ToolOutput::fail(format!("数据库连接不可用: {e}")))
}
