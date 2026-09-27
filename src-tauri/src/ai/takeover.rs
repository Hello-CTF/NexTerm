//! 接管模式（§8.6 / M2-T6）：AI 直接操作真 PTY。
//!
//! 硬性安全要求：
//! - 进入接管必须发横幅事件（UI 顶部「AI 正在操作此终端」+ 立即夺回按钮）；
//! - 用户按任意键（可配）→ CancellationToken 立即取消（steal_on_key）；
//! - 每个 send_keys 写审计日志；
//! - allow_write = false 时 send_keys 全拒（生产只读接管）。

use std::sync::Arc;
use std::time::Duration;

use serde_json::json;
use tauri::ipc::Channel;

use crate::error::{AppError, AppResult};
use crate::state::AppState;
use crate::terminal::keys;

use super::guard::{self, Risk};
use super::provider::{ChatMessage, LlmClient};
use super::{AiEvent, ConfirmDecision};

const DEFAULT_MAX_STEPS: u32 = 30;
const IDLE_QUIET_MS: u64 = 300;

/// 进入接管（注册取消令牌 + 发横幅）。
pub async fn enter(
    state: &AppState,
    tab_id: &str,
    max_steps: u32,
    allow_write: bool,
) -> AppResult<Arc<tokio_util::sync::CancellationToken>> {
    // 标签存在性检查
    state.sessions.get_tab(tab_id).await?;
    let token = Arc::new(tokio_util::sync::CancellationToken::new());
    state
        .ai
        .takeovers
        .write()
        .await
        .insert(tab_id.to_string(), Arc::clone(&token));

    // 横幅注入终端 + 前端事件
    if let Ok(tab) = state.sessions.get_tab(tab_id).await {
        let banner = "\r\n\x1b[41;37m[AI 正在操作此终端 — 按 Esc 或任意键夺回]\x1b[0m\r\n";
        tab.feed_output(banner.as_bytes());
        let _ = tab.send_to_frontend(banner.as_bytes().to_vec()).await;
    }
    let _ = (max_steps, allow_write);
    Ok(token)
}

/// 退出接管（用户夺回或任务完成）。
///
/// **必须先取消令牌再摘除记录** —— `exit` 是「夺回」的唯一出口，
/// `run_takeover` 的循环靠 `token.is_cancelled()` 判断是否继续；
/// 若只 `remove` 不 `cancel`，用户点了夺回循环仍会跑完 max_steps。
pub async fn exit(state: &AppState, tab_id: &str, reason: &str) {
    if let Some(token) = state.ai.takeovers.write().await.remove(tab_id) {
        token.cancel();
    }
    if let Ok(tab) = state.sessions.get_tab(tab_id).await {
        let banner = format!("\r\n\x1b[43;30m[接管结束: {reason}]\x1b[0m\r\n");
        tab.feed_output(banner.as_bytes());
        let _ = tab.send_to_frontend(banner.into_bytes()).await;
    }
}

/// 用户按键夺回钩子：terminal_write 命令在写 PTY 前调用。
pub async fn on_user_key(state: &AppState, tab_id: &str) {
    if state
        .ai
        .steal_on_key
        .load(std::sync::atomic::Ordering::Relaxed)
    {
        if let Some(token) = state.ai.takeovers.read().await.get(tab_id).cloned() {
            token.cancel();
            exit(state, tab_id, "用户夺回控制权").await;
        }
    }
}

