//! 工具注册与分发（§8.2）：每个工具的返回都带 exit_code 与 truncated 标记。

pub mod db;
pub mod docker;
pub mod edit;
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

/// v1 工具 schema 集（server 8 个 + docker 4 + db 4 + meta 2 + edit 3）。
pub fn all_tools() -> Vec<super::provider::ToolSchema> {
    let mut v = server::schemas();
    v.extend(docker::schemas());
    v.extend(db::schemas());
    v.extend(meta::schemas());
    v.extend(edit::schemas());
    v
}

/// 工具卡片顶部那一行摘要：给**用户**看的「AI 刚才到底干了什么」。
///
/// 以前这里是直接把工具的 `description` 当摘要推给前端，于是每张卡片都顶着一整句
/// 「在目标主机批量执行命令（最多 20 条）。命令与输出会完整记录在对话面板的工具卡片里，
/// 用户看得见，不要执行无意义的探查命令。」—— 那是写给**模型**看的说明书，
/// 不是给用户看的标题：十张卡片长得一模一样，扫一眼根本分不出哪张是哪条命令。
///
/// 所以这里按「工具 + 实参」拼一句人能秒懂的话：哪条命令、哪个文件、哪张表、哪个容器。
/// 认不出来的工具返回空串，前端会退回工具名 —— 以后新增工具忘了登记，也不会露出空白卡片。
pub fn display_for(name: &str, args: &serde_json::Value) -> String {
    let s = |k: &str| args.get(k).and_then(|v| v.as_str()).unwrap_or("");
    let n = |k: &str| args.get(k).and_then(|v| v.as_u64());

    let text = match name {
        // ── server ──
        "exec_commands" => {
            let cmds: Vec<&str> = args
                .get("commands")
                .and_then(|v| v.as_array())
                .map(|a| a.iter().filter_map(|x| x.as_str()).collect())
                .unwrap_or_default();
            match cmds.len() {
                0 => "执行命令".into(),
                1 => cmds[0].to_string(),
                len => format!("{} 等 {len} 条命令", cmds[0]),
            }
        }
        "read_file" => format!("读取 {}", s("path")),
        "write_file" => format!("写入 {}", s("path")),
        "list_dir" => format!("列出目录 {}", s("path")),
        "search_files" => {
            if s("by") == "content" {
                format!("在 {} 中搜索内容 {}", s("path"), s("pattern"))
            } else {
                format!("在 {} 中查找文件 {}", s("path"), s("pattern"))
            }
        }
        "read_screen" => "读取当前终端屏幕".into(),
        "send_keys" => format!("向终端发送按键 {}", s("keys")),
        "wait_for" => format!("等待终端输出匹配 {}", s("pattern")),
        // ── docker ──
        "docker_ps" => "列出容器".into(),
        "docker_logs" => {
            let mut line = format!("查看容器 {} 的日志", s("container_id"));
            if let Some(tail) = n("tail") {
                line.push_str(&format!("（末尾 {tail} 行）"));
            }
            if !s("grep").is_empty() {
                line.push_str(&format!("，过滤 {}", s("grep")));
            }
            line
        }
        "docker_exec" => format!("在容器 {} 内执行 {}", s("container_id"), s("cmd")),
        "docker_control" => {
            let action = match s("action") {
                "start" => "启动",
                "stop" => "停止",
                "restart" => "重启",
                "rm" => "删除",
                other => other,
            };
            format!("{action}容器 {}", s("container_id"))
        }
        // ── db ──
        "db_list_tables" => "列出数据库的表".into(),
        "db_describe" => format!("查看表结构 {}", s("table")),
        "db_query" => s("sql").to_string(),
        "redis_scan" => {
            let pattern = s("pattern");
            if pattern.is_empty() {
                "扫描 Redis 键".into()
            } else {
                format!("扫描 Redis 键 {pattern}")
            }
        }
        // ── meta / edit ──
        "list_assets" => "列出资产".into(),
        "ask_user" => s("question").to_string(),
        "edit_file" => format!("修改 {}", s("path")),
        "todo_write" => "更新任务清单".into(),
        "exit_plan_mode" => "提交方案".into(),
        _ => String::new(),
    };

    cap_display(text)
}

