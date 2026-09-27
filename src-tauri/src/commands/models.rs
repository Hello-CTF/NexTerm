//! 模型档案的 IPC 命令层（P0-3）。
//!
//! **文件归属**：本文件由「模型配置」工作流独占。其他工作流不得修改。
//! 命令的注册（`commands/mod.rs` 的 `generate_handler!`）由主线程负责，
//! 本工作流只写 `#[tauri::command]` 函数本身。
//!
//! 约定：命令层保持薄 —— 反序列化 → 调 service → 包装错误（§6.2）。
//!
//! 切换/保存激活档案时必须顺手把 `AiRuntime.provider` 刷成新快照：
//! agent 与 LlmClient 读的是那一份，不刷就等于「界面上切了，实际没生效」。

use crate::ai::profiles::{ModelProfile, ModelProfileStore, ModelProfilesView};
use crate::ai::provider::LlmClient;
use crate::error::{AppError, AppResult};
use crate::state::ManagedState;

/// 档案总览：全部档案 + 当前激活项的 id。
#[tauri::command]
pub async fn ai_model_profiles(state: ManagedState<'_>) -> AppResult<ModelProfilesView> {
    let store = ModelProfileStore::load(&state.store).await?;
    Ok(ModelProfilesView {
        profiles: store.profiles.clone(),
        active_id: store.active_id.clone(),
    })
}

/// 新增或更新一份档案（id 为空即新增），返回落库后的档案。
///
/// 若保存的正是当前激活档案，同时刷新运行时快照 —— 用户改完参数点保存，
/// 下一次提问就该用新参数，不该等到手动切换。
#[tauri::command]
pub async fn ai_model_save(
    state: ManagedState<'_>,
    profile: ModelProfile,
) -> AppResult<ModelProfile> {
    let mut store = ModelProfileStore::load(&state.store).await?;
    let saved = store.upsert(profile);
    store.save(&state.store).await?;
    if store.active_id.as_deref() == Some(saved.id.as_str()) {
        apply_active(&state, &store).await?;
    }
    Ok(saved)
}

/// 删除一份档案；删掉的若是当前激活项，列表里的第一份会自动接任。
#[tauri::command]
pub async fn ai_model_delete(state: ManagedState<'_>, id: String) -> AppResult<()> {
    let mut store = ModelProfileStore::load(&state.store).await?;
    store.delete(&id);
    store.save(&state.store).await?;
    apply_active(&state, &store).await
}

/// 切换当前激活档案。
#[tauri::command]
pub async fn ai_model_activate(state: ManagedState<'_>, id: String) -> AppResult<()> {
    let mut store = ModelProfileStore::load(&state.store).await?;
    store.activate(&id)?;
    store.save(&state.store).await?;
    apply_active(&state, &store).await
}

/// 用给定档案的 baseUrl + apiKey 拉 `/models`。
///
/// 传的是**表单里的值**（可能还没保存），所以不读库、只按入参请求：
/// 用户想先「测一下能不能连」再决定要不要保存，这个顺序必须支持。
#[tauri::command]
pub async fn ai_model_refresh(profile: ModelProfile) -> AppResult<Vec<String>> {
    let client = LlmClient::new(profile.sanitized().to_provider())?;
    client.list_models().await
}

/// 预设 → 一份新档案（面板上的「一键填充」模板）。
#[tauri::command]
pub fn ai_model_preset(preset: String) -> AppResult<ModelProfile> {
    crate::ai::profiles::preset_profile(&preset)
        .ok_or_else(|| AppError::param(format!("未知预设 {preset}")))
}

/// 把当前激活档案推给运行时，并同步旧的单份键。
async fn apply_active(state: &ManagedState<'_>, store: &ModelProfileStore) -> AppResult<()> {
    if let Some(p) = store.active() {
        *state.ai.provider.write().await = p.to_provider();
        store.sync_legacy_provider(&state.store).await?;
    }
    Ok(())
}
