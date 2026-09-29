//! AI 运行时（M2 核心）：Agent + 工具调用 + 接管模式共享同一会话/审计/终端视图。
//!
//! 设计立场（§6.1）：
//! 1. AI 的每个动作都**可追溯**：命令与完整输出记录在对话面板的工具卡片里
//!    （不是往用户终端里回显 —— 见 `tools::server::exec_commands` 的注释）；
//! 2. AI 知道你在看什么（上下文自动装配）；
//! 3. 不该做的事明确拒绝（guard 三级护栏，工具层拦截）。

pub mod agent;
pub mod context;
pub mod guard;
pub mod profiles;
pub mod provider;
pub mod takeover;
pub mod tools;
pub mod usage;

use std::collections::HashMap;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use tokio::sync::{watch, RwLock};
use tokio_util::sync::CancellationToken;

use crate::error::{AppError, AppResult};

/// AI 事件（§8.4），经 ai_chat 的 Channel 流式送达前端。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum AiEvent {
    Status {
        phase: String,
        detail: Option<String>,
        turn: Option<u32>,
    },
    Delta {
        text: String,
    },
    Reasoning {
        text: String,
    },
    ToolCall {
        id: String,
        name: String,
        args: serde_json::Value,
        display: String,
    },
    /// ⚠️ 这个 `#[serde(rename_all)]` 是**必须**的，别删。
    ///
    /// 枚举上的 `rename_all = "camelCase"` 只管 **variant 名**（`ToolCall` →
    /// `toolCall`），**不管 variant 里的字段**。少了这一行，`exit_code` 会原样
    /// 发出去，前端读 `ev.exitCode` 永远是 `undefined` —— 不报错、不崩溃，
    /// 只是工具卡片上的退出码从来没出现过。
    #[serde(rename_all = "camelCase")]
    ToolResult {
        id: String,
        ok: bool,
        /// 折叠态显示的一行摘要（前端卡片标题旁边那段）。
        summary: String,
        /// **完整输出**（`summary` 是它的截断版）。
        ///
        /// 用户点开工具卡片看的应该是这个：只看摘要等于把"AI 到底跑出了什么"
        /// 藏起来，出了问题还得去翻审计日志 —— 而摘要本来就只是省屏幕空间用的。
        /// 仍然设上限（见 `agent.rs`），一条 `cat` 大文件不该把界面卡死。
        text: String,
        truncated: bool,
        exit_code: Option<i32>,
    },
    ConfirmRequired {
        id: String,
        tool: String,
        args: serde_json::Value,
        risk: String,
        rendered: String,
    },
    #[serde(rename_all = "camelCase")]
    Screen {
        tab_id: String,
        text: String,
    },
    /// AI 改动了文件（「变更记录」）。
    ///
    /// 只在内容**真的变了**才推：工具把同样的内容重写一遍不算变更，
    /// 否则每轮都会冒出一堆"无差异的 diff"。
    FileChange {
        id: String,
        path: String,
        before: String,
        after: String,
    },
    /// 每轮用量快照（功能行右侧的上下文圆环）。
    ///
    /// 用**本轮**的输入量而不是累计值 —— 累计会把这一轮之前所有请求的 token
    /// 都滚进来，圆环会虚高到吓人，反而没人敢用。
    Usage(usage::Usage),
    /// 任务清单（`todo_write` 之后推整份；它是「整份替换」语义，不做增量）。
    Todos {
        items: Vec<tools::edit::TodoItem>,
    },
    /// 计划模式：模型提交了完整计划，停下来等用户评审。
    PlanSubmitted {
        plan: String,
    },
    #[serde(rename_all = "camelCase")]
    Done {
        answer: String,
        turns: u32,
        tokens_in: u64,
        tokens_out: u64,
    },
    Error {
        message: String,
        retryable: bool,
    },
}

/// 确认决定（ai_confirm 命令）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ConfirmDecision {
    Allow,
    AllowSession,
    Deny,
}