/// 摘要截断（按字符，不按字节 —— 中文路径不能被切成半个字）。
///
/// 一条命令、一段 SQL 都可能几百字符还带换行。摘要只是卡片标题，
/// 完整内容在正文和「展开完整输出」里，标题不必把整段塞进 DOM 的 title。
fn cap_display(text: String) -> String {
    const MAX: usize = 200;
    let flat = text.replace(['\n', '\r'], " ");
    if flat.chars().count() <= MAX {
        return flat;
    }
    let head: String = flat.chars().take(MAX).collect();
    format!("{head}…")
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
        "exec_commands" => server::exec_commands(state, scope, job, args).await,
        // 读成功/失败都会登记到任务级注册表（先读后写门禁的依据）。
        "read_file" => server::read_file(state, scope, job, args).await,
        // 写前门禁：没读过的文件直接拒绝，不放行到工具实现里。
        // 判定收敛在这一处，是为了让 write_file 与 edit_file 走同一条语义。
        "write_file" => match edit::read_gate(&job.id, args) {
            Some(rej) => rej,
            None => server::write_file(state, scope, args).await,
        },
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
        // edit.rs
        "edit_file" => match edit::read_gate(&job.id, args) {
            Some(rej) => rej,
            None => edit::edit_file(state, scope, args).await,
        },
        "todo_write" => edit::todo_write(&job.id, args),
        "exit_plan_mode" => edit::exit_plan_mode(&job.id, args),
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

#[cfg(test)]
mod display_tests {
    use super::display_for;
    use serde_json::json;

    /// 卡片标题必须是"这次调用干了什么"，**不能**退回工具描述 ——
    /// 曾经每张卡片都顶着「在目标主机批量执行命令（最多 20 条）…」那句话，
    /// 十张卡片一模一样，扫一眼分不出哪张是哪条命令。
    #[test]
    fn exec_commands_shows_the_actual_command_not_the_description() {
        let one = display_for("exec_commands", &json!({"commands": ["docker ps -a"]}));
        assert_eq!(one, "docker ps -a");
        assert!(!one.contains("不要执行无意义的探查命令"));

        let many = display_for(
            "exec_commands",
            &json!({"commands": ["systemctl status nginx", "journalctl -n 50"]}),
        );
        assert_eq!(many, "systemctl status nginx 等 2 条命令");
    }

    /// 每种工具都要给出一句人话；认不出来的一律空串（前端退回工具名）。
    #[test]
    fn every_tool_has_a_human_readable_display() {
        let cases: Vec<(&str, serde_json::Value, &str)> = vec![
            (
                "read_file",
                json!({"path": "/etc/hosts"}),
                "读取 /etc/hosts",
            ),
            ("write_file", json!({"path": "/tmp/a"}), "写入 /tmp/a"),
            ("edit_file", json!({"path": "/tmp/a"}), "修改 /tmp/a"),
            ("list_dir", json!({"path": "/var/log"}), "列出目录 /var/log"),
            (
                "search_files",
                json!({"path": "/etc", "pattern": "listen", "by": "content"}),
                "在 /etc 中搜索内容 listen",
            ),
            ("read_screen", json!({}), "读取当前终端屏幕"),
            ("send_keys", json!({"keys": "ls"}), "向终端发送按键 ls"),
            (
                "wait_for",
                json!({"pattern": "^OK"}),
                "等待终端输出匹配 ^OK",
            ),
            ("docker_ps", json!({}), "列出容器"),
            (
                "docker_logs",
                json!({"container_id": "abc", "tail": 200}),
                "查看容器 abc 的日志（末尾 200 行）",
            ),
            (
                "docker_exec",
                json!({"container_id": "abc", "cmd": "ls"}),
                "在容器 abc 内执行 ls",
            ),
            (
                "docker_control",
                json!({"container_id": "abc", "action": "restart"}),
                "重启容器 abc",
            ),
            ("db_list_tables", json!({}), "列出数据库的表"),
            ("db_describe", json!({"table": "users"}), "查看表结构 users"),
            ("db_query", json!({"sql": "select 1"}), "select 1"),
            (
                "redis_scan",
                json!({"pattern": "sess:*"}),
                "扫描 Redis 键 sess:*",
            ),
            ("list_assets", json!({}), "列出资产"),
            (
                "ask_user",
                json!({"question": "用哪个端口？"}),
                "用哪个端口？",
            ),
            ("todo_write", json!({"todos": []}), "更新任务清单"),
            ("exit_plan_mode", json!({"plan": "..."}), "提交方案"),
        ];
        for (name, args, want) in cases {
            assert_eq!(display_for(name, &args), want, "工具 {name} 的摘要不对");
        }
        // 没登记的工具 → 空串（前端会退回工具名，而不是显示空白）
        assert_eq!(display_for("brand_new_tool", &json!({})), "");
    }

    /// 摘要会被塞进卡片的 DOM title 里，不能让一段 SQL / 命令把它撑爆；
    /// 截断必须按**字符**，中文路径不能被切成半个字。
    #[test]
    fn display_is_capped_by_chars_not_bytes() {
        let long = "日志".repeat(300);
        let got = display_for("db_query", &json!({ "sql": long }));
        assert_eq!(got.chars().count(), 201); // 200 字 + 省略号
        assert!(got.ends_with('…'));

        // 换行会被压平：卡片标题是一行，换行会让同一张卡片高矮不一
        let multi = display_for("db_query", &json!({ "sql": "select 1\nfrom t" }));
        assert_eq!(multi, "select 1 from t");
    }
}
