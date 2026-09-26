//! SSH 靶机端到端测试（真实 PTY 管线）：握手 → 主机密钥 → 密钥认证 →
//! PTY → 泵 → vt100 读屏 → 写入回显。
//!
//! 默认跳过（无靶机/CI 无网络时不应挂）。运行前必须同时提供靶机信息，
//! 否则本测试直接跳过 —— 不在代码里留任何真实主机默认值：
//!
//! ```text
//! NEXTERM_SSH_E2E=1 \
//! NEXTERM_SSH_HOST=<ip> NEXTERM_SSH_USER=<user> NEXTERM_SSH_KEY=<私钥路径> \
//!   cargo test -p nexterm --test ssh_e2e
//! ```
//! `NEXTERM_SSH_PORT` 可选，默认 22。

use std::sync::Arc;
use std::time::Duration;

use nexterm_lib::terminal::pty::{run_pump, ByteSource};
use nexterm_lib::terminal::transcoder::TerminalEncoding;
use nexterm_lib::terminal::{NoopCallbacks, TerminalTab, TerminalWriter};
use nexterm_lib::transport::ssh::{SshAuth, SshConnectParams, SshTransport};
use nexterm_lib::transport::{PtyHandle, Transport};

/// 读取必填环境变量；缺失时返回 None 并打印跳过原因。
fn required(name: &str) -> Option<String> {
    match std::env::var(name) {
        Ok(v) if !v.trim().is_empty() => Some(v),
        _ => {
            eprintln!("[skip] 未设置 {name}，跳过 SSH 实机测试");
            None
        }
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn ssh_pty_pipeline_on_linux_target() {
    if std::env::var("NEXTERM_SSH_E2E").ok().as_deref() != Some("1") {
        eprintln!("[skip] 未设置 NEXTERM_SSH_E2E=1，跳过 SSH 实机测试");
        return;
    }
    let (Some(host), Some(username), Some(key_path)) = (
        required("NEXTERM_SSH_HOST"),
        required("NEXTERM_SSH_USER"),
        required("NEXTERM_SSH_KEY"),
    ) else {
        return;
    };
    let port: u16 = std::env::var("NEXTERM_SSH_PORT")
        .unwrap_or_default()
        .parse()
        .unwrap_or(22);

    // 首次连接允许未知主机（等价 UI 首连确认后 autoAcceptUnknownHost=true）
    let params = SshConnectParams {
        host,
        port,
        username,
        auth: SshAuth::Key {
            path: key_path,
            passphrase: None,
        },
        proxy: None,
        connect_timeout: Duration::from_secs(10),
        auto_accept_unknown: true,
    };
    let store = nexterm_lib::store::Store::open_in_memory()
        .await
        .expect("store");
    let ssh = SshTransport::connect(params, Arc::new(store), "e2e".into())
        .await
        .expect("SSH 连接失败");

    // ping：exec 通道
    let exec = ssh
        .exec("echo PING_OK", Duration::from_secs(10))
        .await
        .expect("exec");
    assert!(
        exec.stdout.contains("PING_OK"),
        "exec 输出异常: {:?}",
        exec.stdout
    );
    assert_eq!(exec.exit_code, Some(0));

    // 交互式 PTY：提示符 + 回显
    let handle = ssh.open_pty(120, 30).await.expect("open_pty");
    let PtyHandle::Ssh { read, write } = handle else {
        panic!("应为 Ssh 句柄");
    };
    let tab = TerminalTab::new_arc(
        "ssh-e2e".into(),
        "e2e".into(),
        120,
        30,
        TerminalEncoding::Utf8,
    );
    tab.set_writer(TerminalWriter::Ssh(write)).await;
    let tab2 = Arc::clone(&tab);
    tokio::spawn(async move {
        run_pump(tab2, ByteSource::Ssh(read), Arc::new(NoopCallbacks)).await;
    });

    // 等提示符
    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut text = String::new();
    while tokio::time::Instant::now() < deadline {
        text = tab.screen_text();
        if text.contains('$') || text.contains('#') {
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    println!("screen = {text:?}");
    assert!(!text.trim().is_empty(), "PTY 无输出");
    assert!(!text.contains("\x1b["), "ANSI 残留");

    // 写入回显（部分 shell 在 banner 后需要一次回车才显示提示符）
    tab.write(b"\r".as_ref()).await.expect("write");
    tokio::time::sleep(Duration::from_millis(500)).await;
    tab.write(b"echo NEXTERM_SSH_E2E\r".as_ref())
        .await
        .expect("write");
    let deadline = tokio::time::Instant::now() + Duration::from_secs(12);
    loop {
        if tab.screen_text().contains("NEXTERM_SSH_E2E") {
            break;
        }
        if tokio::time::Instant::now() >= deadline {
            println!("tail = {:?}", tab.tail_lines(15));
            panic!("回显超时");
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    println!("SSH E2E 通过");
}
