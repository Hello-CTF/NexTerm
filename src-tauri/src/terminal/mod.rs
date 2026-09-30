//! 终端引擎（§4，最高优先级）：PTY + 内核 VT 状态机 + 环形缓冲 + 背压。
//!
//! 数据流（必须严格遵守）：
//! `PTY → PtyPump → ① screen.process ② scrollback.push ③ channel.send(前端)`
//!
//! 内核侧状态机是「AI 读屏 / 空闲判定 / 接管」的唯一数据来源（L2）。

pub mod keys;
pub mod pty;
pub mod scrollback;
pub mod transcoder;

use std::sync::atomic::{AtomicBool, AtomicU16, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use crate::ipc_shim as tauri;
use serde::Serialize;
use tauri::ipc::Channel;
use tokio::sync::RwLock;

use crate::ids::{now_ms, TabId};
use scrollback::Scrollback;
use transcoder::{TerminalEncoding, Transcoder};

/// 默认环形缓冲容量（字节）。
pub const SCROLLBACK_BYTES: usize = 32 * 1024 * 1024;
/// vt100 网格滚动行数。
pub const SCROLLBACK_LINES: usize = 100_000;
/// 背压阈值（§4.4）：inflight 超过即暂停读 PTY。
pub const INFLIGHT_PAUSE: usize = 4 * 1024 * 1024;
pub const INFLIGHT_DROP: usize = 16 * 1024 * 1024;
/// 标签不可见时的推送间隔（毫秒）。
pub const HIDDEN_FLUSH_MS: u64 = 50;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalState {
    pub alt_screen: bool,
    pub cursor_visible: bool,
    pub bracketed_paste: bool,
    pub application_cursor: bool,
}

/// 本地 PTY 的读写句柄（writer 用于写，master 用于 resize）。
/// master 以 Mutex 包装：`dyn MasterPty` 只要求 Send，Mutex 提供跨线程共享。
pub struct LocalPtyIo {
    pub writer: Arc<Mutex<Box<dyn std::io::Write + Send>>>,
    pub master: Arc<Mutex<Box<dyn portable_pty::MasterPty + Send>>>,
}

/// 写入端抽象：本地 PTY 为阻塞 IO，SSH channel 为异步消息。
pub enum TerminalWriter {
    Local(Arc<LocalPtyIo>),
    Ssh(Arc<russh::ChannelWriteHalf<russh::client::Msg>>),
    /// WinRM 行模式：写入按「一行命令」走 exec 通道，由会话层包装。
    None,
}

impl TerminalWriter {
    pub async fn write(&self, data: &[u8]) -> crate::error::AppResult<()> {
        match self {
            Self::Local(io) => {
                let w = Arc::clone(&io.writer);
                let data = data.to_vec();
                tokio::task::spawn_blocking(move || {
                    let mut guard = w.lock().unwrap_or_else(|e| e.into_inner());
                    guard.write_all(&data).map_err(crate::error::AppError::Io)?;
                    guard.flush().map_err(crate::error::AppError::Io)
                })
                .await
                .map_err(|e| crate::error::AppError::Internal(format!("write join: {e}")))?
            }
            Self::Ssh(ch) => {
                ch.data_bytes(data.to_vec())
                    .await
                    .map_err(|e| crate::error::AppError::Ssh(format!("channel 写入失败: {e}")))?;
                Ok(())
            }
            Self::None => Err(crate::error::AppError::Unsupported(
                "该会话不支持直接写入".into(),
            )),
        }
    }
}

/// 泵回调（由会话层注入，解耦 AppHandle 便于单测）。
pub trait TabCallbacks: Send + Sync {
    fn exit(&self, tab_id: &str, exit_code: Option<i32>);
    fn throttled(&self, tab_id: &str, inflight: usize);
}

/// 永不回调的实现（测试用）。
pub struct NoopCallbacks;
impl TabCallbacks for NoopCallbacks {
    fn exit(&self, _tab_id: &str, _exit_code: Option<i32>) {}
    fn throttled(&self, _tab_id: &str, _inflight: usize) {}
}

/// 带结构的屏幕快照（§4.3）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScreenSnapshot {
    pub text: String,
    pub lines: Vec<String>,
    pub cursor_row: u16,
    pub cursor_col: u16,
    pub cols: u16,
    pub rows: u16,
    pub alt_screen: bool,
    pub last_output_ms_ago: u64,
}

/// 终端录制落盘状态。
struct Recording {
    file: tokio::fs::File,
    bytes: u64,
}

