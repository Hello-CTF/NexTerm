//! 本地传输：ConPTY（Windows）/ pty（Unix）+ 本地文件系统。

use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;

use super::{ExecResult, FileEntry, FileSystem, PtyHandle, Transport};
use crate::error::{AppError, AppResult};

pub struct LocalTransport {
    /// 覆盖默认 shell（如 powershell.exe / bash）。
    pub shell: Option<String>,
    pub initial_cwd: std::sync::Mutex<String>,
}

impl LocalTransport {
    pub fn new() -> Self {
        Self {
            shell: None,
            initial_cwd: std::sync::Mutex::new(
                dirs::home_dir()
                    .map(|p| p.to_string_lossy().to_string())
                    .unwrap_or_default(),
            ),
        }
    }
}

impl Default for LocalTransport {
    fn default() -> Self {
        Self::new()
    }
}

fn build_shell_command(shell: &str, cmd: &str) -> std::process::Command {
    #[cfg(windows)]
    {
        let mut c = std::process::Command::new(shell);
        c.arg("-NoLogo").arg("-NoProfile").arg("-Command").arg(cmd);
        c
    }
    #[cfg(not(windows))]
    {
        let mut c = std::process::Command::new(shell);
        c.arg("-lc").arg(cmd);
        c
    }
}

fn default_shell() -> String {
    #[cfg(windows)]
    {
        // PowerShell 7 优先，其次 Windows PowerShell
        if let Ok(pwsh) = which_lookup("pwsh.exe") {
            return pwsh;
        }
        "powershell.exe".to_string()
    }
    #[cfg(not(windows))]
    {
        std::env::var("SHELL").unwrap_or_else(|_| "/bin/sh".to_string())
    }
}

#[cfg(windows)]
fn which_lookup(name: &str) -> std::io::Result<String> {
    let path = std::env::var("PATH").unwrap_or_default();
    for dir in path.split(';') {
        let candidate = std::path::Path::new(dir).join(name);
        if candidate.is_file() {
            return Ok(candidate.to_string_lossy().to_string());
        }
    }
    Err(std::io::Error::new(std::io::ErrorKind::NotFound, name))
}

