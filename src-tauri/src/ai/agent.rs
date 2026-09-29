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
use super::{AiEvent, AiJob, AiScope, ConfirmDecision, FilePreview};

const MAX_TURNS: u32 = 24;

/// 上下文压缩触发线：上一轮的输入占用超过窗口这个百分比时，下一轮发送前先剪枝。
const COMPACT_AT_PERCENT: f64 = 75.0;

/// 推给前端的单条工具输出上限（64K 字符）。
///
/// 不是不信任用户，是怕界面被一条 `cat` 大文件卡死 —— 几 MB 文本一次性塞进
/// DOM，滚动条会当场失去响应。超出部分在模型上下文与审计日志里仍然完整。
const MAX_TOOL_TEXT: usize = 64 * 1024;

/// `ToolArgs` 进度的最小推送间隔。
///
/// 工具参数是逐 token 流式到达的：一份 60 行的文件内容 ≈ 上千个分片，原样转发
/// 等于用 IPC 刷屏（前端每收一条就要重渲染一次列表）。这条事件只需要表达
/// 「还在动」，精确进度由 `ToolCall`（参数齐了）那一步给出，所以按时间节流。
///
/// `pub(crate)` 是因为 `takeover.rs` 的回调要共用同一口径 —— 两处各写一个
/// 数字，迟早会漂移。
pub(crate) const TOOL_ARGS_PING: Duration = Duration::from_millis(120);

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
        // 参数进度节流计时器。闭包每次循环重建 ⇒ 每次 `chat_streaming` 独立计时，
        // 所以一次生成里的第一条进度一定是立刻推出去的（否则用户头两秒还是没反馈）。
        let mut last_args_ping: Option<std::time::Instant> = None;
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
                    // 生成工具参数的进度。以前这个分支不存在，于是模型吐
                    // `write_file.content`（可能几十行）的几十秒里界面零事件 ——
                    // 用户看到的是「AI 说完话就卡死」。不节流会刷屏，见 TOOL_ARGS_PING。
                    StreamItem::ToolArgs { name, chars } => {
                        let now = std::time::Instant::now();
                        let due = last_args_ping
                            .is_none_or(|t| now.duration_since(t) >= TOOL_ARGS_PING);
                        if due {
                            last_args_ping = Some(now);
                            let _ = chan2.send(AiEvent::ToolArgs { tool: name, chars });
                        }
                    }
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

                // ── 先读后写门禁：**提前到确认之前**（2026-09-30）────────────
                // 这道检查原本埋在 `tools::execute` 里，也就是用户点完「允许」
                // **之后**才跑。于是出现这样一串：用户对着一张带着完整 diff 的
                // 卡片点了允许 → 才被告知「本次任务还没读过这个文件」→ 模型
                // read_file、把整份内容**再生成一遍**。白点一次点击，白烧一遍
                // token（写文件的内容常有上千 token，用户看到的就是「重写」「浪费」）。
                //
                // 判定一个字没改（仍是 `edit::read_gate` 那一份），只是把
                // 「什么时候出结论」提前 —— 没读过的写入压根不该弹确认卡片。
                // `tools::execute` 里那份保留，作纵深防御。
                if let Some(rej) = tools::edit::read_gate_for(&call.function.name, &job.id, &args) {
                    let _ = channel.send(AiEvent::ToolResult {
                        id: call.id.clone(),
                        ok: false,
                        summary: truncate_summary(&rej.text, 400),
                        text: rej.text.clone(),
                        truncated: rej.truncated,
                        exit_code: rej.exit_code,
                    });
                    history.push(ChatMessage::tool_result(&call.id, rej.text));
                    continue;
                }

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
                            // 改动预览只在这个分支里算：本会话已放行同类时卡片
                            // 根本不出现，白读一次文件。
                            let preview = preview_change(state, &scope, &call.function.name, &args)
                                .await
                                .as_ref()
                                .and_then(ChangePreview::frontend_preview);
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
                                reason: ruling.reason.clone(),
                                preview,
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

