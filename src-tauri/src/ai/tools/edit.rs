//! 文件编辑方式 + 长任务体验（本轮扩展）：`edit_file` / `todo_write` / `exit_plan_mode`。
//!
//! 三件事放一个模块，是因为它们共用同一份「本次任务的状态」——任务级注册表：
//!   · 改文件前必须读过（先读后写门禁）；
//!   · 任务清单（todo）；
//!   · 待评审的计划（plan mode 提交的产物）。
//!
//! 为什么要按任务维度而不是全局维度存：同一个进程里可能同时跑多个 AI 任务，
//! 全局一份会让 A 任务的「读过」变成 B 任务的通行证，门禁就形同虚设。
//! 键用 `AiJob.id`（该字段已存在，无需给 AiJob 加字段）。

use std::collections::{HashMap, HashSet};
use std::sync::{Mutex, OnceLock};

use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::state::AppState;

use super::{scoped_transport, ToolOutput};

/* ── 任务级状态 ───────────────────────────────────────────────────── */

/// 清单项状态。用 snake_case 与工具入参、前端约定一致。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TodoStatus {
    Pending,
    InProgress,
    Completed,
}

/// 清单项。序列化给前端用 camelCase（与项目其余 DTO 对齐）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TodoItem {
    pub content: String,
    pub status: TodoStatus,
}

/// 单个任务的运行时状态。
#[derive(Default)]
struct TaskState {
    /// 读过（或尝试读过）的文件路径。
    read: HashSet<String>,
    /// 最近一次 todo_write 提交的**完整**清单。
    todos: Vec<TodoItem>,
    /// exit_plan_mode 提交的计划原文。
    plan: Option<String>,
}

fn registry() -> &'static Mutex<HashMap<String, TaskState>> {
    static REG: OnceLock<Mutex<HashMap<String, TaskState>>> = OnceLock::new();
    REG.get_or_init(|| Mutex::new(HashMap::new()))
}

/// 统一加锁入口：锁中毒（某处 panic 后）也继续用里面的数据，
/// 不在这条路径上二次 panic —— 编排状态丢掉一个任务，比整个内核崩了强。
fn lock() -> std::sync::MutexGuard<'static, HashMap<String, TaskState>> {
    registry().lock().unwrap_or_else(|e| e.into_inner())
}

/// 路径规范化。只做「去首尾空白 + 去掉冗余的 `./` 前缀」这种无损变换，
/// 不解析软链接/相对路径 —— 过度归一会把 `/a/b` 与 `/a/./b` 当成两个文件，
/// 也可能把真的不同路径合并，两边都是错的。
fn normalize_path(path: &str) -> String {
    let p = path.trim();
    let p = p.strip_prefix("./").unwrap_or(p);
    p.to_string()
}

/* ── 第 2 条：先读后写门禁 ────────────────────────────────────────── */

/// 登记「这个任务已经看过这个文件」。
///
/// 读**成功**与读**失败**都登记：对新建文件来说，「读不到」正是模型需要知道的全部信息；
/// 若只有成功才登记，模型将永远无法创建一个还不存在的文件（门禁会把它锁死）。
pub fn mark_read(job_id: &str, path: &str) {
    lock()
        .entry(job_id.to_string())
        .or_default()
        .read
        .insert(normalize_path(path));
}

/// 该任务是否已经看过这个文件。
pub fn was_read(job_id: &str, path: &str) -> bool {
    lock()
        .get(job_id)
        .map(|s| s.read.contains(&normalize_path(path)))
        .unwrap_or(false)
}

/// 写前门禁：未读过的文件返回拒绝输出，已读过返回 None。
///
/// 抽成独立函数是为了让 write_file 与 edit_file 走**同一条**判断，
/// 避免两处各写一遍后语义漂移（比如其中一处悄悄放宽了）。
pub fn read_gate(job_id: &str, args: &serde_json::Value) -> Option<ToolOutput> {
    // 没有 path 时不拦截，交给具体工具报「缺少 path」——门禁不该替工具校验参数。
    let path = args.get("path").and_then(|v| v.as_str())?;
    if was_read(job_id, path) {
        return None;
    }
    Some(ToolOutput::fail(format!(
        "拒绝执行：本次任务还没有读过 {path}。请先用 read_file 读取该文件、确认当前内容后再改。\
         没看过内容就写入等于盲改，这个门禁是硬性的。"
    )))
}

