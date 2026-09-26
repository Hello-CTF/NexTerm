//! OpenAI Chat Completions 兼容接入（§8.7）：
//! 流式 SSE + 失败自动降级整块 + 代理可覆盖 + 两步连通性测试。
//! reqwest + rustls，不用 native-tls（§12.5）。

use std::time::Duration;

use futures::StreamExt;
use serde::{Deserialize, Serialize};

use crate::error::{AppError, AppResult};

use super::ProviderConfig;

/// 一条对话消息。
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatMessage {
    pub role: String,
    #[serde(default)]
    pub content: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_calls: Option<Vec<ToolCall>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
}

impl ChatMessage {
    pub fn system(text: impl Into<String>) -> Self {
        Self {
            role: "system".into(),
            content: serde_json::Value::String(text.into()),
            ..Default::default()
        }
    }
    pub fn user(text: impl Into<String>) -> Self {
        Self {
            role: "user".into(),
            content: serde_json::Value::String(text.into()),
            ..Default::default()
        }
    }
    pub fn assistant(text: impl Into<String>) -> Self {
        Self {
            role: "assistant".into(),
            content: serde_json::Value::String(text.into()),
            ..Default::default()
        }
    }
    pub fn tool_result(call_id: &str, text: impl Into<String>) -> Self {
        Self {
            role: "tool".into(),
            content: serde_json::Value::String(text.into()),
            tool_call_id: Some(call_id.to_string()),
            ..Default::default()
        }
    }
}

/// 工具调用（assistant 回复里携带）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolCall {
    pub id: String,
    /// 恒为 "function"
    #[serde(rename = "type")]
    pub kind: String,
    pub function: FunctionCall,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FunctionCall {
    pub name: String,
    pub arguments: String,
}

/// 工具 schema（OpenAI tools 格式）。
#[derive(Debug, Clone, Serialize)]
pub struct ToolSchema {
    pub name: String,
    pub description: String,
    /// JSON Schema。
    pub parameters: serde_json::Value,
}

impl ToolSchema {
    pub fn to_wire(&self) -> serde_json::Value {
        serde_json::json!({
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": self.parameters,
            }
        })
    }
}

/// 一轮完成的聚合结果。
#[derive(Debug, Clone, Default)]
pub struct Completion {
    pub content: String,
    pub reasoning: String,
    pub tool_calls: Vec<ToolCall>,
    pub finish_reason: String,
    pub tokens_in: u64,
    pub tokens_out: u64,
}

/// 流式片段（agent 消费）。
#[derive(Debug, Clone)]
pub enum StreamItem {
    Delta(String),
    Reasoning(String),
}

pub struct LlmClient {
    http: reqwest::Client,
    pub cfg: ProviderConfig,
}

impl LlmClient {
    pub fn new(cfg: ProviderConfig) -> AppResult<Self> {
        let mut builder = reqwest::Client::builder()
            .timeout(Duration::from_secs(300))
            .connect_timeout(Duration::from_secs(20));
        // §8.7：AI 默认跟随系统代理；显式配置则覆盖
        if let Some(proxy) = &cfg.proxy {
            if !proxy.is_empty() {
                let p = reqwest::Proxy::all(proxy)
                    .map_err(|e| AppError::AiProvider(format!("代理配置无效: {e}")))?;
                builder = builder.proxy(p);
            }
        }
        let http = builder
            .build()
            .map_err(|e| AppError::AiProvider(format!("HTTP 客户端构建失败: {e}")))?;
        Ok(Self { http, cfg })
    }

    fn url(&self, path: &str) -> String {
        let base = self.cfg.base_url.trim_end_matches('/');
        format!("{base}{path}")
    }

    fn headers(&self) -> reqwest::header::HeaderMap {
        let mut h = reqwest::header::HeaderMap::new();
        if let Ok(v) =
            reqwest::header::HeaderValue::from_str(&format!("Bearer {}", self.cfg.api_key))
        {
            h.insert(reqwest::header::AUTHORIZATION, v);
        }
        h
    }

    /// 一步：流式请求（内部回调收到 Delta / Reasoning）。
    /// 传输失败 → 自动降级整块请求（§8.7 / RainsIR 降级策略）。
    pub async fn chat_streaming(
        &self,
        messages: &[ChatMessage],
        tools: &[ToolSchema],
        mut on_item: impl FnMut(StreamItem) + Send,
    ) -> AppResult<Completion> {
        if self.cfg.stream {
            match self.try_stream(messages, tools, &mut on_item).await {
                Ok(c) => return Ok(c),
                Err(e) => {
                    tracing::warn!(target: "ai", error = %e, "流式请求失败，降级整块请求");
                }
            }
        }
        self.chat_block(messages, tools).await
    }