/// 执行工具；若是写文件类，补推一条「变更记录」。
///
/// 取数一律在**工具执行前**完成：写完之后再读，读到的就是 after，
/// 前后对照永远为空 —— 这正是本函数存在的全部理由。
async fn execute_with_diff(
    state: &AppState,
    scope: &AiScope,
    job: &Arc<AiJob>,
    call: &super::provider::ToolCall,
    args: &serde_json::Value,
    channel: &Channel<AiEvent>,
) -> tools::ToolOutput {
    let planned = preview_change(state, scope, &call.function.name, args).await;

    let outcome = tools::execute(state, scope, job, &call.function.name, args).await;

    if let Some(pv) = planned {
        // 改后内容以**执行后重读**为准：那是真实落盘结果，比我们的预测可靠
        // （工具可能做了没预料到的事）。重读不出来时退回预测值 ——
        // 有它就不至于让整张「变更记录」凭空消失。
        let after = match read_before(state, scope, &pv.path).await {
            BeforeText::Text(t) => Some(t),
            _ => pv.after.clone(),
        };
        let path = pv.path.clone();
        if let Some((before, after)) = change_record(pv.before.clone(), after) {
            let _ = channel.send(AiEvent::FileChange {
                id: call.id.clone(),
                path,
                before,
                after,
            });
        }
    }
    outcome
}

/* ── 写文件类工具的「前后对照」────────────────────────────────────────
确认前的预览与执行后的变更记录共用同一份取数逻辑：两处都要求"改动一目了然"，
却各自读一次文件的话，早晚会走偏成两套判定。 */

/// 前后对照的读取上限。挡的是「把一个 4GB 的日志读进内存」，与渲染无关。
const MAX_DIFF_BYTES: u64 = 1024 * 1024;

/// 送进**确认卡片**的那对前后文本总长上限。
///
/// 比 `MAX_DIFF_BYTES` 小一档：这一对要在用户按下「允许」**之前**走完 IPC 并渲染，
/// 而没人会去读一张 2MB 的 diff。超限就给不出预览，退回展示原始参数
/// （原始参数里本来就带着全文，所以是"少了个视图"，不是"少了信息"）。
const MAX_PREVIEW_BYTES: usize = 512 * 1024;

/// 写文件类工具：只有这两个会改文件内容。
///
/// 直接复用 `tools::edit::WRITE_TOOLS` —— 那份名单同时被「先读后写」门禁读，
/// 两处各写一遍的话，将来加工具会出现「一边认得、一边不认得」。
fn is_write_tool(name: &str) -> bool {
    tools::edit::is_write_tool(name)
}

/// 执行前对目标文件的了解程度。
///
/// 刻成三态而不是 `Option<String>`：**「文件不存在」和「存在但读不出」必须分开**。
/// 以前这两者被一视同仁地当成 `None`，配合 `if let (Some(_), Some(before))`
/// 直接把新建文件（AI 写文件最常见的形态）的变更记录整条丢掉 ——
/// 界面上写完文件什么都不发生，README 承诺的「每次写文件生成修改前后 diff」
/// 对新建文件根本不成立。
#[derive(Debug, Clone, PartialEq, Eq)]
enum BeforeText {
    /// 执行前文件不存在 ⇒ 这是一次新建，`before` 按空串算（diff 全是新增）。
    Missing,
    /// 读到了文本，这是执行前的内容。
    Text(String),
    /// 存在但读不出文本（二进制 / 超过读取上限 / 无权限）。
    Unreadable,
}

/// 一次写文件类调用的改动预览。
#[derive(Debug, Clone)]
struct ChangePreview {
    path: String,
    before: BeforeText,
    /// 预测的执行后内容；算不出来 = `None`（`edit_file` 匹配不上等）。
    after: Option<String>,
}

