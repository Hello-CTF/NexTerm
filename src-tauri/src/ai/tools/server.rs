//! 服务器类工具（§8.2 server.rs，8 个）。

use serde_json::json;

use crate::ai::AiJob;
use crate::state::AppState;
use crate::terminal::keys;

use super::{echo_to_tab, scoped_transport, ToolOutput};

fn arg_str<'a>(args: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    args.get(key).and_then(|v| v.as_str())
}

fn arg_u64(args: &serde_json::Value, key: &str) -> Option<u64> {
    args.get(key).and_then(|v| v.as_u64())
}

pub fn schemas() -> Vec<crate::ai::provider::ToolSchema> {
    vec![
        s("exec_commands", "在目标主机批量执行命令（最多 20 条）。命令与输出会实时显示在用户终端里，不要执行无意义的探查命令。",
          json!({"type":"object","properties":{
            "commands":{"type":"array","items":{"type":"string"},"description":"要执行的命令列表"},
            "timeout":{"type":"number","description":"每条命令超时秒数，默认 60"},
          },"required":["commands"]})),
        s("read_file", "读取远端文件（自动截断并标记）。",
          json!({"type":"object","properties":{
            "path":{"type":"string"},"max_bytes":{"type":"number"},
          },"required":["path"]})),
        s("write_file", "写入远端文件（默认需用户确认；保存前自动备份）。",
          json!({"type":"object","properties":{
            "path":{"type":"string"},"content":{"type":"string"},
          },"required":["path","content"]})),
        s("list_dir", "列出目录内容（含大小/时间/权限）。",
          json!({"type":"object","properties":{"path":{"type":"string"}},"required":["path"]})),
        s("search_files", "按文件名或内容搜索（find/grep 封装）。",
          json!({"type":"object","properties":{
            "path":{"type":"string"},"pattern":{"type":"string"},
            "by":{"type":"string","enum":["name","content"]},
          },"required":["path","pattern","by"]})),
        s("read_screen", "读取当前终端屏幕内容（接管模式）。",
          json!({"type":"object","properties":{"tab_id":{"type":"string"}}})),
        s("send_keys", "向终端发送按键（接管模式）。特殊键用尖括号：<enter> <ctrl+c> <tab> <up>。",
          json!({"type":"object","properties":{
            "keys":{"type":"string"},"enter":{"type":"boolean"},
          },"required":["keys"]})),
        s("wait_for", "等待终端输出匹配正则（带超时）。",
          json!({"type":"object","properties":{
            "pattern":{"type":"string"},"timeout_ms":{"type":"number"},
          },"required":["pattern","timeout_ms"]})),
    ]
}

fn s(name: &str, desc: &str, params: serde_json::Value) -> crate::ai::provider::ToolSchema {
    crate::ai::provider::ToolSchema {
        name: name.into(),
        description: desc.into(),
        parameters: params,
    }
}

