//! SSH 传输（§5.2，russh）：PTY / exec / SFTP 复用同一条连接。
//!
//! - known_hosts 默认 Strict：首次连接返回 [`AppError::HostKeyPending`]，
//!   前端弹指纹确认；变更时绝不静默接受。
//! - keepalive 30s × 3，无 inactivity 超时（不踢交互会话）。
//! - 代理仅使用显式配置（socks5:// http://），绝不读系统代理（§12.1）。

use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use russh::client::{self, Handle};
use russh::keys::{HashAlg, PrivateKeyWithHashAlg};
use russh_sftp::client::SftpSession;

use super::{ExecResult, FileEntry, FileSystem, PtyHandle, Transport};
use crate::error::{AppError, AppResult};
use crate::store::Store;

/// 连接参数。
#[derive(Debug, Clone)]
pub struct SshConnectParams {
    pub host: String,
    pub port: u16,
    pub username: String,
    pub auth: SshAuth,
    /// 显式代理：socks5://host:port 或 http://host:port；None = 直连。
    pub proxy: Option<String>,
    pub connect_timeout: Duration,
    /// 首次连接自动接受指纹（内网测试用；默认 false 走确认流程）。
    pub auto_accept_unknown: bool,
}

#[derive(Debug, Clone)]
pub enum SshAuth {
    Password(String),
    Key {
        path: String,
        passphrase: Option<String>,
    },
    Agent,
}

/// 待确认的主机指纹。
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HostKeyInfo {
    pub host: String,
    pub port: u16,
    pub key_type: String,
    pub fingerprint: String,
}

/// russh 客户端 Handler：主机指纹校验。
pub struct NexTermHandler {
    pub store: Arc<Store>,
    pub host: String,
    pub port: u16,
    pub strict: bool,
    pub auto_accept_unknown: bool,
    /// 每次校验把指纹信息暂存（供上层错误提示）。
    pub last_key: Arc<std::sync::Mutex<Option<HostKeyInfo>>>,
}

impl client::Handler for NexTermHandler {
    type Error = AppError;

    async fn check_server_key(
        &mut self,
        server_public_key: &russh::keys::PublicKeyOrCertificate,
    ) -> Result<bool, Self::Error> {
        let public_key = server_public_key.public_key();
        let fingerprint = public_key.fingerprint(HashAlg::Sha256).to_string();
        let key_type = public_key.algorithm().to_string();
        let info = HostKeyInfo {
            host: self.host.clone(),
            port: self.port,
            key_type: key_type.clone(),
            fingerprint: fingerprint.clone(),
        };
        *self.last_key.lock().unwrap_or_else(|e| e.into_inner()) = Some(info);

        match self
            .store
            .known_host_get(&self.host, self.port as i32, &key_type)
            .await
        {
            Ok(Some(known)) => {
                if known.fingerprint == fingerprint {
                    Ok(true)
                } else if self.strict {
                    // 主机密钥变更：绝不静默接受（§12.1）
                    tracing::warn!(target: "ssh", host = %self.host, "主机指纹变更，拒绝连接");
                    Ok(false)
                } else {
                    let _ = self
                        .store
                        .known_host_accept(&self.host, self.port as i32, &key_type, &fingerprint)
                        .await;
                    Ok(true)
                }
            }
            Ok(None) => {
                if self.auto_accept_unknown {
                    let _ = self
                        .store
                        .known_host_accept(&self.host, self.port as i32, &key_type, &fingerprint)
                        .await;
                    return Ok(true);
                }
                // Strict 首连：不接受，上层把指纹抛给用户确认
                Ok(false)
            }
            Err(e) => Err(e),
        }
    }
}

/// SSH 传输实现。
pub struct SshTransport {
    pub params: SshConnectParams,
    pub store: Arc<Store>,
    pub session_id: String,
    handle: std::sync::Arc<tokio::sync::Mutex<Handle<NexTermHandler>>>,
    sftp: tokio::sync::Mutex<Option<Arc<SftpSession>>>,
    closed: std::sync::atomic::AtomicBool,
}