#[async_trait]
impl Transport for LocalTransport {
    fn kind(&self) -> &'static str {
        "local"
    }

    async fn open_pty(&self, cols: u16, rows: u16) -> AppResult<PtyHandle> {
        let pty_system = portable_pty::native_pty_system();
        let pair = pty_system
            .openpty(portable_pty::PtySize {
                rows,
                cols,
                pixel_width: 0,
                pixel_height: 0,
            })
            .map_err(|e| AppError::Internal(format!("ConPTY 打开失败: {e}")))?;
        let mut cmd =
            portable_pty::CommandBuilder::new(self.shell.clone().unwrap_or_else(default_shell));
        if let Ok(cwd) = self.initial_cwd.lock() {
            if !cwd.is_empty() {
                cmd.cwd(cwd.clone());
            }
        }
        #[cfg(windows)]
        cmd.env("TERM", "xterm-256color");
        let child = pair
            .slave
            .spawn_command(cmd)
            .map_err(|e| AppError::Internal(format!("shell 启动失败: {e}")))?;
        let reader = pair
            .master
            .try_clone_reader()
            .map_err(|e| AppError::Internal(format!("PTY reader 失败: {e}")))?;
        let writer = pair
            .master
            .take_writer()
            .map_err(|e| AppError::Internal(format!("PTY writer 失败: {e}")))?;
        drop(pair.slave);
        let child = Arc::new(std::sync::Mutex::new(child));
        let killer = child
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .clone_killer();
        Ok(PtyHandle::Local {
            io: Arc::new(crate::terminal::LocalPtyIo {
                writer: Arc::new(std::sync::Mutex::new(writer)),
                master: Arc::new(std::sync::Mutex::new(pair.master)),
            }),
            reader,
            child,
            killer,
        })
    }

    async fn exec(&self, cmd: &str, timeout: Duration) -> AppResult<ExecResult> {
        let start = Instant::now();
        let shell = self.shell.clone().unwrap_or_else(default_shell);
        let mut command = build_shell_command(&shell, cmd);
        if let Ok(cwd) = self.initial_cwd.lock() {
            if !cwd.is_empty() && std::path::Path::new(cwd.as_str()).is_dir() {
                command.current_dir(cwd.as_str());
            }
        }
        let mut child = tokio::process::Command::from(command)
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .stdin(std::process::Stdio::null())
            .spawn()?;
        let out = child.stdout.take();
        let err = child.stderr.take();
        let out_task = tokio::spawn(async move {
            let mut buf = Vec::new();
            if let Some(mut o) = out {
                use tokio::io::AsyncReadExt;
                let mut tmp = vec![0u8; 8192];
                loop {
                    match o.read(&mut tmp).await {
                        Ok(0) | Err(_) => break,
                        Ok(n) => {
                            if buf.len() < 4 * 1024 * 1024 {
                                buf.extend_from_slice(&tmp[..n]);
                            }
                        }
                    }
                }
            }
            buf
        });
        let err_task = tokio::spawn(async move {
            let mut buf = Vec::new();
            if let Some(mut e) = err {
                use tokio::io::AsyncReadExt;
                let mut tmp = vec![0u8; 8192];
                loop {
                    match e.read(&mut tmp).await {
                        Ok(0) | Err(_) => break,
                        Ok(n) => {
                            if buf.len() < 1024 * 1024 {
                                buf.extend_from_slice(&tmp[..n]);
                            }
                        }
                    }
                }
            }
            buf
        });
        let exit = tokio::time::timeout(timeout, child.wait()).await;
        let exit_code = match exit {
            Ok(Ok(status)) => status.code(),
            Ok(Err(e)) => return Err(AppError::Io(e)),
            Err(_) => {
                let _ = child.kill().await;
                return Err(AppError::Timeout(format!(
                    "命令超时（{}s）",
                    timeout.as_secs()
                )));
            }
        };
        let stdout = String::from_utf8_lossy(&out_task.await.unwrap_or_default()).into_owned();
        let stderr = String::from_utf8_lossy(&err_task.await.unwrap_or_default()).into_owned();
        let (stdout, t1) = super::cap_text(&stdout);
        let (stderr, t2) = super::cap_text(&stderr);
        Ok(ExecResult {
            stdout,
            stderr,
            exit_code,
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: t1 || t2,
        })
    }

    async fn fs(&self) -> AppResult<Arc<dyn FileSystem>> {
        Ok(Arc::new(LocalFs))
    }

    async fn ping(&self) -> AppResult<Duration> {
        let start = Instant::now();
        tokio::task::spawn_blocking(|| std::thread::sleep(Duration::from_millis(1)))
            .await
            .map_err(|e| AppError::Internal(format!("{e}")))?;
        Ok(start.elapsed())
    }

    async fn close(&self) {}

    fn as_any(&self) -> &dyn std::any::Any {
        self
    }

    fn as_any_arc(self: Arc<Self>) -> Arc<dyn std::any::Any + Send + Sync> {
        self
    }
}

/// 本地文件系统实现。
pub struct LocalFs;

#[async_trait]
impl FileSystem for LocalFs {
    async fn list(&self, path: &str) -> AppResult<Vec<FileEntry>> {
        let mut rd = tokio::fs::read_dir(path).await?;
        let mut out = Vec::new();
        while let Some(entry) = rd.next_entry().await? {
            let meta = entry.metadata().await;
            let (kind, size, mtime) = match meta {
                Ok(m) => {
                    let kind = if m.is_dir() {
                        "dir"
                    } else if m.is_symlink() {
                        "symlink"
                    } else {
                        "file"
                    };
                    (
                        kind.to_string(),
                        m.len(),
                        m.modified()
                            .ok()
                            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                            .map(|d| d.as_millis() as i64)
                            .unwrap_or(0),
                    )
                }
                Err(_) => ("other".to_string(), 0, 0),
            };
            out.push(FileEntry {
                name: entry.file_name().to_string_lossy().into_owned(),
                path: entry.path().to_string_lossy().into_owned(),
                kind,
                size,
                mode: String::new(),
                owner: None,
                group: None,
                mtime,
                symlink_target: None,
            });
        }
        out.sort_by(|a, b| {
            (b.kind == "dir")
                .cmp(&(a.kind == "dir"))
                .then(a.name.cmp(&b.name))
        });
        Ok(out)
    }

