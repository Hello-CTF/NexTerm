//! 端口转发 / 隧道（M3-T8 / §5.2）：本地静态转发 + SOCKS5 动态转发。
//!
//! 用途：复用 SSH 会话打通内网服务，零额外连接。
//!
//! 两种形态的区别：
//! - **本地静态转发**（`spawn_local_forward`）：建的那一刻就定死目标，
//!   `本地端口 → SSH → target_host:target_port`。适合「我只要连这台 MySQL」。
//! - **SOCKS5 动态转发**（`spawn_socks_forward`）：目标由**客户端当场指定**，
//!   一个端口能当通用代理用（浏览器 / `curl --socks5` / proxychains 都行）。
//!   适合「我要在那台机器所在的网段里随便逛」。

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;

use crate::error::{AppError, AppResult};

#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ForwardSpec {
    pub id: String,
    pub session_id: String,
    /// 本地监听端口。
    pub listen_port: u16,
    /// 静态转发的目标主机；SOCKS5 动态转发没有固定目标，为 `null`。
    pub target_host: Option<String>,
    /// 同上。
    pub target_port: Option<u16>,
    /// `local`（静态本地转发）或 `socks`（SOCKS5 动态转发）。
    pub kind: String,
    pub created_at: u64,
}

/// 运行中的转发。
pub struct RunningForward {
    pub spec: ForwardSpec,
    stop: Arc<tokio_util::sync::CancellationToken>,
    pub connections: Arc<AtomicU64>,
    active: Arc<AtomicBool>,
}

impl RunningForward {
    pub fn is_active(&self) -> bool {
        self.active.load(Ordering::Relaxed)
    }

    pub fn stop(&self) {
        self.stop.cancel();
        self.active.store(false, Ordering::Relaxed);
    }
}

/// 起监听循环并把 running 状态装好 —— 两种转发共用的那一段。
///
/// `handle` 拿到一条已接受的连接，负责把它变成"经 SSH 出去的一条流"；
/// 具体做什么由调用方决定（静态转发是定目标，动态转发是先谈 SOCKS5 协议）。
fn spawn_listener<F, Fut>(
    spec: ForwardSpec,
    listener: tokio::net::TcpListener,
    handle: F,
) -> RunningForward
where
    F: Fn(tokio::net::TcpStream, std::net::SocketAddr) -> Fut + Send + 'static,
    Fut: std::future::Future<Output = ()> + Send + 'static,
{
    let stop = Arc::new(tokio_util::sync::CancellationToken::new());
    let connections = Arc::new(AtomicU64::new(0));
    let active = Arc::new(AtomicBool::new(true));

    let stop2 = Arc::clone(&stop);
    let active2 = Arc::clone(&active);
    let conns = Arc::clone(&connections);
    tokio::spawn(async move {
        loop {
            tokio::select! {
                _ = stop2.cancelled() => break,
                accepted = listener.accept() => {
                    match accepted {
                        Ok((local, addr)) => {
                            // 计数在这里统一加：两种转发的"一条连接"语义一致
                            // （静态 = 一次 direct-tcpip，动态 = 一次 SOCKS5 会话）
                            conns.fetch_add(1, Ordering::Relaxed);
                            tokio::spawn(handle(local, addr));
                        }
                        // accept 失败一般是 listener 被打断或 fd 耗尽，继续等即可
                        Err(_) => continue,
                    }
                }
            }
        }
        active2.store(false, Ordering::Relaxed);
    });

    RunningForward {
        spec,
        stop,
        connections,
        active,
    }
}

/// 绑 127.0.0.1:listen_port；失败时给出人话（端口被占是最常见的）。
async fn bind_local(listen_port: u16) -> AppResult<tokio::net::TcpListener> {
    tokio::net::TcpListener::bind(("127.0.0.1", listen_port))
        .await
        .map_err(|e| AppError::param(format!("端口 {listen_port} 监听失败: {e}")))
}

fn new_spec(
    session_id: &str,
    listen_port: u16,
    kind: &str,
    target: Option<(String, u16)>,
) -> ForwardSpec {
    let (target_host, target_port) = match target {
        Some((h, p)) => (Some(h), Some(p)),
        None => (None, None),
    };
    ForwardSpec {
        id: crate::ids::new_id(),
        session_id: session_id.to_string(),
        listen_port,
        target_host,
        target_port,
        kind: kind.to_string(),
        created_at: crate::ids::now_ms(),
    }
}