impl SshTransport {
    pub async fn connect(
        params: SshConnectParams,
        store: Arc<Store>,
        session_id: String,
    ) -> AppResult<Arc<Self>> {
        let config = Arc::new(client::Config {
            inactivity_timeout: None, // 不超时踢交互会话（§5.2）
            keepalive_interval: Some(Duration::from_secs(30)),
            keepalive_max: 3,
            ..Default::default()
        });
        let strict = !params.auto_accept_unknown;
        let last_key: Arc<std::sync::Mutex<Option<HostKeyInfo>>> =
            Arc::new(std::sync::Mutex::new(None));
        let handler = NexTermHandler {
            store: Arc::clone(&store),
            host: params.host.clone(),
            port: params.port,
            strict,
            auto_accept_unknown: params.auto_accept_unknown,
            last_key: Arc::clone(&last_key),
        };

        let connect_io = async {
            match params.proxy.as_deref() {
                Some(url) if url.starts_with("socks5://") => {
                    let stream = socks5_stream(url, &params.host, params.port).await?;
                    client::connect_stream(config, stream, handler).await
                }
                Some(url) if url.starts_with("http://") => {
                    let stream = http_connect_stream(url, &params.host, params.port).await?;
                    client::connect_stream(config, stream, handler).await
                }
                // 未配置代理 → 直连，绝不读系统代理（§12.1）
                _ => client::connect(config, (params.host.as_str(), params.port), handler).await,
            }
        };
        let mut handle: Handle<NexTermHandler> = match tokio::time::timeout(
            params.connect_timeout.max(Duration::from_secs(10)),
            connect_io,
        )
        .await
        {
            Ok(Ok(h)) => h,
            Ok(Err(e @ AppError::Ssh(_))) if !params.auto_accept_unknown => {
                // 指纹未确认 → 抛 HostKeyPending 给前端
                let pending = last_key.lock().unwrap_or_else(|e| e.into_inner()).clone();
                if let Some(info) = pending {
                    // 已记录的指纹匹配失败时也走确认
                    let known = store
                        .known_host_get(&params.host, params.port as i32, &info.key_type)
                        .await
                        .ok()
                        .flatten();
                    match known {
                        Some(k) if k.fingerprint == info.fingerprint => return Err(e),
                        _ => {
                            return Err(AppError::HostKeyPending {
                                host: info.host,
                                port: info.port,
                                key_type: info.key_type,
                                fingerprint: info.fingerprint,
                            })
                        }
                    }
                }
                return Err(e);
            }
            Ok(Err(e)) => return Err(e),
            Err(_) => {
                return Err(AppError::Timeout(format!(
                    "连接 {}:{} 超时",
                    params.host, params.port
                )))
            }
        };

        // ── 认证 ──
        let authed = match &params.auth {
            SshAuth::Password(p) => handle
                .authenticate_password(&params.username, p)
                .await
                .map_err(AppError::from)?,
            SshAuth::Key { path, passphrase } => {
                let key = russh::keys::load_secret_key(path, passphrase.as_deref())
                    .map_err(|e| AppError::Ssh(format!("私钥加载失败: {e}")))?;
                let hash = handle
                    .best_supported_rsa_hash()
                    .await
                    .map_err(AppError::from)?
                    .flatten();
                handle
                    .authenticate_publickey(
                        &params.username,
                        PrivateKeyWithHashAlg::new(Arc::new(key), hash),
                    )
                    .await
                    .map_err(AppError::from)?
            }
            SshAuth::Agent => {
                #[cfg(unix)]
                {
                    agent_auth_unix(&mut handle, &params.username).await?
                }
                #[cfg(not(unix))]
                {
                    let _ = &handle;
                    return Err(AppError::Unsupported(
                        "SSH Agent 认证在 Windows 上暂不可用（请使用密码或私钥文件）".into(),
                    ));
                }
            }
        };
        if !authed.success() {
            return Err(AppError::Ssh(format!(
                "认证失败：用户 {} 被拒绝",
                params.username
            )));
        }

        Ok(Arc::new(Self {
            params,
            store,
            session_id,
            handle: Arc::new(tokio::sync::Mutex::new(handle)),
            sftp: tokio::sync::Mutex::new(None),
            closed: std::sync::atomic::AtomicBool::new(false),
        }))
    }

