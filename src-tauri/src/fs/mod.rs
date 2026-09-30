//! 文件传输（M1-T6）：上传 / 下载 + 进度事件 + 断点续传。
pub mod mount;

use std::time::Duration;

use serde_json::json;

use crate::error::{AppError, AppResult};
use crate::ids::new_id;
use crate::ipc_shim as tauri;
use crate::state::AppState;

/// 远端打包 / 解压的超时：大目录能跑到分钟级，给足 10 分钟。
const EXEC_TIMEOUT: Duration = Duration::from_secs(600);

/// 上传本地文件 → 远端路径（200ms 间隔推 fs://progress）。
pub async fn upload(
    state: &AppState,
    session_id: &str,
    local_path: String,
    remote_path: String,
    resume: bool,
) -> AppResult<u64> {
    let session = state.sessions.get(session_id).await?;
    let fs = session.transport().await.fs().await?;
    let total = tokio::fs::metadata(&local_path).await?.len();
    let task_id = new_id();

    // 断点续传：远端已存在时从已有大小继续
    let mut start: u64 = 0;
    if resume {
        if let Ok(existing) = fs.size(&remote_path).await {
            if existing > 0 && existing < total {
                start = existing;
            }
        }
    }

    let mut local = tokio::fs::File::open(&local_path).await?;
    use tokio::io::{AsyncReadExt, AsyncSeekExt};
    local.seek(std::io::SeekFrom::Start(start)).await?;
    let mut remote = fs.open_write(&remote_path, start > 0).await?;

    let mut buffer = vec![0u8; 256 * 1024];
    let mut transferred = start;
    let mut last_report = std::time::Instant::now();
    loop {
        let n = local.read(&mut buffer).await?;
        if n == 0 {
            break;
        }
        remote.write(&buffer[..n]).await?;
        transferred += n as u64;
        if last_report.elapsed() > std::time::Duration::from_millis(200) {
            report(state, &task_id, transferred, total, false);
            last_report = std::time::Instant::now();
        }
    }
    remote.finish().await?;
    report(state, &task_id, transferred, total, true);
    Ok(transferred)
}

/// 下载远端文件 → 本地路径。
pub async fn download(
    state: &AppState,
    session_id: &str,
    remote_path: String,
    local_path: String,
) -> AppResult<u64> {
    let session = state.sessions.get(session_id).await?;
    let fs = session.transport().await.fs().await?;
    let task_id = new_id();

    let mut remote = fs.open_read(&remote_path).await?;
    let total = remote.size();
    let mut local = tokio::fs::File::create(&local_path).await?;
    use tokio::io::AsyncWriteExt;

    let mut buffer = vec![0u8; 256 * 1024];
    let mut transferred: u64 = 0;
    let mut last_report = std::time::Instant::now();
    loop {
        let n = remote.read(&mut buffer).await?;
        if n == 0 {
            break;
        }
        local.write_all(&buffer[..n]).await?;
        transferred += n as u64;
        if last_report.elapsed() > std::time::Duration::from_millis(200) {
            report(state, &task_id, transferred, total, false);
            last_report = std::time::Instant::now();
        }
    }
    local.flush().await?;
    report(state, &task_id, transferred, total, true);
    Ok(transferred)
}

/// 远端目录 → 本地 tar.gz（「打包下载当前文件夹」）。
///
/// 为什么不递归拉文件再本地打包：远端基本都带 tar，一条命令的事；
/// 而递归 SFTP 拉一个大目录是成百上千个往返，慢到没法用。
/// 代价是依赖远端有 `tar` —— 对 Linux 靶机是稳的；Windows 远端上
/// `tar` 命中不了时这里会明确报错，不会静默出一个空包。
pub async fn pack_download(
    state: &AppState,
    session_id: &str,
    remote_path: String,
    local_path: String,
) -> AppResult<u64> {
    let session = state.sessions.get(session_id).await?;
    let transport = session.transport().await;

    // 临时包放远端 /tmp：既不污染用户正在看的目录，
    // 也避开「父目录只读但子目录可读」这种看得见却写不了的情况。
    let tmp = format!("/tmp/nexterm-pack-{}.tar.gz", new_id());
    let out = transport
        .exec(&tar_pack_command(&remote_path, &tmp), EXEC_TIMEOUT)
        .await?;
    if out.exit_code.unwrap_or(1) != 0 {
        return Err(AppError::Sftp(format!(
            "远端打包失败：{}{}",
            out.stderr.trim(),
            out.stdout.trim()
        )));
    }

    // 下载失败也必须清掉临时包，否则远端每失败一次就攒一个 tar.gz。
    let result = download(state, session_id, tmp.clone(), local_path).await;
    if let Ok(fs) = transport.fs().await {
        let _ = fs.delete(&tmp, false).await;
    }
    result
}

