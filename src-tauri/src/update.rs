//! 在线更新：检查 → 提示 → 下载 → 安装 → 重启。
//!
//! # 为什么放在内核，而不是前端直接调插件
//!
//! `@tauri-apps/plugin-updater` 只在**桌面**这一个环境里成立。NexTerm 有三态
//! （桌面 Tauri / 服务端浏览器 / 演示），而命令清单 `nexterm_commands!` 只有一份 ——
//! 走前端插件意味着「服务端模式下这条路径根本不存在」，而且绕开了「全部 IPC 收敛到
//! 一张表」的约定（见 `commands/mod.rs`）。放进内核后，两端各自给出**如实**的答案：
//! 桌面能装就装，服务端明说自己不支持。
//!
//! # 判据是「公钥配没配」，不是「有没有网」
//!
//! 应用内安装必须验签 —— 防的是更新通道被投毒，这正是一个终端工具最不能失守的一环。
//! 签名公钥写在 `tauri.conf.json` 的 `plugins.updater.pubkey`，**私钥只存在于发行方
//! 的 CI**（见 `.github/workflows/release.yml`）。仓库里放的是一枚占位值
//! （[`PLACEHOLDER_PUBKEY`]），因为私钥属于发行方，不可能随代码走。
//!
//! 于是「还没配」是一种**预期内的正常状态**，不能报成一串 minisign 解析错误。
//! 检查到占位值就**一次网络都不发**，直接把原因交给界面。
//!
//! # 失败一律降级成「查不到更新」，而不是 `Err`
//!
//! [`check`] 只在交付层真出问题时才返回 `Err`；网络不通、GitHub 限流、清单结构变了，
//! 全部落回 [`UpdateInfo::unavailable`]。理由很实际：更新检查是**附带**功能，
//! 不该让「启动时顺手查一下」变成能把应用搞出错的失败源。这一点在界面上是有区别的 ——
//! 「已是最新」与「查不了」要分得开，所以 `available` 与 `can_install` 是两个字段。

use serde::Serialize;
use ts_rs::TS;

use crate::error::AppResult;
use crate::ipc_shim as tauri;

/// 占位公钥 —— 与 `src-tauri/tauri.conf.json` 里 `plugins.updater.pubkey` 的值
/// **必须逐字一致**。
///
/// 它同时是「发行方尚未生成签名密钥」的哨兵：靠它把「没配」与「配错」分开。
/// 配真公钥时记得把这里也一并替换掉（两个文件各一处）。
pub const PLACEHOLDER_PUBKEY: &str = "REPLACE_ME_RUN_tauri_signer_generate";

/// 一条更新检查的结果（跨 IPC）。
///
/// 刻意把「有没有新版」与「能不能装」拆成两个字段：服务端构建、或公钥未配置时，
/// 仍然可以**知道**有没有新版（[`Self::available`]），只是装不了
/// （[`Self::can_install`]）。合成一个 `bool` 的话，界面就无从区分
/// 「已是最新」与「查不了」—— 而这两句话对用户的意思完全相反。
#[derive(Debug, Clone, Serialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct UpdateInfo {
    /// 当前运行版本（编译期常量，来自 `Cargo.toml`）。
    pub current: String,
    /// 可用的新版本号；没有新版时为 `None`。
    pub latest: Option<String>,
    /// 是否确实有更新的版本。
    pub available: bool,
    /// 本版更新说明（Release 正文，Markdown）。
    pub notes: Option<String>,
    /// 新版本发布时间。
    pub date: Option<String>,
    /// 当前构建能否应用内安装。
    pub can_install: bool,
    /// 不能安装时的原因。**直接展示给用户**，所以写成中文人话。
    pub unavailable_reason: Option<String>,
}

/// 当前版本号，来自 `Cargo.toml`。
fn current_version() -> String {
    env!("CARGO_PKG_VERSION").to_string()
}

impl UpdateInfo {
    /// 「查不了 / 装不了」的通用构造。
    ///
    /// 一律同时置 `can_install = false` —— 否则界面会给出一个「点了必然失败」的按钮。
    fn unavailable(reason: impl Into<String>) -> Self {
        Self {
            current: current_version(),
            latest: None,
            available: false,
            notes: None,
            date: None,
            can_install: false,
            unavailable_reason: Some(reason.into()),
        }
    }
}

// ─────────────────────── 桌面：真检查、真安装 ───────────────────────

// 这里**只**需要 `UpdaterExt`：`AppHandle::config()` 与 `request_restart()` 都是
// 固有方法，`Manager` trait 不必在作用域里。多导一次会被 `-D unused-imports`
// 打红（CI 是 `-D warnings`）。
#[cfg(feature = "desktop")]
use tauri_plugin_updater::UpdaterExt;

