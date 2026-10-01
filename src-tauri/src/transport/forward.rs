//! 端口转发 / 隧道（M3-T8 / §5.2）：静态转发 + SOCKS5 动态转发。
//!
//! 用途：复用 SSH 会话打通内网服务，零额外连接。
//!
//! 两种形态的区别：
//! - **静态转发**（`spawn_local_forward`）：建的那一刻就定死目标，
//!   `监听 bind_ip:listen_port → SSH → target_host:target_port`。适合「我只要连这台 MySQL」。
//! - **SOCKS5 动态转发**（`spawn_socks_forward`）：目标由**客户端当场指定**，
//!   一个端口能当通用代理用（浏览器 / `curl --socks5` / proxychains 都行）。
//!   适合「我要在那台机器所在的网段里随便逛」。
//!
//! ⚠️ 「监听在哪」不是硬编码的回环地址，而是由 `ForwardPolicy` 决定：
//! 桌面形态绑 `127.0.0.1`（只给本机用），服务端形态绑 `0.0.0.0`
//! （外部客户端才够得到 —— 服务端上这条功能的意义就是「把远端服务搬到服务端的端口上」）。
//! 懒猫微服上整个功能被禁用，理由与安全边界见 `ForwardPolicy` 的说明。

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;

use crate::error::{AppError, AppResult};

/// 转发策略：**启动时定死**的「在哪监听、以及能不能用」。
///
/// # 为什么需要它
///
/// 「本地端口转发」在三种情形下语义完全不同，而它们共用同一份代码：
///
/// | 情形 | 监听地址 | 能不能用 |
/// |---|---|---|
/// | 桌面（Tauri 壳，`desktop` feature） | `127.0.0.1` | 能 —— 就是字面意义的「本机端口」 |
/// | 服务端 · 自有部署 | `0.0.0.0` | 能 —— 「把远端服务搬到服务端端口上」，这正是它存在的理由 |
/// | 服务端 · 懒猫微服 | `0.0.0.0` | **不能**（见下） |
///
/// # 懒猫上为什么直接禁用
///
/// 不是因为技术上做不到（平台其实原生支持四层转发），而是因为那条路**没有任何鉴权**：
/// 它是裸 TCP，官方原文写明「微服系统仅能提供底层虚拟网络的保护，从原理上无法提供鉴权流程」。
/// 也就是说平台上没有一层能替本应用挡住「能连上端口的任何人」—— 而 `/rpc` 的权限等价于
/// 完整控制权。所以这里选择**在命令层直接拒绝创建**，而不是「建了但外面连不上」：
/// 后者会让用户以为是自己配错了，是最难查的一类症状。
///
/// 另外，想从外部连内网服务，微服平台自身的转发功能比本应用代劳更合适 ——
/// 界面上的提示文案说的就是这件事。
///
/// # 监听地址的差异是编译期定的
///
/// 桌面形态监听回环、服务端形态监听全部地址，这个差异由 `cfg!(feature = "desktop")`
/// 决定，不需要运行期开关 —— 两种形态本来就是两个二进制（见 `Cargo.toml` 的 `[[bin]]`）。
#[derive(Debug, Clone)]
pub struct ForwardPolicy {
    /// 转发监听地址。
    pub bind_ip: std::net::IpAddr,
    /// 本平台是否允许端口转发。
    pub available: bool,
    /// 部署平台标识：`lazycat` / `other`。只用于回报给界面与文案判定。
    pub platform: &'static str,
}

/// `NEXTERM_PLATFORM` 的判定：只认 `lazycat`（大小写、首尾空白都不计较）。
///
/// 抽成纯函数是为了能直接测。测试里若改用 `std::env::set_var` 造场景，
/// 进程级环境变量会让并行的其他测试跟着抖（Rust 测试默认多线程），
/// 于是「偶尔红一次」的测试比没有测试更糟。
fn is_lazycat(raw: Option<&str>) -> bool {
    raw.map(|v| v.trim().eq_ignore_ascii_case("lazycat"))
        .unwrap_or(false)
}