/// 接管主循环（§8.6）。
///
/// `job_id` 由调用方（`ai_takeover_run`）生成并**返回给前端**，
/// 这样前端的 `ai_cancel(job_id)` 能真正取消这次接管；
/// 早期版本在这里自己 new 一个 id，前端永远拿不到，取消按钮形同虚设。
pub async fn run_takeover(
    state: &AppState,
    job_id: &str,
    tab_id: &str,
    instruction: &str,
    allow_write: bool,
    channel: Channel<AiEvent>,
) -> AppResult<()> {
    let job = super::AiJob::new(job_id);
    state.ai.register_job(Arc::clone(&job)).await;
    let token = enter(state, tab_id, DEFAULT_MAX_STEPS, allow_write).await?;

    let Ok(tab) = state.sessions.get_tab(tab_id).await else {
        exit(state, tab_id, "终端不存在").await;
        state.ai.finish_job(&job.id).await;
        return Err(AppError::NotFound("终端标签".into()));
    };
    let session_id = tab.session_id.clone();

    let provider_cfg = state.ai.provider.read().await.clone();
    let client = LlmClient::new(provider_cfg)?;

    // 接管专用工具集（§8.6 动作类型）
    let tools = vec![
        crate::ai::provider::ToolSchema {
            name: "read_screen".into(),
            description: "读取当前屏幕（纯文本 + 光标位置）".into(),
            parameters: json!({"type":"object","properties":{}}),
        },
        crate::ai::provider::ToolSchema {
            name: "send_keys".into(),
            description: "向终端发送按键。特殊键：<enter> <ctrl+c> <tab> <up> <down>。".into(),
            parameters: json!({"type":"object","properties":{
                "keys":{"type":"string"},"enter":{"type":"boolean"},
            },"required":["keys"]}),
        },
        crate::ai::provider::ToolSchema {
            name: "wait_for".into(),
            description: "等待屏幕出现某正则（超时返回失败）".into(),
            parameters: json!({"type":"object","properties":{
                "pattern":{"type":"string"},"timeout_ms":{"type":"number"},
            },"required":["pattern","timeout_ms"]}),
        },
        crate::ai::provider::ToolSchema {
            name: "done".into(),
            description: "任务完成或无法继续时调用，带总结。".into(),
            parameters: json!({"type":"object","properties":{
                "summary":{"type":"string"},"success":{"type":"boolean"},
            },"required":["summary","success"]}),
        },
    ];

    let mut history: Vec<ChatMessage> = vec![
        ChatMessage::system(format!(
            "{}\n\n你现在处于【接管模式】：直接操作一个真实的交互式终端（可能是 apt/vim/mysql 等 TUI）。\
             每一步只做一个动作，先 read_screen 观察再行动。任务：{instruction}",
            super::system_prompt_base()
        )),
        ChatMessage::user(format!("请开始接管完成这个任务：{instruction}")),
    ];

    let mut steps: u32 = 0;
    let mut answer = String::from("接管结束");
    let mut tokens_in: u64 = 0;
    let mut tokens_out: u64 = 0;

    loop {
        if token.is_cancelled() || job.cancel.is_cancelled() {
            exit(state, tab_id, "已取消").await;
            break;
        }
        steps += 1;
        if steps > DEFAULT_MAX_STEPS {
            exit(state, tab_id, "步数上限").await;
            answer = "达到步数上限，接管终止".into();
            break;
        }

        // 步骤 2：空闲判定 —— 终端还在输出时不发请求（省 token）
        let mut waited: u32 = 0;
        while !tab.is_idle(IDLE_QUIET_MS) && waited < 200 && !token.is_cancelled() {
            tokio::time::sleep(Duration::from_millis(100)).await;
            waited += 1;
        }

        // 步骤 3：读屏 + 请求下一步动作
        let snap = tab.snapshot();
        let _ = channel.send(AiEvent::Screen {
            tab_id: tab_id.to_string(),
            text: snap.text.clone(),
        });
        history.push(ChatMessage::user(format!(
            "当前屏幕（光标 r{} c{}，空闲 {}ms）：\n{}",
            snap.cursor_row, snap.cursor_col, snap.last_output_ms_ago, snap.text
        )));

        let completion = tokio::select! {
            _ = token.cancelled() => { exit(state, tab_id, "用户夺回").await; break; }
            r = client.chat_streaming(&history, &tools, |_item| {}) => match r {
                Ok(c) => c,
                Err(e) => {
                    exit(state, tab_id, "模型请求失败").await;
                    state.ai.finish_job(&job.id).await;
                    let _ = channel.send(AiEvent::Error { message: e.to_string(), retryable: true });
                    return Err(e);
                }
            },
        };
        tokens_in += completion.tokens_in;
        tokens_out += completion.tokens_out;

        history.push(ChatMessage {
            role: "assistant".into(),
            content: if completion.content.is_empty() {
                serde_json::Value::Null
            } else {
                serde_json::Value::String(completion.content.clone())
            },
            tool_calls: Some(completion.tool_calls.clone()),
            ..Default::default()
        });

        if completion.tool_calls.is_empty() {
            answer = completion.content.clone();
            exit(state, tab_id, "模型停止").await;
            break;
        }

        let mut done = false;
        for call in &completion.tool_calls {
            let args: serde_json::Value =
                serde_json::from_str(&call.function.arguments).unwrap_or_default();
            match call.function.name.as_str() {
                "read_screen" => {
                    // 已在上一段读屏，直接回
                    history.push(ChatMessage::tool_result(&call.id, &snap.text));
                }
                "wait_for" => {
                    let pattern = args.get("pattern").and_then(|v| v.as_str()).unwrap_or("");
                    let timeout_ms = args
                        .get("timeout_ms")
                        .and_then(|v| v.as_u64())
                        .unwrap_or(10_000);
                    let re = regex::Regex::new(pattern);
                    let deadline =
                        std::time::Instant::now() + Duration::from_millis(timeout_ms.min(60_000));
                    let mut found = false;
                    loop {
                        if token.is_cancelled() {
                            break;
                        }
                        let text = tab.tail_lines(30).join("\n");
                        if let Ok(r) = &re {
                            if r.is_match(&text) {
                                found = true;
                                break;
                            }
                        }
                        if std::time::Instant::now() > deadline {
                            break;
                        }
                        tokio::time::sleep(Duration::from_millis(200)).await;
                    }
                    history.push(ChatMessage::tool_result(
                        &call.id,
                        if found {
                            "模式已出现"
                        } else {
                            "等待超时"
                        },
                    ));
                }
                "send_keys" => {
                    let keys_text = args.get("keys").and_then(|v| v.as_str()).unwrap_or("");
                    let enter = args.get("enter").and_then(|v| v.as_bool()).unwrap_or(false);

                    if !allow_write {
                        history.push(ChatMessage::tool_result(
                            &call.id,
                            "只读接管模式：send_keys 被全部拒绝",
                        ));
                        continue;
                    }
                    // 步骤 5：护栏（键盘内容同样分级）
                    let mut guard_args = json!({ "keys": keys_text, "enter": enter });
                    let verdict = guard::classify_tool("send_keys", &guard_args);
                    let _ = &mut guard_args;
                    match verdict.risk {
                        Risk::Forbidden => {
                            history.push(ChatMessage::tool_result(
                                &call.id,
                                format!("被拒绝：{}", verdict.reason),
                            ));
                            continue;
                        }
                        Risk::NeedsConfirm | Risk::Danger => {
                            let _ = channel.send(AiEvent::ConfirmRequired {
                                id: call.id.clone(),
                                tool: "send_keys".into(),
                                args: json!({ "keys": keys_text }),
                                risk: "needs_confirm".into(),
                                rendered: format!("AI 要输入：{keys_text}（{}）", verdict.reason),
                            });
                            match job.wait_confirm().await {
                                ConfirmDecision::Deny => {
                                    history.push(ChatMessage::tool_result(
                                        &call.id,
                                        "用户拒绝了此输入",
                                    ));
                                    continue;
                                }
                                ConfirmDecision::AllowSession | ConfirmDecision::Allow => {}
                            }
                        }
                        Risk::Safe => {}
                    }

                    // 步骤 6：执行
                    let encoded = keys::encode_send(keys_text, enter);
                    match tab.write(&encoded).await {
                        Ok(()) => {
                            // 步骤 7：审计
                            let _ = state
                                .store
                                .audit_insert(crate::store::AuditInput {
                                    session_id: Some(session_id.clone()),
                                    asset_id: None,
                                    source: "ai",
                                    kind: "takeover",
                                    payload: json!({ "keys": keys_text, "enter": enter, "tab": tab_id }),
                                    exit_code: Some(0),
                                    duration_ms: None,
                                })
                                .await;
                            history.push(ChatMessage::tool_result(&call.id, "已发送"));
                        }
                        Err(e) => {
                            history
                                .push(ChatMessage::tool_result(&call.id, format!("发送失败: {e}")));
                        }
                    }
                }
                "done" => {
                    answer = args
                        .get("summary")
                        .and_then(|v| v.as_str())
                        .unwrap_or("任务结束")
                        .to_string();
                    let success = args
                        .get("success")
                        .and_then(|v| v.as_bool())
                        .unwrap_or(true);
                    let reason = if success {
                        "任务完成"
                    } else {
                        "任务失败"
                    };
                    exit(state, tab_id, reason).await;
                    done = true;
                    history.push(ChatMessage::tool_result(&call.id, "已结束"));
                }
                other => {
                    history.push(ChatMessage::tool_result(
                        &call.id,
                        format!("未知动作 {other}"),
                    ));
                }
            }
            if done {
                break;
            }
        }
        if done {
            break;
        }
    }

    let _ = channel.send(AiEvent::Done {
        answer,
        turns: steps,
        tokens_in,
        tokens_out,
    });
    state.ai.finish_job(&job.id).await;
    Ok(())
}