/// 「已是最新」—— 这是**成功**路径，不是失败：能装，只是没得装。
#[cfg(feature = "desktop")]
impl UpdateInfo {
    fn up_to_date() -> Self {
        Self {
            current: current_version(),
            latest: None,
            available: false,
            notes: None,
            date: None,
            can_install: true,
            unavailable_reason: None,
        }
    }
}

/// 公钥是否**真的**配了（空值与占位值都不算）。
///
/// 抽成独立函数是为了能单测 —— 它是「这个构建到底有没有更新服务」的唯一判据，
/// 判错的后果很具体：用户点完「立即更新」才在验签那步失败。
#[cfg(feature = "desktop")]
fn pubkey_configured(pubkey: &str) -> bool {
    let k = pubkey.trim();
    !k.is_empty() && !k.contains(PLACEHOLDER_PUBKEY)
}

/// 读 `tauri.conf.json` 里配的签名公钥。
///
/// 走 `Config.plugins`（`tauri::Config` 的字段是 public）而不是在 Rust 里再抄一份常量：
/// 抄一份必然漂移 —— 配置改了、常量没改，症状是「明明配好了却一直报未配置」。
///
/// 先 `to_value` 再取字段，而不是直接摸 `PluginConfig` 的元组字段：前者只要求
/// `Serialize`（`Config: Serialize` 必然满足），后者依赖那个字段公开与否。
#[cfg(feature = "desktop")]
fn configured_pubkey(app: &tauri::AppHandle) -> String {
    let plugins = serde_json::to_value(&app.config().plugins);
    plugins
        .ok()
        .and_then(|v| v.get("updater").cloned())
        .and_then(|u| u.get("pubkey").cloned())
        .and_then(|p| p.as_str().map(str::to_owned))
        .unwrap_or_default()
}

/// 检查更新。
#[cfg(feature = "desktop")]
pub async fn check(app: &tauri::AppHandle) -> AppResult<UpdateInfo> {
    if !pubkey_configured(&configured_pubkey(app)) {
        // ⚠️ 用户可见的长文案一律**先绑定成变量**，不要作为字符串字面量内联进调用。
        // 内联时那一行的宽度由中日韩字符（rustfmt 按宽度 2 算）决定，能不能放下
        // 全看运气；绑定之后每个调用点都是短标识符，格式与取值都不再互相牵制。
        let why = "更新服务未配置：发行方需生成签名密钥，并把公钥填入 tauri.conf.json";
        return Ok(UpdateInfo::unavailable(why));
    }

    let updater = match app.updater() {
        Ok(u) => u,
        Err(e) => {
            let why = format!("更新器不可用：{e}");
            return Ok(UpdateInfo::unavailable(why));
        }
    };

    match updater.check().await {
        // 没查到新版本 = 已是最新（`check` 用 `None` 表达，不是错误）。
        Ok(None) => Ok(UpdateInfo::up_to_date()),
        Ok(Some(u)) => Ok(UpdateInfo {
            current: u.current_version,
            latest: Some(u.version),
            available: true,
            notes: u.body,
            date: u.date.map(|d| d.to_string()),
            can_install: true,
            unavailable_reason: None,
        }),
        // 网络不通 / GitHub 限流 / 清单结构变了 —— 一律降级，不打扰用户。
        Err(e) => {
            let why = format!("检查更新失败：{e}");
            Ok(UpdateInfo::unavailable(why))
        }
    }
}

/// 下载并安装更新。装完**不**自动退出 —— 由界面决定何时重启（见 [`restart`]）。
///
/// 进度用事件推（`update://progress`），不复用返回值：下载几十兆期间界面要有反馈，
/// 而这条命令的返回值只该表达「成没成」。`fs://progress` 是同一条路径。
#[cfg(feature = "desktop")]
pub async fn install(app: &tauri::AppHandle) -> AppResult<()> {
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::Arc;

    use crate::error::AppError;
    use tauri::Emitter;

    if !pubkey_configured(&configured_pubkey(app)) {
        let why = "更新服务未配置：缺少签名公钥，无法校验更新包";
        return Err(AppError::Unsupported(why.into()));
    }

    let updater = match app.updater() {
        Ok(u) => u,
        Err(e) => {
            let why = format!("更新器不可用：{e}");
            return Err(AppError::internal(why));
        }
    };

    let found = match updater.check().await {
        Ok(v) => v,
        Err(e) => {
            let why = format!("检查更新失败：{e}");
            return Err(AppError::internal(why));
        }
    };
    let Some(update) = found else {
        return Err(AppError::NotFound("当前已是最新版本".into()));
    };

    let version = update.version.clone();
    let downloaded = Arc::new(AtomicU64::new(0));
    let counter = Arc::clone(&downloaded);
    let emitter = app.clone();

    // 回调单独绑定而不是内联进 `download_and_install(...)`：内联时那个调用会长到
    // 需要 rustfmt 自行决定怎么折行，而折法随版本变；绑定出来之后
    // `download_and_install(on_chunk, || {})` 一定落在同一行里。
    let on_chunk = move |chunk: usize, total: Option<u64>| {
        let n = counter.fetch_add(chunk as u64, Ordering::Relaxed) + chunk as u64;
        let payload = crate::events::UpdateProgressPayload {
            downloaded: n,
            total,
        };
        let _ = emitter.emit(crate::events::UPDATE_PROGRESS, payload);
    };

    let result = update.download_and_install(on_chunk, || {}).await;
    if let Err(e) = result {
        let why = format!("安装更新失败：{e}");
        return Err(AppError::internal(why));
    }

    tracing::info!(target: "update", %version, "更新包已安装，等待重启生效");
    Ok(())
}