    async fn try_stream(
        &self,
        messages: &[ChatMessage],
        tools: &[ToolSchema],
        on_item: &mut impl FnMut(StreamItem),
    ) -> AppResult<Completion> {
        let mut body = serde_json::json!({
            "model": self.cfg.model,
            "messages": messages,
            "stream": true,
            "temperature": self.cfg.temperature,
            "stream_options": { "include_usage": true },
        });
        if !tools.is_empty() {
            body["tools"] = serde_json::Value::Array(tools.iter().map(|t| t.to_wire()).collect());
        }
        let resp = self
            .http
            .post(self.url("/chat/completions"))
            .headers(self.headers())
            .json(&body)
            .send()
            .await
            .map_err(|e| AppError::AiProvider(format!("请求失败: {e}")))?;
        let status = resp.status();
        if !status.is_success() {
            let text = resp.text().await.unwrap_or_default();
            return Err(AppError::AiProvider(format!(
                "HTTP {status}: {}",
                truncate_for_log(&text, 500)
            )));
        }

        let mut stream = resp.bytes_stream();
        let mut buf: Vec<u8> = Vec::new();
        let mut completion = Completion::default();
        // index → 聚合中的工具调用
        let mut pending_tools: std::collections::HashMap<u64, (String, String, String)> =
            std::collections::HashMap::new();

        while let Some(chunk) = stream
            .next()
            .await
            .map(|r| r.map_err(|e| AppError::AiProvider(format!("流中断: {e}"))))
        {
            let bytes = chunk?;
            buf.extend_from_slice(&bytes);
            while let Some(pos) = find_bon(&buf, b"\n") {
                let line_bytes: Vec<u8> = buf.drain(..=pos).collect();
                let line = String::from_utf8_lossy(&line_bytes).trim_end().to_string();
                let Some(payload) = line.strip_prefix("data: ") else {
                    continue;
                };
                let payload = payload.trim();
                if payload == "[DONE]" {
                    finish_tools(&mut completion, &mut pending_tools);
                    return Ok(completion);
                }
                let v: serde_json::Value = match serde_json::from_str(payload) {
                    Ok(v) => v,
                    Err(_) => continue,
                };
                if let Some(usage) = v.get("usage") {
                    completion.tokens_in = usage
                        .get("prompt_tokens")
                        .and_then(|x| x.as_u64())
                        .unwrap_or(0);
                    completion.tokens_out = usage
                        .get("completion_tokens")
                        .and_then(|x| x.as_u64())
                        .unwrap_or(0);
                }
                let Some(choices) = v.get("choices").and_then(|c| c.as_array()) else {
                    continue;
                };
                if let Some(choice) = choices.first() {
                    if let Some(fr) = choice.get("finish_reason").and_then(|f| f.as_str()) {
                        if !fr.is_empty() && fr != "null" {
                            completion.finish_reason = fr.to_string();
                        }
                    }
                    if let Some(delta) = choice.get("delta") {
                        if let Some(rc) = delta.get("reasoning_content").and_then(|r| r.as_str()) {
                            completion.reasoning.push_str(rc);
                            on_item(StreamItem::Reasoning(rc.to_string()));
                        }
                        if let Some(cc) = delta.get("content").and_then(|c| c.as_str()) {
                            completion.content.push_str(cc);
                            on_item(StreamItem::Delta(cc.to_string()));
                        }
                        if let Some(tcs) = delta.get("tool_calls").and_then(|t| t.as_array()) {
                            for tc in tcs {
                                let idx = tc.get("index").and_then(|i| i.as_u64()).unwrap_or(0);
                                let entry = pending_tools.entry(idx).or_default();
                                if let Some(id) = tc.get("id").and_then(|i| i.as_str()) {
                                    entry.0 = id.to_string();
                                }
                                if let Some(f) = tc.get("function") {
                                    if let Some(n) = f.get("name").and_then(|n| n.as_str()) {
                                        entry.1.push_str(n);
                                    }
                                    if let Some(a) = f.get("arguments").and_then(|a| a.as_str()) {
                                        entry.2.push_str(a);
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        finish_tools(&mut completion, &mut pending_tools);
        Ok(completion)
    }

    /// 整块请求（降级路径）。
    pub async fn chat_block(
        &self,
        messages: &[ChatMessage],
        tools: &[ToolSchema],
    ) -> AppResult<Completion> {
        let mut body = serde_json::json!({
            "model": self.cfg.model,
            "messages": messages,
            "temperature": self.cfg.temperature,
        });
        if !tools.is_empty() {
            body["tools"] = serde_json::Value::Array(tools.iter().map(|t| t.to_wire()).collect());
        }
        let resp = self
            .http
            .post(self.url("/chat/completions"))
            .headers(self.headers())
            .json(&body)
            .timeout(Duration::from_secs(180))
            .send()
            .await
            .map_err(|e| AppError::AiProvider(format!("请求失败: {e}")))?;
        let status = resp.status();
        let text = resp.text().await.unwrap_or_default();
        if !status.is_success() {
            return Err(AppError::AiProvider(format!(
                "HTTP {status}: {}",
                truncate_for_log(&text, 500)
            )));
        }
        let v: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| AppError::AiProvider(format!("响应不是 JSON: {e}")))?;
        let choice = v
            .get("choices")
            .and_then(|c| c.as_array())
            .and_then(|a| a.first())
            .ok_or_else(|| AppError::AiProvider("响应缺少 choices".into()))?;
        let message = choice.get("message").cloned().unwrap_or_default();
        let content = message
            .get("content")
            .and_then(|c| c.as_str())
            .unwrap_or("")
            .to_string();
        let reasoning = message
            .get("reasoning_content")
            .and_then(|c| c.as_str())
            .unwrap_or("")
            .to_string();
        let tool_calls: Vec<ToolCall> = message
            .get("tool_calls")
            .and_then(|t| t.as_array())
            .map(|a| {
                a.iter()
                    .filter_map(|tc| serde_json::from_value(tc.clone()).ok())
                    .collect()
            })
            .unwrap_or_default();
        let finish_reason = choice
            .get("finish_reason")
            .and_then(|f| f.as_str())
            .unwrap_or("")
            .to_string();
        let usage = v.get("usage").cloned().unwrap_or_default();
        Ok(Completion {
            content,
            reasoning,
            tool_calls,
            finish_reason,
            tokens_in: usage
                .get("prompt_tokens")
                .and_then(|x| x.as_u64())
                .unwrap_or(0),
            tokens_out: usage
                .get("completion_tokens")
                .and_then(|x| x.as_u64())
                .unwrap_or(0),
        })
    }

    /// ① GET /v1/models 是否可用。
    pub async fn list_models(&self) -> AppResult<Vec<String>> {
        let resp = self
            .http
            .get(self.url("/models"))
            .headers(self.headers())
            .timeout(Duration::from_secs(15))
            .send()
            .await
            .map_err(|e| AppError::AiProvider(format!("请求失败: {e}")))?;
        let status = resp.status();
        let text = resp.text().await.unwrap_or_default();
        if !status.is_success() {
            return Err(AppError::AiProvider(format!(
                "HTTP {status}: {}",
                truncate_for_log(&text, 300)
            )));
        }
        let v: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| AppError::AiProvider(format!("模型列表不是 JSON: {e}")))?;
        Ok(v.get("data")
            .and_then(|d| d.as_array())
            .map(|a| {
                a.iter()
                    .filter_map(|m| m.get("id").and_then(|i| i.as_str()).map(|s| s.to_string()))
                    .collect()
            })
            .unwrap_or_default())
    }

    /// ② 两步连通性测试（§8.7：区分“模型列表可用”与“实际对话可用”）。
    pub async fn test(&self) -> (Result<(), String>, Result<(), String>) {
        let models = self
            .list_models()
            .await
            .map(|_| ())
            .map_err(|e| e.to_string());
        let chat = self
            .chat_block(&[ChatMessage::user("ping")], &[])
            .await
            .map(|c| {
                if c.content.is_empty() && c.tool_calls.is_empty() {
                    Err("模型返回为空".to_string())
                } else {
                    Ok(())
                }
            })
            .map_err(|e| e.to_string())
            .and_then(|r| r);
        (models, chat)
    }
}

fn finish_tools(
    completion: &mut Completion,
    pending: &mut std::collections::HashMap<u64, (String, String, String)>,
) {
    // 用 drain 取出全部待定 tool_call（避免 keys()+remove 的二次查找与 unwrap），
    // 再按 index 排序，保证 tool_calls 顺序与模型输出的分片到达顺序一致。
    let mut entries: Vec<(u64, (String, String, String))> = pending.drain().collect();
    entries.sort_by_key(|(idx, _)| *idx);
    for (_, (id, name, args)) in entries {
        completion.tool_calls.push(ToolCall {
            id,
            kind: "function".into(),
            function: FunctionCall {
                name,
                arguments: args,
            },
        });
    }
}

fn find_bon(buf: &[u8], pat: &[u8]) -> Option<usize> {
    buf.windows(pat.len()).position(|w| w == pat)
}

fn truncate_for_log(s: &str, n: usize) -> String {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn context_window_clamp() {
        let mut cfg = ProviderConfig {
            context_window: 100,
            ..Default::default()
        };
        assert_eq!(cfg.sane_context_window(), 1_000);
        cfg.context_window = 9_999_999;
        assert_eq!(cfg.sane_context_window(), 2_000_000);
        cfg.context_window = 65_536;
        assert_eq!(cfg.sane_context_window(), 65_536);
    }

    #[test]
    fn presets_fill_params() {
        let p = ProviderConfig::from_preset("deepseek", "sk-x", None).unwrap();
        assert_eq!(p.base_url, "https://api.deepseek.com/v1");
        assert_eq!(p.model, "deepseek-chat");
        assert!(p.context_window >= 1_000);
        assert!(ProviderConfig::from_preset("unknown", "", None).is_none());
    }

    #[test]
    fn message_shapes() {
        let m = ChatMessage::tool_result("call_1", "ok");
        assert_eq!(m.role, "tool");
        assert_eq!(m.tool_call_id.as_deref(), Some("call_1"));
        let s = serde_json::to_string(&ChatMessage::user("hi")).unwrap();
        assert!(s.contains("\"role\":\"user\""));
    }
}
