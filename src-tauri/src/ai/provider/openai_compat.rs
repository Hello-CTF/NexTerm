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

    /// 带图片的用户消息（多模态输入）。
    ///
    /// content 走 OpenAI 的数组形态：一个 text 块 + 若干 `image_url` 块，
    /// 图片用 data URI **内联** —— 不依赖图床，截图也不用离开本机。
    ///
    /// 没图时故意退回 `user()`：部分兼容实现只认字符串形态的 content，
    /// 手上没图就别去赌那个数组分支。
    pub fn user_with_images(text: impl Into<String>, images: &[String]) -> Self {
        let text = text.into();
        if images.is_empty() {
            return Self::user(text);
        }
        let mut parts = vec![serde_json::json!({ "type": "text", "text": text })];
        for img in images {
            // 前端可能给裸 base64，也可能给完整 data URI —— 两种都收
            let url = if img.starts_with("data:") {
                img.clone()
            } else {
                format!("data:image/png;base64,{img}")
            };
            parts.push(serde_json::json!({
                "type": "image_url",
                "image_url": { "url": url }
            }));
        }
        Self {
            role: "user".into(),
            content: serde_json::Value::Array(parts),
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
    /// 输入里命中 prompt cache 的 token（`tokens_in` 的子集）。
    ///
    /// 取不到就是 0 —— 不是所有提供方都会回报缓存命中，缺字段不等于"没命中"。
    pub tokens_cached: u64,
}

/// 流式片段（agent 消费）。
#[derive(Debug, Clone)]
pub enum StreamItem {
    Delta(String),
    Reasoning(String),
    /// 模型正在逐 token 生成某个 tool_call 的**参数**（这个调用还没生成完）。
    ///
    /// 加这个变体是因为一个真机症状（2026-09-30）：让模型写文件时，整份内容
    /// 会作为 `write_file.content` 逐 token 吐出来 —— 参数分片以前只在下面
    /// 那个 `pending_tools` 里默默累积，**一个字都不往外说**。于是界面在
    /// 「AI 说完『好的，我来写』」之后会静止几十秒，用户合理地判断成卡死。
    ///
    /// `chars` 是参数 JSON 已累积的**字节数**（`String::len()`）。刻意不去数
    /// `chars()`：那是 O(n)，在逐 token 累积的循环里会退化成 O(n²)，而这里
    /// 每条分片都会算一次。它也不是最终内容的长度，更不是进度条的分母 ——
    /// 唯一的用途是让前端表达「还在长」。
    ToolArgs {
        name: String,
        chars: usize,
    },
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
                handle_payload(payload, &mut completion, &mut pending_tools, on_item);
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
        let usage = crate::ai::usage::parse_usage(&v.get("usage").cloned().unwrap_or_default());
        Ok(Completion {
            content,
            reasoning,
            tool_calls,
            finish_reason,
            tokens_in: usage.prompt_tokens,
            tokens_out: usage.completion_tokens,
            tokens_cached: usage.cached_tokens,
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

/// 处理一行 SSE 的 `data:` 载荷（`finish_tools` 的输入就是它攒出来的）。
///
/// 抽成独立函数（无 I/O、无时钟）是为了**可测**：这条解析链上现在挂着
/// 「模型生成工具参数时要往外报进度」这种产品行为，而它的真机形态 ——
/// 一份几十行的文件内容要吐几十秒 —— 在单测里造不出来，只能喂假的 SSE 行来守。
///
/// 原来内联时的 `continue`，进了函数都变成 `return`：函数只管一行，跳过即返回。
fn handle_payload(
    payload: &str,
    completion: &mut Completion,
    pending_tools: &mut std::collections::HashMap<u64, (String, String, String)>,
    on_item: &mut impl FnMut(StreamItem),
) {
    let v: serde_json::Value = match serde_json::from_str(payload) {
        Ok(v) => v,
        Err(_) => return,
    };
    if let Some(usage) = v.get("usage") {
        // 兼容各家口径（含缓存字段）。只在真的解析出用量时覆盖 ——
        // 有的网关会在中间分片里塞一个空的 usage，别把最终值冲掉。
        let u = crate::ai::usage::parse_usage(usage);
        if u.has_data() {
            completion.tokens_in = u.prompt_tokens;
            completion.tokens_out = u.completion_tokens;
            completion.tokens_cached = u.cached_tokens;
        }
    }
    let Some(choices) = v.get("choices").and_then(|c| c.as_array()) else {
        return;
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
                            // 逐分片转发，**节流交给消费端**：provider 这一层保持
                            // 纯解析，不引入时间/频率概念，否则「多久报一次」会变成
                            // 藏在传输层里的产品决策。
                            on_item(StreamItem::ToolArgs {
                                name: entry.1.clone(),
                                chars: entry.2.len(),
                            });
                        }
                    }
                }
            }
        }
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

    /// 模型流式生成工具参数时，**每一个参数分片都要往外报一次进度**。
    ///
    /// 守的是 2026-09-30 那个真机症状：让模型写文件，整份内容会作为
    /// `write_file.content` 逐 token 吐出来，而这期间界面一条事件都收不到 ——
    /// 用户看到「AI 说完『好的，我来写』」之后几十秒毫无动静，合理地读成卡死。
    ///
    /// 用真的 SSE 载荷形状（第一个分片给 id + name、后续分片只给 arguments
    /// 增量，这是 OpenAI 兼容协议的常规切法），不手搓简化格式 —— 否则测的是
    /// 我脑补的协议，不是真的那条链。
    #[test]
    fn tool_args_are_reported_on_every_arguments_fragment() {
        let mut completion = Completion::default();
        let mut pending = std::collections::HashMap::new();
        let mut items: Vec<StreamItem> = Vec::new();
        let mut push = |it: StreamItem| items.push(it);

        let fragments = [
            r#"{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write_file","arguments":""}}]}}]}"#,
            r#"{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"/tmp/a\","}}]}}]}"#,
            r#"{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"content\":\"第一行\\n"}}]}}]}"#,
            r#"{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"第二行\"}"}}]}}]}"#,
        ];
        for f in fragments {
            handle_payload(f, &mut completion, &mut pending, &mut push);
        }

        let pings: Vec<(String, usize)> = items
            .iter()
            .filter_map(|i| match i {
                StreamItem::ToolArgs { name, chars } => Some((name.clone(), *chars)),
                _ => None,
            })
            .collect();

        assert_eq!(
            pings.len(),
            fragments.len(),
            "每个参数分片都要报一次，否则写大文件的中途界面仍然是死的：{pings:?}"
        );
        // 名字从第一个分片起就得是对的 —— 前端要靠它说「正在写入 …」，
        // 报成空串就只能显示一句没有主语的「正在准备…」。
        assert!(
            pings.iter().all(|(n, _)| n == "write_file"),
            "每条进度都该带着工具名：{pings:?}"
        );
        // 进度只能往前走，且必须是**参数长度**（不是分片序号之类的计数）。
        assert!(
            pings.windows(2).all(|w| w[0].1 <= w[1].1),
            "进度不许回退：{pings:?}"
        );
        assert_eq!(
            pings.last().unwrap().1,
            pending[&0].2.len(),
            "最后一条进度要等于参数总长"
        );
        assert!(pending[&0].2.contains("第二行"), "报进度不能把聚合搞坏");
        assert_eq!(pending[&0].1, "write_file");
    }

    /// 没有工具参数分片时，**一条进度都不该报**。
    ///
    /// 纯文本回复（最常见的一轮）走的是同一条解析链，凭空多一类事件会污染
    /// 前端的流式渲染 —— 这类误报不会报错、不会崩溃，只会让界面出现
    /// 说不清来历的状态。
    #[test]
    fn plain_text_stream_reports_no_tool_args() {
        let mut completion = Completion::default();
        let mut pending = std::collections::HashMap::new();
        let mut items: Vec<StreamItem> = Vec::new();
        let mut push = |it: StreamItem| items.push(it);

        handle_payload(
            r#"{"choices":[{"delta":{"content":"好的，我来"}}]}"#,
            &mut completion,
            &mut pending,
            &mut push,
        );
        // usage-only 的噪声行（有的网关单独发一帧）
        handle_payload(
            r#"{"usage":{"prompt_tokens":10,"completion_tokens":2},"choices":[]}"#,
            &mut completion,
            &mut pending,
            &mut push,
        );

        assert!(
            items.iter().all(|i| matches!(i, StreamItem::Delta(_))),
            "纯文本流里混进了非正文事件：{items:?}"
        );
        assert_eq!(completion.content, "好的，我来");
        assert_eq!(completion.tokens_in, 10);
    }
}