/// 重启应用。
///
/// macOS / Linux 上装完新版本**必须**重启才生效（替换的是磁盘上的包，跑着的仍是旧
/// 代码）；Windows 的 NSIS 安装器会自己拉起新进程，这条基本用不上，留着是为了三个
/// 平台行为一致。
#[cfg(feature = "desktop")]
pub fn restart(app: &tauri::AppHandle) {
    app.request_restart();
}

// ─────────────────────── 服务端：如实说「不支持」 ───────────────────────

/// 服务端构建：**能查、不能装**。
///
/// 服务端跑在容器里（无 Tauri 运行时），也就没有「用新包替换自己」这回事 ——
/// 升级方式是换镜像 / 换部署包。所以这里如实返回 `can_install = false`，
/// 而不是假装成功，也不是抛错。
///
/// ⚠️ 目前连「查」也没有做（服务端不引入 updater 插件）。真要支持，得自己走
/// GitHub Releases API 比版本号 —— 那会多出第二份「最新版是多少」的判据，本轮不做。
#[cfg(not(feature = "desktop"))]
pub async fn check(_app: &tauri::AppHandle) -> AppResult<UpdateInfo> {
    let why = "服务端构建不支持应用内更新：请拉取新版本的部署包并重启服务";
    Ok(UpdateInfo::unavailable(why))
}

/// 服务端构建没有安装路径。
#[cfg(not(feature = "desktop"))]
pub async fn install(_app: &tauri::AppHandle) -> AppResult<()> {
    let why = "服务端构建不支持应用内更新";
    Err(crate::error::AppError::Unsupported(why.into()))
}

/// 服务端构建没有重启路径（重启是运维动作，不是应用动作）。
#[cfg(not(feature = "desktop"))]
pub fn restart(_app: &tauri::AppHandle) {}

#[cfg(all(test, feature = "desktop"))]
mod tests {
    use super::*;

    /// 占位公钥必须被判成「未配置」。
    ///
    /// 判错的后果不是崩溃，而是**用户在点完「立即更新」之后**才在验签那步失败 ——
    /// 一个本可以在点击之前就说清楚的错误。
    #[test]
    fn placeholder_pubkey_is_unconfigured() {
        assert!(!pubkey_configured(""));
        assert!(!pubkey_configured("   \n"));
        assert!(!pubkey_configured(PLACEHOLDER_PUBKEY));
        let wrapped = format!("  {PLACEHOLDER_PUBKEY}  ");
        assert!(!pubkey_configured(&wrapped));
    }

    /// 真公钥（minisign 的 base64 形态）必须判成「已配置」，否则功能永远起不来。
    #[test]
    fn real_pubkey_is_configured() {
        let k = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3";
        assert!(pubkey_configured(k));
        let padded = format!("  {k}\n");
        assert!(pubkey_configured(&padded));
    }

    /// 「查不了」必须同时关掉安装 —— 否则界面会渲染一个点了必然失败的按钮。
    #[test]
    fn unavailable_also_disables_install() {
        let info = UpdateInfo::unavailable("网络不可达");
        assert!(!info.available);
        assert!(!info.can_install);
        assert_eq!(info.unavailable_reason.as_deref(), Some("网络不可达"));
        assert_eq!(info.current, env!("CARGO_PKG_VERSION"));
    }

    /// 「已是最新」不是失败：能装、且没有原因要展示给用户。
    #[test]
    fn up_to_date_is_success_not_failure() {
        let info = UpdateInfo::up_to_date();
        assert!(!info.available);
        assert!(info.can_install);
        assert!(info.unavailable_reason.is_none());
    }
}
