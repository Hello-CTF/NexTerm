//! AI 命令（§6.2 ai）：chat / cancel / confirm / 接管 / 会话管理。

use futures::FutureExt;
use serde::Deserialize;
use serde_json::json;
use tauri::ipc::Channel;

use crate::ai::{agent, provider::LlmClient, takeover, AiEvent, AiJob, AiScope, ConfirmDecision};
use crate::error::{AppError, AppResult};
use crate::ipc_types::{ConversationDto, MessageDto};
use crate::state::ManagedState;

/// 从首条提问里取一个会话标题。
///
/// 取第一行非空内容、压掉多余空白，再按**字符**（不是字节）截断 ——
/// 中文标题按字节切会切出半个字，是这类"顺手写的截断"最经典的翻车方式。
fn title_from(message: &str) -> String {
    const MAX_CHARS: usize = 24;
    let first = message
        .lines()
        .find(|l| !l.trim().is_empty())
        .unwrap_or("")
        .trim();
    let flat = first.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= MAX_CHARS {
        return flat;
    }
    format!("{}…", flat.chars().take(MAX_CHARS).collect::<String>())
}

/// `ai_chat` 的请求体。
///
/// 收成结构体有两个理由：平铺参数已经到 8 个（clippy 上限 7），
/// 而且这几个字段天然是同一件事 ——「这一次提问要带什么」。
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AiChatArgs {
    pub conversation_id: Option<String>,
    pub scope: AiScope,
    pub message: String,
    pub selection: Option<String>,
    /// 图片附件（裸 base64 或 data URI）；空/不传 = 纯文本。
    pub images: Option<Vec<String>>,
    /// 计划模式：只做只读调研并把方案交出来，等用户批准后再动手。
    pub plan_mode: Option<bool>,
}

#[tauri::command]
pub async fn ai_chat(
    state: ManagedState<'_>,
    args: AiChatArgs,
    channel: Channel<AiEvent>,
) -> AppResult<serde_json::Value> {
    let AiChatArgs {
        conversation_id,
        scope,
        message,
        selection,
        images,
        plan_mode,
    } = args;
    // 会话不存在则创建。标题就取第一条提问 —— 没人会回来手动给会话命名，
    // 而历史列表里一排空白项，等于这个功能没做。
    let conversation_id = match conversation_id {
        Some(id) => id,
        None => {
            let c = state
                .store
                .conv_create(&title_from(&message), &json!({ "scope": scope }))
                .await?;
            c.id
        }
    };
    let job = AiJob::new(&crate::ids::new_id());
    state.ai.register_job(std::sync::Arc::clone(&job)).await;
    let job_id = job.id.clone();

    let input = agent::AgentRunInput {
        job,
        // clone 一份：下面还要把这个 id 回传给前端。前端拿不到新建的会话 id，
        // 下一轮追问就又会带着 `None` 进来 —— 于是每问一句多出一个「独立会话」。
        conversation_id: conversation_id.clone(),
        scope,
        message,
        selection,
        images: images.unwrap_or_default(),
        plan_mode: plan_mode.unwrap_or(false),
        channel,
    };
    let state2 = std::sync::Arc::clone(&state);
    let state3 = std::sync::Arc::clone(&state);
    // 外面留一份 channel：任务 panic 时 `input` 会被丢掉，里面那份就发不出消息了，
    // 那样界面会一直转圈 —— 比直接报错更糟。
    let chan = input.channel.clone();
    let job_for_task = job_id.clone();
    tokio::spawn(async move {
        // 包一层 catch_unwind：AI 任务里任何一处 panic，都不该变成「应用无声退出」。
        // 这条只在 unwind 策略下成立 —— 见根 Cargo.toml 里 panic 策略那段。
        let outcome = std::panic::AssertUnwindSafe(agent::run(&state2, input))
            .catch_unwind()
            .await;
        match outcome {
            Ok(Ok(())) => {}
            Ok(Err(e)) => {
                // 兜底：`agent::run` 内部现在绝大多数异常路径都会自己推 Error
                // （取消、模型请求失败），但这里仍然必须补一发 —— 前端的
                // `aiBusy` 只认 done / error 两个事件，漏推一次就是永久转圈：
                // 输入框禁用、没有任何停止入口，用户只能重启应用。
                tracing::error!(target: "ai", error = %e, "agent 运行失败");
                let _ = chan.send(AiEvent::Error {
                    message: format!("AI 任务失败：{e}"),
                    retryable: true,
                });
                state3.ai.finish_job(&job_for_task).await;
            }
            Err(_) => {
                // panic 详情已经由 lib.rs 的 hook 落到 logs/crash.log，这里只负责
                // 把「本轮废了」告诉界面，并清掉任务，避免转圈转到天荒地老。
                tracing::error!(target: "ai", "AI 任务 panic，本轮已中断");
                let _ = chan.send(AiEvent::Error {
                    message: "AI 任务内部出错了，本轮已中断。可以重发一次试试。".into(),
                    retryable: true,
                });
                state3.ai.finish_job(&job_for_task).await;
            }
        }
    });
    // `conversationId` 必须回传：首轮是这里新建的，前端只有拿到它才认得住同一个会话。
    Ok(json!({ "jobId": job_id, "conversationId": conversation_id }))
}