    pub async fn handle(&self) -> tokio::sync::MutexGuard<'_, Handle<NexTermHandler>> {
        self.handle.lock().await
    }

    /// 打开 direct-tcpip 通道（本地端口转发用）。
    pub async fn open_direct_tcpip(
        &self,
        host: &str,
        port: u16,
        orig_addr: std::net::IpAddr,
        orig_port: u16,
    ) -> AppResult<russh::Channel<russh::client::Msg>> {
        let handle = self.handle().await;
        let channel = handle
            .channel_open_direct_tcpip(
                host,
                u32::from(port),
                orig_addr.to_string(),
                u32::from(orig_port),
            )
            .await?;
        Ok(channel)
    }

    /// 打开一个用于流式读取的 exec channel（docker logs follow 等）。
    pub async fn open_exec_channel(
        &self,
        cmd: &str,
    ) -> AppResult<(
        russh::ChannelReadHalf,
        Arc<russh::ChannelWriteHalf<russh::client::Msg>>,
    )> {
        let handle = self.handle().await;
        let channel = handle.channel_open_session().await?;
        channel.exec(true, cmd.as_bytes().to_vec()).await?;
        let (read, write) = channel.split();
        Ok((read, Arc::new(write)))
    }

    async fn sftp(&self) -> AppResult<Arc<SftpSession>> {
        let mut guard = self.sftp.lock().await;
        if let Some(s) = &*guard {
            return Ok(Arc::clone(s));
        }
        let handle = self.handle().await;
        let channel = handle.channel_open_session().await?;
        // 必须先显式请求 sftp 子系统，再把它当字节流交给 SftpSession。
        // 少了这一步，服务端那个 channel 后面没有任何进程接着（既不是 shell 也不是
        // sftp-server），我们发出去的 SFTP INIT 包不会有任何回应 —— 表现就是
        // 卡满一个超时周期后报「SFTP 初始化失败: Timeout」，而同一个连接上的终端一切正常。
        // 参照 russh-sftp 自带的 examples/client.rs。
        channel
            .request_subsystem(true, "sftp")
            .await
            .map_err(|e| AppError::Sftp(format!("SFTP 子系统请求失败: {e}")))?;
        let stream = channel.into_stream();
        let sftp = SftpSession::new(stream)
            .await
            .map_err(|e| AppError::Sftp(format!("SFTP 初始化失败: {e}")))?;
        let sftp = Arc::new(sftp);
        *guard = Some(Arc::clone(&sftp));
        Ok(sftp)
    }
}

async fn socks5_stream(
    url: &str,
    host: &str,
    port: u16,
) -> AppResult<tokio_socks::tcp::socks5::Socks5Stream<tokio::net::TcpStream>> {
    let target = format!("{host}:{port}");
    // 只用显式代理 URL，绝不读系统设置（§12.1）
    tokio_socks::tcp::socks5::Socks5Stream::connect(url, target.as_str())
        .await
        .map_err(|e| AppError::Ssh(format!("SOCKS5 代理连接失败: {e}")))
}

async fn http_connect_stream(url: &str, host: &str, port: u16) -> AppResult<tokio::net::TcpStream> {
    let proxy = url.trim_start_matches("http://").trim_end_matches('/');
    let stream = tokio::net::TcpStream::connect(proxy)
        .await
        .map_err(|e| AppError::Ssh(format!("HTTP 代理连接失败: {e}")))?;
    let (mut rd, mut wr) = stream.into_split();
    let req = format!("CONNECT {host}:{port} HTTP/1.1\r\nHost: {host}:{port}\r\n\r\n");
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    wr.write_all(req.as_bytes()).await?;
    wr.flush().await?;
    let mut buf = [0u8; 512];
    let n = rd.read(&mut buf).await?;
    let resp = String::from_utf8_lossy(&buf[..n]);
    if !resp.starts_with("HTTP/1.1 200") && !resp.starts_with("HTTP/1.0 200") {
        return Err(AppError::Ssh(format!(
            "HTTP CONNECT 被代理拒绝: {}",
            resp.lines().next().unwrap_or("")
        )));
    }
    rd.reunite(wr)
        .map_err(|_| AppError::Ssh("代理流重组失败".into()))
}