/// 任务结束时清理状态，避免注册表随任务数无限增长。
pub fn clear_task(job_id: &str) {
    lock().remove(job_id);
}

/* ── 第 1 条：edit_file 的唯一性判定（纯函数）─────────────────────── */

/// 一次编辑的判定结果。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EditOutcome {
    /// 替换成功：新内容 + 实际替换处数。
    Replaced { content: String, count: usize },
    /// old_string 为空（无意义，且 `matches("")` 计数会失真，必须先挡掉）。
    EmptyOld,
    /// 文件里找不到 old_string。
    NotFound,
    /// 命中多处且未开 replace_all。
    Ambiguous { count: usize },
}

/// 精确替换判定：`old` 必须在 `haystack` 中恰好出现一次（除非 replace_all）。
///
/// 这是整个 edit_file 的核心价值所在，所以做成纯函数、单独测。
pub fn apply_edit(haystack: &str, old: &str, new: &str, replace_all: bool) -> EditOutcome {
    if old.is_empty() {
        return EditOutcome::EmptyOld;
    }
    let count = haystack.matches(old).count();
    if count == 0 {
        return EditOutcome::NotFound;
    }
    if count > 1 && !replace_all {
        return EditOutcome::Ambiguous { count };
    }
    let content = if replace_all {
        haystack.replace(old, new)
    } else {
        haystack.replacen(old, new, 1)
    };
    EditOutcome::Replaced { content, count }
}

/* ── 第 3 条：plan mode 纯函数判定 ────────────────────────────────── */

/// 计划模式下允许的工具：只读探查 + 清单维护 + 提交计划。
///
/// 刻意**不放行 `exec_commands`**：它在 guard 里可能被判成只读，
/// 但「白名单里的只读命令」和「真的只读了」是两回事（管道、重定向、参数注入
/// 都可能溜过去）。计划模式的语义是「先看，不许动手」，动作一律挡。
pub const PLAN_MODE_ALLOWED: &[&str] = &[
    "read_file",
    "list_dir",
    "search_files",
    "read_screen",
    "wait_for",
    "list_assets",
    "docker_ps",
    "docker_logs",
    "db_list_tables",
    "db_describe",
    "redis_scan",
    "ask_user",
    "todo_write",
    "exit_plan_mode",
];

/// 计划模式拦截判定：允许的工具返回 None，其余返回拒绝原因（给模型看）。
///
/// 是纯函数、不认识 job —— 真正的拦截动作在 agent 循环里做（公共主干）。
pub fn blocked_in_plan_mode(tool: &str) -> Option<&'static str> {
    if PLAN_MODE_ALLOWED.contains(&tool) {
        return None;
    }
    Some(
        "当前处于计划模式：只允许只读探查、维护任务清单或提交计划\
         （read_file / list_dir / search_files / todo_write / exit_plan_mode 等）。\
         请先只读调研，把完整方案写进 exit_plan_mode 交给用户评审；\
         不要执行任何写入、命令或状态变更。",
    )
}

/* ── todo_write ──────────────────────────────────────────────────── */

/// 解析并校验 todo_write 的入参。
///
/// 入参是**完整列表**（每次整份替换）；这里逐项校验并给出「第几项错在哪」，
/// 而不是含糊地说「参数非法」—— 模型据此一次就能改对，少浪费一轮。
pub fn parse_todos(value: Option<&serde_json::Value>) -> Result<Vec<TodoItem>, String> {
    let arr = value
        .and_then(|v| v.as_array())
        .ok_or_else(|| "todos 必须是数组（每次传入完整列表，整份替换，不是增量）".to_string())?;
    if arr.len() > 50 {
        return Err(format!("todos 最多 50 项，当前 {}", arr.len()));
    }
    let mut out = Vec::with_capacity(arr.len());
    for (i, item) in arr.iter().enumerate() {
        let content = item
            .get("content")
            .and_then(|c| c.as_str())
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .ok_or_else(|| format!("第 {} 项缺少 content（不能为空字符串）", i + 1))?;
        let status = match item
            .get("status")
            .and_then(|s| s.as_str())
            .unwrap_or("pending")
        {
            "pending" => TodoStatus::Pending,
            "in_progress" => TodoStatus::InProgress,
            "completed" => TodoStatus::Completed,
            other => {
                return Err(format!(
                    "第 {} 项 status 非法：{other}（只能是 pending / in_progress / completed）",
                    i + 1
                ))
            }
        };
        out.push(TodoItem {
            content: content.to_string(),
            status,
        });
    }
    Ok(out)
}

