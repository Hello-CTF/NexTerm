//! 本地 PTY 管线端到端验证（M0 出口条件的本地版，无需 SSH 靶机）：
//! ConPTY → 泵 → vt100 状态机 → screen_text 必须出现 shell 提示符且无 ANSI 残留。

use std::sync::Arc;
use std::time::Duration;

use nexterm_lib::terminal::pty::{run_pump, ByteSource};
use nexterm_lib::terminal::transcoder::TerminalEncoding;
use nexterm_lib::terminal::{TerminalTab, TerminalWriter};
use nexterm_lib::transport::local::LocalTransport;
use nexterm_lib::transport::{PtyHandle, Transport};

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn local_pty_pipeline_produces_screen_text() {
    let transport = LocalTransport::new();
    let handle = transport.open_pty(120, 30).await.expect("open_pty");
    let PtyHandle::Local {
        io,
        reader,
        child: _child,
        mut killer,
    } = handle
    else {
        panic!("应为 Local 句柄");
    };

    let tab = TerminalTab::new_arc(
        "test-tab".into(),
        "test-session".into(),
        120,
        30,
        TerminalEncoding::Utf8,
    );
    tab.set_writer(TerminalWriter::Local(io)).await;

    let tab2 = Arc::clone(&tab);
    tokio::spawn(async move {
        run_pump(
            tab2,
            ByteSource::Local(reader),
            Arc::new(nexterm_lib::terminal::NoopCallbacks),
        )
        .await;
    });

    // 等待 shell 提示符出现（最长 10 秒）
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    let mut text = String::new();
    while tokio::time::Instant::now() < deadline {
        text = tab.screen_text();
        if !text.trim().is_empty() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }

    println!("screen_text = {text:?}");
    assert!(!text.trim().is_empty(), "10 秒内未收到任何 shell 输出");
    assert!(!text.contains("\x1b["), "ANSI 残留: {text:?}");

    // 写入路径：发一个回显命令，屏幕应出现该命令文本
    tab.write(b"Write-Output NEXTerm_E2E\r".as_ref())
        .await
        .expect("写入");
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    let mut echoed = false;
    while tokio::time::Instant::now() < deadline {
        if tab.screen_text().contains("NEXTerm_E2E") {
            echoed = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    assert!(echoed, "写入→回显链路不通");

    // 收尾：关闭 ConPTY，让阻塞读线程退出，测试进程才能正常结束
    tab.stop.cancel();
    let _ = killer.kill();
    tokio::time::sleep(Duration::from_millis(300)).await;
}
