//! 多模型配置档案（P0-3）。
//!
//! 取代原先「全局唯一一份 ProviderConfig」的形态：现在可以存多份模型档案
//! （BYOK），并记录当前激活的那一份。全部落在 sqlite（与权限配置同一约定），
//! apiKey 也直接存 sqlite —— 与 `ai_get_permission` 保持一致，不额外走 vault。
//!
//! 落库形态刻意**复用 `setting` 表**（键 `ai.models`，值是整张档案表的 JSON），
//! 而不是新开一张表：`store/mod.rs` 的迁移与 `migrations/` 不归本工作流，
//! 且权限配置已经证明「一份结构化配置走 setting JSON」这条路是稳的。
//!
//! 运行时那份 `AiRuntime.provider` 仍然存在，但语义变成「当前激活档案的快照」：
//! 每次切换激活项时由命令层把它刷成 `active().to_provider()`，agent/客户端读法不变。
//!
//! **文件归属**：本文件由「模型配置」工作流独占。其他工作流不得修改。
//! 对外 API 见下方 `pub` 项（调用方在 `commands/models.rs`，由主线程负责接线）。

use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ai::ProviderConfig;
use crate::error::{AppError, AppResult};
use crate::store::Store;

/// `setting` 表里存整张档案表的键。
pub const SETTING_KEY: &str = "ai.models";
/// 旧的单份提供方配置键 —— 只在首次迁移时读一次，之后由激活项同步写回。
pub const LEGACY_SETTING_KEY: &str = "ai.provider";

/// 一份模型档案（BYOK）。
///
/// 字段与 `ProviderConfig` 一一对应，外加一个面向用户的展示名 `name` 与档案 `id`。
#[derive(Debug, Clone, Serialize, Deserialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ModelProfile {
    /// ULID。新建时传空串，由 [`ModelProfileStore::upsert`] 分配。
    pub id: String,
    /// 展示名（下拉里显示的就是它）。
    pub name: String,
    pub base_url: String,
    pub api_key: String,
    pub model: String,
    pub temperature: f32,
    /// 上下文窗口（1k–2M，非法值兜底 —— 同类工具 踩过的坑）。
    #[ts(type = "number")]
    pub context_window: u64,
    /// None = 跟随系统代理（与 SSH/WinRM 策略相反，§8.7）。
    pub proxy: Option<String>,
    pub stream: bool,
}

impl Default for ModelProfile {
    fn default() -> Self {
        // 默认值直接取 `ProviderConfig` 的缺省，避免两处各写一份、日后对不上。
        let p = ProviderConfig::default();
        Self {
            id: String::new(),
            name: String::new(),
            base_url: p.base_url,
            api_key: p.api_key,
            model: p.model,
            temperature: p.temperature,
            context_window: p.context_window,
            proxy: p.proxy,
            stream: p.stream,
        }
    }
}

impl ModelProfile {
    /// 归一化：去空白、补默认展示名、数值范围兜底。**纯函数**，便于单测。
    ///
    /// 放在入库/出库两条路径的前面各调一次：入库存的是干净值（否则空串代理、
    /// NaN 温度会一直躺在库里），出库再兜一次（防手改过的库文件让内核读到脏值）。
    pub fn sanitized(mut self) -> Self {
        self.name = self.name.trim().to_string();
        self.base_url = self.base_url.trim().trim_end_matches('/').to_string();
        self.model = self.model.trim().to_string();
        self.api_key = self.api_key.trim().to_string();
        if self.name.is_empty() {
            // 没起名就退到模型名；模型名也空时给个占位，免得下拉里出现空白项
            self.name = if self.model.is_empty() {
                "未命名模型".to_string()
            } else {
                self.model.clone()
            };
        }
        // NaN 不能直接 clamp（结果仍是 NaN），先挡掉
        self.temperature = if self.temperature.is_finite() {
            self.temperature.clamp(0.0, 2.0)
        } else {
            ProviderConfig::default().temperature
        };
        self.context_window = self.context_window.clamp(1_000, 2_000_000);
        self.proxy = self
            .proxy
            .map(|p| p.trim().to_string())
            .filter(|p| !p.is_empty());
        self
    }