#[tauri::command]
pub async fn ai_cancel(state: ManagedState<'_>, job_id: String) -> AppResult<()> {
    state.ai.cancel_job(&job_id).await
}

#[tauri::command]
pub async fn ai_confirm(
    state: ManagedState<'_>,
    job_id: String,
    decision: ConfirmDecision,
) -> AppResult<()> {
    state.ai.send_confirm(&job_id, decision).await
}

#[tauri::command]
pub async fn ai_models(state: ManagedState<'_>) -> AppResult<Vec<String>> {
    let cfg = state.ai.provider.read().await.clone();
    let client = LlmClient::new(cfg)?;
    client.list_models().await
}

/// 两步连通性测试（§8.7）。
#[tauri::command]
pub async fn ai_test_provider(state: ManagedState<'_>) -> AppResult<serde_json::Value> {
    let cfg = state.ai.provider.read().await.clone();
    let client = LlmClient::new(cfg)?;
    let (models, chat) = client.test().await;
    Ok(json!({
        "modelsOk": models.is_ok(),
        "modelsError": models.err(),
        "chatOk": chat.is_ok(),
        "chatError": chat.err(),
    }))
}

#[tauri::command]
pub async fn ai_set_provider(
    state: ManagedState<'_>,
    config: crate::ai::ProviderConfig,
) -> AppResult<()> {
    *state.ai.provider.write().await = config.clone();
    state
        .store
        .setting_set("ai.provider", &serde_json::to_string(&config)?)
        .await
}

#[tauri::command]
pub async fn ai_get_provider(state: ManagedState<'_>) -> AppResult<crate::ai::ProviderConfig> {
    Ok(state.ai.provider.read().await.clone())
}

#[tauri::command]
pub async fn ai_get_permission(
    state: ManagedState<'_>,
) -> AppResult<crate::ai::guard::PermissionConfig> {
    Ok(state.ai.permission.read().await.clone())
}

/// 写权限配置（档位 + 自定义危险规则）。
#[tauri::command]
pub async fn ai_set_permission(
    state: ManagedState<'_>,
    config: crate::ai::guard::PermissionConfig,
) -> AppResult<()> {
    let mut cfg = config;
    cfg.danger_rules = sanitize_rules(cfg.danger_rules);
    *state.ai.permission.write().await = cfg.clone();
    state
        .store
        .setting_set("ai.permission", &serde_json::to_string(&cfg)?)
        .await
}

/// 清洗自定义危险规则：去首尾空白、丢掉空串、去重（大小写不敏感）。
///
/// **空串必须丢掉**：`contains("")` 恒为真，留一条空的进去会让每条命令
/// 都被判成危险命令 —— 面板上多留一行空输入框就够触发，这个坑很隐蔽。
fn sanitize_rules(raw: Vec<String>) -> Vec<String> {
    let mut seen = std::collections::HashSet::new();
    raw.into_iter()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .filter(|s| seen.insert(s.to_ascii_lowercase()))
        .collect()
}

