//! 在线更新命令（§6.2，薄）：只做「取状态 → 调 service」。
//!
//! 三条命令在**两种构建里都注册**（`nexterm_commands!` 只有一份清单），服务端上
//! 它们如实返回「不支持」而不是假装成功 —— 与 `sync_link_*` 那组同一个处理方式
//! （见 `commands/sync.rs` 的模块文档）。
//!
//! 检查的结果里 `canInstall` 与 `available` 是**两个**字段，别在前面合并成
//! 「有没有新版」一个布尔：服务端、未配签名公钥、网络不通，这三种情况的界面表现
//! 完全不同，前端得看得出来。

use crate::error::AppResult;
use crate::ipc_shim as tauri;
use crate::state::ManagedState;
use crate::update::UpdateInfo;

/// 检查更新。
///
/// 「查不到」不是错误：网络不通、GitHub 限流、更新服务没配，一律降级成
/// `unavailableReason` 走 `Ok` 回来。理由见 `update` 模块文档 —— 更新检查是附带功能，
/// 不该成为「启动时顺手做一下」的失败源。
#[tauri::command]
pub async fn app_update_check(state: ManagedState<'_>) -> AppResult<UpdateInfo> {
    crate::update::check(&state.app).await
}

/// 下载并安装更新。进度经 `update://progress` 事件推送（不复用返回值）。
///
/// 装完**不**自动重启：由界面决定什么时候重启（见 [`app_restart`]）——
/// Windows 的 NSIS 安装器自己会拉起新进程，那边再补一次重启反而会开出两个实例。
#[tauri::command]
pub async fn app_update_install(state: ManagedState<'_>) -> AppResult<()> {
    crate::update::install(&state.app).await
}

/// 重启应用（macOS / Linux 上装完新版本必须重启才生效）。
#[tauri::command]
pub fn app_restart(state: ManagedState<'_>) -> AppResult<()> {
    crate::update::restart(&state.app);
    Ok(())
}