/// 一个终端标签页 = 一个 PTY + 一份状态机 + 一条到前端的通道（§4.2）。
pub struct TerminalTab {
    pub tab_id: TabId,
    pub session_id: String,
    cols: AtomicU16,
    rows: AtomicU16,
    /// 终端类型名。
    pub term: String,
    /// 内核侧状态机：AI 读屏的唯一数据来源。
    screen: Mutex<vt100::Parser>,
    transcoder: Mutex<Transcoder>,
    /// 原始字节环形缓冲（重放/导出用）。
    scrollback: Mutex<Scrollback>,
    /// 前端通道（可能尚未 attach）。
    sink: RwLock<Option<Channel<Vec<u8>>>>,
    /// 前端标签是否可见。
    visible: AtomicBool,
    /// 背压：已读出但尚未送达前端的字节数。
    inflight: Arc<AtomicUsize>,
    /// 写入端（前端按键 / AI 发送）。
    writer: RwLock<Arc<TerminalWriter>>,
    /// 最近一次输出时间（Unix 毫秒）。
    last_output_at: AtomicU64,
    /// 终端模式位（由 vt100 状态推导）。
    state: Mutex<TerminalState>,
    /// 终端录制（可选）。
    recorder: tokio::sync::Mutex<Option<Recording>>,
    /// 泵停止信号。
    pub stop: Arc<tokio_util::sync::CancellationToken>,
}

impl TerminalTab {
    pub fn new_arc(
        tab_id: TabId,
        session_id: String,
        cols: u16,
        rows: u16,
        encoding: TerminalEncoding,
    ) -> Arc<Self> {
        let parser = vt100::Parser::new(rows, cols, SCROLLBACK_LINES);
        Arc::new(Self {
            tab_id,
            session_id,
            cols: AtomicU16::new(cols),
            rows: AtomicU16::new(rows),
            term: "xterm-256color".into(),
            screen: Mutex::new(parser),
            transcoder: Mutex::new(Transcoder::new(encoding)),
            scrollback: Mutex::new(Scrollback::new(SCROLLBACK_BYTES)),
            sink: RwLock::new(None),
            visible: AtomicBool::new(true),
            inflight: Arc::new(AtomicUsize::new(0)),
            writer: RwLock::new(Arc::new(TerminalWriter::None)),
            last_output_at: AtomicU64::new(now_ms()),
            state: Mutex::new(TerminalState {
                cursor_visible: true,
                ..Default::default()
            }),
            recorder: tokio::sync::Mutex::new(None),
            stop: Arc::new(tokio_util::sync::CancellationToken::new()),
        })
    }

    pub fn cols(&self) -> u16 {
        self.cols.load(Ordering::Relaxed)
    }

    pub fn rows(&self) -> u16 {
        self.rows.load(Ordering::Relaxed)
    }

    /// 设置写入端（PTY 打开后调用）。
    pub async fn set_writer(&self, writer: TerminalWriter) {
        *self.writer.write().await = Arc::new(writer);
    }

    pub async fn writer_arc(&self) -> Arc<TerminalWriter> {
        Arc::clone(&*self.writer.read().await)
    }

    /// 前端按键 / AI 发送（§4.5）。
    pub async fn write(&self, data: &[u8]) -> crate::error::AppResult<()> {
        self.writer_arc().await.write(data).await
    }

    /// attach 前端通道：先回放已有 scrollback 尾部，再持续推送。
    pub async fn attach_frontend(&self, channel: Channel<Vec<u8>>, replay_bytes: usize) {
        let backlog = {
            let sb = self.scrollback.lock().unwrap_or_else(|e| e.into_inner());
            sb.dump(replay_bytes)
        };
        if !backlog.is_empty() {
            let _ = channel.send(backlog);
        }
        *self.sink.write().await = Some(channel);
    }

    pub async fn detach_frontend(&self) {
        *self.sink.write().await = None;
    }

    pub fn set_visible(&self, visible: bool) {
        self.visible.store(visible, Ordering::Relaxed);
    }

    pub fn is_visible(&self) -> bool {
        self.visible.load(Ordering::Relaxed)
    }