/// 模型提供方配置（OpenAI Chat Completions 兼容，§8.7）。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProviderConfig {
    pub base_url: String,
    pub api_key: String,
    pub model: String,
    #[serde(default = "default_temperature")]
    pub temperature: f32,
    /// 上下文窗口（1k–2M，非法值兜底 —— 外部配置可能乱填）。
    #[serde(default = "default_context_window")]
    pub context_window: u64,
    /// None = 跟随系统代理（AI 走 HTTP 与 SSH/WinRM 策略相反，§8.7）。
    pub proxy: Option<String>,
    #[serde(default = "default_true")]
    pub stream: bool,
}

fn default_temperature() -> f32 {
    0.3
}
fn default_context_window() -> u64 {
    32_768
}
fn default_true() -> bool {
    true
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            base_url: String::new(),
            api_key: String::new(),
            model: String::new(),
            temperature: default_temperature(),
            context_window: default_context_window(),
            proxy: None,
            stream: true,
        }
    }
}

impl ProviderConfig {
    /// 上下文窗口校验兜底（1k–2M）。
    pub fn sane_context_window(&self) -> u64 {
        self.context_window.clamp(1_000, 2_000_000)
    }

    /// 预设自动填参（§8.7：选预设 → 自动填 base_url/模型/窗口）。
    pub fn from_preset(preset: &str, api_key: &str, model: Option<&str>) -> Option<Self> {
        let (base, default_model, window) = match preset {
            "deepseek" => ("https://api.deepseek.com/v1", "deepseek-chat", 64_000u64),
            "openai" => ("https://api.openai.com/v1", "gpt-4o-mini", 128_000),
            "dashscope" => (
                "https://dashscope.aliyuncs.com/compatible-mode/v1",
                "qwen-plus",
                128_000,
            ),
            "moonshot" => ("https://api.moonshot.cn/v1", "moonshot-v1-32k", 128_000),
            "zhipu" => (
                "https://open.bigmodel.cn/api/paas/v4",
                "glm-4-flash",
                128_000,
            ),
            "ollama" => ("http://127.0.0.1:11434/v1", "qwen2.5:7b", 32_000),
            "lmstudio" => ("http://127.0.0.1:1234/v1", "local-model", 32_000),
            "vllm" => ("http://127.0.0.1:8000/v1", "local-model", 32_000),
            _ => return None,
        };
        Some(Self {
            base_url: base.to_string(),
            api_key: api_key.to_string(),
            model: model.unwrap_or(default_model).to_string(),
            temperature: default_temperature(),
            context_window: window,
            proxy: None,
            stream: true,
        })
    }
}

/// 一个进行中的 AI 任务。
pub struct AiJob {
    pub id: String,
    pub cancel: CancellationToken,
    /// 确认流：None → Some(decision)，读后置回 None。
    pub confirm: (
        watch::Sender<Option<ConfirmDecision>>,
        watch::Receiver<Option<ConfirmDecision>>,
    ),
    /// 会话内放行的风险类别（“本会话允许此类”）。
    pub session_allowed: RwLock<std::collections::HashSet<String>>,
}

impl AiJob {
    pub fn new(id: &str) -> Arc<Self> {
        let (tx, rx) = watch::channel(None);
        Arc::new(Self {
            id: id.to_string(),
            cancel: CancellationToken::new(),
            confirm: (tx, rx),
            session_allowed: RwLock::new(std::collections::HashSet::new()),
        })
    }

