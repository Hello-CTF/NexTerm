//! 磁盘挂载（M1-T8 / §5.5）：
//! - Windows 目标：SMB 映射盘（`net use`）；
//! - Linux 目标：sshfs，卸载走 `fusermount`；
//! - **macOS：暂不可用** —— 见 [`unavailable_reason`]。原计划的 sshfs + macFUSE
//!   链路需要内核扩展授权与重启，本轮 macOS 适配未覆盖，因此明确标记为不可用，
//!   避免留一个「点了必失败」的入口。
//!
//! 铁律（吸收 RainsIR 教训）：**挂载列表以本机真实状态为准** ——
//! 扫描系统现有映射（含其他进程挂的），盘符被重挂后旧记录自动消失。

use serde::Serialize;

use crate::error::{AppError, AppResult};

/// 本平台是否暂不支持磁盘挂载；`Some(原因)` 即不可用。
///
/// 唯一事实来源：命令层与 UI 都从这里派生，不再各自判断平台。
/// `scan()` 不受影响（只读扫描真实挂载表，无副作用），
/// 将来适配完成只需摘掉这里的 macOS 分支。
pub fn unavailable_reason() -> Option<&'static str> {
    if cfg!(target_os = "macos") {
        Some("磁盘挂载在 macOS 上暂不可用：依赖 macFUSE（系统扩展）+ sshfs，尚未完成适配")
    } else {
        None
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MountEntry {
    pub id: String,
    /// Z: 或 /mnt/xxx
    pub local_point: String,
    /// \\host\share 或 user@host:/path
    pub remote: String,
    /// 本进程创建的记录才有 session_id。
    pub session_id: Option<String>,
    pub created_at: Option<u64>,
}

fn run_local(cmd: &str, args: &[&str]) -> AppResult<String> {
    let out = std::process::Command::new(cmd)
        .args(args)
        .output()
        .map_err(AppError::Io)?;
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// 扫描本机真实挂载状态。
pub async fn scan() -> AppResult<Vec<MountEntry>> {
    let out = if cfg!(windows) {
        // net use 输出形态：
        // Z:      \\\\host\\share   Microsoft Windows Network
        run_local("net", &["use"])?
    } else {
        // Linux: `... type fuse.sshfs`；macOS: `... (osxfuse,...)` / `(macfuse,...)`
        run_local(
            "sh",
            &[
                "-c",
                "mount | grep -iE 'fuse.sshfs|osxfuse|macfuse' || true",
            ],
        )?
    };
    Ok(parse_mount_output(&out))
}

fn parse_mount_output(out: &str) -> Vec<MountEntry> {
    let mut entries = Vec::new();
    for line in out.lines() {
        let line = line.trim();
        if line.is_empty()
            || line.starts_with("New connections")
            || line.contains("命令")
            || line.contains("---")
        {
            continue;
        }
        // Windows 形如 "OK  Z:  \\host\\share  ..." —— 找形如 X: 的盘符 + 紧随的 UNC
        {
            let tokens: Vec<&str> = line.split_whitespace().collect();
            let mut i = 0;
            while i < tokens.len() {
                let tok = tokens[i];
                if tok.len() == 2 && tok.ends_with(':') {
                    if let Some(remote) = tokens.get(i + 1) {
                        if remote.starts_with("\\\\") {
                            entries.push(MountEntry {
                                id: String::new(),
                                local_point: tok.to_string(),
                                remote: remote.to_string(),
                                session_id: None,
                                created_at: None,
                            });
                        }
                    }
                }
                i += 1;
            }
        }
        // Linux 形如 user@host:/path on /mnt/point type fuse.sshfs (...)
        // macOS 形如 user@host:/path on /mnt/point (osxfuse,...) / (macfuse,...)
        if let Some(idx) = line.find(" on ") {
            let remote = line[..idx].to_string();
            let rest = &line[idx + 4..];
            let local_point = rest
                .find(" type fuse.sshfs")
                .map(|sp| rest[..sp].to_string())
                .or_else(|| {
                    let lower = rest.to_ascii_lowercase();
                    if lower.contains("osxfuse") || lower.contains("macfuse") {
                        rest.split(" (").next().map(|s| s.trim().to_string())
                    } else {
                        None
                    }
                });
            if let Some(local_point) = local_point {
                entries.push(MountEntry {
                    id: String::new(),
                    local_point,
                    remote,
                    session_id: None,
                    created_at: None,
                });
            }
        }
    }
    entries
}

/// Windows：映射网络盘。非 Windows 平台也能编译（运行时不会走到），
/// 保持无门控以便上面 `cfg!(windows)` 的运行时分支在所有平台通过编译检查。
async fn mount_windows(
    local_point: &str,
    remote: &str,
    username: Option<&str>,
    password: Option<&str>,
) -> AppResult<()> {
    let mut args = vec!["use", local_point, remote, "/persistent:yes"];
    let user_pass;
    if let (Some(u), Some(p)) = (username, password) {
        user_pass = format!("/user:{u} {p}");
        args.push(&user_pass);
    }
    let mut cmd = tokio::process::Command::new("net");
    cmd.args(&args);
    #[cfg(windows)]
    cmd.creation_flags(0x08000000); // CREATE_NO_WINDOW
    let out = cmd.output().await.map_err(AppError::Io)?;
    if !out.status.success() {
        return Err(AppError::Internal(format!(
            "net use 失败: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    Ok(())
}

async fn unmount_windows(local_point: &str) -> AppResult<()> {
    let mut cmd = tokio::process::Command::new("net");
    cmd.args(["use", local_point, "/delete", "/y"]);
    #[cfg(windows)]
    cmd.creation_flags(0x08000000);
    let out = cmd.output().await.map_err(AppError::Io)?;
    if !out.status.success() {
        return Err(AppError::Internal(format!(
            "断开失败: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    Ok(())
}

/// 挂载（由命令层带凭据调用）。
pub async fn mount(
    local_point: &str,
    remote: &str,
    username: Option<&str>,
    password: Option<&str>,
) -> AppResult<()> {
    if let Some(why) = unavailable_reason() {
        // 兜底防线：即使有人绕过 UI 直接 invoke 命令，也拿不到「假装挂上了」的结果。
        return Err(AppError::Unsupported(why.to_string()));
    }
    if cfg!(windows) {
        mount_windows(local_point, remote, username, password).await
    } else {
        // Linux：sshfs user@host:/path /mnt/point -o reconnect
        let out = tokio::process::Command::new("sshfs")
            .args([
                remote,
                local_point,
                "-o",
                "reconnect",
                "-o",
                "ServerAliveInterval=30",
            ])
            .output()
            .await
            .map_err(|e| {
                // sshfs 没装时 `Command::new` 给的是裸 IO 错误（"No such file or directory"），
                // 用户看不出缺的是什么。Linux 这条路径的唯一前置依赖就是 sshfs。
                if e.kind() == std::io::ErrorKind::NotFound {
                    AppError::Unsupported(
                        "未找到 sshfs：请先安装（Debian/Ubuntu: apt install sshfs）".into(),
                    )
                } else {
                    AppError::Io(e)
                }
            })?;
        if !out.status.success() {
            return Err(AppError::Internal(format!(
                "sshfs 失败: {}{}",
                String::from_utf8_lossy(&out.stderr).trim(),
                String::from_utf8_lossy(&out.stdout).trim()
            )));
        }
        Ok(())
    }
}

/// 卸载。
///
/// macOS 分支已随「暂不可用」一并摘除（原实现走系统 `umount`）；
/// 恢复适配时把该分支加回来即可。
pub async fn unmount(local_point: &str) -> AppResult<()> {
    if let Some(why) = unavailable_reason() {
        return Err(AppError::Unsupported(why.to_string()));
    }
    if cfg!(windows) {
        unmount_windows(local_point).await
    } else {
        let out = tokio::process::Command::new("fusermount")
            .args(["-u", local_point])
            .output()
            .await
            .map_err(AppError::Io)?;
        if !out.status.success() {
            return Err(AppError::Internal(format!(
                "fusermount 失败: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            )));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_windows_net_use() {
        let sample = "\nNew connections will be remembered.\n\n\nStatus       Local     Remote                    Network\n\n-------------------------------------------------------------------------------\nOK           Z:        \\\\\\\\host\\\\share      Microsoft Windows Network\nThe command completed successfully.\n";
        let entries = parse_mount_output(sample);
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].local_point, "Z:");
        assert!(entries[0].remote.starts_with("\\\\"));
    }

    #[test]
    fn parse_linux_mount() {
        let sample = "deploy@web-01:/data on /mnt/webdata type fuse.sshfs (rw,nosuid,nodefs)";
        let entries = parse_mount_output(sample);
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].local_point, "/mnt/webdata");
        assert_eq!(entries[0].remote, "deploy@web-01:/data");
    }

    #[test]
    fn parse_macos_mount() {
        // macFUSE 新版标 macfuse，旧版标 osxfuse，两种都要认
        let sample = "deploy@web-01:/data on /Users/ops/mnt/webdata (macfuse, nodev, nosuid, fsname=sshfs@web-01:/data)";
        let entries = parse_mount_output(sample);
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].local_point, "/Users/ops/mnt/webdata");
        assert_eq!(entries[0].remote, "deploy@web-01:/data");

        let sample_old = "deploy@web-01:/data on /mnt/x (osxfuse, allow_other)";
        let entries = parse_mount_output(sample_old);
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].local_point, "/mnt/x");
    }

    #[test]
    fn parse_rejects_unrelated_fuse() {
        // 非 sshfs 的 fuse 挂载不应混进列表（remote 里碰巧带 sshfs 字样也不行）
        let sample = "encfs on /mnt/vault type fuse.encfs (rw)";
        assert!(parse_mount_output(sample).is_empty());
    }

    #[test]
    fn parse_empty() {
        assert!(parse_mount_output("").is_empty());
        assert!(parse_mount_output("没有找到网络连接。").is_empty());
    }

    /// macOS：磁盘挂载被**显式**标记为暂不可用 —— UI 与后端拿到同一个理由，
    /// 且走命令层时是 `unsupported`（可读），不是被吞掉的 `No such file or directory`。
    #[cfg(target_os = "macos")]
    #[tokio::test]
    async fn mac_mount_is_explicitly_unavailable() {
        let why = unavailable_reason().expect("macOS 必须给出不可用原因");
        assert!(why.contains("macFUSE"), "原因要点名缺失依赖：{why}");
        assert!(why.contains("暂不可用"), "标记文案要明确：{why}");

        let e = mount("/tmp/nx-mnt", "user@example.com:/", None, None)
            .await
            .expect_err("macOS 上 mount 必须失败");
        assert_eq!(e.code(), "unsupported");
        assert!(e.to_string().contains("macFUSE"));

        let e2 = unmount("/tmp/nx-mnt")
            .await
            .expect_err("macOS 上 unmount 必须失败");
        assert_eq!(e2.code(), "unsupported");
    }

    #[cfg(not(target_os = "macos"))]
    #[test]
    fn non_mac_mount_stays_available() {
        assert!(unavailable_reason().is_none());
    }
}