#[async_trait]
impl Transport for SshTransport {
    fn kind(&self) -> &'static str {
        "ssh"
    }

    async fn open_pty(&self, cols: u16, rows: u16) -> AppResult<PtyHandle> {
        if self.closed.load(std::sync::atomic::Ordering::Relaxed) {
            return Err(AppError::Disconnected("SSH 连接已关闭".into()));
        }
        let handle = self.handle().await;
        let channel = handle.channel_open_session().await?;
        channel
            .request_pty(true, "xterm-256color", cols as u32, rows as u32, 0, 0, &[])
            .await
            .map_err(|e| AppError::Ssh(format!("PTY 请求失败: {e}")))?;
        channel
            .request_shell(true)
            .await
            .map_err(|e| AppError::Ssh(format!("shell 请求失败: {e}")))?;
        let (read, write) = channel.split();
        Ok(PtyHandle::Ssh {
            read,
            write: Arc::new(write),
        })
    }

    async fn exec(&self, cmd: &str, timeout: Duration) -> AppResult<ExecResult> {
        if self.closed.load(std::sync::atomic::Ordering::Relaxed) {
            return Err(AppError::Disconnected("SSH 连接已关闭".into()));
        }
        let start = Instant::now();
        let handle = self.handle().await;
        let mut channel = handle.channel_open_session().await?;
        channel
            .exec(true, cmd.as_bytes().to_vec())
            .await
            .map_err(|e| AppError::Ssh(format!("exec 失败: {e}")))?;
        let mut stdout: Vec<u8> = Vec::new();
        let mut stderr: Vec<u8> = Vec::new();
        let mut exit_code: Option<i32> = None;
        let collect = async {
            while let Some(msg) = channel.wait().await {
                match msg {
                    russh::ChannelMsg::Data { data } => {
                        if stdout.len() < 8 * 1024 * 1024 {
                            stdout.extend_from_slice(&data);
                        }
                    }
                    russh::ChannelMsg::ExtendedData { data, .. } => {
                        if stderr.len() < 2 * 1024 * 1024 {
                            stderr.extend_from_slice(&data);
                        }
                    }
                    russh::ChannelMsg::ExitStatus { exit_status: c } => {
                        exit_code = Some(c as i32);
                    }
                    // Eof 之后仍可能有 ExitStatus/Close，只有 Close/None 才结束
                    russh::ChannelMsg::Eof => {}
                    russh::ChannelMsg::Close => break,
                    _ => {}
                }
            }
        };
        if tokio::time::timeout(timeout, collect).await.is_err() {
            return Err(AppError::Timeout(format!(
                "命令超时（{}s）: {}",
                timeout.as_secs(),
                cmd
            )));
        }
        let stdout_s = String::from_utf8_lossy(&stdout).into_owned();
        let stderr_s = String::from_utf8_lossy(&stderr).into_owned();
        let (stdout, t1) = super::cap_text(&stdout_s);
        let (stderr, t2) = super::cap_text(&stderr_s);
        Ok(ExecResult {
            stdout,
            stderr,
            exit_code,
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: t1 || t2,
        })
    }

    async fn fs(&self) -> AppResult<Arc<dyn FileSystem>> {
        Ok(Arc::new(SftpFs {
            sftp: self.sftp().await?,
        }))
    }

    async fn ping(&self) -> AppResult<Duration> {
        let start = Instant::now();
        self.exec(": ; true", Duration::from_secs(10)).await?;
        Ok(start.elapsed())
    }

    fn as_any(&self) -> &dyn std::any::Any {
        self
    }

    fn as_any_arc(self: Arc<Self>) -> Arc<dyn std::any::Any + Send + Sync> {
        self
    }

    async fn close(&self) {
        self.closed
            .store(true, std::sync::atomic::Ordering::Relaxed);
        let handle = self.handle.lock().await;
        let _ = handle
            .disconnect(russh::Disconnect::ByApplication, "bye", "")
            .await;
    }
}

/// SFTP 文件系统。
pub struct SftpFs {
    pub sftp: Arc<SftpSession>,
}

impl SftpFs {
    /// 把 `~` 展开成远端家目录。
    ///
    /// SFTP 协议本身不认 `~`（那是 shell 的语法糖），OpenSSH 的 sftp-server 会把会话工作
    /// 目录设成用户家目录，所以 `realpath(".")` 拿到的就是家目录。
    /// 前端左栏文件树的第一发请求就是 `~`，不做这一步每个 SSH 会话都要先白等一次失败。
    async fn real(&self, path: &str) -> AppResult<String> {
        if !path.starts_with('~') {
            return Ok(path.to_string());
        }
        let home = self
            .sftp
            .canonicalize(".")
            .await
            .map_err(|e| AppError::Sftp(format!("无法确定远端家目录: {e}")))?;
        let rest = path[1..].trim_start_matches('/');
        Ok(if rest.is_empty() {
            home
        } else {
            format!("{}/{}", home.trim_end_matches('/'), rest)
        })
    }

    fn entry_from(
        name: String,
        path: String,
        meta: &russh_sftp::client::fs::Metadata,
    ) -> FileEntry {
        let kind = if meta.is_dir() {
            "dir"
        } else if meta.is_symlink() {
            "symlink"
        } else {
            "file"
        };
        FileEntry {
            name,
            path,
            kind: kind.to_string(),
            size: meta.size.unwrap_or(0),
            mode: meta
                .permissions
                .map(|p| format!("{:o}", p & 0o7777))
                .unwrap_or_default(),
            owner: meta.user.clone(),
            group: meta.group.clone(),
            mtime: i64::from(meta.mtime.unwrap_or(0)) * 1000,
            symlink_target: None,
        }
    }
}