    /// 等待用户确认（任务取消 → Deny）。
    ///
    /// ⚠️ 下面「先取决定、再复位槽位」这两步**必须分成两条语句**，不能合成
    /// `if let Some(d) = *rx.borrow_and_update() { … send(None) … }`。
    ///
    /// `borrow_and_update()` 返回的 `Ref` 握着 watch 内部那把 **`std::sync::RwLock`**
    /// 的读锁（`tokio::loom::sync::RwLock` 在非 loom 构建下就是 std 的），
    /// 而 `if let` 的临时量会活到**整个 `if let` 表达式结束**（`then` 块内仍然活着，
    /// 两个版次都是如此 —— 版次 2024 只改了 `else` 分支的时机）。
    /// 于是块里那句 `send(None)` 就成了「自己握着读锁、再去要同一把锁的写锁」：
    /// std 的 `RwLock` 不可重入，当场自死锁，且**读锁永不释放**，
    /// 后续任何 `send`（`ai_confirm` / `ai_cancel`）都会排队等在这把锁上一起堵死。
    ///
    /// 真机症状（2026-09-29 采样确认，见 `docs/`）：第一次需要用户确认的工具
    /// （如 `write_file`）卡片永远转圈、文件根本没落盘、停止按钮点了没反应、
    /// 继续提问也不再有回复 —— 因为确认链上的线程全堵在这把锁上。
    /// 回归测试：`wait_confirm_resets_slot_without_holding_read_guard`。
    pub async fn wait_confirm(&self) -> ConfirmDecision {
        let mut rx = self.confirm.1.clone();
        loop {
            tokio::select! {
                _ = self.cancel.cancelled() => return ConfirmDecision::Deny,
                changed = rx.changed() => {
                    if changed.is_err() {
                        return ConfirmDecision::Deny;
                    }
                    // 单独一条语句取值：临时 `Ref` 在本语句结束就释放读锁，
                    // 下一句 `send(None)` 才拿得到写锁。
                    let decision = *rx.borrow_and_update();
                    if let Some(d) = decision {
                        let _ = self.confirm.0.send(None);
                        return d;
                    }
                }
            }
        }
    }
}

/// AI 作用域：绑定哪个会话 / 标签 / 连接。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AiScope {
    pub session_id: Option<String>,
    pub tab_id: Option<String>,
    pub conn_id: Option<String>,
    pub asset_id: Option<String>,
}

/// AI 运行时：任务表 + 活跃提供方配置 + 权限档位 + 接管状态。
pub struct AiRuntime {
    pub jobs: RwLock<HashMap<String, Arc<AiJob>>>,
    pub provider: RwLock<ProviderConfig>,
    /// 常规模式的权限配置（档位 + 自定义危险规则）。
    ///
    /// **不含终端接管** —— 接管是完全权限、独立模块，见 `takeovers`。
    pub permission: RwLock<guard::PermissionConfig>,
    /// 接管中的标签：tab_id → 取消令牌（用户按键即夺回）。
    pub takeovers: RwLock<HashMap<String, Arc<CancellationToken>>>,
    /// 用户按任意键即夺回（默认开，可配 takeover_steal_on_key）。
    pub steal_on_key: std::sync::atomic::AtomicBool,
}

impl AiRuntime {
    pub fn new(provider: ProviderConfig) -> Self {
        Self {
            jobs: RwLock::new(HashMap::new()),
            provider: RwLock::new(provider),
            permission: RwLock::new(guard::PermissionConfig::default()),
            takeovers: RwLock::new(HashMap::new()),
            steal_on_key: std::sync::atomic::AtomicBool::new(true),
        }
    }

    pub async fn register_job(&self, job: Arc<AiJob>) {
        self.jobs.write().await.insert(job.id.clone(), job);
    }

    pub async fn finish_job(&self, job_id: &str) {
        if let Some(j) = self.jobs.write().await.remove(job_id) {
            j.cancel.cancel();
        }
    }

    pub async fn cancel_job(&self, job_id: &str) -> AppResult<()> {
        let jobs = self.jobs.read().await;
        let job = jobs
            .get(job_id)
            .ok_or_else(|| AppError::NotFound(format!("AI 任务 {job_id}")))?;
        job.cancel.cancel();
        let _ = job.confirm.0.send(Some(ConfirmDecision::Deny));
        Ok(())
    }

