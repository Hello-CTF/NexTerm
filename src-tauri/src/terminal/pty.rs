//! PtyPump（§4.1）：每个会话一个泵任务，把 PTY 字节流同时喂给
//! ① 内核 vt100 状态机 ② 环形缓冲 ③ 前端通道。
//!
//! 背压（§4.4）：
//! - 本地读取走「阻塞线程 + 有界通道(64×64KB)」，泵停则读停，端到端反压；
//! - inflight 超过 [`super::INFLIGHT_PAUSE`] 暂停消费并广播 throttled 事件；
//! - 标签不可见时按 [`super::HIDDEN_FLUSH_MS`] 批量推送，降 IPC 频率。

use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::{Duration, Instant};

use russh::ChannelMsg;
use tokio::sync::mpsc;

use super::{TabCallbacks, TerminalTab};

const READ_CHUNK: usize = 64 * 1024;
const QUEUE_MESSAGES: usize = 64;
/// 不可见标签的批量上限，超过立即刷。
const HIDDEN_BATCH_MAX: usize = 1024 * 1024;

/// 字节源：本地 PTY（阻塞 Read）或 SSH channel。
pub enum ByteSource {
    Local(Box<dyn std::io::Read + Send>),
    Ssh(russh::ChannelReadHalf),
}

/// 本地 PTY 退出监听：轮询子进程状态，退出后回调 exit。
pub struct LocalExitWatcher {
    pub child: Arc<std::sync::Mutex<Box<dyn portable_pty::Child + Send + Sync>>>,
}

impl LocalExitWatcher {
    pub fn spawn(
        self,
        tab_id: String,
        callbacks: Arc<dyn TabCallbacks>,
        stop: Arc<tokio_util::sync::CancellationToken>,
    ) {
        std::thread::spawn(move || loop {
            if stop.is_cancelled() {
                return;
            }
            {
                let mut child = self.child.lock().unwrap_or_else(|e| e.into_inner());
                match child.try_wait() {
                    Ok(Some(status)) => {
                        let code = if status.success() {
                            0
                        } else {
                            status.exit_code() as i32
                        };
                        callbacks.exit(&tab_id, Some(code));
                        return;
                    }
                    Ok(None) => {}
                    Err(_) => {
                        callbacks.exit(&tab_id, None);
                        return;
                    }
                }
            }
            std::thread::sleep(Duration::from_millis(500));
        });
    }
}

/// 启动泵。泵在后台运行，直到 stop 取消 / EOF / Exit。
pub async fn run_pump(tab: Arc<TerminalTab>, source: ByteSource, callbacks: Arc<dyn TabCallbacks>) {
    match source {
        ByteSource::Local(reader) => run_local_pump(tab, reader, callbacks).await,
        ByteSource::Ssh(channel) => run_ssh_pump(tab, channel, callbacks).await,
    }
}

async fn run_local_pump(
    tab: Arc<TerminalTab>,
    mut reader: Box<dyn std::io::Read + Send>,
    callbacks: Arc<dyn TabCallbacks>,
) {
    let (tx, mut rx) = mpsc::channel::<Vec<u8>>(QUEUE_MESSAGES);
    let inflight = tab.inflight_arc();
    // 阻塞读线程：读 → 计 inflight → blocking_send（队列满则阻塞，形成反压）
    std::thread::spawn(move || {
        let mut buf = vec![0u8; READ_CHUNK];
        loop {
            match reader.read(&mut buf) {
                Ok(0) => {
                    let _ = tx.blocking_send(Vec::new());
                    break;
                }
                Ok(n) => {
                    inflight.fetch_add(n, Ordering::Relaxed);
                    if tx.blocking_send(buf[..n].to_vec()).is_err() {
                        break;
                    }
                }
                Err(e) => {
                    tracing::debug!(target: "pty", error = %e, "本地 PTY 读结束");
                    let _ = tx.blocking_send(Vec::new());
                    break;
                }
            }
        }
    });
    consume(tab, &mut rx, callbacks).await;
}