#[tauri::command]
pub async fn ai_presets() -> AppResult<Vec<String>> {
    Ok(vec![
        "deepseek".into(),
        "openai".into(),
        "dashscope".into(),
        "moonshot".into(),
        "zhipu".into(),
        "ollama".into(),
        "lmstudio".into(),
        "vllm".into(),
    ])
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TakeoverEnterArgs {
    pub tab_id: String,
}

#[tauri::command]
pub async fn ai_takeover_enter(state: ManagedState<'_>, args: TakeoverEnterArgs) -> AppResult<()> {
    takeover::enter(&state, &args.tab_id).await?;
    Ok(())
}

#[tauri::command]
pub async fn ai_takeover_exit(
    state: ManagedState<'_>,
    tab_id: String,
    reason: Option<String>,
) -> AppResult<()> {
    takeover::exit(&state, &tab_id, reason.as_deref().unwrap_or("用户退出")).await;
    Ok(())
}

/// 启动接管循环（§8.6）。
#[tauri::command]
pub async fn ai_takeover_run(
    state: ManagedState<'_>,
    tab_id: String,
    instruction: String,
    allow_write: Option<bool>,
    max_steps: Option<u32>,
    channel: Channel<AiEvent>,
) -> AppResult<String> {
    let job_id = crate::ids::new_id();
    let state2 = std::sync::Arc::clone(&state);
    let job_for_task = job_id.clone();
    tokio::spawn(async move {
        if let Err(e) = takeover::run_takeover(
            &state2,
            &job_for_task,
            &tab_id,
            &instruction,
            // 至少给 1 步，避免 0 直接「步数上限」闪退
            max_steps.unwrap_or(takeover::DEFAULT_MAX_STEPS).max(1),
            allow_write.unwrap_or(true),
            channel,
        )
        .await
        {
            tracing::error!(target: "ai", error = %e, "接管失败");
        }
    });
    Ok(job_id)
}

#[tauri::command]
pub async fn ai_conversation_create(
    state: ManagedState<'_>,
    title: Option<String>,
    scope: Option<AiScope>,
) -> AppResult<ConversationDto> {
    Ok(state
        .store
        .conv_create(
            title.as_deref().unwrap_or(""),
            &json!({ "scope": scope.unwrap_or_default() }),
        )
        .await?
        .into())
}

#[tauri::command]
pub async fn ai_conversation_list(state: ManagedState<'_>) -> AppResult<Vec<ConversationDto>> {
    Ok(state
        .store
        .conv_list()
        .await?
        .into_iter()
        .map(Into::into)
        .collect())
}

#[tauri::command]
pub async fn ai_conversation_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.conv_delete(&id).await
}

#[tauri::command]
pub async fn ai_messages(
    state: ManagedState<'_>,
    conversation_id: String,
) -> AppResult<Vec<MessageDto>> {
    Ok(state
        .store
        .msg_list(&conversation_id)
        .await?
        .into_iter()
        .map(Into::into)
        .collect())
}

#[allow(dead_code)]
fn _unused() -> Result<(), AppError> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::title_from;

    #[test]
    fn title_takes_first_nonempty_line_and_flattens_whitespace() {
        assert_eq!(title_from("第一行\n第二行"), "第一行");
        assert_eq!(title_from("  帮我看看   磁盘  "), "帮我看看 磁盘");
        assert_eq!(title_from(""), "");
        assert_eq!(title_from("\n\n  \n"), "");
    }

    /// 按**字符**截断，不是按字节 —— 中文按字节切会切出半个字（乱码或直接 panic）。
    #[test]
    fn title_caps_by_chars_not_bytes() {
        let long = "这是一个很长很长的中文提问".repeat(5);
        let t = title_from(&long);
        assert_eq!(t.chars().count(), 25, "24 个字 + 省略号：{t}");
        assert!(t.ends_with('…'), "{t}");
    }
}
