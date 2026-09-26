//! M0-T6 吞吐基准：衡量终端引擎（vt100 状态机 + 环形缓冲 + 转码）真实吞吐。
//! 运行：cargo bench -p nexterm --bench terminal_throughput
//! 结果记录：docs/bench-m0.md

use std::hint::black_box;
use std::time::Instant;

use nexterm_lib::terminal::transcoder::TerminalEncoding;
use nexterm_lib::terminal::TerminalTab;

/// 生成贴近真实终端输出的字节流（大量文本行 + 定期光标移动/清屏）。
fn build_payload(bytes: usize) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes);
    let mut seed = 0x243F6A8885A308D3u64;
    while out.len() < bytes {
        seed = seed
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        let line_len = (seed >> 33) as usize % 96 + 16;
        let mut line = String::with_capacity(line_len + 1);
        for i in 0..line_len {
            let b = b'a' + ((seed >> (i % 32)) & 15) as u8;
            line.push(b as char);
        }
        line.push('\r');
        line.push('\n');
        out.extend_from_slice(line.as_bytes());
        if out.len() % (256 * 1024) < line_len + 2 {
            // 模拟全屏应用刷新
            out.extend_from_slice(b"\x1b[2J\x1b[H");
        }
    }
    out.truncate(bytes);
    out
}

fn main() {
    const TOTAL: usize = 100 * 1024 * 1024; // 100 MB（对齐 M0-T4 内存验收）
    const CHUNK: usize = 64 * 1024;
    let payload = build_payload(CHUNK);

    let tab = TerminalTab::new_arc(
        "bench".into(),
        "bench-session".into(),
        120,
        40,
        TerminalEncoding::Utf8,
    );
    tab.set_visible(false); // 模拟后台标签的最坏路径（无前端消费）

    let start = Instant::now();
    let mut fed = 0usize;
    while fed < TOTAL {
        let n = CHUNK.min(TOTAL - fed);
        tab.feed_output(&payload[..n]);
        fed += n;
    }
    let elapsed = start.elapsed();
    let mb = fed as f64 / (1024.0 * 1024.0);
    let secs = elapsed.as_secs_f64();
    println!(
        "terminal-engine throughput: {mb:.0} MB in {secs:.3}s => {:.1} MB/s",
        mb / secs
    );

    // 引擎输出合法性抽查
    let snap = tab.snapshot();
    println!(
        "screen after feed: {}x{}, lines_len={}",
        snap.cols,
        snap.rows,
        snap.lines.len()
    );
    assert!(!snap.text.contains("\x1b["), "ANSI 残留");

    // 内存有界性：环形缓冲只保留尾部
    let dump = tab.dump(usize::MAX);
    println!("scrollback retained: {} bytes", dump.len());
    black_box(dump);

    // 多字节 + 转码路径（GBK）
    let tab_gbk = TerminalTab::new_arc(
        "bench-gbk".into(),
        "bench-session".into(),
        120,
        40,
        TerminalEncoding::Gbk,
    );
    let gbk: Vec<u8> = payload.iter().copied().take(CHUNK).collect();
    let start_gbk = Instant::now();
    let rounds = TOTAL / CHUNK;
    for _ in 0..rounds {
        tab_gbk.feed_output(black_box(&gbk));
    }
    let gbk_secs = start_gbk.elapsed().as_secs_f64();
    println!("gbk transcode throughput: {:.1} MB/s", mb / gbk_secs);
}