/// 远端解压（「解压」）：按扩展名挑 tar / unzip，统一解到「与压缩包同名的目录」。
///
/// 刻意**不就地展开**：就地展开时包里有顶层目录就多一层、没有就散一地，
/// 用户根本没法预期。固定落在一个新建目录里，解错了整个删掉即可。
pub async fn extract(state: &AppState, session_id: &str, remote_path: String) -> AppResult<String> {
    let session = state.sessions.get(session_id).await?;
    let transport = session.transport().await;

    let (parent, name) = split_parent_name(&remote_path);
    let target = format!("{parent}/{}", strip_archive_ext(&name));
    let cmd = extract_command(&name, &remote_path, &target)?;

    let out = transport.exec(&cmd, EXEC_TIMEOUT).await?;
    if out.exit_code.unwrap_or(1) != 0 {
        return Err(AppError::Sftp(format!(
            "解压失败：{}{}",
            out.stderr.trim(),
            out.stdout.trim()
        )));
    }
    // 返回真实落地目录，前端直接拿它刷列表 / 提示。
    Ok(target)
}

/// POSIX shell 单引号转义：空格、`$`、反引号一律不解释，单引号本身拼成 `'\''`。
fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// 拆「父目录 + 基名」，顺带吃掉尾斜杠并正确处理根目录。
fn split_parent_name(path: &str) -> (String, String) {
    let p = path.trim_end_matches('/');
    match p.rfind('/') {
        Some(0) => ("/".to_string(), p[1..].to_string()),
        Some(i) => (p[..i].to_string(), p[i + 1..].to_string()),
        None => (".".to_string(), p.to_string()),
    }
}

/// 去掉压缩包后缀 → 解压目标目录名。
fn strip_archive_ext(name: &str) -> String {
    let lower = name.to_lowercase();
    for ext in [
        ".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".tbz", ".txz", ".tar", ".zip",
    ] {
        if lower.ends_with(ext) {
            return name[..name.len() - ext.len()].to_string();
        }
    }
    name.to_string()
}

/// 打包命令：`tar -czf <out> -C <父目录> <基名>`。
///
/// 用 `-C` 而不是 `tar -czf out /a/b/c`：后者会把整条绝对路径塞进归档，
/// 解压时在本地重建 `/a/b` 那一串；用 `-C` 出来就是干净的一层目录。
fn tar_pack_command(remote_path: &str, out: &str) -> String {
    let (parent, name) = split_parent_name(remote_path);
    format!(
        "tar -czf {} -C {} {}",
        sh_quote(out),
        sh_quote(&parent),
        sh_quote(&name)
    )
}