    /// 转成运行时使用的 `ProviderConfig`（agent / LlmClient 读的是这一份）。
    pub fn to_provider(&self) -> ProviderConfig {
        ProviderConfig {
            base_url: self.base_url.clone(),
            api_key: self.api_key.clone(),
            model: self.model.clone(),
            temperature: self.temperature,
            context_window: self.context_window,
            proxy: self.proxy.clone(),
            stream: self.stream,
        }
    }

    /// 从旧的单份配置升格成一份档案（一次性迁移用）。
    fn from_legacy(cfg: &ProviderConfig) -> Self {
        Self {
            id: String::new(),
            name: String::new(),
            base_url: cfg.base_url.clone(),
            api_key: cfg.api_key.clone(),
            model: cfg.model.clone(),
            temperature: cfg.temperature,
            context_window: cfg.context_window,
            proxy: cfg.proxy.clone(),
            stream: cfg.stream,
        }
    }
}

/// 预设 → 一份新档案（模板）。
///
/// 复用 `ProviderConfig::from_preset` 的预设表，避免「预设」这件事在两处各写一份。
/// 返回的档案 id 为空（尚未入库），apiKey 为空（预设里没有密钥）。
pub fn preset_profile(preset: &str) -> Option<ModelProfile> {
    let cfg = ProviderConfig::from_preset(preset, "", None)?;
    Some(ModelProfile {
        id: String::new(),
        name: preset.to_string(),
        base_url: cfg.base_url,
        model: cfg.model,
        temperature: cfg.temperature,
        context_window: cfg.context_window,
        proxy: cfg.proxy,
        stream: cfg.stream,
        api_key: String::new(),
    })
}

/// 档案表：全部档案 + 当前激活项的 id。
///
/// 直接作为 `ai_model_profiles` 的返回值给前端，省得前端拉两次。
#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ModelProfilesView {
    pub profiles: Vec<ModelProfile>,
    pub active_id: Option<String>,
}

/// 落库形态（`ai.models` 的 JSON 形状）。**不导出**给前端 —— 前端只认 view。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelProfileStore {
    #[serde(default)]
    pub profiles: Vec<ModelProfile>,
    #[serde(default)]
    pub active_id: Option<String>,
}

impl ModelProfileStore {
    /// 从两段 setting JSON 还原档案表（纯函数，含旧配置迁移）。
    ///
    /// `models_json` 解析失败 —— 例如手改坏了 —— 一律退化成空表，而不是报错：
    /// 配置坏掉不该让 AI 面板整个打不开，用户重填一份即可。
    pub fn from_setting(models_json: Option<&str>, legacy_json: Option<&str>) -> Self {
        if let Some(raw) = models_json {
            if let Ok(store) = serde_json::from_str::<ModelProfileStore>(raw) {
                return store.sanitize();
            }
        }
        // 迁移：旧版本只有一份 ai.provider，把它升格成第一条档案并设为激活
        if let Some(raw) = legacy_json {
            if let Ok(cfg) = serde_json::from_str::<ProviderConfig>(raw) {
                let has_content =
                    !cfg.base_url.is_empty() || !cfg.model.is_empty() || !cfg.api_key.is_empty();
                if has_content {
                    return Self {
                        profiles: vec![ModelProfile::from_legacy(&cfg).sanitized()],
                        active_id: None,
                    }
                    .sanitize();
                }
            }
        }
        Self::default()
    }

    /// 读库；首次迁移会顺手把结果落库（否则每次启动都会重算一个新 id）。
    pub async fn load(store: &Store) -> AppResult<Self> {
        let models = store.setting_get(SETTING_KEY).await?;
        let legacy = if models.is_none() {
            store.setting_get(LEGACY_SETTING_KEY).await?
        } else {
            None
        };
        let s = Self::from_setting(models.as_deref(), legacy.as_deref());
        if models.is_none() && !s.profiles.is_empty() {
            s.save(store).await?;
        }
        Ok(s)
    }