/// 把清单渲染成一段紧凑文本，回给模型 —— 让模型看到自己提交的清单被记下了。
fn render_todos(todos: &[TodoItem]) -> String {
    if todos.is_empty() {
        return "已清空任务清单。".to_string();
    }
    use TodoStatus::*;
    let mut s = format!("已更新任务清单（{} 项）：", todos.len());
    for t in todos {
        let mark = match t.status {
            Pending => "[ ]",
            InProgress => "[~]",
            Completed => "[x]",
        };
        s.push('\n');
        s.push_str(&format!("{mark} {}", t.content));
    }
    s
}

/// 覆盖式写入任务清单（整份替换）。
pub fn todo_write(job_id: &str, args: &serde_json::Value) -> ToolOutput {
    let todos = match parse_todos(args.get("todos")) {
        Ok(t) => t,
        Err(e) => return ToolOutput::fail(e),
    };
    lock().entry(job_id.to_string()).or_default().todos = todos.clone();
    ToolOutput::ok(render_todos(&todos))
}

/// 查询某个任务当前的清单（供主线程每轮推给前端）。
pub fn todos_for(job_id: &str) -> Vec<TodoItem> {
    lock()
        .get(job_id)
        .map(|s| s.todos.clone())
        .unwrap_or_default()
}

/* ── exit_plan_mode ─────────────────────────────────────────────── */

/// 提交完整计划，交给用户评审。
///
/// 工具本身只负责「把计划内容交出来并记下来」；评审与放行逻辑在 agent 侧
/// （公共主干）。工具返回后模型应停止改动等待，这一点写进返回文案里。
pub fn exit_plan_mode(job_id: &str, args: &serde_json::Value) -> ToolOutput {
    let plan = args
        .get("plan")
        .and_then(|v| v.as_str())
        .map(str::trim)
        .unwrap_or("");
    if plan.is_empty() {
        return ToolOutput::fail("plan 不能为空：请把完整计划写进来再提交。");
    }
    lock().entry(job_id.to_string()).or_default().plan = Some(plan.to_string());
    ToolOutput::ok(
        "计划已提交，等待用户评审。提交后请不要继续执行任何修改动作，\
         等用户确认或提出修改意见。",
    )
}

/// 取某个任务提交的计划原文（主线程据此弹评审）。
pub fn plan_for(job_id: &str) -> Option<String> {
    lock().get(job_id).and_then(|s| s.plan.clone())
}

/* ── schemas ────────────────────────────────────────────────────── */

pub fn schemas() -> Vec<crate::ai::provider::ToolSchema> {
    vec![
        s(
            "edit_file",
            "对远端文本文件做精确替换：old_string 必须在文件里**恰好出现一次**，否则拒绝执行。\
             改文件前必须先用 read_file 读过该文件。只适合改动局部；大范围重写用 write_file。",
            json!({"type":"object","properties":{
                "path":{"type":"string","description":"目标文件路径"},
                "old_string":{"type":"string","description":"要被替换的原文，需包含足够上下文以保证在文件中唯一"},
                "new_string":{"type":"string","description":"替换后的新内容"},
                "replace_all":{"type":"boolean","description":"为 true 时替换全部匹配（默认 false，要求唯一匹配）"},
            },"required":["path","old_string","new_string"]}),
        ),
        s(
            "todo_write",
            "维护本次任务的任务清单。每次传入**完整列表**（整份替换，不是增量），\
             每项为 {content, status}，status 取 pending / in_progress / completed。",
            json!({"type":"object","properties":{
                "todos":{"type":"array","description":"完整的任务清单","items":{
                    "type":"object","properties":{
                        "content":{"type":"string"},
                        "status":{"type":"string","enum":["pending","in_progress","completed"]},
                    },"required":["content","status"]},
                },
            },"required":["todos"]}),
        ),
        s(
            "exit_plan_mode",
            "提交完整方案并结束计划阶段，交给用户评审。计划模式下（只读调研后）用它交出方案；\
             提交后不要再执行任何修改动作。",
            json!({"type":"object","properties":{
                "plan":{"type":"string","description":"完整计划，包含目标、步骤、涉及文件与风险点"},
            },"required":["plan"]}),
        ),
    ]
}

fn s(name: &str, desc: &str, params: serde_json::Value) -> crate::ai::provider::ToolSchema {
    crate::ai::provider::ToolSchema {
        name: name.into(),
        description: desc.into(),
        parameters: params,
    }
}