impl ChangePreview {
    fn is_create(&self) -> bool {
        matches!(self.before, BeforeText::Missing)
    }

    fn before_text(&self) -> Option<String> {
        match &self.before {
            BeforeText::Missing => Some(String::new()),
            BeforeText::Text(t) => Some(t.clone()),
            BeforeText::Unreadable => None,
        }
    }

    /// 送进确认卡片的前后对照。给不出确切对照就返回 `None`。
    fn frontend_preview(&self) -> Option<FilePreview> {
        let before = self.before_text()?;
        let after = self.after.clone()?;
        // 前后一模一样就不给预览：卡片上会渲染成"一段没变的内容"，
        // 比直接摆原始参数更让人困惑。
        if after == before {
            return None;
        }
        if before.len() + after.len() > MAX_PREVIEW_BYTES {
            return None;
        }
        Some(FilePreview {
            path: self.path.clone(),
            before,
            after,
            kind: if self.is_create() { "create" } else { "modify" }.into(),
        })
    }
}

/// 执行**前**读目标文件。
///
/// 必须在工具之前调用，理由见 `execute_with_diff`。
async fn read_before(state: &AppState, scope: &AiScope, path: &str) -> BeforeText {
    let Some(sid) = scope.session_id.as_deref() else {
        return BeforeText::Unreadable;
    };
    let Ok(session) = state.sessions.get(sid).await else {
        return BeforeText::Unreadable;
    };
    let Ok(fs) = session.transport().await.fs().await else {
        return BeforeText::Unreadable;
    };

    // 先问"在不在"，再读。不靠 read_file 的错误类型去反推 ENOENT：
    // 那等于把各 Transport 的实现细节焊进来（本地是 Io(NotFound)，
    // SFTP / WinRM 未必），换一种连接方式判定就会静默失效。
    match fs.exists(path).await {
        Ok(true) => {}
        Ok(false) => return BeforeText::Missing,
        // 连"在不在"都问不出来（断线 / 无权限）：不猜。
        Err(_) => return BeforeText::Unreadable,
    }

    match fs.read_file(path, MAX_DIFF_BYTES).await {
        // 非 UTF-8 就是二进制，生成不了文本 diff。
        Ok(bytes) => match String::from_utf8(bytes) {
            Ok(t) => BeforeText::Text(t),
            Err(_) => BeforeText::Unreadable,
        },
        Err(_) => BeforeText::Unreadable,
    }
}

/// 算出一次写文件类调用的改动预览。
///
/// 非写文件类工具、缺 `path`、或拿不到可信的前后内容 ⇒ `None`。
async fn preview_change(
    state: &AppState,
    scope: &AiScope,
    name: &str,
    args: &serde_json::Value,
) -> Option<ChangePreview> {
    if !is_write_tool(name) {
        return None;
    }
    let path = args.get("path").and_then(|v| v.as_str())?.to_string();
    let before = read_before(state, scope, &path).await;
    let after = planned_after(name, args, &before);
    Some(ChangePreview {
        path,
        before,
        after,
    })
}

/// 从工具参数算出「预测的改后内容」（纯函数，单独测）。
///
/// 抽出来只为一件事：这条判定必须和 `edit_file` 执行时的判定是同一套
/// —— 都走 `tools::edit::apply_edit`。各写一份的话，预览和真实结果
/// 迟早会说的是两回事，而用户正是照着预览点的「允许」。
fn planned_after(name: &str, args: &serde_json::Value, before: &BeforeText) -> Option<String> {
    match name {
        "write_file" => args
            .get("content")
            .and_then(|v| v.as_str())
            .map(str::to_string),
        // edit_file 的改后内容**真的算得出来**。算不出来（找不到 old_string /
        // 命中多处但没开 replace_all / 原文根本读不出来）就不给预览 ——
        // 工具马上会报同样的错，猜一个 diff 只会把用户带偏。
        "edit_file" => {
            let BeforeText::Text(current) = before else {
                return None;
            };
            let old = args.get("old_string").and_then(|v| v.as_str())?;
            let new = args
                .get("new_string")
                .and_then(|v| v.as_str())
                .unwrap_or("");
            let all = args
                .get("replace_all")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            match tools::edit::apply_edit(current, old, new, all) {
                tools::edit::EditOutcome::Replaced { content, .. } => Some(content),
                _ => None,
            }
        }
        _ => None,
    }
}

