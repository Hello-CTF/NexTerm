//! 磁盘挂载（M1-T8 / §5.5）：
//! - Windows 目标：SMB 映射盘（`net use`）；
//! - Linux 目标：sshfs。
//!
//! 铁律（吸收 RainsIR 教训）：**挂载列表以本机真实状态为准** ——
//! 扫描系统现有映射（含其他进程挂的），盘符被重挂后旧记录自动消失。

use serde::Serialize;

use crate::error::{AppError, AppResult};

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
        run_local("sh", &["-c", "mount | grep fuse.sshfs || true"])?
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
        if let Some(idx) = line.find(" on ") {
            let remote = line[..idx].to_string();
            let rest = &line[idx + 4..];
            if let Some(sp) = rest.find(" type fuse.sshfs") {
                entries.push(MountEntry {
                    id: String::new(),
                    local_point: rest[..sp].to_string(),
                    remote,
                    session_id: None,
                    created_at: None,
                });
            }
        }
    }
    entries
}

#[cfg(windows)]
/// Windows：映射网络盘。
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
    if cfg!(windows) {
        mount_windows(local_point, remote, username, password).await
    } else {
        // sshfs user@host:/path /mnt/point -o reconnect
        tokio::process::Command::new("sshfs")
            .args([
                remote,
                local_point,
                "-o",
                "reconnect",
                "-o",
                "ServerAliveInterval=30",
            ])
            .spawn()?
            .wait()
            .await?;
        Ok(())
    }
}

/// 卸载。
pub async fn unmount(local_point: &str) -> AppResult<()> {
    if cfg!(windows) {
        unmount_windows(local_point).await
    } else {
        let out = tokio::process::Command::new("fusermount")
            .args(["-u", local_point])
            .output()
            .await
            .map_err(AppError::Io)?;
        if !out.status.success() {
            return Err(AppError::Internal("fusermount 失败".into()));
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
    fn parse_empty() {
        assert!(parse_mount_output("").is_empty());
        assert!(parse_mount_output("没有找到网络连接。").is_empty());
    }
}
