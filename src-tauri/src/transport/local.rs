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

    /// 在本机起一个**跑指定命令**的 PTY（不是交互 shell）。
    ///
    /// 用途：`docker logs -f` / `docker exec -it` 这类「要一个长驻 tty」的通道。
    /// 以前这些通道只会 `downcast_ref::<SshTransport>()`，本机会话直接撞
    /// 「该通道需要 SSH 会话」—— 而本机跑着 Docker 恰恰是最常见的场景。
    ///
    /// 参数形态与 [`exec`](Self::exec) 保持一致（同一个 shell、同样的 `-lc`
    /// / `-Command`），这样「终端里能跑的命令，容器面板里也能跑」。
    pub fn open_command_pty(&self, cmd: &str, cols: u16, rows: u16) -> AppResult<PtyHandle> {
        let shell = self.shell.clone().unwrap_or_else(default_shell);
        self.spawn_pty(shell_command_builder(&shell, cmd), cols, rows)
    }

    /// 建 PTY + 起进程 + 收好三个句柄。`open_pty` 与 `open_command_pty` 共用。
    ///
    /// `TERM` / `LANG` 的注入写在这里而不是各写一遍：这两条是「终端里中文
    /// 不变问号、vim 不变哑终端」的**唯一**来源，漏掉任何一条的症状都很难查
    /// （只表现为"某些命令看起来不对"）。见 `utf8_locale` 的注释。
    fn spawn_pty(
        &self,
        mut cmd: portable_pty::CommandBuilder,
        cols: u16,
        rows: u16,
    ) -> AppResult<PtyHandle> {
        let pty_system = portable_pty::native_pty_system();
        let pair = pty_system
            .openpty(portable_pty::PtySize {
                rows,
                cols,
                pixel_width: 0,
                pixel_height: 0,
            })
            .map_err(|e| AppError::Internal(format!("ConPTY 打开失败: {e}")))?;
        if let Ok(cwd) = self.initial_cwd.lock() {
            if !cwd.is_empty() {
                cmd.cwd(cwd.clone());
            }
        }
        // TERM 描述的是**我们提供的这个 pty**（xterm.js，256 色），不是启动 NexTerm
        // 的那个终端，所以无条件覆盖 —— 宿主若是 `TERM=screen` 的 tmux 里起来的，
        // 透传下去会让子进程按错误的能力表发序列。
        // （原先只在 Windows 分支设，Unix 上一直是「没有 TERM」：vim/less 会退化成
        // 哑终端，`ls` 也不再上色。）
        cmd.env("TERM", "xterm-256color");
        if let Some(locale) = utf8_locale() {
            cmd.env("LANG", locale);
        }
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
}

impl Default for LocalTransport {
    fn default() -> Self {
        Self::new()
    }
}

/// 宿主没安排 locale 时，给子进程补的 UTF-8 locale。
///
/// 选 `C.UTF-8` 而不是 `zh_CN.UTF-8`/`en_US.UTF-8`：它正是 macOS 的
/// `/etc/zprofile` 给登录 shell 补的值（本机 `locale -a` 确认在册），
/// 现代 Linux（glibc ≥ 2.35）也有。语言保持 C（错误信息仍是英文，
/// 便于按文本解析命令输出的调用方），但 charmap 是 UTF-8。
const UTF8_LOCALE: &str = "C.UTF-8";

/// 决定要不要补 locale（注入取值数组以便单测，避免改全局环境变量）。
///
/// 三个变量**任一**非空就算宿主已安排：`LC_ALL` 覆盖一切，`LC_CTYPE` 只管字符分类，
/// `LANG` 是兜底。空串按「没设」处理 —— 有些启动器会塞 `LANG=` 这种空值，
/// 若当成已设置就会退回 C locale，等于没修。
fn utf8_locale_from(vars: [Option<String>; 3]) -> Option<&'static str> {
    let present = vars
        .iter()
        .any(|v| v.as_deref().map(|s| !s.trim().is_empty()).unwrap_or(false));
    if present {
        None
    } else {
        Some(UTF8_LOCALE)
    }
}