/* ── edit_file 实现 ─────────────────────────────────────────────── */

fn arg_str<'a>(args: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    args.get(key).and_then(|v| v.as_str())
}

/// edit_file 能处理的单文件上限。精确替换要求把整个文件读进内存后重写，
/// 因此在读这一层就设上限，而不是读到一半再截断（截断的文本做替换会写坏文件）。
const EDIT_MAX_BYTES: u64 = 2 * 1024 * 1024;

pub async fn edit_file(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let (Some(path), Some(old), Some(new)) = (
        arg_str(args, "path"),
        arg_str(args, "old_string"),
        arg_str(args, "new_string"),
    ) else {
        return ToolOutput::fail("缺少 path/old_string/new_string");
    };
    let replace_all = args
        .get("replace_all")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    // 同字面量替换是纯粹的无效动作，大概率是模型笔误 —— 直接挡掉，省一次往返。
    if old == new {
        return ToolOutput::fail(
            "old_string 与 new_string 相同，不会产生任何变化；如需确认内容请用 read_file。",
        );
    }
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let fs = match transport.fs().await {
        Ok(f) => f,
        Err(e) => return ToolOutput::fail(format!("文件系统不可用: {e}")),
    };
    let data = match fs.read_file(path, EDIT_MAX_BYTES).await {
        Ok(d) => d,
        Err(e) => return ToolOutput::fail(format!("读取失败: {e}")),
    };
    let text = match String::from_utf8(data) {
        Ok(t) => t,
        Err(_) => {
            return ToolOutput::fail(format!(
                "{path} 不是有效的 UTF-8 文本，edit_file 只处理文本文件。"
            ))
        }
    };
    let (content, count) = match apply_edit(&text, old, new, replace_all) {
        EditOutcome::Replaced { content, count } => (content, count),
        EditOutcome::EmptyOld => return ToolOutput::fail("old_string 不能为空。"),
        EditOutcome::NotFound => {
            return ToolOutput::fail(format!(
                "文件里没有这段内容，可能已经改过了，请先用 read_file 读一次 {path} 确认当前内容。"
            ))
        }
        EditOutcome::Ambiguous { count } => {
            return ToolOutput::fail(format!(
                "这段内容在文件里出现 {count} 次，请带上更多上下文让它唯一；\
                 若确实要全部替换，请显式设置 replace_all=true。"
            ))
        }
    };
    // backup=true：沿用 write_file 的「保存前自动备份」行为。
    match fs.write_file(path, content.as_bytes(), true).await {
        Ok(()) => {
            let _ = state
                .store
                .audit_insert(crate::store::AuditInput {
                    session_id: scope.session_id.clone(),
                    asset_id: scope.asset_id.clone(),
                    source: "ai",
                    kind: "edit_file",
                    payload: json!({ "path": path, "replacements": count }),
                    exit_code: Some(0),
                    duration_ms: None,
                })
                .await;
            ToolOutput::ok(format!("已编辑 {path}（替换 {count} 处，已备份原文件）"))
        }
        Err(e) => ToolOutput::fail(format!("写入失败: {e}")),
    }
}

