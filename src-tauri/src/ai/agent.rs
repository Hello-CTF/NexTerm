//! Agent 工具调用循环（§8.1 / M2-T3）：流式事件 + 取消 + 轮次上限（默认 24）。
//!
//! 工具调用与接管共享同一个 Agent、Session、审计与终端视图。

use std::sync::Arc;
use std::time::Duration;

use tauri::ipc::Channel;

use crate::error::AppResult;
use crate::state::AppState;

use super::guard::{self, Risk};
use super::provider::{ChatMessage, LlmClient};
use super::tools;
use super::{AiEvent, AiJob, AiScope, ConfirmDecision};

const MAX_TURNS: u32 = 24;

/// 一次对话任务的输入。
pub struct AgentRunInput {
    pub job: Arc<AiJob>,
    pub conversation_id: String,
    pub scope: AiScope,
    pub message: String,
    pub selection: Option<String>,
    pub channel: Channel<AiEvent>,
}

/// 运行一轮完整对话（工具循环）。
pub async fn run(state: &AppState, input: AgentRunInput) -> AppResult<()> {
    let AgentRunInput {
        job,
        conversation_id,
        scope,
        message,
        selection,
        channel,
    } = input;
    let tools = tools::all_tools();
    let mut history = load_history(state, &conversation_id).await;
    let context_text =
        super::context::render(&super::context::build(state, &scope, selection).await);

    // system 消息：基础骨架 + 上下文包
    history.insert(
        0,
        ChatMessage::system(format!(
            "{}\n\n{}",
            super::system_prompt_base(),
            context_text
        )),
    );
    history.push(ChatMessage::user(message.clone()));

    // 持久化用户消息
    let _ = state
        .store
        .msg_insert(
            &conversation_id,
            "user",
            &serde_json::json!({ "role": "user", "content": message }),
            None,
            None,
        )
        .await;

    let _ = channel.send(AiEvent::Status {
        phase: "thinking".into(),
        detail: None,
        turn: Some(0),
    });

    let provider_cfg = state.ai.provider.read().await.clone();
    let client = LlmClient::new(provider_cfg)?;

    let mut turn: u32 = 0;
    let mut tokens_in: u64 = 0;
    let mut tokens_out: u64 = 0;
    let mut final_answer = String::new();

    loop {
        if job.cancel.is_cancelled() {
            let _ = channel.send(AiEvent::Error {
                message: "已取消".into(),
                retryable: false,
            });
            break;
        }
        turn += 1;
        if turn > MAX_TURNS {
            let _ = channel.send(AiEvent::Error {
                message: format!("已达到最大轮次 {MAX_TURNS}，停止执行"),
                retryable: false,
            });
            break;
        }

        let job2 = Arc::clone(&job);
        let chan2 = channel.clone();
        let completion = tokio::select! {
            _ = job.cancel.cancelled() => {
                let _ = channel.send(AiEvent::Error { message: "已取消".into(), retryable: false });
                break;
            }
            r = client.chat_streaming(&history, &tools, move |item| {
                use crate::ai::provider::StreamItem;
                match item {
                    StreamItem::Delta(t) => { let _ = chan2.send(AiEvent::Delta { text: t }); }
                    StreamItem::Reasoning(t) => { let _ = chan2.send(AiEvent::Reasoning { text: t }); }
                }
                let _ = &job2;
            }) => r?,
        };
        tokens_in += completion.tokens_in;
        tokens_out += completion.tokens_out;

        if !completion.tool_calls.is_empty() {
            // assistant 消息（含 tool_calls）入历史
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

            for call in &completion.tool_calls {
                if job.cancel.is_cancelled() {
                    break;
                }
                let args: serde_json::Value =
                    serde_json::from_str(&call.function.arguments).unwrap_or_default();
                let display = tools::all_tools()
                    .iter()
                    .find(|t| t.name == call.function.name)
                    .map(|t| t.description.clone())
                    .unwrap_or_default();
                let _ = &display;

                let _ = channel.send(AiEvent::ToolCall {
                    id: call.id.clone(),
                    name: call.function.name.clone(),
                    args: args.clone(),
                    display: display.clone(),
                });

                // ── 护栏：工具层拦截（§8.3）──
                let verdict = guard::classify_tool(&call.function.name, &args);
                let outcome = match verdict.risk {
                    Risk::Forbidden => {
                        let _ = channel.send(AiEvent::ToolResult {
                            id: call.id.clone(),
                            ok: false,
                            summary: format!("危险操作被拒绝：{}", verdict.reason),
                            truncated: false,
                            exit_code: None,
                        });
                        history.push(ChatMessage::tool_result(
                            &call.id,
                            format!("被系统拒绝：{}。请改用安全的方式完成任务。", verdict.reason),
                        ));
                        continue;
                    }
                    Risk::NeedsConfirm => {
                        let kind_allowed = verdict
                            .kind
                            .map(|k| job.session_allowed.blocking_read().contains(k))
                            .unwrap_or(false);
                        if !kind_allowed {
                            let _ = channel.send(AiEvent::ConfirmRequired {
                                id: call.id.clone(),
                                tool: call.function.name.clone(),
                                args: args.clone(),
                                risk: "needs_confirm".into(),
                                rendered: format!(
                                    "{} {}\n{}",
                                    call.function.name, call.function.arguments, verdict.reason
                                ),
                            });
                            match job.wait_confirm().await {
                                ConfirmDecision::Allow => {}
                                ConfirmDecision::AllowSession => {
                                    if let Some(k) = verdict.kind {
                                        job.session_allowed.write().await.insert(k.to_string());
                                    }
                                }
                                ConfirmDecision::Deny => {
                                    let _ = channel.send(AiEvent::ToolResult {
                                        id: call.id.clone(),
                                        ok: false,
                                        summary: "用户拒绝了该操作".into(),
                                        truncated: false,
                                        exit_code: None,
                                    });
                                    history.push(ChatMessage::tool_result(
                                        &call.id,
                                        "用户拒绝了此操作。请询问用户希望怎么做。",
                                    ));
                                    continue;
                                }
                            }
                        }
                        tools::execute(state, &scope, &job, &call.function.name, &args).await
                    }
                    Risk::Safe => {
                        tools::execute(state, &scope, &job, &call.function.name, &args).await
                    }
                };

                // 审计（user|ai 关键动作，§3 audit_log）
                let _ = state
                    .store
                    .audit_insert(crate::store::AuditInput {
                        session_id: scope.session_id.clone(),
                        asset_id: scope.asset_id.clone(),
                        source: "ai",
                        kind: "exec",
                        payload: serde_json::json!({
                            "tool": call.function.name,
                            "args": args,
                        }),
                        exit_code: outcome.exit_code,
                        duration_ms: None,
                    })
                    .await;

                let _ = channel.send(AiEvent::ToolResult {
                    id: call.id.clone(),
                    ok: outcome.ok,
                    summary: truncate_summary(&outcome.text, 400),
                    truncated: outcome.truncated,
                    exit_code: outcome.exit_code,
                });
                history.push(ChatMessage::tool_result(&call.id, &outcome.text));
            }
            continue; // 带着工具结果再请求下一轮
        }

        // 无工具调用 = 最终回答
        final_answer = completion.content.clone();
        break;
    }

    // 持久化助手消息
    let _ = state
        .store
        .msg_insert(
            &conversation_id,
            "assistant",
            &serde_json::json!({ "role": "assistant", "content": final_answer }),
            Some(tokens_in as i64),
            Some(tokens_out as i64),
        )
        .await;
    let _ = state.store.conv_touch(&conversation_id).await;

    let _ = channel.send(AiEvent::Done {
        answer: final_answer,
        turns: turn,
        tokens_in,
        tokens_out,
    });
    state.ai.finish_job(&job.id).await;
    Ok(())
}

async fn load_history(state: &AppState, conversation_id: &str) -> Vec<ChatMessage> {
    state
        .store
        .msg_list(conversation_id)
        .await
        .unwrap_or_default()
        .iter()
        .filter_map(|m| serde_json::from_str::<ChatMessage>(&m.content_json).ok())
        .collect()
}

fn truncate_summary(s: &str, n: usize) -> String {
    if s.len() <= n {
        s.to_string()
    } else {
        let mut end = n;
        while end > 0 && !s.is_char_boundary(end) {
            end -= 1;
        }
        format!("{}…", &s[..end])
    }
}

/// 超时包装（防御 provider 挂起）。
pub async fn with_timeout<F, T>(fut: F, d: Duration) -> AppResult<T>
where
    F: std::future::Future<Output = AppResult<T>>,
{
    tokio::time::timeout(d, fut)
        .await
        .map_err(|_| crate::error::AppError::Timeout("AI 请求超时".into()))?
}