pub async fn exec_commands(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let cmds: Vec<String> = args
        .get("commands")
        .and_then(|v| v.as_array())
        .map(|a| {
            a.iter()
                .filter_map(|x| x.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default();
    if cmds.is_empty() {
        return ToolOutput::fail("commands 为空");
    }
    if cmds.len() > 20 {
        return ToolOutput::fail("一次最多 20 条命令（RainsIR 上限沿用）");
    }
    let timeout = std::time::Duration::from_secs(arg_u64(args, "timeout").unwrap_or(60).min(300));
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let mut report = String::new();
    let mut last_code: Option<i32> = None;
    for cmd in &cmds {
        echo_to_tab(state, scope, cmd).await;
        match transport.exec(cmd, timeout).await {
            Ok(out) => {
                last_code = out.exit_code;
                report.push_str(&format!("$ {cmd}\n{}", out.stdout));
                if !out.stderr.is_empty() {
                    report.push_str(&format!("[stderr]\n{}\n", out.stderr));
                }
                report.push_str(&format!("[exit_code={}]\n", out.exit_code.unwrap_or(-1)));
                if out.truncated {
                    report.push_str("[输出已截断]\n");
                }
            }
            Err(e) if e.is_disconnected() => {
                return ToolOutput::fail(format!(
                    "命令「{cmd}」执行失败：{e}\n连接已断开，请停止排查并提示用户重新连接。"
                ));
            }
            Err(e) => {
                report.push_str(&format!("$ {cmd}\n[执行失败: {e}]\n"));
            }
        }
    }
    ToolOutput {
        ok: true,
        text: report,
        exit_code: last_code,
        truncated: false,
    }
}

pub async fn read_file(
    state: &AppState,
    scope: &crate::ai::AiScope,
    job: &AiJob,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(path) = arg_str(args, "path") else {
        return ToolOutput::fail("缺少 path");
    };
    // 先读后写门禁的登记点：无论这次读成功与否都登记（详见 edit::mark_read 的注释），
    // 否则模型将永远无法创建还不存在的新文件。
    super::edit::mark_read(&job.id, path);
    let max = arg_u64(args, "max_bytes")
        .unwrap_or(120 * 1024)
        .min(1024 * 1024);
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let fs = match transport.fs().await {
        Ok(f) => f,
        Err(e) => return ToolOutput::fail(format!("文件系统不可用: {e}")),
    };
    match fs.read_file(path, max).await {
        Ok(data) => {
            let (text, truncated) = crate::transport::cap_text(&String::from_utf8_lossy(&data));
            ToolOutput {
                ok: true,
                text,
                exit_code: Some(0),
                truncated,
            }
        }
        Err(e) => ToolOutput::fail(format!("读取失败: {e}")),
    }
}

pub async fn write_file(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let (Some(path), Some(content)) = (arg_str(args, "path"), arg_str(args, "content")) else {
        return ToolOutput::fail("缺少 path/content");
    };
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let fs = match transport.fs().await {
        Ok(f) => f,
        Err(e) => return ToolOutput::fail(format!("文件系统不可用: {e}")),
    };
    // 审计 + 备份（§12.4：AI 修改文件前备份）
    match fs.write_file(path, content.as_bytes(), true).await {
        Ok(()) => {
            let _ = state
                .store
                .audit_insert(crate::store::AuditInput {
                    session_id: scope.session_id.clone(),
                    asset_id: scope.asset_id.clone(),
                    source: "ai",
                    kind: "write_file",
                    payload: json!({ "path": path, "bytes": content.len() }),
                    exit_code: Some(0),
                    duration_ms: None,
                })
                .await;
            ToolOutput::ok(format!(
                "已写入 {path}（{} 字节，已备份原文件）",
                content.len()
            ))
        }
        Err(e) => ToolOutput::fail(format!("写入失败: {e}")),
    }
}

pub async fn list_dir(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(path) = arg_str(args, "path") else {
        return ToolOutput::fail("缺少 path");
    };
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let fs = match transport.fs().await {
        Ok(f) => f,
        Err(e) => return ToolOutput::fail(format!("文件系统不可用: {e}")),
    };
    match fs.list(path).await {
        Ok(entries) => {
            let lines: Vec<String> = entries
                .iter()
                .take(500)
                .map(|e| {
                    format!(
                        "{} {:>10} {}",
                        if e.kind == "dir" { "d" } else { "-" },
                        e.size,
                        e.name
                    )
                })
                .collect();
            ToolOutput::ok(lines.join("\n"))
        }
        Err(e) => ToolOutput::fail(format!("列目录失败: {e}")),
    }
}

pub async fn search_files(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let (Some(path), Some(pattern)) = (arg_str(args, "path"), arg_str(args, "pattern")) else {
        return ToolOutput::fail("缺少 path/pattern");
    };
    let by = arg_str(args, "by").unwrap_or("name");
    // pattern 经 shell 引号包裹防注入
    let quoted = format!("'{}'", pattern.replace('\'', "'\\''"));
    let cmd = match by {
        "content" => format!(
            "grep -rn --binary-files=without-match -m 3 -I {quoted} {path} 2>/dev/null | head -100"
        ),
        _ => format!("find {path} -name {quoted} -maxdepth 6 2>/dev/null | head -100"),
    };
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    echo_to_tab(state, scope, &cmd).await;
    match transport
        .exec(&cmd, std::time::Duration::from_secs(30))
        .await
    {
        Ok(out) => ToolOutput {
            ok: true,
            text: if out.stdout.trim().is_empty() {
                "无匹配".into()
            } else {
                out.stdout
            },
            exit_code: out.exit_code,
            truncated: out.truncated,
        },
        Err(e) => ToolOutput::fail(format!("搜索失败: {e}")),
    }
}

pub async fn read_screen(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let tab_id = arg_str(args, "tab_id")
        .map(String::from)
        .or_else(|| scope.tab_id.clone());
    let Some(tab_id) = tab_id else {
        return ToolOutput::fail("没有可读取的终端标签");
    };
    match state.sessions.get_tab(&tab_id).await {
        Ok(tab) => {
            let snap = tab.snapshot();
            ToolOutput::ok(format!(
                "光标 ({}, {})，空闲 {}ms\n{}",
                snap.cursor_row, snap.cursor_col, snap.last_output_ms_ago, snap.text
            ))
        }
        Err(e) => ToolOutput::fail(format!("读屏失败: {e}")),
    }
}

pub async fn send_keys(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(keys_text) = arg_str(args, "keys") else {
        return ToolOutput::fail("缺少 keys");
    };
    let enter = args.get("enter").and_then(|v| v.as_bool()).unwrap_or(false);
    let tab_id = scope
        .tab_id
        .clone()
        .or_else(|| arg_str(args, "tab_id").map(String::from));
    let Some(tab_id) = tab_id else {
        return ToolOutput::fail("没有目标终端标签");
    };
    let Ok(tab) = state.sessions.get_tab(&tab_id).await else {
        return ToolOutput::fail("终端标签不存在");
    };
    let encoded = keys::encode_send(keys_text, enter);
    match tab.write(&encoded).await {
        Ok(()) => {
            // 审计（接管模式下每个 send_keys 都写日志，§8.6）
            let _ = state
                .store
                .audit_insert(crate::store::AuditInput {
                    session_id: Some(tab.session_id.clone()),
                    asset_id: scope.asset_id.clone(),
                    source: "ai",
                    kind: "takeover",
                    payload: json!({ "keys": keys_text, "enter": enter, "tab": tab_id }),
                    exit_code: Some(0),
                    duration_ms: None,
                })
                .await;
            ToolOutput::ok("已发送")
        }
        Err(e) => ToolOutput::fail(format!("发送失败: {e}")),
    }
}

pub async fn wait_for(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
    _job: &AiJob,
) -> ToolOutput {
    let Some(pattern) = arg_str(args, "pattern") else {
        return ToolOutput::fail("缺少 pattern");
    };
    let timeout_ms = arg_u64(args, "timeout_ms").unwrap_or(10_000).min(120_000);
    let tab_id = scope
        .tab_id
        .clone()
        .or_else(|| arg_str(args, "tab_id").map(String::from));
    let Some(tab_id) = tab_id else {
        return ToolOutput::fail("没有目标终端标签");
    };
    let Ok(tab) = state.sessions.get_tab(&tab_id).await else {
        return ToolOutput::fail("终端标签不存在");
    };
    let re = match regex::Regex::new(pattern) {
        Ok(r) => r,
        Err(e) => return ToolOutput::fail(format!("正则无效: {e}")),
    };
    let deadline = std::time::Instant::now() + std::time::Duration::from_millis(timeout_ms);
    loop {
        if std::time::Instant::now() > deadline {
            return ToolOutput {
                ok: false,
                text: format!(
                    "等待超时（{timeout_ms}ms）。当前屏幕尾部：\n{}",
                    tab.tail_lines(10).join("\n")
                ),
                exit_code: Some(1),
                truncated: false,
            };
        }
        let text = tab.tail_lines(30).join("\n");
        if re.is_match(&text) {
            return ToolOutput::ok("模式已出现");
        }
        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    }
}