/// 本机 shell 需要的 locale 补齐。
///
/// ## 为什么必须有这一步（中文文件名整片变成 `?` 的真因）
///
/// macOS 的 GUI 应用由 launchd 启动，环境里**没有 `LANG` / `LC_ALL`**
/// （实测 `ps eww <NexTerm>` 只有 PATH/HOME/SHELL/USER/TMPDIR/…）。
/// 从这里 fork 出来的 shell 因此落在 C/POSIX locale，`locale charmap` = `US-ASCII`。
///
/// 后果不是「字节被改坏」——`printf`、`cat` 这类纯字节透传的程序完全正常，
/// 所以这个问题看起来只影响「某些命令」。真正会中招的是**按 locale 判断字符
/// 可打印性的工具**：BSD `ls` 在 stdout 是 tty 时默认带 `-q`，会把每个非 ASCII
/// **字节**换成一个 `?`，于是 `中文目录Ω`（14 字节）就显示成 14 个问号。
/// （`find` / `grep` / `sort` 同属「按 locale 判断」这一类，但本仓库**只实测过 `ls`**，
/// 其余是按机制推断，没验过就不当成结论用。）
///
/// ## 为什么不能指望对方 shell 自己修好
///
/// macOS 确实有 `/etc/zprofile`：`if [ -z "$LANG" ]; then export LANG=C.UTF-8; fi`。
/// 但那是**登录 shell** 才读的文件。Terminal.app / iTerm 默认都起登录 shell
/// （`zsh -l`），所以它们没这个问题；本应用起的是普通交互 shell，读不到它。
/// 实测对照（同一 GUI 环境、同一夹具、`ls -1` 直出 tty）：
///
/// | 启动方式 | `locale charmap` | `ls` 中文名 |
/// |---|---|---|
/// | `zsh`（本应用原状） | US-ASCII | ❌ `????????????` |
/// | `zsh -l` | UTF-8 | ✅ |
/// | `zsh` + `LANG=C.UTF-8` | UTF-8 | ✅ |
///
/// 我们选后者：直接写进子进程环境，不去改用户的 shell 启动方式 ——
/// 起登录 shell 会连带 source `~/.zprofile`/`~/.zlogin`，副作用大得多。
fn utf8_locale() -> Option<&'static str> {
    utf8_locale_from([
        std::env::var("LC_ALL").ok(),
        std::env::var("LC_CTYPE").ok(),
        std::env::var("LANG").ok(),
    ])
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

