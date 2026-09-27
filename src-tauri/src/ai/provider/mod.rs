//! 模型接入（§8.7）：v1 只做 OpenAI Chat Completions 兼容协议。
pub mod openai_compat;

pub use crate::ai::usage::Usage;
pub use crate::ai::ProviderConfig;
pub use openai_compat::{
    ChatMessage, Completion, FunctionCall, LlmClient, StreamItem, ToolCall, ToolSchema,
};
