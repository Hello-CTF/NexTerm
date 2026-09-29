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

/// 回归：本地终端必须以 UTF-8 locale 起 shell —— 中文文件名不能变成问号。
///
/// 症状（macOS GUI 启动）：launchd 给 GUI 应用的环境里**没有** `LANG`/`LC_ALL`，
/// 从这里 fork 出的 shell 落在 C locale（`locale charmap` = US-ASCII）。
/// BSD `ls` 在 stdout 是 tty 时默认带 `-q`，会把每个非 ASCII **字节**换成 `?`：
/// `中文目录Ω` 是 14 字节，屏幕上就是 14 个问号。
///
/// 注意 `printf`/`cat` 这类纯字节透传的程序**完全不受影响**，所以这个 bug
/// 很容易被误判成「渲染/字体问题」而查错方向；判别点在于「工具是否按 locale
/// 判断字符可打印性」，以及「stdout 是不是 tty」（管道下 `ls` 不转写）。
///
/// 夹具文件名用中文、目录名用 ASCII：命令行里因此不出现中文，
/// 屏幕上只要出现中文就只可能来自 `ls` 的输出（否则命令回显会让断言假通过）。
#[cfg(unix)]
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn local_pty_keeps_utf8_filenames() {
    // 前置条件：本用例的目标现场是「宿主没有 locale」这一种（GUI 启动的形态）。
    // 宿主若已显式设了 locale，子进程该用什么由用户的选择决定，不是本用例判得了的
    // —— 跳过并说明原因（`cargo test -- --nocapture` 可见），不做静默假绿。
    let host_locale: Vec<String> = ["LC_ALL", "LC_CTYPE", "LANG"]
        .iter()
        .filter_map(|k| std::env::var(k).ok())
        .filter(|v| !v.trim().is_empty())
        .collect();
    if !host_locale.is_empty() {
        eprintln!("[skip] 宿主已有 locale {host_locale:?}，本用例只在无 locale 时判得动");
        return;
    }

    const CN_NAME: &str = "中文文件Ω.txt";
    let dir = std::env::temp_dir().join("nexterm-pty-utf8-regression");
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("建夹具目录");
    std::fs::write(dir.join(CN_NAME), b"x").expect("建夹具文件");
    let cmd_line = format!("ls -1 '{}'\r", dir.display());
    assert!(
        cmd_line.is_ascii(),
        "命令行必须全 ASCII，否则命令回显会让断言假通过"
    );

    let handle = LocalTransport::new()
        .open_pty(120, 30)
        .await
        .expect("open_pty");

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
        "utf8-tab".into(),
        "utf8-session".into(),
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

    // 等提示符（shell 起来）再发命令
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    while tokio::time::Instant::now() < deadline && tab.screen_text().trim().is_empty() {
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    tab.write(cmd_line.as_bytes()).await.expect("写入");

    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    let mut ok = false;
    while tokio::time::Instant::now() < deadline {
        if tab.screen_text().contains(CN_NAME) {
            ok = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    let text = tab.screen_text();

    tab.stop.cancel();
    let _ = killer.kill();
    tokio::time::sleep(Duration::from_millis(300)).await;
    let _ = std::fs::remove_dir_all(&dir);

    assert!(
        ok,
        "中文文件名没出现在终端里 —— 子 shell 大概又落回非 UTF-8 locale 了。\n\
         屏幕内容: {text:?}"
    );
}
