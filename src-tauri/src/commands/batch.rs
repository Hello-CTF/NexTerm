//! 批量执行命令（§6.2 batch）：一组资产并发跑同一条命令。
//!
//! 每台资产各建一条**临时**传输（[`crate::session::exec_once`]），跑完即关 ——
//! 不往会话池里塞孤儿会话。单台失败不影响其他台，错误进该行的 `error`。

use std::sync::Arc;
use std::time::Duration;

use futures::stream::{self, StreamExt};
use serde::{Deserialize, Serialize};

use crate::error::{AppError, AppResult};
use crate::ids::now_ms;
use crate::ipc_shim as tauri;
use crate::state::{AppState, ManagedState};

const DEFAULT_TIMEOUT_MS: u64 = 30_000;
const DEFAULT_CONCURRENCY: usize = 6;
const MIN_CONCURRENCY: usize = 1;
const MAX_CONCURRENCY: usize = 16;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BatchExecArgs {
    pub asset_ids: Vec<String>,
    pub command: String,
    pub timeout_ms: Option<u64>,
    pub concurrency: Option<usize>,
}

/// 单台资产的执行结果。**DTO，不是 Row** —— 回 Row 只会让前端字段永远 undefined。
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BatchExecRow {
    pub asset_id: String,
    pub name: String,
    pub host: String,
    /// 传输层成功且退出码为 0。
    pub ok: bool,
    pub exit_code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
    /// 失败原因（建连失败 / 命令超时 / 资产不存在等）；成功为 null。
    pub error: Option<String>,
    pub duration_ms: u64,
}

/// 并发度裁剪：默认 6，夹紧到 1..=16（0 也夹到 1）。
pub(crate) fn clamp_concurrency(v: Option<usize>) -> usize {
    v.unwrap_or(DEFAULT_CONCURRENCY)
        .clamp(MIN_CONCURRENCY, MAX_CONCURRENCY)
}

/// 去重，且保留每个 id 首次出现的顺序。
fn dedup_ids(ids: Vec<String>) -> Vec<String> {
    let mut seen = std::collections::HashSet::new();
    ids.into_iter()
        .filter(|id| seen.insert(id.clone()))
        .collect()
}

/// 按输入序号把并发收集到的结果还原成输入顺序。
///
/// 并发执行时任务是**按完成先后**交回的，直接返回会让「结果第 i 行」对不上
/// 「assetIds 第 i 个」。这里以输入序号为键重排，保证前端按输入顺序对齐。
fn order_by_input(mut items: Vec<(usize, BatchExecRow)>) -> Vec<BatchExecRow> {
    items.sort_by_key(|(idx, _)| *idx);
    items.into_iter().map(|(_, row)| row).collect()
}

#[tauri::command]
pub async fn batch_exec(
    state: ManagedState<'_>,
    args: BatchExecArgs,
) -> AppResult<Vec<BatchExecRow>> {
    let command = args.command.trim().to_string();
    if command.is_empty() {
        return Err(AppError::param("命令不能为空"));
    }
    let ids = dedup_ids(args.asset_ids);
    if ids.is_empty() {
        return Err(AppError::param("至少要选择一个资产"));
    }
    let timeout = Duration::from_millis(args.timeout_ms.unwrap_or(DEFAULT_TIMEOUT_MS));
    let concurrency = clamp_concurrency(args.concurrency);
    let started = now_ms();

    let rows = run_batch(state.inner().clone(), &ids, &command, timeout, concurrency).await;

    // 一次 batch_exec 写**一条**审计：用户不关心逐台，审计的目的是回答
    // 「谁在什么时候对哪些机器跑了什么命令、几成几败」。
    let ok = rows.iter().filter(|r| r.ok).count();
    let _ = state
        .store
        .audit_insert(crate::store::AuditInput {
            session_id: None,
            asset_id: None,
            source: "user",
            kind: "batch_exec",
            payload: serde_json::json!({
                "command": command,
                "assets": ids,
                "ok": ok,
                "failed": rows.len() - ok,
            }),
            exit_code: None,
            duration_ms: Some(now_ms().saturating_sub(started) as i64),
        })
        .await;
    Ok(rows)
}

/// 并发跑完全部资产，结果按输入顺序返回。
async fn run_batch(
    state: Arc<AppState>,
    ids: &[String],
    command: &str,
    timeout: Duration,
    concurrency: usize,
) -> Vec<BatchExecRow> {
    let items: Vec<(usize, BatchExecRow)> =
        stream::iter(ids.iter().cloned().enumerate().map(|(idx, id)| {
            let state = Arc::clone(&state);
            let command = command.to_string();
            async move {
                let row = run_one(&state, &id, &command, timeout).await;
                (idx, row)
            }
        }))
        .buffer_unordered(concurrency)
        .collect()
        .await;
    order_by_input(items)
}

/// 执行单台：资产查不到 / 建连失败 / 命令超时都只落到这一行的 `error`，不外抛。
async fn run_one(
    state: &AppState,
    asset_id: &str,
    command: &str,
    timeout: Duration,
) -> BatchExecRow {
    let started = now_ms();
    let fail = |name: String, host: String, error: String| BatchExecRow {
        asset_id: asset_id.to_string(),
        name,
        host,
        ok: false,
        exit_code: None,
        stdout: String::new(),
        stderr: String::new(),
        error: Some(error),
        duration_ms: now_ms().saturating_sub(started),
    };
    let asset = match state.store.asset_get(asset_id).await {
        Ok(a) => a,
        Err(_) => return fail(String::new(), String::new(), "资产不存在".into()),
    };
    let name = asset.name.clone();
    let host = asset.host.clone().unwrap_or_default();
    match crate::session::exec_once(state, &asset, command, timeout).await {
        Ok(r) => BatchExecRow {
            asset_id: asset_id.to_string(),
            name,
            host,
            ok: r.exit_code == Some(0),
            exit_code: r.exit_code,
            stdout: r.stdout,
            stderr: r.stderr,
            error: None,
            duration_ms: r.duration_ms,
        },
        Err(e) => fail(name, host, e.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: &str) -> BatchExecRow {
        BatchExecRow {
            asset_id: id.to_string(),
            name: String::new(),
            host: String::new(),
            ok: true,
            exit_code: Some(0),
            stdout: String::new(),
            stderr: String::new(),
            error: None,
            duration_ms: 0,
        }
    }

    #[test]
    fn clamp_concurrency_defaults_and_clamps() {
        assert_eq!(clamp_concurrency(None), 6);
        assert_eq!(clamp_concurrency(Some(0)), 1);
        assert_eq!(clamp_concurrency(Some(1)), 1);
        assert_eq!(clamp_concurrency(Some(16)), 16);
        assert_eq!(clamp_concurrency(Some(17)), 16);
        assert_eq!(clamp_concurrency(Some(1000)), 16);
    }

    #[test]
    fn dedup_keeps_first_occurrence_order() {
        let got = dedup_ids(vec![
            "a".into(),
            "b".into(),
            "a".into(),
            "c".into(),
            "b".into(),
        ]);
        assert_eq!(got, vec!["a", "b", "c"]);
    }

    #[test]
    fn results_follow_input_order_despite_completion_order() {
        // 完成顺序是 idx 2、0、1 → 输出必须还原成 0、1、2。
        let items = vec![(2, row("c")), (0, row("a")), (1, row("b"))];
        let rows = order_by_input(items);
        let order: Vec<&str> = rows.iter().map(|r| r.asset_id.as_str()).collect();
        assert_eq!(order, vec!["a", "b", "c"]);
    }
}
