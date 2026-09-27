//! 上下文用量与缓存命中统计（P0-5）。
//!
//! provider 层在流式响应里收集 token 账本（`provider::Completion::tokens_*`），
//! agent 每轮结束推给前端，前端渲染成功能行右侧的圆环（占用百分比 + 构成 + 缓存命中率）。
//!
//! **文件归属**：本文件由「上下文与用量」工作流独占。其他工作流不得修改。
//!
//! # 对外 API（调用方在 `ai/agent.rs`，由主线程接线）
//!
//! - 结构体 [`Usage`]：字段 `prompt_tokens` / `completion_tokens` / `cached_tokens` /
//!   `context_window`，序列化为 camelCase（`promptTokens` …）。
//! - [`Usage::context_used_percent`]：输入侧占上下文窗口的百分比（0..=100，除零保护）。
//! - [`Usage::cache_hit_percent`]：缓存命中率（0..=100，除零保护）。
//! - [`Usage::accumulate`]：把多轮累加进一个账本（用于会话总计；**不要**拿累计值去算占用）。
//! - [`parse_usage`]：对不同提供方的 `usage` JSON 做兼容解析，取不到一律当 0、绝不报错。
//!
//! 接线要点：圆环的「占用」应基于**最近一轮**的 `Usage`（`prompt_tokens` 是这一轮实际
//! 送出的输入 token），而不是累计值 —— 累计值是给计费看的，拿它除窗口会随轮次虚高。

use serde::{Deserialize, Serialize};

/// 一次请求（或一个会话累计）的 token 账本。
///
/// `cached_tokens` 是 `prompt_tokens` 的**子集**（命中 prompt cache 的那部分输入），
/// 因此它天然 `<= prompt_tokens`；解析时已做钳制，命中率辅助函数再钳一次兜底。
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Usage {
    /// 输入侧 token（含命中缓存的部分）。
    pub prompt_tokens: u64,
    /// 输出侧 token。
    pub completion_tokens: u64,
    /// 命中 prompt cache 的输入 token（`prompt_tokens` 的子集）。
    pub cached_tokens: u64,
    /// 模型上下文窗口上限；未知时为 0。
    pub context_window: u64,
}

impl Usage {
    /// 以给定窗口新建空账本。
    pub fn new(context_window: u64) -> Self {
        Self {
            context_window,
            ..Default::default()
        }
    }

    /// 是否有任何可展示的数据（还没对话过时为 false）。
    pub fn has_data(&self) -> bool {
        self.prompt_tokens > 0 || self.completion_tokens > 0
    }

    /// 上下文占用百分比（输入侧 / 窗口，0..=100）。
    ///
    /// 窗口为 0（未知）时返回 0 —— 宁可不画满环，也不能让前端拿到 NaN。
    /// 用**最近一轮**的账本计算；累计账本会虚高。
    pub fn context_used_percent(&self) -> f64 {
        if self.context_window == 0 {
            return 0.0;
        }
        clamp_percent(self.prompt_tokens as f64 / self.context_window as f64 * 100.0)
    }

    /// 缓存命中率（命中 / 输入，0..=100）。
    ///
    /// 输入为 0 时返回 0。命中数超过输入数（个别网关口径不一致）时按 100% 计。
    pub fn cache_hit_percent(&self) -> f64 {
        if self.prompt_tokens == 0 {
            return 0.0;
        }
        let hit = self.cached_tokens.min(self.prompt_tokens);
        clamp_percent(hit as f64 / self.prompt_tokens as f64 * 100.0)
    }

    /// 把另一份账本累加进来（会话总计）。
    ///
    /// `context_window` 取「非零者」—— 单轮账本的窗口常是 0（provider 不知道窗口），
    /// 只有 agent 用配置回填后才有值，累加时别被 0 覆盖掉。
    pub fn accumulate(&mut self, other: &Usage) {
        self.prompt_tokens = self.prompt_tokens.saturating_add(other.prompt_tokens);
        self.completion_tokens = self
            .completion_tokens
            .saturating_add(other.completion_tokens);
        self.cached_tokens = self.cached_tokens.saturating_add(other.cached_tokens);
        if other.context_window > 0 {
            self.context_window = other.context_window;
        }
    }
}