/// 变更记录的取舍：这一次改动要不要生成 diff 卡片。
///
/// `Some((before, after))` = 推；`None` = 不推。三个 `None` 的理由各不相同，
/// 别把它们合并成一个 —— 合并的结果就是「新建文件没有任何变更记录」。
fn change_record(before: BeforeText, after: Option<String>) -> Option<(String, String)> {
    let before = match before {
        // 文件原本不存在 ⇒ 全部内容都是本次新增，对照的起点是空串。
        BeforeText::Missing => String::new(),
        BeforeText::Text(t) => t,
        // 存在但读不出：报"原样保留"和"全部新增"都是在编，
        // 宁可不推这张卡片。
        BeforeText::Unreadable => return None,
    };
    // 拿不到改后内容 ⇒ 没有对照可言。
    let after = after?;
    // 同一份内容重写一遍不算变更，否则每轮都会冒出一堆"无差异的 diff"。
    if after == before {
        return None;
    }
    Some((before, after))
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

    /* ── 「文件变更可审」：新建文件也必须有 diff ──────────────────────
    这一组守的就是 README 第 49 行那句承诺「每次写文件生成修改前后 diff」。
    以前 `before` 读不到（= 文件不存在）就整条 FileChange 不推，于是
    AI 新建文件 —— 写文件里最常见的形态 —— 在界面上一点痕迹都没有。 */

    #[test]
    fn new_file_still_gets_a_change_record() {
        // 反向验证：把 `BeforeText::Missing` 改回 `return None`，本用例立刻红。
        let (before, after) = change_record(BeforeText::Missing, Some("Hello\n".to_string()))
            .expect("新建文件必须生成变更记录（整份都是新增）");
        assert_eq!(before, "", "新建文件的对照起点是空串，不是「没有变更」");
        assert_eq!(after, "Hello\n");
    }

    #[test]
    fn modified_file_records_the_real_before() {
        let (before, after) =
            change_record(BeforeText::Text("a\nb\n".into()), Some("a\nc\n".into()))
                .expect("内容变了就该有记录");
        assert_eq!(before, "a\nb\n");
        assert_eq!(after, "a\nc\n");
    }

    #[test]
    fn unreadable_before_does_not_fake_a_full_add() {
        // 二进制 / 超大 / 无权限：报"全部新增"会把「没读到」谎报成「原来没有」，
        // 报"原样保留"同样是编的 —— 宁可不推这张卡片。
        assert_eq!(
            change_record(BeforeText::Unreadable, Some("x".into())),
            None
        );
    }

    #[test]
    fn missing_after_is_not_a_change_record() {
        // 拿不到改后内容（写入失败 / 文件被删）⇒ 没有对照可言。
        assert_eq!(change_record(BeforeText::Text("a".into()), None), None);
    }

    #[test]
    fn rewriting_identical_content_is_not_a_change() {
        assert_eq!(
            change_record(BeforeText::Text("same\n".into()), Some("same\n".into())),
            None
        );
        // 边界：空内容写进不存在的文件 —— 前后都是空串，同样不算变更。
        assert_eq!(
            change_record(BeforeText::Missing, Some(String::new())),
            None
        );
    }

    /* ── 确认卡片的前后预览 ──────────────────────────────────────── */

    fn preview(before: BeforeText, after: Option<&str>) -> Option<FilePreview> {
        ChangePreview {
            path: "/etc/nginx/nginx.conf".into(),
            before,
            after: after.map(str::to_string),
        }
        .frontend_preview()
    }

    #[test]
    fn preview_tells_create_from_modify() {
        let created =
            preview(BeforeText::Missing, Some("server_tokens off;\n")).expect("新建文件要给预览");
        assert_eq!(created.kind, "create");
        assert_eq!(created.before, "");

        let modified = preview(
            BeforeText::Text("server_tokens on;\n".into()),
            Some("server_tokens off;\n"),
        )
        .expect("修改要给预览");
        assert_eq!(modified.kind, "modify");
        assert_eq!(modified.before, "server_tokens on;\n");
    }

    #[test]
    fn preview_is_dropped_when_the_outcome_cannot_be_predicted() {
        // after 算不出来时不硬编一个：卡片退回展示原始参数。
        assert!(preview(BeforeText::Text("a\n".into()), None).is_none());
        // 原文读不出（二进制等）同样不给 —— 前后没有可信的起点。
        assert!(preview(BeforeText::Unreadable, Some("b\n")).is_none());
    }

    #[test]
    fn preview_is_dropped_when_nothing_would_change() {
        // 渲染成"一段没变的内容"比直接摆原始参数更让人困惑。
        assert!(preview(BeforeText::Text("same\n".into()), Some("same\n")).is_none());
    }

    #[test]
    fn preview_skips_pairs_too_big_to_review() {
        // 边界是「总长超过上限」而不是「≥」：恰好等于上限仍要给（别把边界写松）。
        let big = "x".repeat(MAX_PREVIEW_BYTES + 1);
        assert!(preview(BeforeText::Missing, Some(&big)).is_none());
        let edge = "x".repeat(MAX_PREVIEW_BYTES);
        assert!(preview(BeforeText::Missing, Some(&edge)).is_some());
        // 前后各占一半、合计不超限也照样给。
        let half = "x".repeat(MAX_PREVIEW_BYTES / 2);
        assert!(preview(
            BeforeText::Text("y".repeat(MAX_PREVIEW_BYTES / 2)),
            Some(&half)
        )
        .is_some());
    }

    /* ── edit_file 的预览必须与真实执行同源 ─────────────────────── */

    #[test]
    fn edit_preview_reuses_the_tools_own_uniqueness_rule() {
        let args = serde_json::json!({
            "path": "/etc/nginx/nginx.conf",
            "old_string": "listen 80;",
            "new_string": "listen 8080;",
        });
        let current = BeforeText::Text("listen 80;\nroot /var/www;\n".into());
        assert_eq!(
            planned_after("edit_file", &args, &current),
            Some("listen 8080;\nroot /var/www;\n".into())
        );

        // 找不到 old_string / 命中多处但没开 replace_all：工具会拒，预览也不给。
        let absent = serde_json::json!({
            "path": "/x", "old_string": "没有这一行", "new_string": "y",
        });
        assert_eq!(planned_after("edit_file", &absent, &current), None);
        let ambiguous = serde_json::json!({
            "path": "/x", "old_string": "listen 80;", "new_string": "listen 8080;",
        });
        let twice = BeforeText::Text("listen 80;\nlisten 80;\n".into());
        assert_eq!(planned_after("edit_file", &ambiguous, &twice), None);
        // 开 replace_all 就放行。
        let mut with_all = ambiguous.clone();
        with_all["replace_all"] = serde_json::json!(true);
        assert_eq!(
            planned_after("edit_file", &with_all, &twice),
            Some("listen 8080;\nlisten 8080;\n".into())
        );

        // 原文读不出来（文件不存在 / 读不出）时不预测。
        assert_eq!(
            planned_after("edit_file", &ambiguous, &BeforeText::Missing),
            None
        );
        assert_eq!(
            planned_after("edit_file", &ambiguous, &BeforeText::Unreadable),
            None
        );
    }

    #[test]
    fn previews_are_only_for_write_tools() {
        let args = serde_json::json!({"path": "/etc/hosts", "content": "1.1.1.1\n"});
        assert_eq!(
            planned_after("write_file", &args, &BeforeText::Missing),
            Some("1.1.1.1\n".into())
        );
        // 其他工具一律不给预览（它们不改文件内容）。
        for name in ["read_file", "list_dir", "exec_commands", "docker_exec"] {
            assert_eq!(
                planned_after(name, &args, &BeforeText::Unreadable),
                None,
                "{name}"
            );
        }
        assert!(!is_write_tool("read_file"));
        assert!(is_write_tool("write_file") && is_write_tool("edit_file"));
    }

    /* ── 跨模块的「连线」也得有东西守着 ───────────────────────────── */

    /// 先读后写门禁要跑在**弹确认卡片之前**。
    ///
    /// 这条守不住行为（`run()` 要一整个 `AppState`，单测里起不来），但它回归的
    /// 后果非常具体：一旦挪回确认之后，用户就会「点完允许才被告知没读过这个
    /// 文件」，接着模型 read_file、把整份内容**再生成一遍** —— 白点一次点击 +
    /// 白烧一遍 token（2026-09-30 用户报的正是这个）。所以直接盯源码里的先后
    /// 顺序：守的是**顺序**，不是实现。
    #[test]
    fn read_gate_runs_before_the_confirm_card() {
        assert!(
            gate_precedes_confirm(production_source()),
            "先读后写门禁必须早于 ConfirmRequired：晚一步，用户就会白点一次允许"
        );
        // 反向：顺序反过来、或缺任一边，都必须判 false。
        // 少了这段，本条用例就退化成"符号存在性检查"—— 门禁被挪到确认之后，
        // 它照样是绿的，而那正是要防的那种回归。
        assert!(!gate_precedes_confirm(
            "先弹 AiEvent::ConfirmRequired，再调 read_gate_for"
        ));
        assert!(!gate_precedes_confirm("只有 read_gate_for"));
        assert!(!gate_precedes_confirm("只有 AiEvent::ConfirmRequired"));
        assert!(!gate_precedes_confirm(""));
    }

    /// 门禁是否排在确认卡片之前。
    ///
    /// 抽成函数是为了能拿**构造的文本**验证顺序本身 —— 直接对生产源码断言，
    /// 只能证明两个符号都在文件里，证明不了谁在前面。
    fn gate_precedes_confirm(src: &str) -> bool {
        match (
            src.find("read_gate_for"),
            src.find("AiEvent::ConfirmRequired"),
        ) {
            (Some(g), Some(c)) => g < c,
            _ => false,
        }
    }

    /// 回调必须把 `StreamItem::ToolArgs` 转成 `AiEvent::ToolArgs` 推出去。
    ///
    /// 这是个极容易被"顺手清理"的分支：它不产出任何最终结果，删掉之后编译照样
    /// 过、所有行为测试照样绿 —— 只有界面在模型写大文件时重新变成死的
    /// （就是 2026-09-30 那个「以为卡死」）。跨了进程边界，行为测试够不着，
    /// 所以在这里守一根线。
    #[test]
    fn tool_args_progress_is_forwarded_to_the_frontend() {
        let prod = production_source();
        assert!(
            prod.contains("StreamItem::ToolArgs") && prod.contains("AiEvent::ToolArgs"),
            "回调里要把参数生成进度转成 AiEvent::ToolArgs，否则写大文件时界面零反馈"
        );
    }

    /// 本文件 `#[cfg(test)]` 之前的那部分 —— 也就是生产代码。
    ///
    /// 上面两条测试靠源码顺序断言，而 `include_str!` 把测试模块自身也带了进来
    /// （断言文本、错误消息里同样写着那两个名字）。不切开的话它们会自己匹配
    /// 自己，测试永远绿 —— 等于没写。
    fn production_source() -> &'static str {
        include_str!("agent.rs")
            .split("#[cfg(test)]")
            .next()
            .unwrap_or_default()
    }
}