    pub async fn save(&self, store: &Store) -> AppResult<()> {
        let json = serde_json::to_string(self)?;
        store.setting_set(SETTING_KEY, &json).await
    }

    /// 让旧的单份键跟随当前激活项，避免没接线的旧代码（如老版功能行胶囊）读到过期值。
    pub async fn sync_legacy_provider(&self, store: &Store) -> AppResult<()> {
        if let Some(p) = self.active() {
            let json = serde_json::to_string(&p.to_provider())?;
            store.setting_set(LEGACY_SETTING_KEY, &json).await?;
        }
        Ok(())
    }

    /// 当前激活的档案。
    pub fn active(&self) -> Option<&ModelProfile> {
        let id = self.active_id.as_ref()?;
        self.profiles.iter().find(|p| &p.id == id)
    }

    /// 激活指定档案；id 不存在即报错（比静默不动更早暴露前端传错 id）。
    pub fn activate(&mut self, id: &str) -> AppResult<()> {
        if !self.profiles.iter().any(|p| p.id == id) {
            return Err(AppError::NotFound(format!("模型档案 {id}")));
        }
        self.active_id = Some(id.to_string());
        Ok(())
    }

    /// 新增或更新一份档案，返回落库后的档案（新档案在这里被分配 id）。
    pub fn upsert(&mut self, profile: ModelProfile) -> ModelProfile {
        let mut p = profile.sanitized();
        if p.id.is_empty() {
            p.id = crate::ids::new_id();
        }
        match self.profiles.iter_mut().find(|x| x.id == p.id) {
            Some(slot) => *slot = p.clone(),
            None => self.profiles.push(p.clone()),
        }
        self.ensure_active();
        p
    }

    /// 删除档案；返回是否真的删到了（供命令层决定要不要提示）。
    pub fn delete(&mut self, id: &str) -> bool {
        let before = self.profiles.len();
        self.profiles.retain(|p| p.id != id);
        self.ensure_active();
        self.profiles.len() != before
    }

    /// 去重 + 归一化 + 保证激活项有效。加载路径的统一收口。
    fn sanitize(mut self) -> Self {
        let mut seen = std::collections::HashSet::new();
        let mut out = Vec::with_capacity(self.profiles.len());
        for p in self.profiles.drain(..) {
            let p = p.sanitized();
            if seen.insert(p.id.clone()) {
                out.push(p);
            }
        }
        self.profiles = out;
        self.ensure_active();
        self
    }