/* ── 单测（纯函数 + 注册表）─────────────────────────────────────── */

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn t01_apply_edit_unique_ok() {
        let hay = "fn main() {\n    let a = 1;\n}\n";
        match apply_edit(hay, "let a = 1;", "let a = 2;", false) {
            EditOutcome::Replaced { content, count } => {
                assert_eq!(count, 1);
                assert!(content.contains("let a = 2;"));
                assert!(!content.contains("let a = 1;"));
            }
            other => panic!("应替换成功，实际 {other:?}"),
        }
    }

    #[test]
    fn t02_apply_edit_not_found() {
        assert_eq!(apply_edit("abc", "zzz", "y", false), EditOutcome::NotFound);
    }

    #[test]
    fn t03_apply_edit_ambiguous_refused_unless_replace_all() {
        let hay = "x\nx\n";
        assert_eq!(
            apply_edit(hay, "x", "y", false),
            EditOutcome::Ambiguous { count: 2 }
        );
        match apply_edit(hay, "x", "y", true) {
            EditOutcome::Replaced { content, count } => {
                assert_eq!(count, 2);
                assert_eq!(content, "y\ny\n");
            }
            other => panic!("replace_all 应全部替换，实际 {other:?}"),
        }
    }

    #[test]
    fn t04_apply_edit_empty_old_is_rejected() {
        // `matches("")` 会给出 len+1 这种失真计数，必须在判定前挡掉
        assert_eq!(apply_edit("abc", "", "y", false), EditOutcome::EmptyOld);
        assert_eq!(apply_edit("abc", "", "y", true), EditOutcome::EmptyOld);
    }

    #[test]
    fn t05_apply_edit_multiline_context() {
        let hay = "a\nb\nc\n";
        match apply_edit(hay, "b\nc", "B\nC", false) {
            EditOutcome::Replaced { content, .. } => assert_eq!(content, "a\nB\nC\n"),
            other => panic!("多行替换应成功，实际 {other:?}"),
        }
    }

    #[test]
    fn t06_parse_todos_valid_and_defaults() {
        let v = json!([
            {"content": "读代码", "status": "completed"},
            {"content": "改代码", "status": "in_progress"},
            {"content": "少写 status"}
        ]);
        let todos = parse_todos(Some(&v)).expect("应解析成功");
        assert_eq!(todos.len(), 3);
        assert_eq!(todos[0].status, TodoStatus::Completed);
        assert_eq!(todos[1].status, TodoStatus::InProgress);
        // 缺 status 默认 pending
        assert_eq!(todos[2].status, TodoStatus::Pending);
    }

    #[test]
    fn t07_parse_todos_rejects_bad_input() {
        assert!(parse_todos(None).is_err());
        assert!(parse_todos(Some(&json!({"content": "x"}))).is_err());
        assert!(parse_todos(Some(&json!([{"status": "pending"}]))).is_err());
        assert!(parse_todos(Some(&json!([{"content": "  ", "status": "pending"}]))).is_err());
        assert!(parse_todos(Some(&json!([{"content": "x", "status": "doing"}]))).is_err());
        // 空列表合法（表示清空）
        assert!(parse_todos(Some(&json!([])))
            .expect("空数组应合法")
            .is_empty());
    }

    #[test]
    fn t08_plan_mode_gate() {
        for ok in [
            "read_file",
            "list_dir",
            "search_files",
            "todo_write",
            "exit_plan_mode",
            "ask_user",
        ] {
            assert!(blocked_in_plan_mode(ok).is_none(), "{ok} 应放行");
        }
        for blocked in [
            "write_file",
            "edit_file",
            "exec_commands",
            "send_keys",
            "docker_control",
            "db_query",
            "unknown_tool",
        ] {
            assert!(blocked_in_plan_mode(blocked).is_some(), "{blocked} 应拦截");
        }
    }

    #[test]
    fn t09_read_gate_blocks_until_marked() {
        let job = "test-job-gate";
        clear_task(job);
        let args = json!({"path": "./etc/app.conf", "content": "x"});
        // 未读过 → 拒绝，且理由里要点出路径与 read_file
        let rej = read_gate(job, &args).expect("未读过应被拒绝");
        assert!(!rej.ok);
        assert!(rej.text.contains("read_file"));
        // 登记（含 ./ 前缀规范化）后放行
        mark_read(job, "/etc/app.conf");
        assert!(read_gate(job, &json!({"path": "/etc/app.conf"})).is_none());
        // 任务之间互不影响
        assert!(read_gate("test-job-gate-other", &json!({"path": "/etc/app.conf"})).is_some());
        clear_task(job);
    }

    #[test]
    fn t10_todos_and_plan_are_job_scoped() {
        let job = "test-job-todos";
        clear_task(job);
        assert!(todos_for(job).is_empty());
        let out = todo_write(
            job,
            &json!({"todos": [{"content": "第一步", "status": "in_progress"}]}),
        );
        assert!(out.ok);
        let list = todos_for(job);
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].status, TodoStatus::InProgress);
        // 整份替换：第二次只传一项，旧的那项必须消失
        let _ = todo_write(
            job,
            &json!({"todos": [{"content": "只此一项", "status": "pending"}]}),
        );
        assert_eq!(todos_for(job).len(), 1);

        assert!(plan_for(job).is_none());
        assert!(!exit_plan_mode(job, &json!({"plan": "   "})).ok);
        assert!(exit_plan_mode(job, &json!({"plan": "1. 查日志 2. 重启"})).ok);
        assert_eq!(plan_for(job).as_deref(), Some("1. 查日志 2. 重启"));
        clear_task(job);
    }
}
