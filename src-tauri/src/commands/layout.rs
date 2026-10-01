//! 工作区布局命令：把「打开着哪些窗口与标签」这件事放到服务端。
//!
//! # 内核不解释布局
//!
//! `data` 是一段**前端自己的** JSON（工作区 / 面板 / 标签 / 激活项 / 分栏比例），
//! 内核只负责「存、取、版本」。这样做的好处是前端调整布局结构不必动 Rust，
//! 也不会掉进「内核的字段和前端对不上、某个字段永远是 undefined」那类坑。
//!
//! # 为什么需要它（而不是继续留在浏览器里）
//!
//! 改造前布局只活在浏览器的 zustand store 里（`localStorage` 全项目只存了
//! API 地址与侧栏宽度），于是「关掉页面 = 工作区没了」「换台设备打开 = 另一份
//! 完全不同的工作区」。而服务端其实早就握着连接与回滚缓冲（见 `terminal` /
//! `session` 两个模块）—— 缺的就是这份视图。把它落到服务端，浏览器才真正退化
//! 成一个显示器。
//!
//! # 冲突：宁可让用户重来一次，也不静默合并
//!
//! 写入带 `revision` 做乐观锁（见 [`crate::store::Store::layout_save`]）。冲突时
//! 返回 `conflict: true` 而不是自动合并 —— 布局没有可合并的语义（对端删掉的标签
//! 该不该复活？两个窗口的尺寸听谁的？），强行合并只会产出一份谁也看不懂的布局。

use crate::ipc_shim as tauri;
use serde::{Deserialize, Serialize};
use tauri::Emitter;

use crate::error::AppResult;
use crate::state::ManagedState;

/// 布局变化事件：其他设备收到后重新拉取。
///
/// 事件里带 `revision`，让收到的一方能判断「这是不是我自己刚写的那次」——
/// 不判断就会自己触发自己，形成来回刷新的回环。
pub const LAYOUT_CHANGED: &str = "layout://changed";

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LayoutDto {
    pub revision: i64,
    pub updated_at: i64,
    /// 布局正文；从没保存过时是 `null`（前端用默认布局）。
    pub data: Option<serde_json::Value>,
}

/// 读当前布局。
#[tauri::command]
pub async fn layout_get(state: ManagedState<'_>) -> AppResult<LayoutDto> {
    let (revision, updated_at, data) = state.store.layout_load().await?;
    Ok(LayoutDto {
        revision,
        updated_at,
        data: data.and_then(|s| serde_json::from_str(&s).ok()),
    })
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LayoutSaveDto {
    pub saved: bool,
    /// 写入后的 revision（`saved=false` 时是**对端**的当前 revision）。
    pub revision: i64,
    /// `true` = 对端在你之后改过。前端应拉最新再决定，**不要**直接重试覆盖。
    pub conflict: bool,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LayoutPutArgs {
    /// 布局正文（前端序列化好的 JSON 字符串）。
    pub data: String,
    /// 我这份布局基于哪个 revision。
    pub revision: i64,
}

/// 保存布局（乐观锁）。
#[tauri::command]
pub async fn layout_put(state: ManagedState<'_>, args: LayoutPutArgs) -> AppResult<LayoutSaveDto> {
    let (saved, revision) = state.store.layout_save(args.revision, &args.data).await?;
    if saved {
        // 广播给其他设备（含服务端的其他浏览器、桌面版）。
        // 发失败不是错误：没有人在订阅事件是常态。
        let _ = state
            .app
            .emit(LAYOUT_CHANGED, serde_json::json!({ "revision": revision }));
    }
    Ok(LayoutSaveDto {
        saved,
        revision,
        conflict: !saved,
    })
}