    pub async fn send_confirm(&self, job_id: &str, decision: ConfirmDecision) -> AppResult<()> {
        let jobs = self.jobs.read().await;
        let job = jobs
            .get(job_id)
            .ok_or_else(|| AppError::NotFound(format!("AI 任务 {job_id}")))?;
        let _ = job.confirm.0.send(Some(decision));
        Ok(())
    }
}

/// system prompt 通用骨架（§6.3：断连显式告知、命令可见等约束）。
pub fn system_prompt_base() -> &'static str {
    "你是 NexTerm 内置的运维助手，运行在用户的开发运维终端里。\n\
     规则：\n\
     1. 你执行的每条命令都会记录在右侧对话面板的工具卡片里，用户点开就能看到完整输出。\
     命令不会写进用户的终端区域，所以不要以为「终端里能看到」——需要用户知道的事情，\
     要在回答里说清楚。\n\
     2. 工具返回带 exit_code 与 truncated 标记；若返回「连接已断开」，\
     请立即停止排查并提示用户重新连接，不要重试，不要编造结果。\n\
     3. 危险命令会被系统拦截：写操作和 sudo 需要用户确认；rm -rf / 等会被直接拒绝。\n\
     4. 结论必须带依据：引用你实际执行过的命令与关键输出行。\n\
     5. 中文回答。"
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `ToolResult` 必须把**完整输出**一路送到前端。
    ///
    /// 守的是这一类静默 bug：内核少序列化一个字段 / 改了名字，前端按
    /// `ev.text` 读只会拿到 `undefined` —— 不报错、不崩溃，只是「展开完整输出」
    /// 永远不出现，用户以为 AI 什么都没跑。和历史上 `*Row` 直接序列化出
    /// `content_json` 那次是同一个形状：**字段名对不上，界面静默变空**。
    #[test]
    fn tool_result_carries_full_text() {
        let ev = AiEvent::ToolResult {
            id: "call-1".into(),
            ok: true,
            summary: "$ ls /srv…".into(),
            text: "$ ls /srv\napi-server\nweb\n".into(),
            truncated: false,
            exit_code: Some(0),
        };
        let v = serde_json::to_value(&ev).unwrap();
        // 前端 switch 认的就是这个 tag
        assert_eq!(v["type"], "toolResult");
        assert_eq!(v["text"], "$ ls /srv\napi-server\nweb\n");
        assert_eq!(v["summary"], "$ ls /srv…");
        assert_eq!(v["exitCode"], 0);
    }

    /// 所有事件的字段名都必须是 camelCase。
    ///
    /// 这条守的是一个**已经踩过三次**的坑：内核序列化出去的 key 与前端读的
    /// 对不上，前端拿到 `undefined` —— 不报错、不崩溃，界面只是静默地空掉。
    /// （前两次是 `*Row` 直接序列化出 `content_json`、`ai_chat` 没回传
    /// `conversationId`；这次是 `exit_code`。）
    ///
    /// 根源很反直觉：枚举上的 `rename_all = "camelCase"` **只重命名 variant**，
    /// 不会碰 variant 内部的下划线字段 —— 每个变体得自己再写一次。
    /// 单靠人工 review 记不住这条，所以让测试把每个变体都序列化一遍，
    /// 只认一条规则：**平铺出来的 key 里不许出现下划线**。
    #[test]
    fn every_event_field_is_camel_case() {
        let events = vec![
            AiEvent::Status {
                phase: "thinking".into(),
                detail: None,
                turn: Some(1),
            },
            AiEvent::Delta { text: "x".into() },
            AiEvent::Reasoning { text: "x".into() },
            AiEvent::ToolCall {
                id: "c".into(),
                name: "n".into(),
                args: serde_json::json!({}),
                display: "d".into(),
            },
            AiEvent::ToolResult {
                id: "c".into(),
                ok: true,
                summary: "s".into(),
                text: "t".into(),
                truncated: false,
                exit_code: Some(0),
            },
            AiEvent::ConfirmRequired {
                id: "c".into(),
                tool: "t".into(),
                args: serde_json::json!({}),
                risk: "r".into(),
                rendered: "x".into(),
            },
            AiEvent::Screen {
                tab_id: "t".into(),
                text: "x".into(),
            },
            AiEvent::FileChange {
                id: "c".into(),
                path: "p".into(),
                before: "a".into(),
                after: "b".into(),
            },
            AiEvent::Usage(usage::Usage::new(1000)),
            AiEvent::Todos { items: vec![] },
            AiEvent::PlanSubmitted { plan: "p".into() },
            AiEvent::Done {
                answer: "a".into(),
                turns: 1,
                tokens_in: 1,
                tokens_out: 2,
            },
            AiEvent::Error {
                message: "m".into(),
                retryable: false,
            },
        ];

        let mut offenders: Vec<String> = Vec::new();
        for ev in &events {
            let v = serde_json::to_value(ev).unwrap();
            let Some(obj) = v.as_object() else { continue };
            for key in obj.keys() {
                if key.contains('_') {
                    offenders.push(format!("{} → {key}", v["type"]));
                }
            }
        }
        assert!(
            offenders.is_empty(),
            "这些字段名前端读不到（枚举上的 rename_all 只管 variant 名，\
             变体内部要自己再写一次）：{offenders:?}"
        );
    }

    /// 守 `wait_confirm` 的自死锁：读到决定后要把槽位复位（`send(None)`），
    /// 但**不能在自己还握着读锁的时候去要写锁**。
    ///
    /// `rx.borrow_and_update()` 返回的 `Ref` 持着 watch 内部那把
    /// `std::sync::RwLock` 的**读锁**；edition 2021 下 `if let` 的临时量活到整个
    /// 块结束，所以原先写成
    ///
    /// ```ignore
    /// if let Some(d) = *rx.borrow_and_update() { self.confirm.0.send(None); return d; }
    /// ```
    ///
    /// 就是「自己握着读锁，再去要同一把锁的写锁」—— `std::sync::RwLock` 不可重入，
    /// 当场死锁。真机症状（2026-09-29 实机采样确认，见 `docs/`）：第一次需要用户
    /// 确认的工具（如 `write_file`）卡片永远转圈、文件根本没写、停止按钮点了没反应，
    /// 之后继续提问也不再有回复 —— 因为整条确认链上所有线程都堵在这把锁上。
    ///
    /// 这里**不用 `tokio::time::timeout`**：死锁是线程级阻塞，同一条任务被卡住时
    /// 定时器没法在同一 worker 上开火；改用一个纯 std 线程 + `recv_timeout`，
    /// 无论卡多久都能把「超时」变成一次明确的断言失败。
    #[test]
    fn wait_confirm_resets_slot_without_holding_read_guard() {
        let job = Arc::new(AiJob::new("t-wait-confirm"));
        // 先投一个决定进去，让 wait_confirm 走「读到值 → 复位槽位 → 返回」这条路。
        job.confirm
            .0
            .send(Some(ConfirmDecision::Allow))
            .expect("watch 送达决定");

        let j = Arc::clone(&job);
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("建测试运行时");
            let _ = tx.send(rt.block_on(j.wait_confirm()));
        });

        match rx.recv_timeout(std::time::Duration::from_secs(5)) {
            Ok(d) => assert_eq!(d, ConfirmDecision::Allow),
            Err(_) => {
                panic!("wait_confirm 卡死了：确认槽位复位时自己握着读锁又要写锁（见本测试注释）")
            }
        }
        // 复位必须真的发生：否则下一轮等待会被上一轮的决定直接放行。
        assert_eq!(*job.confirm.1.borrow(), None, "决定被读到后槽位要回到 None");
    }
}
