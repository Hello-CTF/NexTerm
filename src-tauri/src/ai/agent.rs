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
use super::usage::Usage;
use super::{AiEvent, AiJob, AiScope, ConfirmDecision};

const MAX_TURNS: u32 = 24;

/// 上下文压缩触发线：上一轮的输入占用超过窗口这个百分比时，下一轮发送前先剪枝。
const COMPACT_AT_PERCENT: f64 = 75.0;

/// 推给前端的单条工具输出上限（64K 字符）。
///
/// 不是不信任用户，是怕界面被一条 `cat` 大文件卡死 —— 几 MB 文本一次性塞进
/// DOM，滚动条会当场失去响应。超出部分在模型上下文与审计日志里仍然完整。
const MAX_TOOL_TEXT: usize = 64 * 1024;

/// 一次对话任务的输入。
pub struct AgentRunInput {
    pub job: Arc<AiJob>,
    pub conversation_id: String,
    pub scope: AiScope,
    pub message: String,
    pub selection: Option<String>,
    /// 图片附件（裸 base64 或 data URI）。空 = 纯文本，走字符串形态的 content。
    pub images: Vec<String>,
    /// 计划模式：只做只读调研并提交方案，等用户批准后才动手。
    pub plan_mode: bool,
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
        images,
        plan_mode,
        channel,
    } = input;
    // 权限档位与自定义危险规则：整轮读一次、固定住。
    // 不每步重读 —— 一轮对话中途改档位会让行为变得没法预期，
    // 用户改完档位期望的是「下一轮生效」。
    let (perm_mode, perm_danger) = {
        let p = state.ai.permission.read().await;
        (p.mode, p.danger_rules.clone())
    };
    let tools = tools::all_tools();
    let mut history = load_history(state, &conversation_id).await;

    // 上下文装配：**稳定前缀 + 易变后缀**。
    // 角色设定与工具规范进 system（字节级不变），当前目录/会话状态/选中文本
    // 挪到消息数组尾部 —— prompt cache 命中的前提是前缀逐字节稳定，
    // 以前把易变内容拼进 system，等于每一轮都亲手让缓存失效。
    let split = super::context::build_split(state, &scope, selection).await;
    history.insert(0, ChatMessage::system(split.stable.clone()));
    // 易变那段只服务于本轮提问，**不落库** —— 存进历史第二天就成了过期情报
    if let Some(ctx_msg) = split.volatile_message() {
        history.push(ctx_msg);
    }
    history.push(ChatMessage::user_with_images(message.clone(), &images));

    // 持久化用户消息。带图时只记数量、不落 base64 原文 ——
    // 一张截图就能把 sqlite 撑到几十兆，而历史记录的价值远不值这个价。
    let _ = state
        .store
        .msg_insert(
            &conversation_id,
            "user",
            &serde_json::json!({
                "role": "user",
                "content": message,
                "imageCount": images.len(),
            }),
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
    // 窗口按运行时配置的兜底值取 —— 圆环百分比与压缩触发线都按它算
    let context_window = client.cfg.sane_context_window();

    let mut turn: u32 = 0;
    let mut tokens_in: u64 = 0;
    let mut tokens_out: u64 = 0;
    let mut final_answer = String::new();
    let mut prune_pending = false;
    // 这一轮是"被打断"结束的（用户取消 / 模型请求失败），不是正常收尾。
    // 决定末尾要不要推 `Done` —— 详见底部那段注释。
    let mut aborted = false;

    'turns: loop {
        if job.cancel.is_cancelled() {
            aborted = true;
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

        // 上下文压缩第一步：把早期工具结果的原文换成一行占位。
        // 工具结果是历史里体积最大、回头引用率最低的部分 —— 先剪它们，
        // 比砍对话轮次安全得多。（第二步"让模型摘要"要额外一次往返，暂不做。）
        if prune_pending {
            prune_pending = false;
            let n = prune_tool_results(&mut history);
            if n > 0 {
                let _ = channel.send(AiEvent::Status {
                    phase: "compacting".into(),
                    detail: Some(format!("上下文接近窗口上限，已压缩 {n} 条早期工具结果")),
                    turn: Some(turn),
                });
            }
        }

        let job2 = Arc::clone(&job);
        let chan2 = channel.clone();
        let completion = tokio::select! {
            _ = job.cancel.cancelled() => {
                aborted = true;
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
            }) => match r {
                Ok(c) => c,
                Err(e) => {
                    // 这里以前是 `r?` —— `run()` 直接返回 Err，而调用方（commands/ai.rs）
                    // 那一支**只记日志、不推任何事件**，于是界面永远停在「转圈」：
                    // 输入框禁用、没有停止按钮，用户只能重启应用。这就是「卡住了」的
                    // 一半来源（另一半是工具执行不可中断）。
                    tracing::error!(target: "ai", error = %e, "模型请求失败");
                    aborted = true;
                    let _ = channel.send(AiEvent::Error {
                        message: format!("模型请求失败：{e}"),
                        retryable: true,
                    });
                    break;
                }
            },
        };
        tokens_in += completion.tokens_in;
        tokens_out += completion.tokens_out;

        // 每轮把用量快照推给前端圆环。用**本轮**值而不是累计值 ——
        // 累计会把这一轮之前所有请求的 token 都滚进来，圆环虚高就没参考价值了。
        let usage = Usage {
            prompt_tokens: completion.tokens_in,
            completion_tokens: completion.tokens_out,
            cached_tokens: completion.tokens_cached,
            context_window,
        };
        // 本轮历史已经发出去了、改不动，所以只安排"下一轮发送前"压缩
        if usage.context_used_percent() >= COMPACT_AT_PERCENT {
            prune_pending = true;
        }
        let _ = channel.send(AiEvent::Usage(usage));

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
                    aborted = true;
                    break;
                }
                let args: serde_json::Value =
                    serde_json::from_str(&call.function.arguments).unwrap_or_default();
                // 卡片标题 = 这次调用**实际做了什么**（命令/文件/表/容器），
                // 而不是工具的 description —— 后者是给模型看的说明书，
                // 拿来当标题会让每张卡片长得一模一样。详见 tools::display_for。
                let display = tools::display_for(&call.function.name, &args);

                let _ = channel.send(AiEvent::ToolCall {
                    id: call.id.clone(),
                    name: call.function.name.clone(),
                    args: args.clone(),
                    display: display.clone(),
                });

                // ── 计划模式：先出方案，不落手 ──
                // 只读探查 + 维护清单 + 提交计划放行，其余一律拒绝。
                // 注意这里**不**复用 guard 的只读白名单：白名单判的是"命令看起来只读"，
                // 管道和重定向能溜过去；计划模式的语义是"一个字都不许改"，保守挡。
                if plan_mode {
                    if let Some(reason) = tools::edit::blocked_in_plan_mode(&call.function.name) {
                        let msg = format!("计划模式下被拦截：{}", call.function.name);
                        let _ = channel.send(AiEvent::ToolResult {
                            id: call.id.clone(),
                            ok: false,
                            summary: msg.clone(),
                            text: format!("{msg}\n{reason}"),
                            truncated: false,
                            exit_code: None,
                        });
                        history.push(ChatMessage::tool_result(
                            &call.id,
                            format!("{reason}\n（系统拦截，不是你调错了）"),
                        ));
                        continue;
                    }
                }

                // ── 护栏：分类 + 决策（§8.3）──
                // 「档位 × 风险 → 放/问/拒」只在这里合成一次；下面三个分支
                // 各自只负责怎么执行、怎么拒绝，不再自己判断风险。
                let ruling = guard::judge(perm_mode, &perm_danger, &call.function.name, &args);
                let outcome = match ruling.decision {
                    guard::Decision::Deny => {
                        let msg = format!("已拒绝：{}", ruling.reason);
                        let _ = channel.send(AiEvent::ToolResult {
                            id: call.id.clone(),
                            ok: false,
                            summary: msg.clone(),
                            text: msg,
                            truncated: false,
                            exit_code: None,
                        });
                        history.push(ChatMessage::tool_result(
                            &call.id,
                            format!(
                                "被系统拒绝：{}。请改用安全的方式完成任务；\
                                 若确有必要，提示用户切换权限档位，不要反复重试。",
                                ruling.reason
                            ),
                        ));
                        continue;
                    }
                    guard::Decision::Ask => {
                        let kind_allowed = session_allows(&job, ruling.kind).await;
                        if !kind_allowed {
                            let _ = channel.send(AiEvent::ConfirmRequired {
                                id: call.id.clone(),
                                tool: call.function.name.clone(),
                                args: args.clone(),
                                risk: if ruling.risk == Risk::Danger {
                                    "danger".into()
                                } else {
                                    "needs_confirm".into()
                                },
                                rendered: format!(
                                    "{} {}\n{}",
                                    call.function.name, call.function.arguments, ruling.reason
                                ),
                            });
                            match job.wait_confirm().await {
                                ConfirmDecision::Allow => {}
                                ConfirmDecision::AllowSession => {
                                    // 危险命令**不进**「本会话允许此类」的记忆：
                                    // 它本来就该每次问，这是静默档唯一保留的刹车。
                                    if let Some(k) =
                                        ruling.kind.filter(|k| *k != guard::KIND_DANGER)
                                    {
                                        job.session_allowed.write().await.insert(k.to_string());
                                    }
                                }
                                ConfirmDecision::Deny => {
                                    let _ = channel.send(AiEvent::ToolResult {
                                        id: call.id.clone(),
                                        ok: false,
                                        summary: "用户拒绝了该操作".into(),
                                        text: "用户拒绝了该操作。".into(),
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
                        execute_with_diff(state, &scope, &job, call, &args, &channel).await
                    }
                    guard::Decision::Allow => {
                        execute_with_diff(state, &scope, &job, call, &args, &channel).await
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
                    // 完整输出照推（上限 64K）：摘要只够扫一眼，排查问题得看原文。
                    text: truncate_summary(&outcome.text, MAX_TOOL_TEXT),
                    truncated: outcome.truncated,
                    exit_code: outcome.exit_code,
                });
                history.push(ChatMessage::tool_result(&call.id, &outcome.text));

                // 清单是「整份替换」语义，推给前端直接整体渲染
                if call.function.name == "todo_write" {
                    let _ = channel.send(AiEvent::Todos {
                        items: tools::edit::todos_for(&job.id),
                    });
                }
                // 计划模式收口：模型提交计划即本轮结束 —— 后面那一步是不是要执行，
                // 得等用户读完方案自己决定，不能替他把"批准"也一起点了。
                if call.function.name == "exit_plan_mode" {
                    if let Some(plan) = tools::edit::plan_for(&job.id) {
                        let _ = channel.send(AiEvent::PlanSubmitted { plan: plan.clone() });
                        final_answer = plan;
                        break 'turns;
                    }
                }
            }
            continue; // 带着工具结果再请求下一轮
        }

        // 无工具调用 = 最终回答
        final_answer = completion.content.clone();
        break;
    }

    // 持久化助手消息。中途被打断且一个字都没产出时不落库 ——
    // 否则历史列表里会多出一条点开是空白的助手消息，看起来像"记录坏了"。
    if !(aborted && final_answer.is_empty()) {
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
    }
    let _ = state.store.conv_touch(&conversation_id).await;

    // 被打断时不推 `Done`：它的语义是"这一轮正常收尾"，前端收到就追加一条
    // 回答气泡 —— 出错/取消之后再追一条「(无回答)」只会让人更困惑。
    // 但**一定要推点东西**：前端 `aiBusy` 只由 done/error 复位，
    // 一个事件都不推，界面就永远解不了锁。上面两条路径都已经推过 Error 了。
    if !aborted {
        let _ = channel.send(AiEvent::Done {
            answer: final_answer,
            turns: turn,
            tokens_in,
            tokens_out,
        });
    }
    // 任务级状态（已读文件 / 清单 / 计划）随任务一起销毁，
    // 不清的话这张注册表会跟着会话数一直长
    tools::edit::clear_task(&job.id);
    state.ai.finish_job(&job.id).await;
    Ok(())
}

/// 执行工具；若是写文件类且内容确实变了，补推一条「变更记录」。
///
/// `before` 在执行**前**读。读不到（不存在 / 无权限 / 不是文本）就当没有 before ——
/// 那种情况本来也生成不出有意义的 diff，硬凑一个只会误导用户。
async fn execute_with_diff(
    state: &AppState,
    scope: &AiScope,
    job: &Arc<AiJob>,
    call: &super::provider::ToolCall,
    args: &serde_json::Value,
    channel: &Channel<AiEvent>,
) -> tools::ToolOutput {
    let target = if matches!(call.function.name.as_str(), "write_file" | "edit_file") {
        args.get("path")
            .and_then(|p| p.as_str())
            .map(str::to_string)
    } else {
        None
    };
    let before = match &target {
        Some(p) => read_text(state, scope, p).await,
        None => None,
    };

    let outcome = tools::execute(state, scope, job, &call.function.name, args).await;

    if let (Some(p), Some(before)) = (target, before) {
        if let Some(after) = read_text(state, scope, &p).await {
            // 只推真正的变更：把同样的内容重写一遍不算
            if after != before {
                let _ = channel.send(AiEvent::FileChange {
                    id: call.id.clone(),
                    path: p,
                    before,
                    after,
                });
            }
        }
    }
    outcome
}

/// 读远端文件为文本（变更记录用）。二进制 / 无权限一律 None。
async fn read_text(state: &AppState, scope: &AiScope, path: &str) -> Option<String> {
    let sid = scope.session_id.as_deref()?;
    let session = state.sessions.get(sid).await.ok()?;
    let fs = session.transport().await.fs().await.ok()?;
    let bytes = fs.read_file(path, 1024 * 1024).await.ok()?;
    String::from_utf8(bytes).ok()
}

/// 把较早的工具结果压成一行占位，保留最近几条的原文。
///
/// 返回剪掉的条数。只动**工具结果**，不动 user/assistant 对话 ——
/// 对话是"我们商量过什么"，工具结果是"当时看到了什么"，后者可以重跑，
/// 前者丢了就真丢了。
fn prune_tool_results(history: &mut [ChatMessage]) -> usize {
    /// 最近这几条工具结果保持原文：模型往往还在引用它们。
    const KEEP_RECENT: usize = 4;
    const PLACEHOLDER: &str =
        "（这条工具结果的原文因上下文接近上限已压缩，需要时请重新执行该工具）";

    let idx: Vec<usize> = history
        .iter()
        .enumerate()
        .filter(|(_, m)| m.role == "tool")
        .map(|(i, _)| i)
        .collect();
    if idx.len() <= KEEP_RECENT {
        return 0;
    }
    let mut pruned = 0;
    for &i in &idx[..idx.len() - KEEP_RECENT] {
        let replaceable = match &history[i].content {
            serde_json::Value::String(s) => s.len() > PLACEHOLDER.len(),
            _ => false,
        };
        if replaceable {
            history[i].content = serde_json::Value::String(PLACEHOLDER.to_string());
            pruned += 1;
        }
    }
    pruned
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

/// 「本会话已允许此类」判定。
///
/// 单独抽成函数只为一件事：把「**必须异步读**」这条钉在测试里。
///
/// 这里曾经写成 `blocking_read()`。本函数跑在 tokio 的 worker 线程上 —— 也就是
/// 运行时内部，同步阻塞 API 在那里会直接 panic（"Cannot block the current thread
/// from within a runtime"）。再叠加 release 的 `panic = "abort"`，一次 panic 就是
/// 整个应用无声消失：没有日志、没有提示，用户那边看到的就是「AI 用到一半退出了」。
async fn session_allows(job: &AiJob, kind: Option<&'static str>) -> bool {
    match kind {
        Some(k) => job.session_allowed.read().await.contains(k),
        None => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 刻意用多线程 flavor：真机上的 panic 就发生在 `tokio-rt-worker` 线程，
    /// 单线程 flavor 复现不出同一个执行上下文，这条回归就守不住了。
    ///
    /// 反向验证过：把 `session_allows` 改回 `blocking_read()`，本测试立刻 panic 失败。
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn session_allows_is_readable_from_a_runtime() {
        let job = AiJob::new("t");
        assert!(!session_allows(&job, None).await);
        assert!(!session_allows(&job, Some("write_fs")).await);

        job.session_allowed.write().await.insert("write_fs".into());
        assert!(session_allows(&job, Some("write_fs")).await);
        // 危险命令不进这张表 —— 它本来就该每次问。
        assert!(!session_allows(&job, Some(guard::KIND_DANGER)).await);
    }
}