    /// 调整尺寸：内核状态机 + 写入端同步（§4.2 / §5.2）。
    pub async fn resize(&self, cols: u16, rows: u16) -> crate::error::AppResult<()> {
        if cols == 0 || rows == 0 || cols > 1024 || rows > 1024 {
            return Err(crate::error::AppError::param("非法终端尺寸"));
        }
        self.screen
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .screen_mut()
            .set_size(rows, cols);
        self.cols.store(cols, Ordering::Relaxed);
        self.rows.store(rows, Ordering::Relaxed);
        match &*self.writer_arc().await {
            TerminalWriter::Local(io) => {
                let master = io.master.lock().unwrap_or_else(|e| e.into_inner());
                let _ = master.resize(portable_pty::PtySize {
                    rows,
                    cols,
                    pixel_width: 0,
                    pixel_height: 0,
                });
            }
            TerminalWriter::Ssh(ch) => {
                let _ = ch
                    .window_change(u32::from(cols), u32::from(rows), 0, 0)
                    .await;
            }
            TerminalWriter::None => {}
        }
        Ok(())
    }

    /// 向引擎喂输出（泵调用；也用于重连横幅、WinRM 行模式注入）。
    /// 返回值：检测到 DSR(ESC[6n) 请求时应答的光标报告字节（写入 PTY）。
    /// ConPTY 会阻塞等待该应答，不回应则整个 PTY 输出停摆。
    pub fn feed_output(&self, raw: &[u8]) -> Option<Vec<u8>> {
        self.last_output_at.store(now_ms(), Ordering::Relaxed);
        self.scrollback
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(raw);
        let utf8 = {
            let mut tr = self.transcoder.lock().unwrap_or_else(|e| e.into_inner());
            tr.feed(raw)
        };
        let (alt, cursor_visible, app_cursor, bracketed) = {
            let mut screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            screen.process(&utf8);
            let s = screen.screen();
            (
                s.alternate_screen(),
                !s.hide_cursor(),
                s.application_cursor(),
                self.transcoder
                    .lock()
                    .unwrap_or_else(|e| e.into_inner())
                    .bracketed_paste(),
            )
        };
        *self.state.lock().unwrap_or_else(|e| e.into_inner()) = TerminalState {
            alt_screen: alt,
            cursor_visible,
            bracketed_paste: bracketed,
            application_cursor: app_cursor,
        };

        // DSR 应答：以内核状态机的真实光标位置回应（真实终端行为）
        if utf8.windows(4).any(|w| w == b"[6n") {
            let screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            let (row, col) = screen.screen().cursor_position();
            return Some(
                format!("[{};{}R", row.saturating_add(1), col.saturating_add(1)).into_bytes(),
            );
        }
        None
    }

    /// 运行中切换编码（§12.2）。
    pub fn switch_encoding(&self, encoding: TerminalEncoding) {
        self.transcoder
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .switch(encoding);
    }

    pub fn encoding(&self) -> TerminalEncoding {
        self.transcoder
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .encoding()
    }