    /// 激活项缺失/指向已删档案时，退到列表第一份（有档案就一定有激活项）。
    fn ensure_active(&mut self) {
        let valid = self
            .active_id
            .as_ref()
            .map(|id| self.profiles.iter().any(|p| &p.id == id))
            .unwrap_or(false);
        if !valid {
            self.active_id = self.profiles.first().map(|p| p.id.clone());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn profile(id: &str, name: &str) -> ModelProfile {
        ModelProfile {
            id: id.to_string(),
            name: name.to_string(),
            base_url: "https://api.example.com/v1".to_string(),
            api_key: "sk-x".to_string(),
            model: "m1".to_string(),
            ..Default::default()
        }
    }

    #[test]
    fn sanitize_trims_fills_and_clamps() {
        let p = ModelProfile {
            name: "   ".to_string(),
            base_url: "  https://x/v1/  ".to_string(),
            model: " gpt ".to_string(),
            api_key: " k ".to_string(),
            temperature: 9.0,
            context_window: 10,
            proxy: Some("   ".to_string()),
            ..Default::default()
        }
        .sanitized();
        // 没起名 → 退到模型名
        assert_eq!(p.name, "gpt");
        assert_eq!(p.base_url, "https://x/v1");
        assert_eq!(p.model, "gpt");
        assert_eq!(p.api_key, "k");
        assert_eq!(p.temperature, 2.0);
        assert_eq!(p.context_window, 1_000);
        assert!(p.proxy.is_none());
    }

    #[test]
    fn sanitize_rejects_nan_temperature() {
        let p = ModelProfile {
            temperature: f32::NAN,
            ..Default::default()
        }
        .sanitized();
        assert!(p.temperature.is_finite());
    }

    #[test]
    fn to_provider_maps_fields() {
        let p = profile("id1", "deepseek");
        let cfg = p.to_provider();
        assert_eq!(cfg.base_url, "https://api.example.com/v1");
        assert_eq!(cfg.api_key, "sk-x");
        assert_eq!(cfg.model, "m1");
        assert!(cfg.stream);
    }

    #[test]
    fn first_upsert_is_assigned_id_and_activated() {
        let mut store = ModelProfileStore::default();
        let saved = store.upsert(profile("", "first"));
        assert!(!saved.id.is_empty());
        assert_eq!(store.active_id.as_deref(), Some(saved.id.as_str()));
    }

    #[test]
    fn upsert_updates_in_place_and_keeps_order() {
        let mut store = ModelProfileStore {
            profiles: vec![profile("1", "a"), profile("2", "b")],
            active_id: Some("1".to_string()),
        };
        let saved = store.upsert(ModelProfile {
            name: "b-renamed".to_string(),
            ..profile("2", "b")
        });
        assert_eq!(saved.name, "b-renamed");
        assert_eq!(store.profiles.len(), 2);
        assert_eq!(store.profiles[1].name, "b-renamed");
        // 更新不该改动激活项
        assert_eq!(store.active_id.as_deref(), Some("1"));
    }

    #[test]
    fn deleting_active_falls_back_to_first() {
        let mut store = ModelProfileStore {
            profiles: vec![profile("1", "a"), profile("2", "b")],
            active_id: Some("2".to_string()),
        };
        assert!(store.delete("2"));
        assert_eq!(store.active_id.as_deref(), Some("1"));
        assert!(!store.delete("nope"));
    }

    #[test]
    fn deleting_last_profile_leaves_no_active() {
        let mut store = ModelProfileStore {
            profiles: vec![profile("1", "a")],
            active_id: Some("1".to_string()),
        };
        assert!(store.delete("1"));
        assert!(store.profiles.is_empty());
        assert!(store.active_id.is_none());
    }

    #[test]
    fn activate_rejects_unknown_id() {
        let mut store = ModelProfileStore {
            profiles: vec![profile("1", "a")],
            active_id: Some("1".to_string()),
        };
        assert!(store.activate("missing").is_err());
        assert!(store.activate("1").is_ok());
    }

    #[test]
    fn migrates_legacy_single_provider() {
        let legacy = serde_json::to_string(&ProviderConfig {
            base_url: "https://api.deepseek.com/v1".to_string(),
            model: "deepseek-chat".to_string(),
            api_key: "sk-legacy".to_string(),
            ..Default::default()
        })
        .unwrap();
        let store = ModelProfileStore::from_setting(None, Some(&legacy));
        assert_eq!(store.profiles.len(), 1);
        assert_eq!(store.profiles[0].model, "deepseek-chat");
        assert_eq!(store.profiles[0].name, "deepseek-chat");
        assert_eq!(
            store.active_id.as_deref(),
            Some(store.profiles[0].id.as_str())
        );
    }

    #[test]
    fn empty_legacy_provider_is_not_migrated() {
        let legacy = serde_json::to_string(&ProviderConfig::default()).unwrap();
        let store = ModelProfileStore::from_setting(None, Some(&legacy));
        assert!(store.profiles.is_empty());
    }

    #[test]
    fn broken_models_json_falls_back_to_empty() {
        let store = ModelProfileStore::from_setting(Some("{ not json"), None);
        assert!(store.profiles.is_empty());
        assert!(store.active_id.is_none());
    }

    #[test]
    fn preset_profile_fills_known_preset() {
        let p = preset_profile("deepseek").unwrap();
        assert_eq!(p.base_url, "https://api.deepseek.com/v1");
        assert_eq!(p.model, "deepseek-chat");
        assert!(p.id.is_empty());
        assert!(preset_profile("no-such-preset").is_none());
    }
}
