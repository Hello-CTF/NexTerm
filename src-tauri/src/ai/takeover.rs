//! 接管模式（§8.6 / M2-T6）：AI 直接操作真 PTY。
//!
//! 硬性安全要求：
//! - 进入接管必须发横幅事件（UI 顶部「AI 正在操作此终端」+ 立即夺回按钮）；
//! - 用户按任意键（可配）→ CancellationToken 立即取消（steal_on_key）；
//! - 每个 send_keys 写审计日志；
//! - allow_write = false 时 send_keys 全拒（生产只读接管）。
//!
//! 锁纪律：`takeovers` 是把 `AiRuntime` 上的一把 tokio RwLock。任何函数都
//! **不允许在持有它的读/写锁时再去拿同一把锁**（同任务自锁 = 永久挂死），
//! 也不允许攥着锁去 await 别的东西 —— 取出要用的数据，立刻放锁。
//! 内核函数（`begin` / `steal_back` / `finish_owned` / `finish_all`）只吃
//! `(&AiRuntime, &SessionManager)`，不为测试硬造 `tauri::AppHandle`。

use std::sync::Arc;
use std::time::Duration;

use serde_json::json;
use tauri::ipc::Channel;
use tokio_util::sync::CancellationToken;

use crate::error::AppResult;
use crate::session::SessionManager;
use crate::state::AppState;
use crate::terminal::keys;

use super::guard::{self, Risk};
use super::provider::{ChatMessage, LlmClient};
use super::{AiEvent, AiRuntime, ConfirmDecision};

pub(crate) const DEFAULT_MAX_STEPS: u32 = 30;
const IDLE_QUIET_MS: u64 = 300;

/// 进入接管（注册取消令牌 + 发横幅）。
pub async fn enter(state: &AppState, tab_id: &str) -> AppResult<Arc<CancellationToken>> {
    begin(&state.ai, &state.sessions, tab_id).await
}

/// 内核：见 [`enter`]。
pub(crate) async fn begin(
    ai: &AiRuntime,
    sessions: &SessionManager,
    tab_id: &str,
) -> AppResult<Arc<CancellationToken>> {
    // 标签存在性检查
    sessions.get_tab(tab_id).await?;
    let token = Arc::new(CancellationToken::new());
    {
        let mut takeovers = ai.takeovers.write().await;
        // 同一标签再次进入接管：先取消旧令牌再覆盖。否则旧循环（手里攥着
        // 旧令牌）感知不到被替换，会继续跑到步数上限 —— 两个 AI 同时往
        // 一个 PTY 写键，终端表现就是「抽风」。
        if let Some(old) = takeovers.insert(tab_id.to_string(), Arc::clone(&token)) {
            old.cancel();
        }
    }
    push_banner(
        sessions,
        tab_id,
        "\r\n\x1b[41;37m[AI 正在操作此终端 — 按 Esc 或任意键夺回]\x1b[0m\r\n",
    )
    .await;
    Ok(token)
}

/// 退出接管（用户夺回或任务完成）。命令入口（`ai_takeover_exit`）用：
/// 按 tab 无条件摘除当前登记。接管循环内部请用 [`finish_owned`]。
///
/// **必须先取消令牌再摘除记录** —— `exit` 是「夺回」的唯一出口，
/// `run_takeover` 的循环靠 `token.is_cancelled()` 判断是否继续；
/// 若只 `remove` 不 `cancel`，用户点了夺回循环仍会跑完 max_steps。
pub async fn exit(state: &AppState, tab_id: &str, reason: &str) {
    finish_all(&state.ai, &state.sessions, tab_id, reason).await;
}

/// 内核：见 [`exit`]。
async fn finish_all(ai: &AiRuntime, sessions: &SessionManager, tab_id: &str, reason: &str) {
    if let Some(token) = ai.takeovers.write().await.remove(tab_id) {
        token.cancel();
    }
    push_banner(
        sessions,
        tab_id,
        &format!("\r\n\x1b[43;30m[接管结束: {reason}]\x1b[0m\r\n"),
    )
    .await;
}