/// 把百分比钳进 0..=100；非有限值（NaN/Inf）一律当 0。
fn clamp_percent(x: f64) -> f64 {
    if !x.is_finite() {
        return 0.0;
    }
    x.clamp(0.0, 100.0)
}

/// 兼容解析 OpenAI Chat Completions 各家的 `usage` 对象。
///
/// 覆盖的口径（按优先级取第一个能取到的）：
/// - 输入：`prompt_tokens` → `input_tokens`
/// - 输出：`completion_tokens` → `output_tokens`
/// - 缓存命中：
///   1. `prompt_tokens_details.cached_tokens`（OpenAI）
///   2. `input_tokens_details.cached_tokens`
///   3. `prompt_cache_hit_tokens`（DeepSeek）
///   4. `cached_tokens`（部分网关平铺到家目录）
///   5. `cache_read_input_tokens`（Anthropic 风格命名）
///
/// 另外：某些实现只给 DeepSeek 的 hit/miss 而不给 `prompt_tokens`，这时用
/// `hit + miss` 反推输入量。任何字段缺失/类型不对都当 0，**不返回错误**。
///
/// 返回的账本 `context_window` 恒为 0 —— 窗口是本地配置，由调用方事后回填。
pub fn parse_usage(v: &serde_json::Value) -> Usage {
    let mut prompt = first_u64(v, &[&["prompt_tokens"], &["input_tokens"]]);
    let completion = first_u64(v, &[&["completion_tokens"], &["output_tokens"]]);
    let hit = first_u64(v, &[&["prompt_cache_hit_tokens"]]);
    let cached = first_u64(
        v,
        &[
            &["prompt_tokens_details", "cached_tokens"],
            &["input_tokens_details", "cached_tokens"],
            &["prompt_cache_hit_tokens"],
            &["cached_tokens"],
            &["cache_read_input_tokens"],
        ],
    );
    // 只给了 hit/miss 的情况：用两者之和反推输入量（DeepSeek 口径）。
    if prompt == 0 && hit > 0 {
        prompt = hit.saturating_add(first_u64(v, &[&["prompt_cache_miss_tokens"]]));
    }
    // 命中不可能超过输入：钳一下，避免个别网关口径不一致把命中率顶到 100% 以上。
    let cached = if prompt == 0 {
        cached
    } else {
        cached.min(prompt)
    };
    Usage {
        prompt_tokens: prompt,
        completion_tokens: completion,
        cached_tokens: cached,
        context_window: 0,
    }
}

/// 沿 `path` 逐级 `get`，任一级缺失即 None。
fn value_at<'a>(v: &'a serde_json::Value, path: &[&str]) -> Option<&'a serde_json::Value> {
    let mut cur = v;
    for key in path {
        cur = cur.get(*key)?;
    }
    Some(cur)
}

/// 宽松取 u64：既认数字，也认「数字字符串」——有些网关把计数当字符串发。
fn as_u64_lenient(v: Option<&serde_json::Value>) -> Option<u64> {
    let v = v?;
    if let Some(n) = v.as_u64() {
        return Some(n);
    }
    v.as_str()?.trim().parse::<u64>().ok()
}