impl ForwardPolicy {
    /// 探测当前策略。**整个进程只调用一次**（`AppState::new`），结果存进 `AppState`。
    ///
    /// 这里读环境变量而不是走 `server/cli.rs` 的参数解析，是刻意的：
    /// `NEXTERM_PLATFORM` 描述的是「这份部署跑在哪个平台上」这个**事实**，
    /// 不是用户可调的运行参数 —— 给它一个命令行开关，等于允许把「懒猫」覆盖成「非懒猫」，
    /// 而那个开关一旦被误用，界面就会承诺一个平台根本不会兑现的能力。
    ///
    /// 与 `blobs` 那几处「各自去读环境变量」的旧写法不同：那些是**读同一个值读到分家**
    /// （命令行传了 `--data-dir` 就与库不一致）。这里只读一次、只存一份，没有那个问题。
    pub fn detect() -> Self {
        // 桌面形态：目标就是「把远端的口子搬到本机」，监听回环是唯一说得通的地址 ——
        // 绑 0.0.0.0 等于把内网入口白送给同网段的人（SOCKS5 尤其如此，它本身无认证）。
        if cfg!(feature = "desktop") {
            return Self {
                bind_ip: std::net::IpAddr::V4(std::net::Ipv4Addr::LOCALHOST),
                available: true,
                platform: "other",
            };
        }

        // 服务端形态：操作者自己部署、自己负责边界。这跟 `/rpc` 本身没有自身鉴权是同一件事
        // （见 `server/mod.rs` 的非回环警告：默认值就必须是 0.0.0.0，否则容器里连不上）。
        // 所以这里绑全部地址，让转发真的可达；风险由「非回环监听」那条警告 + 操作者自己的
        // 网络边界承担。
        let lazycat = is_lazycat(std::env::var("NEXTERM_PLATFORM").ok().as_deref());
        Self {
            bind_ip: std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED),
            available: !lazycat,
            platform: if lazycat { "lazycat" } else { "other" },
        }
    }
}

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