#[async_trait]
impl FileSystem for SftpFs {
    async fn list(&self, path: &str) -> AppResult<Vec<FileEntry>> {
        let path = self.real(path).await?;
        let rd = self
            .sftp
            .read_dir(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("列目录失败: {e}")))?;
        let mut out = Vec::new();
        for entry in rd {
            let name = entry.file_name();
            if name == "." || name == ".." {
                continue;
            }
            let path_full = if path.ends_with('/') {
                format!("{path}{name}")
            } else {
                format!("{path}/{name}")
            };
            out.push(Self::entry_from(name, path_full, &entry.metadata()));
        }
        out.sort_by(|a, b| {
            (b.kind == "dir")
                .cmp(&(a.kind == "dir"))
                .then(a.name.cmp(&b.name))
        });
        Ok(out)
    }

    async fn read_file(&self, path: &str, max_bytes: u64) -> AppResult<Vec<u8>> {
        let path = self.real(path).await?;
        let meta = self
            .sftp
            .metadata(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("stat 失败: {e}")))?;
        let size = meta.size.unwrap_or(0);
        if size > max_bytes {
            return Err(AppError::param(format!(
                "文件超过读取上限（{size} > {max_bytes} 字节）"
            )));
        }
        let mut file = self
            .sftp
            .open(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("打开失败: {e}")))?;
        use tokio::io::AsyncReadExt;
        let mut buf = Vec::with_capacity(size as usize);
        file.read_to_end(&mut buf)
            .await
            .map_err(|e| AppError::Sftp(format!("读取失败: {e}")))?;
        Ok(buf)
    }

    async fn write_file(&self, path: &str, data: &[u8], backup: bool) -> AppResult<()> {
        let path = self.real(path).await?;
        if backup && self.exists(&path).await? {
            let backup_path = format!("{path}.nexterm-bak");
            let src = self
                .sftp
                .open(&path)
                .await
                .map_err(|e| AppError::Sftp(e.to_string()))?;
            let dst = self
                .sftp
                .create(&backup_path)
                .await
                .map_err(|e| AppError::Sftp(e.to_string()))?;
            let (mut r, mut w) = tokio::io::split(src);
            // 简化：一次性拷贝
            use tokio::io::AsyncReadExt;
            let mut all = Vec::new();
            r.read_to_end(&mut all)
                .await
                .map_err(|e| AppError::Sftp(e.to_string()))?;
            tokio::io::copy(&mut all.as_slice(), &mut w)
                .await
                .map_err(|e| AppError::Sftp(e.to_string()))?;
            w.flush().await.map_err(|e| AppError::Sftp(e.to_string()))?;
            dst.sync_all().await.ok();
        }
        let mut file = self
            .sftp
            .create(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("创建失败: {e}")))?;
        use tokio::io::AsyncWriteExt;
        file.write_all(data)
            .await
            .map_err(|e| AppError::Sftp(format!("写入失败: {e}")))?;
        file.flush()
            .await
            .map_err(|e| AppError::Sftp(e.to_string()))?;
        file.sync_all().await.ok();
        Ok(())
    }

    async fn mkdir(&self, path: &str) -> AppResult<()> {
        let path = self.real(path).await?;
        self.sftp
            .create_dir(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("mkdir 失败: {e}")))
    }

    async fn rename(&self, from: &str, to: &str) -> AppResult<()> {
        let from = self.real(from).await?;
        let to = self.real(to).await?;
        self.sftp
            .rename(&from, &to)
            .await
            .map_err(|e| AppError::Sftp(format!("重命名失败: {e}")))
    }

    async fn delete(&self, path: &str, is_dir: bool) -> AppResult<()> {
        let path = self.real(path).await?;
        let r = if is_dir {
            self.sftp.remove_dir(&path).await
        } else {
            self.sftp.remove_file(&path).await
        };
        r.map_err(|e| AppError::Sftp(format!("删除失败: {e}")))
    }

    async fn chmod(&self, path: &str, mode: u32) -> AppResult<()> {
        let path = self.real(path).await?;
        let meta = russh_sftp::client::fs::Metadata {
            permissions: Some(mode),
            ..Default::default()
        };
        self.sftp
            .set_metadata(&path, meta)
            .await
            .map_err(|e| AppError::Sftp(format!("chmod 失败: {e}")))
    }

    async fn checksum(&self, path: &str, algo: &str) -> AppResult<String> {
        // 远端无哈希依赖：读取后本地计算（限 64MB）
        let data = self.read_file(path, 64 * 1024 * 1024).await?;
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
        let path = self.real(path).await?;
        Ok(self.sftp.try_exists(&path).await.unwrap_or(false))
    }

    async fn size(&self, path: &str) -> AppResult<u64> {
        let path = self.real(path).await?;
        let meta = self
            .sftp
            .metadata(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("stat 失败: {e}")))?;
        Ok(meta.size.unwrap_or(0))
    }

    async fn open_read(&self, path: &str) -> AppResult<Box<dyn super::RemoteRead>> {
        let path = self.real(path).await?;
        let file = self
            .sftp
            .open(&path)
            .await
            .map_err(|e| AppError::Sftp(format!("打开失败: {e}")))?;
        let size = file.metadata().await.ok().and_then(|m| m.size).unwrap_or(0);
        Ok(Box::new(SftpReader { file, size }))
    }

    async fn open_write(&self, path: &str, append: bool) -> AppResult<Box<dyn super::RemoteWrite>> {
        let path = self.real(path).await?;
        let file = if append {
            use tokio::io::AsyncSeekExt;
            let mut f = self
                .sftp
                .open_with_flags(
                    &path,
                    russh_sftp::protocol::OpenFlags::WRITE
                        | russh_sftp::protocol::OpenFlags::CREATE,
                )
                .await
                .map_err(|e| AppError::Sftp(format!("打开失败: {e}")))?;
            let _ = f.seek(std::io::SeekFrom::End(0)).await;
            f
        } else {
            self.sftp
                .create(&path)
                .await
                .map_err(|e| AppError::Sftp(format!("创建失败: {e}")))?
        };
        Ok(Box::new(SftpWriter { file }))
    }
}