/// 退出接管（带令牌身份）：只有 map 里登记的**仍是这个令牌**时才摘除、发横幅。
///
/// 场景：同一标签被二次接管后，旧循环收尾时走到这里 —— 它拿的是旧令牌，
/// map 里已是新令牌；不能把新接管的登记摘掉（否则新循环失去夺回保护），
/// 也不能打「接管结束」横幅打断新任务。
async fn finish_owned(
    ai: &AiRuntime,
    sessions: &SessionManager,
    tab_id: &str,
    token: &Arc<CancellationToken>,
    reason: &str,
) {
    token.cancel();
    let removed = {
        let mut takeovers = ai.takeovers.write().await;
        let still_mine = takeovers.get(tab_id).is_some_and(|t| Arc::ptr_eq(t, token));
        still_mine && takeovers.remove(tab_id).is_some()
    };
    if removed {
        push_banner(
            sessions,
            tab_id,
            &format!("\r\n\x1b[43;30m[接管结束: {reason}]\x1b[0m\r\n"),
        )
        .await;
    }
}

/// 用户按键夺回钩子：terminal_write 命令在写 PTY 前调用。
pub async fn on_user_key(state: &AppState, tab_id: &str) {
    steal_back(&state.ai, &state.sessions, tab_id).await;
}

/// 内核：见 [`on_user_key`]。
async fn steal_back(ai: &AiRuntime, sessions: &SessionManager, tab_id: &str) {
    if !ai.steal_on_key.load(std::sync::atomic::Ordering::Relaxed) {
        return;
    }
    // 令牌必须在调 finish 之前 clone 出来、立刻放掉读锁：finish 要拿同一把
    // RwLock 的写位，读锁攥在手里等写锁是同任务自锁 —— 用户在接管中按的
    // 第一个键就把 terminal_write 永久挂死；而且 tokio RwLock 写者优先，
    // 排队的写锁会挡住后续所有读请求，全应用的终端输入一起冻死。
    let token = ai.takeovers.read().await.get(tab_id).cloned();
    if let Some(token) = token {
        token.cancel();
        finish_owned(ai, sessions, tab_id, &token, "用户夺回控制权").await;
    }
}

/// 往终端注入一条横幅（enter/exit 共用；标签已关时静默跳过）。
async fn push_banner(sessions: &SessionManager, tab_id: &str, banner: &str) {
    if let Ok(tab) = sessions.get_tab(tab_id).await {
        tab.feed_output(banner.as_bytes());
        let _ = tab.send_to_frontend(banner.as_bytes().to_vec()).await;
    }
}

/// send_keys 卡片的标题：把「要敲什么」直接摆出来，enter 单独标出。
fn keys_display(keys: &str, enter: bool) -> String {
    if enter {
        format!("输入：{keys} ⏎")
    } else {
        format!("输入：{keys}")
    }
}

/// 发一张动作卡片（与主对话 agent 同款 ToolCall 事件，侧栏渲染成卡片）。
/// 接管动作此前不发这张卡 —— 用户只看到一张读屏卡，AI 就开始闷头敲键盘了。
fn action_card(
    channel: &Channel<AiEvent>,
    id: &str,
    name: &str,
    args: &serde_json::Value,
    display: String,
) {
    let _ = channel.send(AiEvent::ToolCall {
        id: id.to_string(),
        name: name.to_string(),
        args: args.clone(),
        display,
    });
}

