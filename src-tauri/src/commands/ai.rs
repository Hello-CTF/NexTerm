//! AI 命令（§6.2 ai）：chat / cancel / confirm / 接管 / 会话管理。

use serde::Deserialize;
use serde_json::json;
use tauri::ipc::Channel;

use crate::ai::{agent, provider::LlmClient, takeover, AiEvent, AiJob, AiScope, ConfirmDecision};
use crate::error::{AppError, AppResult};
use crate::state::ManagedState;

#[tauri::command]
pub async fn ai_chat(
    state: ManagedState<'_>,
    conversation_id: Option<String>,
    scope: AiScope,
    message: String,
    selection: Option<String>,
    channel: Channel<AiEvent>,
) -> AppResult<serde_json::Value> {
    // 会话不存在则创建
    let conversation_id = match conversation_id {
        Some(id) => id,
        None => {
            let c = state
                .store
                .conv_create("", &json!({ "scope": scope }))
                .await?;
            c.id
        }
    };
    let job = AiJob::new(&crate::ids::new_id());
    state.ai.register_job(std::sync::Arc::clone(&job)).await;
    let job_id = job.id.clone();

    let input = agent::AgentRunInput {
        job,
        conversation_id,
        scope,
        message,
        selection,
        channel,
    };
    let state2 = std::sync::Arc::clone(&state);
    tokio::spawn(async move {
        if let Err(e) = agent::run(&state2, input).await {
            tracing::error!(target: "ai", error = %e, "agent 运行失败");
        }
    });
    Ok(json!({ "jobId": job_id }))
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
    pub max_steps: Option<u32>,
    pub allow_write: Option<bool>,
}

#[tauri::command]
pub async fn ai_takeover_enter(state: ManagedState<'_>, args: TakeoverEnterArgs) -> AppResult<()> {
    takeover::enter(
        &state,
        &args.tab_id,
        args.max_steps.unwrap_or(30),
        args.allow_write.unwrap_or(true),
    )
    .await?;
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
) -> AppResult<crate::store::ConversationRow> {
    state
        .store
        .conv_create(
            title.as_deref().unwrap_or(""),
            &json!({ "scope": scope.unwrap_or_default() }),
        )
        .await
}

#[tauri::command]
pub async fn ai_conversation_list(
    state: ManagedState<'_>,
) -> AppResult<Vec<crate::store::ConversationRow>> {
    state.store.conv_list().await
}

#[tauri::command]
pub async fn ai_conversation_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    state.store.conv_delete(&id).await
}

#[tauri::command]
pub async fn ai_messages(
    state: ManagedState<'_>,
    conversation_id: String,
) -> AppResult<Vec<crate::store::MessageRow>> {
    state.store.msg_list(&conversation_id).await
}

#[allow(dead_code)]
fn _unused() -> Result<(), AppError> {
    Ok(())
}