async fn run_ssh_pump(
    tab: Arc<TerminalTab>,
    mut channel: russh::ChannelReadHalf,
    callbacks: Arc<dyn TabCallbacks>,
) {
    let mut exit_code: Option<i32> = None;
    let mut pending: Vec<u8> = Vec::new();
    let mut flush_deadline: Option<Instant> = None;

    loop {
        flush_if_due(&tab, &mut pending, &mut flush_deadline).await;
        // 背压暂停（SSH 通道无界缓冲，需主动限速）
        while tab.inflight() > super::INFLIGHT_PAUSE {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        tokio::select! {
            _ = tab.stop.cancelled() => break,
            msg = channel.wait() => {
                match msg {
                    Some(ChannelMsg::Data { data }) => {
                        let chunk = data.to_vec();
                        on_chunk(&tab, chunk, &mut pending, &mut flush_deadline).await;
                    }
                    Some(ChannelMsg::ExtendedData { data, .. }) => {
                        let chunk = data.to_vec();
                        on_chunk(&tab, chunk, &mut pending, &mut flush_deadline).await;
                    }
                    Some(ChannelMsg::ExitStatus { exit_status: c }) => {
                        exit_code = Some(c as i32);
                    }
                    // Eof 后仍可能有 ExitStatus；Close 才真正结束
                    Some(ChannelMsg::Eof) => {}
                    Some(ChannelMsg::Close) => break,
                    None => break,
                    _ => {}
                }
            }
        }
    }
    flush_if_due(&tab, &mut pending, &mut flush_deadline).await;
    if !pending.is_empty() {
        let _ = tab.send_to_frontend(pending).await;
    }
    callbacks.exit(&tab.tab_id, exit_code);
}

/// 本地泵的统一消费循环：可见即推、不可见批量、背压暂停。
async fn consume(
    tab: Arc<TerminalTab>,
    rx: &mut mpsc::Receiver<Vec<u8>>,
    callbacks: Arc<dyn TabCallbacks>,
) {
    let mut pending: Vec<u8> = Vec::new();
    let mut flush_deadline: Option<Instant> = None;
    let mut throttled_announced = false;

    loop {
        // 背压：未送达字节超阈值 → 暂停消费，通知前端
        while tab.inflight() > super::INFLIGHT_PAUSE {
            if !throttled_announced {
                callbacks.throttled(&tab.tab_id, tab.inflight());
                throttled_announced = true;
                tracing::warn!(target: "pty", tab = %tab.tab_id, inflight = tab.inflight(), "触发背压，暂停读取");
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        if tab.inflight() <= super::INFLIGHT_PAUSE / 4 {
            throttled_announced = false;
        }

        flush_if_due(&tab, &mut pending, &mut flush_deadline).await;

        tokio::select! {
            _ = tab.stop.cancelled() => break,
            chunk = rx.recv() => {
                match chunk {
                    None => break, // 队列关闭
                    Some(c) if c.is_empty() => break, // EOF 标记
                    Some(c) => on_chunk(&tab, c, &mut pending, &mut flush_deadline).await,
                }
            }
            _ = hidden_timer(flush_deadline), if !pending.is_empty() && !tab.is_visible() => {
                // 到期刷写由 flush_if_due 完成，这里仅唤醒
            }
        }
    }

    // 收尾：刷掉未发的批量
    if !pending.is_empty() {
        let _ = tab.send_to_frontend(pending).await;
    }
}

async fn on_chunk(
    tab: &Arc<TerminalTab>,
    chunk: Vec<u8>,
    pending: &mut Vec<u8>,
    flush_deadline: &mut Option<Instant>,
) {
    if let Some(dsr) = tab.feed_output(&chunk) {
        // ConPTY 阻塞等待光标报告，必须立刻回写 PTY
        let _ = tab.write(&dsr).await;
    }
    tab.record(&chunk).await;
    if tab.is_visible() {
        let size = chunk.len();
        let _ = tab.send_to_frontend(chunk).await;
        saturating_sub(tab, size);
    } else {
        pending.extend_from_slice(&chunk);
        flush_deadline
            .get_or_insert_with(|| Instant::now() + Duration::from_millis(super::HIDDEN_FLUSH_MS));
        if pending.len() > HIDDEN_BATCH_MAX {
            flush_pending(tab, pending, flush_deadline).await;
        }
    }
}

async fn flush_if_due(
    tab: &Arc<TerminalTab>,
    pending: &mut Vec<u8>,
    flush_deadline: &mut Option<Instant>,
) {
    let due = !pending.is_empty()
        && (tab.is_visible()
            || pending.len() > HIDDEN_BATCH_MAX
            || flush_deadline.map(|d| d <= Instant::now()).unwrap_or(false));
    if due {
        flush_pending(tab, pending, flush_deadline).await;
    }
}

async fn flush_pending(
    tab: &Arc<TerminalTab>,
    pending: &mut Vec<u8>,
    flush_deadline: &mut Option<Instant>,
) {
    let batch = std::mem::take(pending);
    let size = batch.len();
    let _ = tab.send_to_frontend(batch).await;
    saturating_sub(tab, size);
    *flush_deadline = None;
}

/// inflight 饱和递减：SSH 泵没有生产者递增，防止下溢成巨大值卡死背压。
fn saturating_sub(tab: &TerminalTab, size: usize) {
    let cur = tab.inflight();
    tab.inflight_arc()
        .store(cur.saturating_sub(size), Ordering::Relaxed);
}

async fn hidden_timer(deadline: Option<Instant>) {
    match deadline {
        Some(d) => {
            let until = tokio::time::Instant::from_std(d);
            tokio::time::sleep_until(until).await;
        }
        None => tokio::time::sleep(Duration::from_secs(3600)).await,
    }
}
