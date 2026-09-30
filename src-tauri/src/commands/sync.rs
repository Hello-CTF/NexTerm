//! 资产同步命令（§12.4 / M5）。
//!
//! 分成两组，边界很清楚：
//!
//! - **两端共用**（`sync_digest` / `sync_export` / `sync_import`）：纯粹是
//!   「本地库 ↔ 同步包」的转换，不涉及网络。服务端就是靠这三个响应桌面的推送。
//! - **仅桌面**（`sync_link_*` / `sync_remote_digest` / `sync_push` / `sync_pull`）：
//!   出站连对端。服务端上它们返回明确的 `unsupported`，而不是「假装成功」——
//!   命令表只有一份、两端都注册，运行时的拒绝是这里唯一的表达方式。
//!
//! 单向与双向在这里没有代码差异：推是 `push`（本地 export → 远端 import），
//! 拉是 `pull`（远端 export → 本地 import）。用户勾哪些资产、往哪个方向，
//! 由界面决定。

use serde::Deserialize;

use crate::error::{AppError, AppResult};
use crate::ipc_shim as tauri;
use crate::state::ManagedState;
use crate::sync::bundle::{ImportReport, SyncBundle, SyncDigest};
use crate::sync::client::{self, SyncLink};

/// 本机摘要（不含任何密文）。
#[tauri::command]
pub async fn sync_digest(state: ManagedState<'_>) -> AppResult<SyncDigest> {
    crate::sync::digest(&state.store).await
}

