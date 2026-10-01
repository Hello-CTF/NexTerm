//! Rust → 前端事件名常量与 payload 结构（§6.3）。
//! 控制信息走事件，二进制数据走 Channel —— 两者绝不混用（§6.4）。

use serde::Serialize;

pub const SESSION_STATUS: &str = "session://status";
pub const TERMINAL_EXIT: &str = "terminal://exit";
pub const TERMINAL_THROTTLED: &str = "terminal://throttled";
/// 终端控制权 / 观看人数快照变化。见 [`TerminalControlPayload`]。
pub const TERMINAL_CONTROL: &str = "terminal://control";
pub const FS_PROGRESS: &str = "fs://progress";
pub const DOCKER_STATS: &str = "docker://stats";
pub const AI_EVENT: &str = "ai://event";
/// M5 预留，v1 不发。
pub const SYNC_STATUS: &str = "sync://status";
pub const APP_ERROR: &str = "app://error";

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionStatusPayload {
    pub session_id: String,
    /// connecting | connected | reconnecting | disconnected | failed
    pub status: String,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalExitPayload {
    pub tab_id: String,
    pub exit_code: Option<i32>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalThrottledPayload {
    pub tab_id: String,
    pub inflight_bytes: usize,
}

/// 终端控制权 / 观看人数快照（`terminal://control`）。
///
/// # 为什么需要推送
///
/// 没有它的时候，控制权易主对**观察者端**是不可见的：B 点了「接管控制」，A 的画面
/// 仍旧显示自己是操作者，直到 A 下一次敲键被 `not_controller` 顶回来才翻成观察者态 ——
/// 中间那段时间 UI 在骗人。同理「几个人在看」也只在交互时才刷新。
///
/// 五个字段每次都给全，前端按同一份快照覆盖本地状态即可，不必做增量推断。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalControlPayload {
    /// 内核标签 id。
    pub tab_id: String,
    /// 当前持权者的 `clientId`；`None`（JSON `null`）= 无人持权（此时谁都不能敲）。
    pub controller: Option<String>,
    /// 订阅者数量 —— `TerminalTab::subscriber_count()` 的**原值，含收到事件的那一端
    /// 自己**，与 `terminal_list` 的 `LiveTabInfo.subscribers` 完全同口径。
    ///
    /// ⚠️ 这是**通道数**（同一台设备开两个页面就 +2），不是设备数。界面上的
    /// 「N 个设备正在观看」请看 [`Self::viewers`]。两者都保留：`subscribers` 仍被
    /// 用作「后端是否还有推送管道」的判据。
    pub subscribers: usize,
    /// **观看设备数**（按 `client` 去重）—— `TerminalTab::viewer_count()` 的原值，
    /// 与 `LiveTabInfo.viewers` 同口径。
    ///
    /// 与 `subscribers` 的区别就在于去重：同一浏览器开两个页面看同一个标签，
    /// `subscribers == 2` 而 `viewers == 1`——因为两个页面共享同一个 `clientId`。
    /// 界面上的「N 个设备正在观看」用这个。
    pub viewers: usize,
    /// 该标签的进程是否已结束。
    pub exited: bool,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FsProgressPayload {
    pub task_id: String,
    pub transferred: u64,
    pub total: u64,
    pub done: bool,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AppErrorPayload {
    pub code: String,
    pub message: String,
    pub detail: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `terminal://control` 的线格式就是前后端之间的契约：camelCase 五字段。
    /// 字段名写错在这里会静默失配（前端拿到的全是 `undefined`），所以钉死它。
    #[test]
    fn control_payload_wire_shape() {
        let v = serde_json::to_value(TerminalControlPayload {
            tab_id: "t1".into(),
            controller: Some("device-a".into()),
            subscribers: 2,
            viewers: 1,
            exited: false,
        })
        .unwrap();
        assert_eq!(
            v,
            serde_json::json!({
                "tabId": "t1",
                "controller": "device-a",
                "subscribers": 2,
                "viewers": 1,
                "exited": false,
            })
        );

        // 无人持权 / 已结束：controller 必须是显式 null，而不是缺字段。
        let v2 = serde_json::to_value(TerminalControlPayload {
            tab_id: "t1".into(),
            controller: None,
            subscribers: 0,
            viewers: 0,
            exited: true,
        })
        .unwrap();
        assert_eq!(v2["controller"], serde_json::Value::Null);
        assert_eq!(v2["subscribers"], 0);
        assert_eq!(v2["viewers"], 0, "viewers 必须出现在线形里，不能被跳过");
        assert_eq!(v2["exited"], true);
    }
}