/// 回填动作卡片的执行结果（ToolResult）。
fn action_result(channel: &Channel<AiEvent>, id: &str, ok: bool, summary: &str, text: String) {
    let _ = channel.send(AiEvent::ToolResult {
        id: id.to_string(),
        ok,
        summary: summary.to_string(),
        text,
        truncated: false,
        exit_code: None,
    });
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
    max_steps: u32,
    allow_write: bool,
    channel: Channel<AiEvent>,
) -> AppResult<()> {
    let job = super::AiJob::new(job_id);
    state.ai.register_job(Arc::clone(&job)).await;
    // enter 失败也要把 job 摘掉：jobs 表是全局的，泄漏的注册会让前端的
    // ai_cancel 一直命中一个早已死掉的会话，直到重启应用。
    let token = match begin(&state.ai, &state.sessions, tab_id).await {
        Ok(t) => t,
        Err(e) => {
            state.ai.finish_job(&job.id).await;
            return Err(e);
        }
    };

    let Ok(tab) = state.sessions.get_tab(tab_id).await else {
        finish_owned(&state.ai, &state.sessions, tab_id, &token, "终端不存在").await;
        state.ai.finish_job(&job.id).await;
        return Err(crate::error::AppError::NotFound("终端标签".into()));
    };
    let session_id = tab.session_id.clone();

    let provider_cfg = state.ai.provider.read().await.clone();
    let client = match LlmClient::new(provider_cfg) {
        Ok(c) => c,
        Err(e) => {
            // 与 begin 失败同一类清理：拿不到模型客户端时令牌和 job 都不能泄漏
            finish_owned(&state.ai, &state.sessions, tab_id, &token, "模型未配置").await;
            state.ai.finish_job(&job.id).await;
            return Err(e);
        }
    };

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
             每一步只做一个动作，先 read_screen 观察再行动。\
             汇报节奏（硬性要求）：每发起一个动作之前，先用一两句话把因果说清楚 ——\
             「看到 <屏幕上的关键信息>；因为 <你的判断>；所以下一步 <要做的动作>」。\
             这段话会实时展示给用户 —— 沉默地连续调用动作工具是不允许的。\
             任务：{instruction}",
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
            finish_owned(&state.ai, &state.sessions, tab_id, &token, "已取消").await;
            break;
        }
        steps += 1;
        if steps > max_steps {
            finish_owned(&state.ai, &state.sessions, tab_id, &token, "步数上限").await;
            answer = "达到步数上限，接管终止".into();
            break;
        }

        // 步骤 2：空闲判定 —— 终端还在输出时不发请求（省 token）
        let mut waited: u32 = 0;
        while !tab.is_idle(IDLE_QUIET_MS)
            && waited < 200
            && !token.is_cancelled()
            && !job.cancel.is_cancelled()
        {
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

        // 模型每步的叙述（「看到…因为…所以…」）必须实时流给用户 ——
        // 丢弃回调的话侧栏全程零输出，AI 看起来就是「自顾自操作」。
        let chan2 = channel.clone();
        let completion = tokio::select! {
            _ = token.cancelled() => {
                finish_owned(&state.ai, &state.sessions, tab_id, &token, "用户夺回").await;
                break;
            }
            // ai_cancel（前端「夺回」按钮先调它再调 takeover_exit）也要能
            // 打断正在流的模型请求，不能等 reqwest 自己超时（上限 300s）。
            _ = job.cancel.cancelled() => {
                finish_owned(&state.ai, &state.sessions, tab_id, &token, "已取消").await;
                break;
            }
            r = client.chat_streaming(&history, &tools, move |item| {
                use crate::ai::provider::StreamItem;
                match item {
                    StreamItem::Delta(t) => {
                        let _ = chan2.send(AiEvent::Delta { text: t });
                    }
                    StreamItem::Reasoning(t) => {
                        let _ = chan2.send(AiEvent::Reasoning { text: t });
                    }
                }
            }) => match r {
                Ok(c) => c,
                Err(e) => {
                    finish_owned(&state.ai, &state.sessions, tab_id, &token, "模型请求失败").await;
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
            finish_owned(&state.ai, &state.sessions, tab_id, &token, "模型停止").await;
            break;
        }

        let mut done = false;
        for call in &completion.tool_calls {
            // 夺回/取消后，本轮剩余动作全部跳过：外层循环顶部的检查要等
            // 整轮 tool call 执行完才轮到，挡不住这里的 send_keys。
            if token.is_cancelled() || job.cancel.is_cancelled() {
                break;
            }
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
                    action_card(
                        &channel,
                        &call.id,
                        "wait_for",
                        &args,
                        format!("等待出现：{pattern}"),
                    );
                    let re = regex::Regex::new(pattern);
                    let deadline =
                        std::time::Instant::now() + Duration::from_millis(timeout_ms.min(60_000));
                    let mut found = false;
                    let mut last_text = String::new();
                    loop {
                        if token.is_cancelled() || job.cancel.is_cancelled() {
                            break;
                        }
                        let text = tab.tail_lines(30).join("\n");
                        if let Ok(r) = &re {
                            if r.is_match(&text) {
                                found = true;
                                last_text = text;
                                break;
                            }
                        }
                        last_text = text;
                        if std::time::Instant::now() > deadline {
                            break;
                        }
                        tokio::time::sleep(Duration::from_millis(200)).await;
                    }
                    let cancelled = token.is_cancelled() || job.cancel.is_cancelled();
                    let summary = if found {
                        "模式已出现"
                    } else if cancelled {
                        "等待被取消（用户夺回或任务取消）"
                    } else {
                        "等待超时"
                    };
                    action_result(&channel, &call.id, found, summary, last_text);
                    history.push(ChatMessage::tool_result(&call.id, summary));
                }
                "send_keys" => {
                    let keys_text = args.get("keys").and_then(|v| v.as_str()).unwrap_or("");
                    let enter = args.get("enter").and_then(|v| v.as_bool()).unwrap_or(false);
                    action_card(
                        &channel,
                        &call.id,
                        "send_keys",
                        &args,
                        keys_display(keys_text, enter),
                    );

                    if !allow_write {
                        action_result(
                            &channel,
                            &call.id,
                            false,
                            "只读接管模式：send_keys 被全部拒绝",
                            String::new(),
                        );
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
                            let msg = format!("被拒绝：{}", verdict.reason);
                            action_result(&channel, &call.id, false, &msg, String::new());
                            history.push(ChatMessage::tool_result(&call.id, msg));
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
                            // 夺回也必须能打断确认等待：wait_confirm 只听 job 的
                            // 令牌和用户的确认，不听接管令牌 —— 不在这里 select，
                            // 用户在弹窗期间按 Esc 夺回后循环会永远停在等确认。
                            let decision = tokio::select! {
                                _ = token.cancelled() => {
                                    action_result(
                                        &channel,
                                        &call.id,
                                        false,
                                        "用户已夺回控制权，本次输入取消",
                                        String::new(),
                                    );
                                    history.push(ChatMessage::tool_result(
                                        &call.id,
                                        "用户已夺回控制权，本次输入取消",
                                    ));
                                    continue;
                                }
                                d = job.wait_confirm() => d,
                            };
                            match decision {
                                ConfirmDecision::Deny => {
                                    action_result(
                                        &channel,
                                        &call.id,
                                        false,
                                        "用户拒绝了此输入",
                                        String::new(),
                                    );
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
                            // 步骤 7：审计 —— 把模型本步的因果叙述一并落库：
                            // 只记按键的话，回头翻日志只看得出「敲了什么」，
                            // 看不出「为什么敲」，追溯就断了一半。
                            let why: String = completion.content.chars().take(500).collect();
                            let _ = state
                                .store
                                .audit_insert(crate::store::AuditInput {
                                    session_id: Some(session_id.clone()),
                                    asset_id: None,
                                    source: "ai",
                                    kind: "takeover",
                                    payload: json!({ "keys": keys_text, "enter": enter, "tab": tab_id, "why": why }),
                                    exit_code: Some(0),
                                    duration_ms: None,
                                })
                                .await;
                            action_result(&channel, &call.id, true, "已发送", String::new());
                            history.push(ChatMessage::tool_result(&call.id, "已发送"));
                        }
                        Err(e) => {
                            let msg = format!("发送失败: {e}");
                            action_result(&channel, &call.id, false, &msg, String::new());
                            history.push(ChatMessage::tool_result(&call.id, msg));
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
                    action_card(&channel, &call.id, "done", &args, format!("结束：{answer}"));
                    action_result(&channel, &call.id, success, reason, answer.clone());
                    finish_owned(&state.ai, &state.sessions, tab_id, &token, reason).await;
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::terminal::transcoder::TerminalEncoding;
    use crate::terminal::TerminalTab;

    fn runtime() -> (AiRuntime, SessionManager) {
        (AiRuntime::new(Default::default()), SessionManager::new())
    }

    /// 造一个挂在 SessionManager 里的真实标签（begin 的存在性检查要过）。
    async fn with_tab(sessions: &SessionManager, tab_id: &str) {
        let tab = TerminalTab::new_arc(
            tab_id.into(),
            "sess-1".into(),
            80,
            24,
            TerminalEncoding::Utf8,
        );
        sessions.tabs.write().await.insert(tab_id.into(), tab);
    }

    /// 回归：`steal_back` 曾在持有 takeovers **读锁**时调 finish（拿**写锁**）
    /// —— tokio RwLock 同任务自锁，接管中用户按的第一个键就把 terminal_write
    /// 永久挂死；写锁一排队，后续所有标签的按键（都要先读这把锁）全被挡住，
    /// 全应用终端输入冻结。修复前本测试 3 秒超时失败。
    #[tokio::test]
    async fn steal_back_must_not_hold_read_lock_across_finish() {
        let (ai, sessions) = runtime();
        ai.takeovers
            .write()
            .await
            .insert("t1".into(), Arc::new(CancellationToken::new()));

        let t0 = std::time::Instant::now();
        steal_back(&ai, &sessions, "t1").await;
        assert!(
            t0.elapsed() < Duration::from_secs(3),
            "死锁回归：steal_back 未在 3s 内返回"
        );
        assert!(ai.takeovers.read().await.is_empty(), "夺回后登记应被摘除");
    }

    /// 回归：同一标签二次进入接管必须取消旧令牌。修复前旧循环感知不到被
    /// 替换，两个 AI 循环同时往一个 PTY 写键。
    #[tokio::test]
    async fn reenter_cancels_previous_token() {
        let (ai, sessions) = runtime();
        with_tab(&sessions, "t1").await;

        let first = begin(&ai, &sessions, "t1").await.expect("first enter");
        let second = begin(&ai, &sessions, "t1").await.expect("second enter");

        assert!(first.is_cancelled(), "旧令牌应随二次进入被取消");
        assert!(!second.is_cancelled(), "新令牌不应被误伤");
    }

    /// 回归：被替换的旧循环收尾（finish_owned 带旧令牌）不能把新接管的登记
    /// 摘掉、也不能取消新令牌 —— 否则新循环失去夺回保护、横幅错乱。
    #[tokio::test]
    async fn stale_finish_keeps_new_registration() {
        let (ai, sessions) = runtime();
        with_tab(&sessions, "t1").await;

        let first = begin(&ai, &sessions, "t1").await.expect("first enter");
        let second = begin(&ai, &sessions, "t1").await.expect("second enter");

        finish_owned(&ai, &sessions, "t1", &first, "旧循环收尾").await;

        assert!(!second.is_cancelled(), "新令牌不应被旧循环收尾取消");
        assert!(
            ai.takeovers.read().await.contains_key("t1"),
            "新登记不应被旧循环收尾摘除"
        );
    }
}