/// 启动一条本地静态转发：`本地 listen_port → SSH → target`。
pub async fn spawn_local_forward(
    session_id: &str,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<RunningForward> {
    let listener = bind_local(listen_port).await?;
    let spec = new_spec(
        session_id,
        listen_port,
        "local",
        Some((target_host.clone(), target_port)),
    );
    Ok(spawn_listener(spec, listener, move |mut local, addr| {
        let ssh = Arc::clone(&ssh);
        let target_host = target_host.clone();
        async move {
            // 打开失败就只是这一条连接失败，不能把监听循环带崩
            let channel = match ssh
                .open_direct_tcpip(&target_host, target_port, addr.ip(), addr.port())
                .await
            {
                Ok(ch) => ch,
                Err(e) => {
                    tracing::warn!(target: "forward", error = %e, "direct-tcpip 打开失败");
                    return;
                }
            };
            let mut remote = channel.into_stream();
            let _ = tokio::io::copy_bidirectional(&mut local, &mut remote).await;
        }
    }))
}

/// 启动一条 SOCKS5 动态转发：本地 `listen_port` 上跑 SOCKS5 服务端，
/// 每条 CONNECT 都由客户端指定目标，再经这条 SSH 出去。
///
/// 只监听 `127.0.0.1`：这是个**无认证**的代理，绑到 `0.0.0.0` 等于把
/// 内网入口白送给同网段的人。要给别人用应该各自建转发。
pub async fn spawn_socks_forward(
    session_id: &str,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    listen_port: u16,
) -> AppResult<RunningForward> {
    let listener = bind_local(listen_port).await?;
    let spec = new_spec(session_id, listen_port, "socks", None);
    Ok(spawn_listener(spec, listener, move |local, addr| {
        let ssh = Arc::clone(&ssh);
        async move {
            if let Err(e) = socks5_serve(local, ssh, addr).await {
                // 客户端随便断开是常态，所以是 debug 不是 warn
                tracing::debug!(target: "forward", peer = %addr, error = %e, "socks5 会话结束");
            }
        }
    }))
}

/* ── SOCKS5（RFC 1928）───────────────────────────────────────────────────
 *
 * 只实现「动态转发」真正需要的那一小块，刻意不做的东西：
 * - **不做 BIND / UDP ASSOCIATE**：SSH 的 direct-tcpip 只给 TCP，UDP 得走别的机制；
 * - **不做用户名密码认证**：监听在 127.0.0.1，能连上的本来就已经是本机用户；
 * - **不做 BND.ADDR/BND.PORT 回填**：我们拿不到出口侧的真实绑定地址，
 *   RFC 允许填 0.0.0.0:0，客户端（curl / 浏览器）不会用它做任何决策。
 */

const SOCKS_VER: u8 = 0x05;
const SOCKS_NO_AUTH: u8 = 0x00;
const SOCKS_NO_ACCEPTABLE: u8 = 0xFF;
const SOCKS_CMD_CONNECT: u8 = 0x01;

const ATYP_IPV4: u8 = 0x01;
const ATYP_DOMAIN: u8 = 0x03;
const ATYP_IPV6: u8 = 0x04;

/// REP（应答码）：成功。
const REP_SUCCESS: u8 = 0x00;
/// REP：连接被拒（目标不可达 / SSH 侧打不开）。
const REP_CONNECTION_REFUSED: u8 = 0x05;
/// REP：不支持的命令。
const REP_CMD_NOT_SUPPORTED: u8 = 0x07;
/// REP：不支持的地址类型。
const REP_ATYP_NOT_SUPPORTED: u8 = 0x08;

/// 处理一条 SOCKS5 连接：握手 → CONNECT → 双向泵。
///
/// 返回 `Err` 只用于日志；协议层的失败已经以 REP 码回给客户端了
/// （不回码客户端只会干等，比直接断开更难排查）。
async fn socks5_serve(
    mut local: tokio::net::TcpStream,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    peer: std::net::SocketAddr,
) -> Result<(), String> {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    // ── 1) 握手：VER, NMETHODS, METHODS[NMETHODS] ──
    let mut head = [0u8; 2];
    local
        .read_exact(&mut head)
        .await
        .map_err(|e| format!("读握手头失败: {e}"))?;
    if head[0] != SOCKS_VER {
        // 不是 SOCKS5（可能是 SOCKS4 / 直接乱敲），没法按 5 的格式回话
        return Err(format!("不是 SOCKS5（VER=0x{:02x}）", head[0]));
    }
    let mut methods = vec![0u8; head[1] as usize];
    local
        .read_exact(&mut methods)
        .await
        .map_err(|e| format!("读方法列表失败: {e}"))?;
    if !methods.contains(&SOCKS_NO_AUTH) {
        let _ = local.write_all(&[SOCKS_VER, SOCKS_NO_ACCEPTABLE]).await;
        return Err("客户端不接受无认证方式".into());
    }
    local
        .write_all(&[SOCKS_VER, SOCKS_NO_AUTH])
        .await
        .map_err(|e| format!("回握手失败: {e}"))?;

    // ── 2) 请求：VER, CMD, RSV, ATYP, ADDR, PORT ──
    let mut req = [0u8; 4];
    local
        .read_exact(&mut req)
        .await
        .map_err(|e| format!("读请求头失败: {e}"))?;
    if req[0] != SOCKS_VER {
        return Err(format!("请求 VER=0x{:02x} 不是 SOCKS5", req[0]));
    }
    if req[1] != SOCKS_CMD_CONNECT {
        socks5_reply(&mut local, REP_CMD_NOT_SUPPORTED).await;
        return Err(format!("只支持 CONNECT，收到 CMD=0x{:02x}", req[1]));
    }
    let (host, port) = match read_target(&mut local, req[3]).await {
        Ok(v) => v,
        Err(e) => {
            socks5_reply(&mut local, REP_ATYP_NOT_SUPPORTED).await;
            return Err(e);
        }
    };

    // ── 3) 经这条 SSH 出去 ──
    //
    // orig_addr 用**客户端的**地址：有些 sshd 的日志/ACL 会看它，
    // 填 127.0.0.1 反而会让服务端以为是本机发起的。
    let channel = match ssh
        .open_direct_tcpip(&host, port, peer.ip(), peer.port())
        .await
    {
        Ok(ch) => ch,
        Err(e) => {
            socks5_reply(&mut local, REP_CONNECTION_REFUSED).await;
            return Err(format!("direct-tcpip {host}:{port} 失败: {e}"));
        }
    };
    if local
        .write_all(&socks5_reply_bytes(REP_SUCCESS))
        .await
        .is_err()
    {
        return Err("回成功应答失败".into());
    }

    // ── 4) 双向泵 ──
    let mut remote = channel.into_stream();
    tokio::io::copy_bidirectional(&mut local, &mut remote)
        .await
        .map_err(|e| format!("转发中断: {e}"))?;
    Ok(())
}

/// 解析 CONNECT 的目标（ATYP 之后的 ADDR + PORT）。
///
/// 域名**不在这里解析** —— 原样交给 SSH 服务端去 resolve。
/// 这是 SOCKS5 动态转发最有用的地方：`db.internal` 这种只在内网 DNS 里存在的
/// 名字，本地根本解析不了，但目标机解析得了。
async fn read_target(local: &mut tokio::net::TcpStream, atyp: u8) -> Result<(String, u16), String> {
    use tokio::io::AsyncReadExt;

    let host = match atyp {
        ATYP_IPV4 => {
            let mut b = [0u8; 4];
            local
                .read_exact(&mut b)
                .await
                .map_err(|e| format!("读 IPv4 失败: {e}"))?;
            std::net::Ipv4Addr::from(b).to_string()
        }
        ATYP_IPV6 => {
            let mut b = [0u8; 16];
            local
                .read_exact(&mut b)
                .await
                .map_err(|e| format!("读 IPv6 失败: {e}"))?;
            std::net::Ipv6Addr::from(b).to_string()
        }
        ATYP_DOMAIN => {
            let mut len = [0u8; 1];
            local
                .read_exact(&mut len)
                .await
                .map_err(|e| format!("读域名长度失败: {e}"))?;
            let mut b = vec![0u8; len[0] as usize];
            local
                .read_exact(&mut b)
                .await
                .map_err(|e| format!("读域名失败: {e}"))?;
            String::from_utf8(b).map_err(|_| "域名不是合法 UTF-8".to_string())?
        }
        other => return Err(format!("不支持的 ATYP=0x{other:02x}")),
    };

    let mut pb = [0u8; 2];
    local
        .read_exact(&mut pb)
        .await
        .map_err(|e| format!("读端口失败: {e}"))?;
    Ok((host, u16::from_be_bytes(pb)))
}

/// 一条 SOCKS5 应答：VER, REP, RSV, ATYP=IPv4, BND.ADDR=0.0.0.0, BND.PORT=0。
fn socks5_reply_bytes(rep: u8) -> [u8; 10] {
    [SOCKS_VER, rep, 0x00, ATYP_IPV4, 0, 0, 0, 0, 0, 0]
}

/// 尽力回一条应答（失败就算了，反正接下来要断开）。
async fn socks5_reply(local: &mut tokio::net::TcpStream, rep: u8) {
    use tokio::io::AsyncWriteExt;
    let _ = local.write_all(&socks5_reply_bytes(rep)).await;
    let _ = local.flush().await;
}
