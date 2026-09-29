//! 「改写一个已存在的文件」这条路径的端到端测试 —— 跑在**真的 OpenSSH
//! `sftp-server`** 上，用 socketpair 直连，不需要 sshd、不需要网络。
//!
//! 为什么值得单独拉一个文件：真机上（2026-09-30）AI 每次改远端已有文件都会得到
//!
//! ```text
//! write_file → 写入失败: SFTP 错误: No such file
//! ```
//!
//! 而**新建**文件永远成功。这条规律把范围锁死在 `backup && exists()` 那段分支里，
//! 但「为什么是 No such file 而不是别的」只有真服务器才答得出来 ——
//! OpenSSH 的 `errno_to_portable()` 把 `EBADF`（对只读 fd 写）归到
//! `SSH2_FX_NO_SUCH_FILE`，所以「拿只读句柄去写」会伪装成一个「文件不存在」。
//!
//! 用真服务器（而不是自己写个 mock handler）的理由就在这里：mock 会照着我的
//! 理解编一个错误码，而我要验的恰恰是**真服务器的错误码**。
//!
//! 找不到 `sftp-server`（例如 Windows、精简容器）时**跳过**，与 `ssh_e2e.rs` 同风格。

#![cfg(unix)]

use std::os::fd::OwnedFd;
use std::os::unix::net::UnixStream as StdUnixStream;
use std::path::Path;
use std::process::Stdio;
use std::sync::Arc;

use nexterm_lib::transport::ssh::SftpFs;
use nexterm_lib::transport::FileSystem;

use tokio::process::{Child, Command};

/// 起一个本地 sftp-server，返回（客户端会话，子进程句柄）。
/// 子进程 handle 必须被持有 —— 掉了就等于把服务器关了，测试会以「流中断」失败，
/// 看起来像是产品 bug 而其实是夹具生命周期。
async fn local_sftp() -> Option<(russh_sftp::client::SftpSession, Child)> {
    let bin = ["/usr/libexec/sftp-server", "/usr/lib/openssh/sftp-server"]
        .into_iter()
        .find(|p| Path::new(p).exists())?;

    // socketpair 的两端：一端归客户端，一端同时当子进程的 stdin 与 stdout。
    // 不能用管道对 —— fork 出来的 stdin/stdout 是两个 fd，而 SFTP 需要
    // **同一个双向流**（服务器的应答得回到它自己读的那个连接上）。
    let (client, server) = StdUnixStream::pair().ok()?;
    client.set_nonblocking(true).ok()?;
    let client = tokio::net::UnixStream::from_std(client).ok()?;

    let child = Command::new(bin)
        .stdin(Stdio::from(OwnedFd::from(server.try_clone().ok()?)))
        .stdout(Stdio::from(OwnedFd::from(server)))
        .kill_on_drop(true)
        .spawn()
        .ok()?;

    let session = russh_sftp::client::SftpSession::new(client).await.ok()?;
    Some((session, child))
}

#[tokio::test]
async fn writing_an_existing_file_keeps_a_faithful_backup() {
    let Some((session, _child)) = local_sftp().await else {
        eprintln!("[skip] 本机没有 OpenSSH sftp-server，跳过");
        return;
    };
    let dir = tempfile::tempdir().expect("tempdir");
    let fs = SftpFs {
        sftp: Arc::new(session),
    };

    // 用中文是有意的：这条路径上的字节数 ≠ 字符数，顺手守住「不能按字符截断」。
    let old: &[u8] = "v1: 原内容\n".as_bytes();
    let new: &[u8] = "v2: 改写后的内容\n".as_bytes();

    // ── ① 目标已存在：走备份分支。真机就是这条一直失败。
    let path = dir.path().join("Helloworld");
    std::fs::write(&path, old).expect("放一份初始文件");
    let p = path.to_str().unwrap();

    fs.write_file(p, new, true).await.expect(
        "改写**已存在**的文件必须成功 —— 真机这里报的是 \
         「SFTP 错误: No such file」（对只读句柄写 → EBADF → OpenSSH 把它映射成 NO_SUCH_FILE）",
    );
    assert_eq!(std::fs::read(&path).unwrap(), new, "新内容要落盘");

    let bak = format!("{p}.nexterm-bak");
    let bak = Path::new(&bak);
    assert!(
        bak.exists(),
        "既然说了「保存前自动备份」，备份文件就得真的存在"
    );
    assert_eq!(
        std::fs::read(bak).unwrap(),
        old,
        "备份必须是**原内容**。空的备份比没有备份更坏 —— 用户以为有退路，其实没有"
    );

    // ── ② 目标不存在：不该被备份分支拦住（这条真机本来是好的，一起守住，
    //     免得修 ① 的时候把新建路径改坏）。
    let fresh = dir.path().join("Fresh");
    fs.write_file(fresh.to_str().unwrap(), new, true)
        .await
        .expect("新建文件必须成功");
    assert_eq!(std::fs::read(&fresh).unwrap(), new);
    assert!(
        !Path::new(&format!("{}.nexterm-bak", fresh.to_str().unwrap())).exists(),
        "新建时没有原文件可备份，不该凭空造一个空备份出来"
    );
}