/// SFTP 流式读。
pub struct SftpReader {
    file: russh_sftp::client::fs::File,
    size: u64,
}

#[async_trait]
impl super::RemoteRead for SftpReader {
    async fn read(&mut self, buf: &mut [u8]) -> AppResult<usize> {
        use tokio::io::AsyncReadExt;
        self.file
            .read(buf)
            .await
            .map_err(|e| AppError::Sftp(format!("读取失败: {e}")))
    }
    fn size(&self) -> u64 {
        self.size
    }
}

/// SFTP 流式写。
pub struct SftpWriter {
    file: russh_sftp::client::fs::File,
}

#[async_trait]
impl super::RemoteWrite for SftpWriter {
    async fn write(&mut self, data: &[u8]) -> AppResult<()> {
        use tokio::io::AsyncWriteExt;
        self.file
            .write_all(data)
            .await
            .map_err(|e| AppError::Sftp(format!("写入失败: {e}")))
    }
    async fn finish(&mut self) -> AppResult<()> {
        use tokio::io::AsyncWriteExt;
        self.file
            .flush()
            .await
            .map_err(|e| AppError::Sftp(e.to_string()))?;
        self.file.sync_all().await.ok();
        Ok(())
    }
}

/// Unix 下的 SSH Agent 认证（SSH_AUTH_SOCK）。
#[cfg(unix)]
async fn agent_auth_unix(
    handle: &mut Handle<NexTermHandler>,
    username: &str,
) -> AppResult<russh::client::AuthResult> {
    use russh::keys::PrivateKeyWithHashAlg;
    let mut agent = russh::keys::agent::client::AgentClient::connect_env()
        .await
        .map_err(|e| AppError::Ssh(format!("SSH Agent 不可用: {e}")))?;
    let identities = agent
        .request_identities()
        .await
        .map_err(|e| AppError::Ssh(format!("Agent 身份列表失败: {e}")))?;
    let mut result = None;
    for id in identities {
        let hash = handle
            .best_supported_rsa_hash()
            .await
            .map_err(AppError::from)?
            .flatten();
        match handle
            .authenticate_publickey_with(username, id, hash, &mut agent)
            .await
        {
            Ok(r) => {
                result = Some(r);
                if r.success() {
                    break;
                }
            }
            Err(e) => return Err(AppError::Ssh(format!("Agent 认证失败: {e}"))),
        }
    }
    result.ok_or_else(|| AppError::Ssh("Agent 无可用身份".into()))
}