/// 本机实例标识（界面上显示「这批是从哪台来的」）。
#[tauri::command]
pub async fn sync_origin(state: ManagedState<'_>) -> AppResult<String> {
    crate::sync::origin(&state.store).await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncExportArgs {
    pub asset_ids: Vec<String>,
    /// 是否把被引用的凭据（明文解出来）一并打包。需要本机凭据库处于解锁态。
    #[serde(default)]
    pub with_creds: bool,
}

/// 导出选中资产为同步包（本地，不走网络）。
#[tauri::command]
pub async fn sync_export(state: ManagedState<'_>, args: SyncExportArgs) -> AppResult<SyncBundle> {
    crate::sync::export(&state.store, &state.vault, &args.asset_ids, args.with_creds).await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncImportArgs {
    pub bundle: SyncBundle,
    /// 覆盖「本机这份更新」的条目（默认跳过，见 `sync::apply` 的模块文档）。
    #[serde(default)]
    pub force: bool,
}

/// 应用一个同步包（本地，不走网络）。服务端就是靠它接收桌面的推送。
#[tauri::command]
pub async fn sync_import(state: ManagedState<'_>, args: SyncImportArgs) -> AppResult<ImportReport> {
    crate::sync::apply(&state.store, &state.vault, &args.bundle, args.force).await
}

/// 本机同步令牌。**只有服务端有** —— 桌面是发起方，不需要别人连它。
#[tauri::command]
pub async fn sync_token(state: ManagedState<'_>) -> AppResult<Option<String>> {
    if cfg!(feature = "desktop") {
        return Ok(None);
    }
    Ok(Some(crate::sync::ensure_token(&state.store).await?))
}

/// 换一个同步令牌（旧令牌立刻失效）。用于「令牌贴到别处了」。
#[tauri::command]
pub async fn sync_token_rotate(state: ManagedState<'_>) -> AppResult<Option<String>> {
    if cfg!(feature = "desktop") {
        return Ok(None);
    }
    Ok(Some(crate::sync::rotate_token(&state.store).await?))
}

// ── 以下仅桌面版有意义 ────────────────────────────────────────────────

/// 出站命令的守卫。
///
/// 命令表两端共用（一份清单，不可能漂移），所以「这条只在桌面成立」只能靠
/// 运行时拒绝表达。拒绝给出的是**可读原因**，而不是让界面点了没反应。
fn require_desktop() -> AppResult<()> {
    if cfg!(feature = "desktop") {
        return Ok(());
    }
    Err(AppError::Unsupported(
        "出站同步只在桌面版可用：微服上的服务端是被同步的一端，不需要主动连别人".into(),
    ))
}

#[tauri::command]
pub async fn sync_link_get(state: ManagedState<'_>) -> AppResult<SyncLink> {
    client::load_link(&state.store).await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncLinkArgs {
    pub url: String,
    /// 对端服务端装在哪儿：`box`（懒猫微服）| `server`（自建服务器）。
    /// 缺省或非法值都落到 `server` —— 见 `client::normalize_link`。
    #[serde(default)]
    pub token_kind: Option<String>,
    /// 缺省 = 不改动已存的令牌（界面留空时不覆盖）。
    #[serde(default)]
    pub token: Option<String>,
    #[serde(default)]
    pub insecure: Option<bool>,
}

#[tauri::command]
pub async fn sync_link_set(state: ManagedState<'_>, args: SyncLinkArgs) -> AppResult<SyncLink> {
    let mut link = client::load_link(&state.store).await?;
    link.url = args.url.trim().to_string();
    if let Some(kind) = args.token_kind {
        // 只认两个值；其余（含空串与旧版的 session/platform）交给同一处归一逻辑，
        // 免得「界面上不可能出现的值」在这里悄悄变成一个第三种状态。
        link.token_kind = if kind == client::TOKEN_KIND_BOX {
            client::TOKEN_KIND_BOX.to_string()
        } else {
            client::TOKEN_KIND_SERVER.to_string()
        };
    }
    if let Some(t) = args.token {
        link.token = t.trim().to_string();
    }
    if let Some(i) = args.insecure {
        link.insecure = i;
    }
    client::save_link(&state.store, &link).await?;
    Ok(link)
}

/// 探一次对端，并把结果（成功时间 / 失败原因）落回连接配置。
///
/// 为什么要落盘：界面上「上次连接」的状态必须来自**真实尝试**，而不是
/// 「上次保存时看起来没问题」。落盘失败只记日志、不打断本次调用 —— 用户的
/// 目的是「连上」，不是「把状态写下来」。
async fn record_probe(
    store: &crate::store::Store,
    link: &SyncLink,
    outcome: &AppResult<SyncDigest>,
) {
    let mut updated = link.clone();
    if outcome.is_ok() {
        updated.verified_at = crate::ids::now_ms() as i64;
    }
    updated.last_error = outcome.as_ref().err().map(|e| e.to_string());
    if let Err(e) = client::save_link(store, &updated).await {
        tracing::warn!(target: "sync", error = %e, "连接状态落盘失败");
    }
}

/// 拉对端摘要 —— 同时就是连通性探测。
#[tauri::command]
pub async fn sync_remote_digest(state: ManagedState<'_>) -> AppResult<SyncDigest> {
    require_desktop()?;
    let link = client::load_link(&state.store).await?;
    if !link.is_configured() {
        return Err(AppError::param("还没配置对端地址与访问令牌"));
    }
    let outcome = client::remote_digest(&link).await;
    record_probe(&state.store, &link, &outcome).await;
    outcome
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncTransferArgs {
    pub asset_ids: Vec<String>,
    #[serde(default)]
    pub with_creds: bool,
    /// 覆盖「对端/本机更新」的条目。
    #[serde(default)]
    pub force: bool,
}

/// 推：把选中的本地资产送到对端。
#[tauri::command]
pub async fn sync_push(state: ManagedState<'_>, args: SyncTransferArgs) -> AppResult<ImportReport> {
    require_desktop()?;
    let link = client::load_link(&state.store).await?;
    if !link.is_configured() {
        return Err(AppError::param("还没配置对端地址与访问令牌"));
    }
    client::push(
        &state.store,
        &state.vault,
        &link,
        &args.asset_ids,
        args.with_creds,
        args.force,
    )
    .await
}

/// 拉：把对端上的选中资产取回本地。
#[tauri::command]
pub async fn sync_pull(state: ManagedState<'_>, args: SyncTransferArgs) -> AppResult<ImportReport> {
    require_desktop()?;
    let link = client::load_link(&state.store).await?;
    if !link.is_configured() {
        return Err(AppError::param("还没配置对端地址与访问令牌"));
    }
    client::pull(
        &state.store,
        &state.vault,
        &link,
        &args.asset_ids,
        args.with_creds,
        args.force,
    )
    .await
}