/// 按扩展名生成解压命令，并带上 mkdir。
fn extract_command(name: &str, file: &str, target: &str) -> AppResult<String> {
    let lower = name.to_lowercase();
    let f = sh_quote(file);
    let t = sh_quote(target);
    let body = if lower.ends_with(".tar.gz") || lower.ends_with(".tgz") {
        format!("tar -xzf {f} -C {t}")
    } else if lower.ends_with(".tar.bz2") || lower.ends_with(".tbz2") || lower.ends_with(".tbz") {
        format!("tar -xjf {f} -C {t}")
    } else if lower.ends_with(".tar.xz") || lower.ends_with(".txz") {
        format!("tar -xJf {f} -C {t}")
    } else if lower.ends_with(".tar") {
        format!("tar -xf {f} -C {t}")
    } else if lower.ends_with(".zip") {
        // zip 不像 tar 那么遍地都是：先探一下，缺了给一句人话而不是一屏乱码。
        format!(
            "command -v unzip >/dev/null 2>&1 || \
             {{ echo '远端没有 unzip，请先安装（如 apt install unzip）' >&2; exit 127; }}; \
             unzip -o {f} -d {t}"
        )
    } else {
        return Err(AppError::param(
            "不支持的压缩格式（支持 .tar / .tar.gz / .tgz / .tar.bz2 / .tar.xz / .zip）",
        ));
    };
    Ok(format!("mkdir -p {t} && {body}"))
}

fn report(state: &AppState, task_id: &str, transferred: u64, total: u64, done: bool) {
    use tauri::Emitter;
    let _ = state.app.emit(
        crate::events::FS_PROGRESS,
        json!({
            "taskId": task_id,
            "transferred": transferred,
            "total": total,
            "done": done,
        }),
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sh_quote_wraps_and_escapes_single_quotes() {
        assert_eq!(sh_quote("plain"), "'plain'");
        assert_eq!(sh_quote("/a b/c"), "'/a b/c'");
        // 单引号必须拼成 '\''，否则目录名里带引号会把整条命令截断
        assert_eq!(sh_quote("it's"), r"'it'\''s'");
        // 变量与反引号在单引号里不解释 —— 这正是不用双引号的原因
        assert_eq!(sh_quote("$HOME`id`"), "'$HOME`id`'");
    }

    #[test]
    fn split_parent_name_handles_root_and_relative() {
        assert_eq!(
            split_parent_name("/a/b/c"),
            ("/a/b".to_string(), "c".to_string())
        );
        assert_eq!(split_parent_name("/a/"), ("/".to_string(), "a".to_string()));
        assert_eq!(split_parent_name("/a"), ("/".to_string(), "a".to_string()));
        assert_eq!(split_parent_name("a"), (".".to_string(), "a".to_string()));
    }

    #[test]
    fn strip_archive_ext_eats_the_whole_suffix() {
        assert_eq!(strip_archive_ext("pkg.tar.gz"), "pkg");
        assert_eq!(strip_archive_ext("pkg.TGZ"), "pkg");
        assert_eq!(strip_archive_ext("pkg.zip"), "pkg");
        // 不是归档就原样返回，别把 `notes.txt` 削成 `notes`
        assert_eq!(strip_archive_ext("notes.txt"), "notes.txt");
    }

    #[test]
    fn tar_pack_uses_dash_c_so_the_archive_has_no_absolute_paths() {
        let cmd = tar_pack_command("/home/deploy/my dir", "/tmp/o.tar.gz");
        assert!(cmd.starts_with("tar -czf '/tmp/o.tar.gz' -C '/home/deploy'"));
        assert!(cmd.ends_with("'my dir'"));
    }

    #[test]
    fn extract_command_picks_the_tool_by_suffix() {
        let gz = extract_command("a.tar.gz", "/x/a.tar.gz", "/x/a").unwrap();
        assert!(gz.contains("tar -xzf"));
        assert!(extract_command("a.tgz", "/x/a.tgz", "/x/a")
            .unwrap()
            .contains("tar -xzf"));
        assert!(extract_command("a.tar.xz", "/x/a.tar.xz", "/x/a")
            .unwrap()
            .contains("tar -xJf"));
        assert!(extract_command("a.zip", "/x/a.zip", "/x/a")
            .unwrap()
            .contains("unzip -o"));
        // 目标目录必须先建出来，否则 tar -C 直接失败
        assert!(gz.starts_with("mkdir -p "));
    }

    #[test]
    fn extract_command_rejects_unknown_suffix() {
        assert!(extract_command("a.rar", "/x/a.rar", "/x/a").is_err());
        assert!(extract_command("a.7z", "/x/a.7z", "/x/a").is_err());
    }
}