    /// 录制开关。
    pub async fn start_recording(&self, path: std::path::PathBuf) -> crate::error::AppResult<()> {
        let file = tokio::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&path)
            .await?;
        *self.recorder.lock().await = Some(Recording { file, bytes: 0 });
        Ok(())
    }

    pub async fn stop_recording(&self) -> u64 {
        use tokio::io::AsyncWriteExt;
        let mut rec = self.recorder.lock().await;
        match rec.take() {
            Some(mut r) => {
                let _ = r.file.flush().await;
                r.bytes
            }
            None => 0,
        }
    }

    pub(crate) async fn record(&self, data: &[u8]) {
        let mut guard = self.recorder.lock().await;
        if let Some(rec) = guard.as_mut() {
            use tokio::io::AsyncWriteExt;
            if rec.file.write_all(data).await.is_ok() {
                rec.bytes += data.len() as u64;
            }
        }
    }

    pub async fn recording_bytes(&self) -> u64 {
        self.recorder
            .lock()
            .await
            .as_ref()
            .map(|r| r.bytes)
            .unwrap_or(0)
    }

    // ───────── 读屏 API（§4.3）：AI 接管的唯一入口 ─────────

    /// 屏幕当前可见内容（纯文本，无 ANSI 残留）。
    pub fn screen_text(&self) -> String {
        self.screen
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .screen()
            .contents()
    }

    pub fn snapshot(&self) -> ScreenSnapshot {
        let (text, rows, cols, cursor_row, cursor_col, alt) = {
            let screen = self.screen.lock().unwrap_or_else(|e| e.into_inner());
            let s = screen.screen();
            let (rows, cols) = s.size();
            let (cursor_row, cursor_col) = s.cursor_position();
            (
                s.contents(),
                rows,
                cols,
                cursor_row,
                cursor_col,
                s.alternate_screen(),
            )
        };
        ScreenSnapshot {
            lines: text.lines().map(|l| l.trim_end().to_string()).collect(),
            cursor_row,
            cursor_col,
            cols,
            rows,
            alt_screen: alt,
            last_output_ms_ago: now_ms()
                .saturating_sub(self.last_output_at.load(Ordering::Relaxed)),
            text,
        }
    }

    /// 空闲判定：最近 quiet_ms 内无输出。
    pub fn is_idle(&self, quiet_ms: u64) -> bool {
        now_ms().saturating_sub(self.last_output_at.load(Ordering::Relaxed)) >= quiet_ms
    }

    /// 最后 N 行（含滚动缓冲）。
    pub fn tail_lines(&self, n: usize) -> Vec<String> {
        let mut all: Vec<String> = Vec::new();
        {
            let sb = self.scrollback.lock().unwrap_or_else(|e| e.into_inner());
            let bytes = sb.dump(256 * 1024);
            let text = String::from_utf8_lossy(&bytes);
            all.extend(
                text.split('\n')
                    .map(|l| l.trim_end_matches('\r').to_string()),
            );
        }
        all.extend(self.snapshot().lines);
        if all.len() > n {
            all.split_off(all.len() - n)
        } else {
            all
        }
    }

    pub fn dump(&self, max_bytes: usize) -> Vec<u8> {
        self.scrollback
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .dump(max_bytes)
    }

    pub fn inflight(&self) -> usize {
        self.inflight.load(Ordering::Relaxed)
    }

    pub fn inflight_arc(&self) -> Arc<AtomicUsize> {
        Arc::clone(&self.inflight)
    }

    pub async fn send_to_frontend(&self, data: Vec<u8>) -> bool {
        let sink = self.sink.read().await;
        match &*sink {
            Some(ch) => ch.send(data).is_ok(),
            None => false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tab() -> Arc<TerminalTab> {
        TerminalTab::new_arc("t1".into(), "s1".into(), 80, 24, TerminalEncoding::Utf8)
    }

    #[test]
    fn screen_text_has_no_ansi_residue() {
        let t = tab();
        t.feed_output(b"\x1b[2J\x1b[H");
        t.feed_output(b"hello \x1b[31;1mworld\x1b[0m\r\n");
        t.feed_output(b"$ \x1b[?25lhidden");
        let text = t.screen_text();
        assert!(text.contains("hello world"), "文本: {text:?}");
        assert!(text.contains("$"), "提示符: {text:?}");
        assert!(!text.contains("\x1b["), "残留 ANSI: {text:?}");
    }

    #[test]
    fn cursor_position_reported() {
        let t = tab();
        t.feed_output(b"abc");
        let snap = t.snapshot();
        assert_eq!(snap.rows, 24);
        assert_eq!(snap.cols, 80);
        assert_eq!(snap.cursor_row, 0);
        assert_eq!(snap.cursor_col, 3);
    }

    #[test]
    fn alt_screen_detection() {
        let t = tab();
        t.feed_output(b"\x1b[?1049h"); // 进入备用屏幕（vim 类）
        assert!(t.snapshot().alt_screen);
        t.feed_output(b"\x1b[?1049l");
        assert!(!t.snapshot().alt_screen);
    }

    #[test]
    fn idle_detection() {
        let t = tab();
        t.feed_output(b"boom\r\n");
        std::thread::sleep(std::time::Duration::from_millis(5));
        assert!(!t.is_idle(5_000));
        assert!(t.is_idle(1));
    }

    #[test]
    fn tail_lines_with_scrollback() {
        let t = tab();
        for i in 0..100 {
            t.feed_output(format!("line-{i}\r\n").as_bytes());
        }
        let tail = t.tail_lines(10);
        assert_eq!(tail.len(), 10);
        assert!(tail.iter().any(|l| l.contains("line-99")));
    }

    #[test]
    fn high_volume_feed_memory_bounded() {
        // 喂 100MB 伪随机字节：滚动缓冲按容量截断，vt100 网格有界 → 内存有界
        let t = tab();
        let chunk: Vec<u8> = (0..65_536u32).map(|i| (i % 251) as u8 + 1).collect();
        for _ in 0..1600 {
            t.feed_output(&chunk);
        }
        assert!(t.dump(1024).len() <= 1024);
        let _ = t.screen_text();
    }

    #[test]
    fn resize_updates_state_machine() {
        let t = tab();
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            t.resize(120, 40).await.unwrap();
        });
        t.feed_output(b"x");
        let snap = t.snapshot();
        assert_eq!(snap.cols, 120);
        assert_eq!(snap.rows, 40);
    }
}