/// 按顺序尝试多条路径，返回第一个能取到的 u64；都取不到返回 0。
fn first_u64(v: &serde_json::Value, paths: &[&[&str]]) -> u64 {
    for p in paths {
        if let Some(n) = as_u64_lenient(value_at(v, p)) {
            return n;
        }
    }
    0
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn parse_deepseek_hit_miss() {
        let v = json!({
            "prompt_tokens": 100,
            "completion_tokens": 20,
            "prompt_cache_hit_tokens": 64,
            "prompt_cache_miss_tokens": 36
        });
        let u = parse_usage(&v);
        assert_eq!(u.prompt_tokens, 100);
        assert_eq!(u.completion_tokens, 20);
        assert_eq!(u.cached_tokens, 64);
        assert_eq!(u.cache_hit_percent(), 64.0);
    }

    #[test]
    fn parse_deepseek_hit_miss_only_infers_prompt() {
        // 只给 hit/miss、不给 prompt_tokens：输入量按两者之和反推。
        let v = json!({
            "prompt_cache_hit_tokens": 40,
            "prompt_cache_miss_tokens": 10
        });
        let u = parse_usage(&v);
        assert_eq!(u.prompt_tokens, 50);
        assert_eq!(u.cached_tokens, 40);
    }

    #[test]
    fn parse_openai_nested_details() {
        let v = json!({
            "prompt_tokens": 200,
            "completion_tokens": 8,
            "prompt_tokens_details": { "cached_tokens": 128 }
        });
        let u = parse_usage(&v);
        assert_eq!(u.cached_tokens, 128);
        assert_eq!(u.cache_hit_percent(), 64.0);
    }

    #[test]
    fn parse_variants_and_string_numbers() {
        // input/output 命名 + 平铺 cached_tokens + 字符串数字，全部要兜住。
        let v = json!({
            "input_tokens": "1000",
            "output_tokens": 3,
            "cached_tokens": 250
        });
        let u = parse_usage(&v);
        assert_eq!(u.prompt_tokens, 1000);
        assert_eq!(u.completion_tokens, 3);
        assert_eq!(u.cached_tokens, 250);

        // Anthropic 风格命名。
        let u2 = parse_usage(&json!({ "input_tokens": 80, "cache_read_input_tokens": 80 }));
        assert_eq!(u2.cache_hit_percent(), 100.0);
    }

    #[test]
    fn parse_missing_or_garbage_is_zero_not_error() {
        let u = parse_usage(&json!({}));
        assert_eq!(u, Usage::default());
        assert_eq!(u.cached_tokens, 0);
        // 类型不对（对象/布尔/负号字符串）也当 0。
        let u2 = parse_usage(&json!({
            "prompt_tokens": { "unexpected": true },
            "completion_tokens": "-5",
            "cached_tokens": true
        }));
        assert_eq!(u2.prompt_tokens, 0);
        assert_eq!(u2.cached_tokens, 0);
    }

    #[test]
    fn cached_is_clamped_to_prompt() {
        // 命中数 > 输入数（口径不一致）：钳到输入量，命中率不超过 100%。
        let u = parse_usage(&json!({ "prompt_tokens": 100, "cached_tokens": 130 }));
        assert_eq!(u.cached_tokens, 100);
        assert_eq!(u.cache_hit_percent(), 100.0);
    }

    #[test]
    fn percentages_guard_against_zero_division() {
        let zero = Usage::default();
        assert_eq!(zero.context_used_percent(), 0.0);
        assert_eq!(zero.cache_hit_percent(), 0.0);
        // 只有窗口、没有用量：占用 0，不 panic。
        let win_only = Usage::new(64000);
        assert_eq!(win_only.context_used_percent(), 0.0);
    }

    #[test]
    fn used_percent_is_clamped_and_computed() {
        let u = Usage {
            prompt_tokens: 32_000,
            context_window: 64_000,
            cached_tokens: 8_000,
            completion_tokens: 500,
        };
        assert_eq!(u.context_used_percent(), 50.0);
        assert_eq!(u.cache_hit_percent(), 25.0);
        // 超过窗口也封顶 100%。
        let over = Usage {
            prompt_tokens: 100_000,
            context_window: 64_000,
            ..Default::default()
        };
        assert_eq!(over.context_used_percent(), 100.0);
    }

    #[test]
    fn accumulate_keeps_nonzero_window() {
        let mut total = Usage::default();
        total.accumulate(&Usage {
            prompt_tokens: 100,
            completion_tokens: 10,
            cached_tokens: 40,
            context_window: 0,
        });
        total.accumulate(&Usage {
            prompt_tokens: 200,
            completion_tokens: 20,
            cached_tokens: 60,
            context_window: 64_000,
        });
        assert_eq!(total.prompt_tokens, 300);
        assert_eq!(total.completion_tokens, 30);
        assert_eq!(total.cached_tokens, 100);
        assert_eq!(total.context_window, 64_000);
    }
}
