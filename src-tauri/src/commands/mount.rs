//! 挂载命令（§6.2 mount）：列表以本机真实状态为准（§5.5）。
//!
//! 命令体只做一件事：把 `ManagedState` 里的 `Store` 掏出来交给 `*_inner`。
//! 逻辑全在 `*_inner` 里，这样测试能用 `Store::open_in_memory()` 直接跑完整条
//! 路径（包括「失败也要落审计」），不必去构造 Tauri 的 `State`。

use serde::Deserialize;

use crate::error::{AppError, AppResult};
use crate::fs::mount::{self, MountEntry};
use crate::state::ManagedState;
use crate::store::{AuditInput, Store};

/// 磁盘挂载功能可用性：`null` = 可用，字符串 = **暂不可用**的原因。
///
/// 前端在首帧渲染前（`main.tsx` 的 bootstrap）读取，用来把入口置灰并把理由说清楚；
/// 真正说不的是 [`mount_create`] / [`mount_remove`]（返回 `AppError::Unsupported`）。
/// 放在后端而不是让前端 `isMac()` 自己猜：将来适配完成只改 Rust 一处。
#[tauri::command]
pub fn mount_capability() -> Option<String> {
    mount::unavailable_reason().map(str::to_string)
}

#[tauri::command]
pub async fn mount_list(force_refresh: Option<bool>) -> AppResult<Vec<MountEntry>> {
    let _ = force_refresh;
    mount::scan().await
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MountCreateArgs {
    pub session_id: String,
    /// 远端：Windows 为 \\\\host\\share，Linux 为 user@host:/path
    pub remote_path: String,
    /// Z: 或 /mnt/point
    pub local_point: String,
    pub username: Option<String>,
    pub password: Option<String>,
}

/// 挂载 / 卸载统一落审计。
///
/// 之前只有「挂载成功」这一条路径写了审计：失败被 `?` 直接抛走，卸载则完全没记。
/// 结果是审计里只剩成功的勾，出了问题反查不到任何痕迹 —— 而挂载恰恰是
/// 「改变本机状态」的动作，成没成都得对得上账。
async fn audit(
    store: &Store,
    session_id: Option<String>,
    payload: serde_json::Value,
    exit_code: i32,
) {
    let _ = store
        .audit_insert(AuditInput {
            session_id,
            asset_id: None,
            source: "user",
            kind: "mount",
            payload,
            exit_code: Some(exit_code),
            duration_ms: None,
        })
        .await;
}

#[tauri::command]
pub async fn mount_create(state: ManagedState<'_>, args: MountCreateArgs) -> AppResult<MountEntry> {
    mount_create_inner(&state.store, args).await
}

async fn mount_create_inner(store: &Store, args: MountCreateArgs) -> AppResult<MountEntry> {
    // 凭据默认复用资产（v1 直传；后续接 vault）
    if args.local_point.is_empty() || args.remote_path.is_empty() {
        return Err(AppError::param("挂载点与远端路径不能为空"));
    }
    if let Err(e) = mount::mount(
        &args.local_point,
        &args.remote_path,
        args.username.as_deref(),
        args.password.as_deref(),
    )
    .await
    {
        audit(
            store,
            Some(args.session_id.clone()),
            serde_json::json!({
                "remote": args.remote_path,
                "point": args.local_point,
                "ok": false,
                "error": e.to_string(),
            }),
            1,
        )
        .await;
        return Err(e);
    }
    audit(
        store,
        Some(args.session_id.clone()),
        serde_json::json!({
            "remote": args.remote_path,
            "point": args.local_point,
            "ok": true,
        }),
        0,
    )
    .await;
    Ok(MountEntry {
        id: crate::ids::new_id(),
        local_point: args.local_point,
        remote: args.remote_path,
        session_id: Some(args.session_id),
        created_at: Some(crate::ids::now_ms()),
    })
}

/// 断开映射。`session_id` 只用于审计归属（老前端不传则为 `None`，不影响功能）。
#[tauri::command]
pub async fn mount_remove(
    state: ManagedState<'_>,
    local_point: String,
    session_id: Option<String>,
) -> AppResult<()> {
    mount_remove_inner(&state.store, local_point, session_id).await
}

async fn mount_remove_inner(
    store: &Store,
    local_point: String,
    session_id: Option<String>,
) -> AppResult<()> {
    if let Err(e) = mount::unmount(&local_point).await {
        audit(
            store,
            session_id,
            serde_json::json!({ "point": local_point, "ok": false, "error": e.to_string() }),
            1,
        )
        .await;
        return Err(e);
    }
    audit(
        store,
        session_id,
        serde_json::json!({ "point": local_point, "ok": true }),
        0,
    )
    .await;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 某个动作在审计表里的条数。
    async fn audit_count(store: &Store, exit_code: i32) -> i64 {
        sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM audit_log WHERE kind = 'mount' AND exit_code = ?",
        )
        .bind(exit_code)
        .fetch_one(store.pool())
        .await
        .expect("查询审计表")
    }

    /// 读回唯一一条审计的 payload。
    ///
    /// 只在 macOS 的失败用例里用得到 —— 那是唯一一条**由平台保证失败**的路径。
    /// Windows / Linux 上 `mount_create_inner` 会真的去跑 `net use` / `sshfs`，
    /// 成不成取决于运行环境，断言不了，所以连用例一起按平台收窄。
    ///
    /// 刻意不用 `#[allow(dead_code)]` 盖告警：CI 跑的是 `clippy -D warnings`，
    /// 一旦改成 allow，将来这条用例被误门控掉（等于守护消失）也没人会发现。
    #[cfg(target_os = "macos")]
    async fn audit_payload(store: &Store) -> String {
        sqlx::query_scalar::<_, String>("SELECT payload_json FROM audit_log LIMIT 1")
            .fetch_one(store.pool())
            .await
            .expect("查询审计 payload")
    }

    /// macOS：挂载被**显式**拒绝，而且这一次失败必须留在审计里。
    ///
    /// 这是「失败的挂载动作不落审计」那条缺陷的守门用例：注释掉 `audit(...)`
    /// 之后，`audit_count(.., 1)` 会变成 0，用例红。
    #[cfg(target_os = "macos")]
    #[tokio::test]
    async fn mac_mount_failure_is_audited() {
        let store = Store::open_in_memory().await.expect("内存库");
        let args = MountCreateArgs {
            session_id: "sess-1".into(),
            remote_path: "linuxcore@host:/data".into(),
            local_point: "/tmp/nx-mnt".into(),
            username: None,
            password: None,
        };
        let err = mount_create_inner(&store, args)
            .await
            .expect_err("macOS 上必须拒绝");
        assert_eq!(err.code(), "unsupported");

        assert_eq!(audit_count(&store, 1).await, 1, "失败必须留一条审计");
        assert_eq!(audit_count(&store, 0).await, 0, "失败不能同时记成功");
        let payload = audit_payload(&store).await;
        assert!(payload.contains("\"ok\":false"), "payload: {payload}");
        assert!(
            payload.contains("macFUSE"),
            "payload 要带上真实原因: {payload}"
        );
    }

    /// 卸载同样：失败也要落审计（原来 `mount_remove` 一条都不记）。
    #[cfg(target_os = "macos")]
    #[tokio::test]
    async fn mac_unmount_failure_is_audited() {
        let store = Store::open_in_memory().await.expect("内存库");
        let err = mount_remove_inner(&store, "/tmp/nx-mnt".into(), Some("sess-1".into()))
            .await
            .expect_err("macOS 上必须拒绝");
        assert_eq!(err.code(), "unsupported");
        assert_eq!(audit_count(&store, 1).await, 1);
    }

    /// 参数为空时在动审计之前就返回 —— 空参数不是「一次挂载尝试」，不该污染审计。
    #[tokio::test]
    async fn empty_args_are_rejected_without_audit() {
        let store = Store::open_in_memory().await.expect("内存库");
        let args = MountCreateArgs {
            session_id: "sess-1".into(),
            remote_path: "  ".into(),
            local_point: "".into(),
            username: None,
            password: None,
        };
        let err = mount_create_inner(&store, args)
            .await
            .expect_err("必须拒绝");
        assert_eq!(err.code(), "bad_param");
        assert_eq!(
            audit_count(&store, 0).await + audit_count(&store, 1).await,
            0
        );
    }
}