    async fn read_file(&self, path: &str, max_bytes: u64) -> AppResult<Vec<u8>> {
        let meta = tokio::fs::metadata(path).await?;
        if meta.len() > max_bytes {
            return Err(AppError::param(format!(
                "文件超过读取上限（{} > {} 字节）",
                meta.len(),
                max_bytes
            )));
        }
        Ok(tokio::fs::read(path).await?)
    }

    async fn write_file(&self, path: &str, data: &[u8], backup: bool) -> AppResult<()> {
        if backup && tokio::fs::try_exists(path).await.unwrap_or(false) {
            let backup_path = format!("{path}.nexterm-bak");
            tokio::fs::copy(path, &backup_path).await?;
        }
        tokio::fs::write(path, data).await?;
        Ok(())
    }

    async fn mkdir(&self, path: &str) -> AppResult<()> {
        tokio::fs::create_dir_all(path).await?;
        Ok(())
    }

    async fn rename(&self, from: &str, to: &str) -> AppResult<()> {
        tokio::fs::rename(from, to).await?;
        Ok(())
    }

    async fn delete(&self, path: &str, is_dir: bool) -> AppResult<()> {
        if is_dir {
            tokio::fs::remove_dir_all(path).await?;
        } else {
            tokio::fs::remove_file(path).await?;
        }
        Ok(())
    }

    async fn chmod(&self, _path: &str, _mode: u32) -> AppResult<()> {
        Err(AppError::Unsupported("Windows 文件系统不支持 chmod".into()))
    }

    async fn checksum(&self, path: &str, algo: &str) -> AppResult<String> {
        let data = tokio::fs::read(path).await?;
        Ok(match algo.to_ascii_lowercase().as_str() {
            "md5" => {
                use md5::Digest;
                let mut h = md5::Md5::new();
                h.update(&data);
                format!("{:x}", h.finalize())
            }
            "sha256" => {
                use sha2::Digest;
                let mut h = sha2::Sha256::new();
                h.update(&data);
                format!("{:x}", h.finalize())
            }
            _ => return Err(AppError::param("不支持的校验算法（md5/sha256）")),
        })
    }

    async fn exists(&self, path: &str) -> AppResult<bool> {
        Ok(tokio::fs::try_exists(path).await?)
    }

    async fn size(&self, path: &str) -> AppResult<u64> {
        Ok(tokio::fs::metadata(path).await?.len())
    }

    async fn open_read(&self, path: &str) -> AppResult<Box<dyn super::RemoteRead>> {
        let file = tokio::fs::File::open(path).await?;
        let size = file.metadata().await?.len();
        Ok(Box::new(LocalReader { file, size }))
    }

    async fn open_write(&self, path: &str, append: bool) -> AppResult<Box<dyn super::RemoteWrite>> {
        let file = tokio::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .append(append)
            .truncate(!append)
            .open(path)
            .await?;
        Ok(Box::new(LocalWriter { file }))
    }
}

struct LocalReader {
    file: tokio::fs::File,
    size: u64,
}

#[async_trait]
impl super::RemoteRead for LocalReader {
    async fn read(&mut self, buf: &mut [u8]) -> AppResult<usize> {
        use tokio::io::AsyncReadExt;
        self.file.read(buf).await.map_err(AppError::Io)
    }
    fn size(&self) -> u64 {
        self.size
    }
}

struct LocalWriter {
    file: tokio::fs::File,
}

#[async_trait]
impl super::RemoteWrite for LocalWriter {
    async fn write(&mut self, data: &[u8]) -> AppResult<()> {
        use tokio::io::AsyncWriteExt;
        self.file.write_all(data).await.map_err(AppError::Io)
    }
    async fn finish(&mut self) -> AppResult<()> {
        use tokio::io::AsyncWriteExt;
        self.file.flush().await.map_err(AppError::Io)
    }
}