/// `build_shell_command` 的 portable_pty 版本：同一套参数形态，给 PTY 用。
///
/// 两处必须保持一致（`-lc` / `-Command`）。不一致的症状很隐蔽：
/// 同一条命令在终端里能跑、在容器日志面板里报"找不到命令"。
fn shell_command_builder(shell: &str, cmd: &str) -> portable_pty::CommandBuilder {
    let mut c = portable_pty::CommandBuilder::new(shell);
    #[cfg(windows)]
    {
        c.arg("-NoLogo");
        c.arg("-NoProfile");
        c.arg("-Command");
        c.arg(cmd);
    }
    #[cfg(not(windows))]
    {
        c.arg("-lc");
        c.arg(cmd);
    }
    c
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
        let shell = self.shell.clone().unwrap_or_else(default_shell);
        self.spawn_pty(portable_pty::CommandBuilder::new(shell), cols, rows)
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
        // locale 与交互式 PTY 保持一致：同一条 `ls` 在终端里和经 AI 工具执行时
        // 不该有不同结果。（`exec` 没有 tty，所以不设 TERM —— 那是描述终端的变量。）
        if let Some(locale) = utf8_locale() {
            command.env("LANG", locale);
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
            // 4MB 是防内存爆掉的硬熔断，不是 §6.3 预算裁剪；触顶要如实上报。
            let mut capped = false;
            if let Some(mut o) = out {
                use tokio::io::AsyncReadExt;
                let mut tmp = vec![0u8; 8192];
                loop {
                    match o.read(&mut tmp).await {
                        Ok(0) | Err(_) => break,
                        Ok(n) => {
                            if buf.len() < 4 * 1024 * 1024 {
                                buf.extend_from_slice(&tmp[..n]);
                            } else {
                                capped = true;
                            }
                        }
                    }
                }
            }
            (buf, capped)
        });
        let err_task = tokio::spawn(async move {
            let mut buf = Vec::new();
            let mut capped = false;
            if let Some(mut e) = err {
                use tokio::io::AsyncReadExt;
                let mut tmp = vec![0u8; 8192];
                loop {
                    match e.read(&mut tmp).await {
                        Ok(0) | Err(_) => break,
                        Ok(n) => {
                            if buf.len() < 1024 * 1024 {
                                buf.extend_from_slice(&tmp[..n]);
                            } else {
                                capped = true;
                            }
                        }
                    }
                }
            }
            (buf, capped)
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
        let (out_buf, out_capped) = out_task.await.unwrap_or_default();
        let (err_buf, err_capped) = err_task.await.unwrap_or_default();
        let stdout = String::from_utf8_lossy(&out_buf).into_owned();
        let stderr = String::from_utf8_lossy(&err_buf).into_owned();
        // 不做 §6.3 裁剪（原因见 ExecResult 注释）：调用方里既有结构化解析，也有模型输入。
        Ok(ExecResult {
            stdout,
            stderr,
            exit_code,
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: out_capped || err_capped,
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

/// 把 `~` 展开成本机用户目录。
///
/// 前端左栏文件树的第一发请求固定是 `~`（"先看这台机器的家目录"），
/// 而 `dirs::home_dir()` 是唯一同时覆盖 Windows（`C:\Users\x`）与
/// Unix（`/home/x`）的取法。不展开的话每个本地会话都要先白等一次失败、
/// 再退到文件系统根（Windows 上就是 `C:\`，满屏系统目录）。
fn real(path: &str) -> std::path::PathBuf {
    if path == "~" || path.starts_with("~/") || path.starts_with("~\\") {
        if let Some(home) = dirs::home_dir() {
            let rest = path[1..].trim_start_matches(['/', '\\']);
            return if rest.is_empty() {
                home
            } else {
                home.join(rest)
            };
        }
    }
    std::path::PathBuf::from(path)
}

#[async_trait]
impl FileSystem for LocalFs {
    async fn list(&self, path: &str) -> AppResult<Vec<FileEntry>> {
        let path = real(path);
        let mut rd = tokio::fs::read_dir(&path).await?;
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
        let path = real(path);
        let meta = tokio::fs::metadata(&path).await?;
        if meta.len() > max_bytes {
            return Err(AppError::param(format!(
                "文件超过读取上限（{} > {} 字节）",
                meta.len(),
                max_bytes
            )));
        }
        Ok(tokio::fs::read(&path).await?)
    }

    async fn write_file(&self, path: &str, data: &[u8], backup: bool) -> AppResult<()> {
        let path = real(path);
        if backup && tokio::fs::try_exists(&path).await.unwrap_or(false) {
            let backup_path = std::path::PathBuf::from(format!("{}.nexterm-bak", path.display()));
            tokio::fs::copy(&path, &backup_path).await?;
        }
        tokio::fs::write(&path, data).await?;
        Ok(())
    }

    async fn mkdir(&self, path: &str) -> AppResult<()> {
        tokio::fs::create_dir_all(real(path)).await?;
        Ok(())
    }

    async fn rename(&self, from: &str, to: &str) -> AppResult<()> {
        tokio::fs::rename(real(from), real(to)).await?;
        Ok(())
    }

    async fn delete(&self, path: &str, is_dir: bool) -> AppResult<()> {
        let path = real(path);
        if is_dir {
            tokio::fs::remove_dir_all(&path).await?;
        } else {
            tokio::fs::remove_file(&path).await?;
        }
        Ok(())
    }

    #[cfg(unix)]
    async fn chmod(&self, path: &str, mode: u32) -> AppResult<()> {
        use std::os::unix::fs::PermissionsExt;
        tokio::fs::set_permissions(real(path), std::fs::Permissions::from_mode(mode)).await?;
        Ok(())
    }

    #[cfg(not(unix))]
    async fn chmod(&self, _path: &str, _mode: u32) -> AppResult<()> {
        Err(AppError::Unsupported("Windows 文件系统不支持 chmod".into()))
    }

    async fn checksum(&self, path: &str, algo: &str) -> AppResult<String> {
        let data = tokio::fs::read(real(path)).await?;
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
        Ok(tokio::fs::try_exists(real(path)).await?)
    }

    async fn size(&self, path: &str) -> AppResult<u64> {
        Ok(tokio::fs::metadata(real(path)).await?.len())
    }

    async fn open_read(&self, path: &str) -> AppResult<Box<dyn super::RemoteRead>> {
        let file = tokio::fs::File::open(real(path)).await?;
        let size = file.metadata().await?.len();
        Ok(Box::new(LocalReader { file, size }))
    }

    async fn open_write(&self, path: &str, append: bool) -> AppResult<Box<dyn super::RemoteWrite>> {
        let file = tokio::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .append(append)
            .truncate(!append)
            .open(real(path))
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

#[cfg(test)]
mod tests {
    use super::*;

    fn vars(
        lc_all: Option<&str>,
        lc_ctype: Option<&str>,
        lang: Option<&str>,
    ) -> [Option<String>; 3] {
        [
            lc_all.map(str::to_string),
            lc_ctype.map(str::to_string),
            lang.map(str::to_string),
        ]
    }

    #[test]
    fn locale_filled_when_host_has_none() {
        // GUI 启动的真实形态：三个变量全没有 → 必须补
        assert_eq!(utf8_locale_from(vars(None, None, None)), Some(UTF8_LOCALE));
    }

    #[test]
    fn locale_not_overridden_when_host_has_it() {
        // 从终端 pnpm tauri dev 起来时会带 locale，用户的设置要原样透传
        assert_eq!(
            utf8_locale_from(vars(None, None, Some("en_US.UTF-8"))),
            None
        );
        assert_eq!(
            utf8_locale_from(vars(None, Some("zh_CN.UTF-8"), None)),
            None
        );
        assert_eq!(utf8_locale_from(vars(Some("C"), None, None)), None);
    }

    #[test]
    fn locale_filled_when_values_are_blank() {
        // 空串等价于没设：若当成「已设置」，就会静默退回 C locale，等于没修。
        assert_eq!(
            utf8_locale_from(vars(None, None, Some(""))),
            Some(UTF8_LOCALE)
        );
        assert_eq!(
            utf8_locale_from(vars(Some("   "), None, None)),
            Some(UTF8_LOCALE)
        );
    }

    #[test]
    fn locale_constant_is_utf8() {
        // 常量本身别被改坏（改成非 UTF-8 会静默退回 ASCII，
        // 症状只有「中文文件名变问号」，不看终端根本发现不了）
        assert!(UTF8_LOCALE.to_ascii_uppercase().contains("UTF-8"));
    }

    /// 命令 PTY 必须真能跑命令并回吐输出。
    ///
    /// 这是本机会话上 `docker logs -f` / `docker exec -it` 的底座：以前这两条
    /// 通道只认 SSH，本机会话一律「该通道需要 SSH 会话」。
    ///
    /// 只跑在 Unix：夹具用的是 `printf`，Windows 的 shell 是 PowerShell，
    /// 写法完全不同 —— 用 `cfg` 收窄而不是写死 Unix 命令让对侧必红。
    #[cfg(unix)]
    #[test]
    fn command_pty_runs_and_produces_output() {
        use std::io::Read;
        let t = LocalTransport::new();
        let handle = t
            .open_command_pty("printf 'nx-pty-ok\\n'", 80, 24)
            .expect("开命令 PTY");
        let PtyHandle::Local {
            // 名字故意不是 `_`：`_` 会立刻析构，master 一掉子进程就收 SIGHUP，
            // 输出还没读到就读不到了（这是本用例最容易踩的坑）。
            io: _io,
            reader,
            child,
            mut killer,
        } = handle
        else {
            panic!("本地传输必须返回 Local 句柄");
        };

        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut r = reader;
            let mut buf = [0u8; 4096];
            let mut acc = String::new();
            while let Ok(n) = r.read(&mut buf) {
                if n == 0 {
                    break;
                }
                acc.push_str(&String::from_utf8_lossy(&buf[..n]));
                if acc.contains("nx-pty-ok") {
                    break;
                }
            }
            let _ = tx.send(acc);
        });
        let got = rx
            .recv_timeout(Duration::from_secs(10))
            .expect("10s 内应拿到命令输出");
        assert!(got.contains("nx-pty-ok"), "实拿输出：{got:?}");

        let _ = killer.kill();
        let mut c = child.lock().unwrap_or_else(|e| e.into_inner());
        let _ = c.wait();
    }
}
