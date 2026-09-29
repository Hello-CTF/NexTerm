//! 传输层（§5）：统一 Transport trait + 三实现（SSH / WinRM / 本地）。
//!
//! 铁律（§5.2）：未显式配置代理时一律直连，绝不读系统代理。
//! 一个 Session 一条底层连接，PTY / SFTP / exec 全部复用。

pub mod forward;
pub mod local;
pub mod ssh;
pub mod winrm;

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use serde::Serialize;

use crate::error::{AppError, AppResult};
use crate::terminal::LocalPtyIo;

/// 单次执行结果（§5.1）。
///
/// `stdout` / `stderr` 是**未经 §6.3 预算裁剪**的原始输出。这一条是硬约束：
/// 消费方既有「喂给模型」的（`ai::tools` / `ai::context`），也有「结构化解析」的
/// （`docker images --format '{{json .}}'`）。后者一旦被按行裁剪，解析结果就**静默**
/// 少一截 —— 实机上就出现过「容器面板显示镜像 400，`docker images` 实际 569」。
/// 所以裁剪不放在传输层，而是由喂模型的那一侧自己调 [`cap_text`]。
///
/// `truncated` 只表示**原始字节硬熔断**触顶（SSH 8MB / 本地 4MB），不代表 §6.3 的
/// 行数·字节预算裁剪；预算裁剪由消费方自行判断。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExecResult {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: Option<i32>,
    pub duration_ms: u64,
    pub truncated: bool,
}

/// 文件条目（列表用）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FileEntry {
    pub name: String,
    pub path: String,
    /// dir | file | symlink | other
    pub kind: String,
    pub size: u64,
    /// 八进制权限（如 0o755 → "755"）。
    pub mode: String,
    pub owner: Option<String>,
    pub group: Option<String>,
    /// Unix 毫秒。
    pub mtime: i64,
    pub symlink_target: Option<String>,
}

/// 流式远端读句柄（上传/下载进度传输用）。
#[async_trait]
pub trait RemoteRead: Send {
    async fn read(&mut self, buf: &mut [u8]) -> AppResult<usize>;
    fn size(&self) -> u64;
}

/// 流式远端写句柄。
#[async_trait]
pub trait RemoteWrite: Send {
    async fn write(&mut self, data: &[u8]) -> AppResult<()>;
    async fn finish(&mut self) -> AppResult<()>;
}

/// 文件子系统抽象：SFTP 与本地 FS 各有一实现。
#[async_trait]
pub trait FileSystem: Send + Sync {
    async fn list(&self, path: &str) -> AppResult<Vec<FileEntry>>;
    async fn read_file(&self, path: &str, max_bytes: u64) -> AppResult<Vec<u8>>;
    async fn write_file(&self, path: &str, data: &[u8], backup: bool) -> AppResult<()>;
    async fn mkdir(&self, path: &str) -> AppResult<()>;
    async fn rename(&self, from: &str, to: &str) -> AppResult<()>;
    async fn delete(&self, path: &str, is_dir: bool) -> AppResult<()>;
    async fn chmod(&self, path: &str, mode: u32) -> AppResult<()>;
    async fn checksum(&self, path: &str, algo: &str) -> AppResult<String>;
    async fn exists(&self, path: &str) -> AppResult<bool>;
    /// 文件大小（断点续传判断用）。
    async fn size(&self, path: &str) -> AppResult<u64>;
    /// 流式打开（默认不支持；SFTP/本地实现覆盖）。
    async fn open_read(&self, path: &str) -> AppResult<Box<dyn RemoteRead>> {
        let _ = path;
        Err(AppError::Unsupported("该文件系统不支持流式读取".into()))
    }
    async fn open_write(&self, path: &str, append: bool) -> AppResult<Box<dyn RemoteWrite>> {
        let _ = (path, append);
        Err(AppError::Unsupported("该文件系统不支持流式写入".into()))
    }
}

/// 打开的交互式 PTY 句柄。
pub enum PtyHandle {
    /// 本地 ConPTY：reader/writer/master/child。
    Local {
        io: Arc<LocalPtyIo>,
        reader: Box<dyn std::io::Read + Send>,
        child: Arc<std::sync::Mutex<Box<dyn portable_pty::Child + Send + Sync>>>,
        killer: Box<dyn portable_pty::ChildKiller + Send + Sync>,
    },
    /// SSH：read 半边给泵，write 半边共享给写入端。
    Ssh {
        read: russh::ChannelReadHalf,
        write: Arc<russh::ChannelWriteHalf<russh::client::Msg>>,
    },
}

/// 统一传输 trait（§5.1）。
#[async_trait]
pub trait Transport: Send + Sync {
    /// 打开交互式 PTY。WinRM 返回 Unsupported（§5.3）。
    async fn open_pty(&self, cols: u16, rows: u16) -> AppResult<PtyHandle>;
    /// 单次执行（AI 工具 / Docker CLI 通道）。
    async fn exec(&self, cmd: &str, timeout: Duration) -> AppResult<ExecResult>;
    /// 文件子系统。
    async fn fs(&self) -> AppResult<Arc<dyn FileSystem>>;
    async fn ping(&self) -> AppResult<Duration>;
    async fn close(&self);
    /// 会话类型名。
    fn kind(&self) -> &'static str;
    /// 向下转型支持（docker exec 通道等 SSH 专属能力）。
    fn as_any(&self) -> &dyn std::any::Any;
    /// 取回具体类型的 Arc（端口转发等需要所有权的能力）。
    fn as_any_arc(self: Arc<Self>) -> Arc<dyn std::any::Any + Send + Sync>;
    /// WinRM 等非交互通道的 cwd 维护（§5.4）；交互式返回 None。
    fn cwd(&self) -> Option<String> {
        None
    }
    fn set_cwd(&self, _cwd: &str) {}
}

