//! 服务端 RPC 分发（懒猫微包容器，无 Tauri）。
//!
//! # 契约
//!
//! 前端 `POST /rpc`，body 是 `{ "cmd": "<命令名>", "args": <对象> }`。
//! `args` 的形状**与 Tauri 的 `invoke(cmd, args)` 完全一致** —— 这是刻意的，
//! 前端那 773 行 `src/ipc/` 才能一行不改地在两种运行模式下工作。
//!
//! 响应固定 HTTP 200 + 信封（错误也走 200：前端要读 body 才能拿到 `code`，
//! 用非 2xx 会让 `fetch` 的语义和错误体形状分叉成两套）：
//!
//! ```json
//! { "ok": true,  "data": <任意可序列化值> }
//! { "ok": false, "error": { "code": "...", "message": "...", "detail": {...} } }
//! ```
//!
//! # 适配器从哪来
//!
//! 每个 `#[command]` 函数在服务端模式下会**额外**展开出一个同名模块，
//! 里面的 `call` 就是这里的 [`RpcFn`]。表和桌面侧的 `generate_handler!`
//! 共用同一份命令清单（`commands/mod.rs`），所以不会漂移。

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use serde_json::Value;

use crate::error::{AppError, AppResult};
use crate::ipc_shim::{AppHandle, Channel, Hub, IntoPayload, State};
use crate::state::{AppState, ManagedState};

/// 适配器返回的 future。
pub type RpcFuture<'a> = Pin<Box<dyn Future<Output = AppResult<Value>> + Send + 'a>>;

/// 一条命令的适配器。
pub type RpcFn = for<'a> fn(&'a Ctx<'a>) -> RpcFuture<'a>;

/// 分发表里的一项。
pub struct Entry {
    pub name: &'static str,
    pub call: RpcFn,
}

/// 一次调用的上下文：状态、句柄、通道总线、以及这次调用的参数。
pub struct Ctx<'a> {
    app_state: &'a Arc<AppState>,
    app: &'a AppHandle,
    hub: &'a Arc<dyn Hub>,
    payload: &'a Value,
}

impl<'a> Ctx<'a> {
    pub fn new(app_state: &'a Arc<AppState>, app: &'a AppHandle, payload: &'a Value) -> Self {
        Self {
            app_state,
            app,
            hub: app.hub(),
            payload,
        }
    }

    /// 命令的第一类形参 `ManagedState<'_>`。
    pub fn state(&self) -> ManagedState<'a> {
        State::new(self.app_state)
    }

    /// `tauri::AppHandle` 形参（当前没有命令用它，但门面要完整）。
    pub fn app(&self) -> AppHandle {
        self.app.clone()
    }

    /// 普通形参：先按 camelCase 找，再退回 snake_case。
    ///
    /// 两种都收，是因为前端（camelCase）和手写 curl 调试（snake_case）
    /// 都会用到这个入口，而多写一个 `or_else` 比让调试者踩空字段便宜得多。
    pub fn arg<T: serde::de::DeserializeOwned>(&self, camel: &str, snake: &str) -> AppResult<T> {
        let raw = match self.payload {
            Value::Object(map) => map
                .get(camel)
                .or_else(|| map.get(snake))
                .cloned()
                .unwrap_or(Value::Null),
            // 无参命令：前端可能整个 args 都不传。
            Value::Null => Value::Null,
            other => other.clone(),
        };
        serde_json::from_value(raw).map_err(|e| AppError::param(format!("参数 {camel}: {e}")))
    }

    /// `Channel<T>` 形参：载荷里放的是 WS 通道 id（桌面下这个参数由 Tauri 托管）。
    pub fn channel<T: IntoPayload>(&self, camel: &str, snake: &str) -> AppResult<Channel<T>> {
        let raw = match self.payload {
            Value::Object(map) => map
                .get(camel)
                .or_else(|| map.get(snake))
                .cloned()
                .unwrap_or(Value::Null),
            _ => Value::Null,
        };
        let id = match raw {
            Value::String(s) => s,
            // 宽容一点：前端若原样把 Tauri 的 Channel 对象序列化过来（`{id: N}`），
            // 也认。这不是必须支持的形式，但省得两边对不上时只看到「参数错误」。
            Value::Object(o) => o
                .get("id")
                .or_else(|| o.get("channelId"))
                .and_then(|v| {
                    v.as_str()
                        .map(str::to_owned)
                        .or_else(|| v.as_u64().map(|n| n.to_string()))
                })
                .ok_or_else(|| AppError::param(format!("参数 {camel}: 通道对象缺少 id")))?,
            _ => {
                return Err(AppError::param(format!(
                    "参数 {camel}: 期望通道 id 字符串（服务端模式下前端要先开 /ws/channel/<id>）"
                )))
            }
        };
        Ok(Channel::new(Arc::clone(self.hub), id))
    }

    /// 适配器出口：把命令的返回值序列化成响应载荷。
    pub fn ok<T: serde::Serialize>(&self, value: T) -> AppResult<Value> {
        serde_json::to_value(value)
            .map_err(|e| AppError::internal(format!("序列化返回值失败: {e}")))
    }
}

/// 命令名 → 适配器。
pub struct Table {
    entries: std::collections::HashMap<&'static str, RpcFn>,
}

impl Table {
    /// 从 `commands::rpc_table()` 建表。
    pub fn build() -> Self {
        let list = crate::commands::rpc_table();
        let mut entries = std::collections::HashMap::with_capacity(list.len());
        for e in list {
            // 重名 = 命令清单里有两条同叶名路径。桌面侧不会报（generate_handler!
            // 允许），服务端这里若静默覆盖就是「有一条命令永远打不到」——
            // 所以直接 panic，让它在启动时暴露。
            let prev = entries.insert(e.name, e.call);
            assert!(prev.is_none(), "命令名重复: {}", e.name);
        }
        Self { entries }
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    /// 表里所有命令名（`/healthz` 用它自证装了多少条）。
    pub fn names(&self) -> Vec<&'static str> {
        let mut v: Vec<_> = self.entries.keys().copied().collect();
        v.sort_unstable();
        v
    }

    pub async fn call(
        &self,
        name: &str,
        payload: Value,
        app_state: &Arc<AppState>,
        app: &AppHandle,
    ) -> AppResult<Value> {
        let Some(f) = self.entries.get(name) else {
            return Err(AppError::NotFound(format!("未知命令: {name}")));
        };
        let ctx = Ctx::new(app_state, app, &payload);
        f(&ctx).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `to_camel` 在宏里；这里单独验一遍「取叶名」的等价规则不适用于本模块，
    /// 只能验表本身：**表必须是全的**。
    ///
    /// 这条测试的价值在于它是「服务端漏注册命令」的唯一自动防线 ——
    /// 漏一条不会编译失败，只会让某个面板点了没反应。
    #[test]
    fn table_covers_every_command() {
        let t = Table::build();
        assert!(t.len() > 100, "命令表只有 {} 条，疑似生成器没生效", t.len());
        // 随手点几个跨模块的：只要有一条缺失就说明 `#[command]` 展开没覆盖到。
        for name in [
            "app_platform",
            "session_connect",
            "terminal_attach",
            "fs_write",
            "docker_ps",
            "db_query",
            "ai_chat",
            "vault_status",
            "asset_list",
            "app_info",
        ] {
            assert!(
                t.entries.contains_key(name),
                "命令表缺少 {name}（服务端模式下它会 404）"
            );
        }
    }
}