/// 绑 `bind_ip:listen_port`；失败时给出人话（端口被占是最常见的）。
///
/// `bind_ip` 由 `ForwardPolicy` 定：桌面形态是 `127.0.0.1`，服务端形态是 `0.0.0.0`。
/// 报错里带上地址，是因为这两种形态下「同一个端口」的失败原因不同
/// （回环上被占 vs 全部地址上被占），日志里能一眼分辨。
async fn bind_on(
    bind_ip: std::net::IpAddr,
    listen_port: u16,
) -> AppResult<tokio::net::TcpListener> {
    tokio::net::TcpListener::bind((bind_ip, listen_port))
        .await
        .map_err(|e| AppError::param(format!("{bind_ip}:{listen_port} 监听失败: {e}")))
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

/// 启动一条本地静态转发：`监听 bind_ip:listen_port → SSH → target`。
///
/// `bind_ip` 来自 `ForwardPolicy`：桌面形态绑回环（只给本机用），服务端形态绑全部地址
/// （外部客户端才够得到 —— 这正是服务端上这条功能的意义）。
pub async fn spawn_local_forward(
    session_id: &str,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    bind_ip: std::net::IpAddr,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<RunningForward> {
    let listener = bind_on(bind_ip, listen_port).await?;
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

/// 启动一条 SOCKS5 动态转发：在 `bind_ip:listen_port` 上跑 SOCKS5 服务端，
/// 每条 CONNECT 都由客户端指定目标，再经这条 SSH 出去。
///
/// ⚠️ **这是一个无认证的代理**，`bind_ip` 选错就等于开放代理：
///
/// - 桌面形态绑 `127.0.0.1` —— 能连上的本来就已经是本机用户，所以可以不做认证；要给别人用
///   应该各自建转发（各自的 SSH 会话 = 各自的权限边界）。
/// - 服务端形态绑 `0.0.0.0` —— 这是操作者显式要的语义（「服务端上开个口子」），
///   但它同时意味着**任何能连到这个端口的人都能借这条 SSH 会话逛内网**。
///   与完整版 `/rpc` 没有自身鉴权是同一类风险，所以边界必须由操作者的网络/防火墙来划
///   （服务端启动时那条非回环警告覆盖的就是这件事）。
pub async fn spawn_socks_forward(
    session_id: &str,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    bind_ip: std::net::IpAddr,
    listen_port: u16,
) -> AppResult<RunningForward> {
    let listener = bind_on(bind_ip, listen_port).await?;
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

#[cfg(test)]
mod tests {
    use super::*;

    /// `NEXTERM_PLATFORM` 只有 `lazycat` 一个有意义的值，且不该计较大小写/空白。
    ///
    /// 反向是重点：**没设值时必须是「非懒猫」**（自建服务端），否则本机上根本没法用这个功能。
    #[test]
    fn platform_only_lazycat_disables_forwards() {
        assert!(is_lazycat(Some("lazycat")));
        assert!(is_lazycat(Some("LazyCat")));
        assert!(is_lazycat(Some("  lazycat ")));
        assert!(!is_lazycat(Some("other")));
        assert!(!is_lazycat(Some("")));
        assert!(!is_lazycat(None));
    }

    /// 桌面形态：监听回环。
    ///
    /// 绑 `0.0.0.0` 就等于把内网入口白送给同网段的人 —— 尤其是 SOCKS5 那条无认证代理。
    #[cfg(feature = "desktop")]
    #[test]
    fn desktop_form_binds_loopback_only() {
        let p = ForwardPolicy::detect();
        assert!(p.bind_ip.is_loopback(), "桌面形态不该绑非回环地址");
        assert!(p.available, "桌面形态的转发必须是可用的");
    }

    /// 服务端形态：绑全部地址（外部客户端才够得到），且默认平台不是懒猫。
    ///
    /// 注：这里不断言 `available` 在设了 `NEXTERM_PLATFORM=lazycat` 时为假 ——
    /// 那需要改进程环境变量，会让并行测试跟着抖（见 `is_lazycat` 的说明），
    /// 所以那一条由 `platform_only_lazycat_disables_forwards` 覆盖。
    #[cfg(not(feature = "desktop"))]
    #[test]
    fn server_form_binds_all_addresses() {
        let p = ForwardPolicy::detect();
        assert!(
            !p.bind_ip.is_loopback(),
            "服务端形态要绑全部地址，否则外部连不上"
        );
        assert!(p.bind_ip.is_unspecified(), "服务端形态应当是 0.0.0.0");
    }

    /// `bind_on` 真的按传入的地址绑 —— 上面两条测的是「策略值」，这条测「实际落地的 socket」。
    ///
    /// 必要性：策略对了但 `bind_on` 里写死了 `127.0.0.1`，上面的断言照样全绿，
    /// 而外部客户端永远连不上。所以这里看 listener 自己的 `local_addr()`。
    ///
    /// 端口传 0 让内核挑一个空闲端口，避免测试之间抢端口。
    #[tokio::test]
    async fn bind_on_honours_the_given_address() {
        let any = bind_on(std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED), 0)
            .await
            .expect("绑 0.0.0.0 不该失败");
        assert_eq!(
            any.local_addr().expect("取本地地址").ip(),
            std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED),
            "传 0.0.0.0 就必须真的绑在 0.0.0.0 上"
        );

        let lo = bind_on(std::net::IpAddr::V4(std::net::Ipv4Addr::LOCALHOST), 0)
            .await
            .expect("绑 127.0.0.1 不该失败");
        assert!(
            lo.local_addr().expect("取本地地址").ip().is_loopback(),
            "传 127.0.0.1 就必须真的绑在回环上"
        );
    }
}