/// 输出裁剪（§6.3 / RainsIR cap_text）：400 行 / 120KB。
///
/// **由消费方调用**，不在 `Transport::exec` 里做（原因见 [`ExecResult`] 的注释）。
/// 目前只用在两个「喂模型」的地方：`ai::tools::server` 的 exec / search 工具、
/// `ai::context::recon_snapshot`。终端、文件树、Docker 面板拿到的都是完整输出。
pub const CAP_LINES: usize = 400;
pub const CAP_BYTES: usize = 120 * 1024;

/// 裁剪到行数与字节上限，返回 (裁剪结果, 是否截断)。
/// 截断标记显式写出，让模型知道信息不全（§6.3）。
pub fn cap_text(text: &str) -> (String, bool) {
    let lines: Vec<&str> = text.lines().collect();
    let over_lines = lines.len() > CAP_LINES;
    let over_bytes = text.len() > CAP_BYTES;
    if !over_lines && !over_bytes {
        return (text.to_string(), false);
    }
    let mut kept: String = lines
        .iter()
        .take(CAP_LINES)
        .copied()
        .collect::<Vec<_>>()
        .join("\n");
    if kept.len() > CAP_BYTES {
        let mut end = CAP_BYTES;
        while end > 0 && !kept.is_char_boundary(end) {
            end -= 1;
        }
        kept.truncate(end);
    }
    kept.push_str(&format!(
        "\n... [已截断，原始 {} 行 / {} 字节]",
        lines.len(),
        text.len()
    ));
    (kept, true)
}

/// 把环境选项 JSON 解析为表。
pub fn parse_options(options_json: &str) -> HashMap<String, serde_json::Value> {
    serde_json::from_str::<serde_json::Value>(options_json)
        .ok()
        .and_then(|v| v.as_object().cloned())
        .map(|m| m.into_iter().collect())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cap_text_limits() {
        let long_line = "x".repeat(200_000);
        let (s, t) = cap_text(&long_line);
        assert!(t);
        assert!(s.contains("已截断"));
        let many_lines: String = (0..1000).map(|i| format!("line {i}\n")).collect();
        let (s2, t2) = cap_text(&many_lines);
        assert!(t2);
        assert!(s2.lines().count() <= CAP_LINES + 1);
        let (s3, t3) = cap_text("short output");
        assert!(!t3);
        assert_eq!(s3, "short output");
    }

    /// 回归：传输层 `exec` **不得**按行裁剪。
    ///
    /// 这是「容器面板显示镜像 400、实机 `docker images` 569」那个 bug 的守门用例 ——
    /// 裁剪一旦回到 `Transport::exec`，本用例立刻红。
    ///
    /// 生成大输出的命令必须按平台给：`LocalTransport::exec` 走的是**本机 shell**
    /// （Windows 是 PowerShell，Unix 是 sh），`seq` 在 Windows 上不存在。
    /// 原先写死 `seq 1 1500`，clippy 能过、用例却会在 Windows 上连命令都找不到 ——
    /// 正是「本地全绿、另一侧必红」那类跨平台暗坑。PowerShell 用区间表达式
    /// `1..1500` 生成同样 1500 行。
    ///
    /// 已由 CI 的 `windows-latest` 任务实测通过（run 36532246405）。改这里之后，
    /// 别只看本机 clippy —— 必须确认 Windows job 的那次运行也是绿的。
    #[tokio::test]
    async fn local_exec_keeps_full_output() {
        let t = crate::transport::local::LocalTransport::new();
        // 注意：Windows 分支是 PowerShell 语法，本机（macOS）无 pwsh 无法本地实测。
        // 真要改这里，先确认两边都还能出 1500 行。
        let cmd = if cfg!(windows) {
            "1..1500"
        } else {
            "seq 1 1500"
        };
        let out = t
            .exec(cmd, Duration::from_secs(30))
            .await
            .expect("本地 exec 失败");
        assert_eq!(
            out.exit_code,
            Some(0),
            "命令 `{cmd}` 未成功执行；stderr={}",
            out.stderr.trim()
        );
        let lines = out.stdout.lines().filter(|l| !l.trim().is_empty()).count();
        assert!(
            lines >= 1500,
            "传输层裁剪了输出：`{cmd}` 只拿到 {lines} 行（CAP_LINES={CAP_LINES}）"
        );
        assert!(!out.stdout.contains("已截断"), "传输层不应插入截断标记");
        assert!(!out.truncated, "1500 行远未触及字节熔断，不应标截断");
    }

    /// 预算裁剪依旧存在，只是归属消费方（喂模型的那一侧）。
    #[test]
    fn cap_text_bounds_before_model() {
        let many: String = (0..1500).map(|i| format!("line {i}\n")).collect();
        let (s, t) = cap_text(&many);
        assert!(t, "1500 行必须被裁剪");
        assert!(s.lines().count() <= CAP_LINES + 1);
        assert!(s.contains("已截断"));
    }
}
