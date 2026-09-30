//! 桌面 → 盒子的出站客户端。
//!
//! # 通道为什么是 HTTP 而不是别的
//!
//! 两端已经在 `/sync/rpc` 上有一套完整的命令分发（`server::rpc`），桌面端
//! 只要会用同一份契约说话，就复用了全部错误语义（`{ok,error}` 信封、
//! `AppError` 的稳定 code）。换成自造协议等于把这套东西再实现一遍。
//!
//! # 认证：三种钥匙，一条通道
//!
//! 盒子的公网入口有平台登录门，**桌面版不是浏览器、过不了那道门**（实测：不带凭证
//! 一律 `307` 到 `/sys/login`）。所以「填个地址就能连」是不成立的，必须先解决钥匙。
//! 按「谁来开这把锁」分三种，[`SyncLink::token_kind`] 就是选哪种：
//!
//! 1. **懒猫客户端会话票据**（[`TOKEN_KIND_SESSION`]，`Lzc-Auth-Token`）—— **推荐**。
//!    懒猫客户端每开一个 Web 应用窗口，就把当次票据写在窗口进程的命令行上
//!    （`--authToken=`）。桌面端把它读出来即可（[`find_session_token`]），
//!    用户在盒子上**不需要做任何事**。代价：会话级，窗口关了/客户端重启就换。
//! 2. **平台 API 令牌**（[`TOKEN_KIND_PLATFORM`]，`Lzc-Api-Auth-Token`）—— 长期有效，
//!    但要用 `hc api_auth_token gen` 生成，而那个命令只在**盒子的 shell** 上可用
//!    （开发者侧能进的 `debug.bridge` 里没有 `hc`）。适合能直接登盒子的人。
//! 3. **应用同步令牌**（[`TOKEN_KIND_APP`]，`X-NexTerm-Sync-Token`）—— 本应用自己的，
//!    用于「同网段直连」或 `public_path` 放行这类**不经过平台网关**的场合。
//!
//! 三种在服务端都认（见 `server::authorize_sync`），这里按配置选一个发。
//!
//! ⚠️ **不跟随重定向**是这条通道的关键细节，见 [`gate_error`]。
//!
//! # TLS：盒子用的是私有 CA
//!
//! rustls 默认只信公信根，而懒猫盒子的证书由微服自己签发 ⇒ **不显式放开就会
//! 握手失败**。这不是「顺手把校验关掉」：它是这台盒子本来就有的信任模型
//! （客户端要靠懒猫自己的客户端完成首次信任）。所以做成一个显式开关，
//! 默认关闭，由用户确认。

use std::time::Duration;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use ts_rs::TS;

use crate::error::{AppError, AppResult};
use crate::store::Store;

/// 令牌种类：懒猫**客户端会话票据**（`Lzc-Auth-Token`，从客户端窗口进程读出来）。
pub const TOKEN_KIND_SESSION: &str = "session";
/// 令牌种类：平台 API Token（`Lzc-Api-Auth-Token`）。
pub const TOKEN_KIND_PLATFORM: &str = "platform";
/// 令牌种类：本应用同步令牌（`X-NexTerm-Sync-Token`）。
pub const TOKEN_KIND_APP: &str = "app";

/// 懒猫客户端打开一个 Web 应用窗口时的动作参数（票据就在同一条命令行上）。
const CLIENT_WINDOW_ACTION: &str = "open_web_app_window";
/// 客户端窗口命令行里「应用地址」的开关。
const CLIENT_ARG_APP_URL: &str = "--appUrl=";
/// 客户端窗口命令行里「会话票据」的开关。
const CLIENT_ARG_AUTH_TOKEN: &str = "--authToken=";

/// 连接配置所在 setting 键。
const SETTING_LINK: &str = "sync.link";

/// 出站连接配置。
///
/// ⚠️ `token` 是**明文存在本地库**的（和凭据库分开）。这是刻意的取舍：它要能
/// 在用户没解锁凭据库时也把「上次连的那个盒子」显示出来；换成密文就得先解锁
/// 才能看一眼设置页。风险面在界面上明说（密码框 + 文案）。
#[derive(Debug, Clone, Default, Serialize, Deserialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct SyncLink {
    /// 盒子公网地址，如 `https://nexterm.heiyu.space`
    pub url: String,
    /// `platform` | `app`
    pub token_kind: String,
    pub token: String,
    /// 跳过 TLS 证书校验（懒猫盒子私有 CA）。
    pub insecure: bool,
    /// 最近一次连通性探测成功的时间（Unix 毫秒；0 = 没测过）
    #[ts(type = "number")]
    pub verified_at: i64,
    /// 最近一次探测的失败原因（成功时为空）
    pub last_error: Option<String>,
}

impl SyncLink {
    /// 是否填够了「可以尝试连接」的信息。
    pub fn is_configured(&self) -> bool {
        !self.url.trim().is_empty() && !self.token.trim().is_empty()
    }
}

pub async fn load_link(store: &Store) -> AppResult<SyncLink> {
    Ok(store
        .setting_get(SETTING_LINK)
        .await?
        .and_then(|s| serde_json::from_str::<SyncLink>(&s).ok())
        .unwrap_or_default())
}

pub async fn save_link(store: &Store, link: &SyncLink) -> AppResult<()> {
    let raw = serde_json::to_string(link)
        .map_err(|e| AppError::internal(format!("序列化连接配置失败: {e}")))?;
    store.setting_set(SETTING_LINK, &raw).await
}

/// 一次已配置好的连接。
pub struct SyncClient {
    http: reqwest::Client,
    base: String,
    headers: Vec<(String, String)>,
}

impl SyncClient {
    pub fn new(link: &SyncLink) -> AppResult<Self> {
        let base = normalize_base(&link.url)?;
        // ⚠️ **不跟随重定向**是这里的关键。平台登录门的表现就是 `307` +
        // `Location: …/sys/login`；默认策略会把请求一路跟到登录页，于是我们拿到
        // 一张 HTTP 200 的 HTML、报「响应体解不出来」—— 真正的原因（没过鉴权）
        // 被完全藏住（用户报的就是这个现象）。改成不跟随，才能看见 307 本身。
        let http = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .danger_accept_invalid_certs(link.insecure)
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(60))
            .build()
            .map_err(|e| AppError::internal(format!("HTTP 客户端构建失败: {e}")))?;

        let mut headers = Vec::new();
        let token = link.token.trim();
        if !token.is_empty() {
            let name = match link.token_kind.as_str() {
                TOKEN_KIND_PLATFORM => crate::sync::PLATFORM_TOKEN_HEADER,
                TOKEN_KIND_SESSION => crate::sync::SESSION_TOKEN_HEADER,
                _ => crate::sync::TOKEN_HEADER,
            };
            headers.push((name.to_string(), token.to_string()));
        }
        Ok(Self {
            http,
            base,
            headers,
        })
    }

    /// 调一条 RPC 命令，返回信封里的 `data`。
    pub async fn call(&self, cmd: &str, args: Value) -> AppResult<Value> {
        let url = format!("{}/sync/rpc", self.base);
        let mut req = self
            .http
            .post(&url)
            .json(&json!({ "cmd": cmd, "args": args }));
        for (k, v) in &self.headers {
            req = req.header(k.as_str(), v.as_str());
        }

        let resp = req.send().await.map_err(|e| {
            // 这里报出来的最常见两种：地址写错（DNS / 连接被拒）、TLS 握手失败。
            // 后者有专门的成因（盒子私有 CA），所以提示里直接点出来。
            AppError::Disconnected(format!(
                "连不上 {}：{e}。若提示证书相关错误，请在下方勾选「跳过证书校验」（懒猫盒子用自签证书）",
                self.base
            ))
        })?;

        let status = resp.status();
        // 「被挡在门外」必须在读 body **之前**判断：登录门的 307 响应体是空的，
        // 等 `json()` 失败再来猜成因，就只能报出一句没有信息量的解析错误。
        let location = resp
            .headers()
            .get(reqwest::header::LOCATION)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_string();
        if status.is_redirection() {
            return Err(gate_error(&self.base, status.as_u16(), &location));
        }
        let ctype = resp
            .headers()
            .get(reqwest::header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_string();
        let body: Value = resp.json().await.map_err(|e| {
            AppError::Internal(format!(
                "{} 返回的不是 NexTerm 的响应（HTTP {status}{}）：{e}。\
                 确认地址填的是盒子上的 NexTerm 应用入口、且令牌有效",
                self.base,
                if ctype.is_empty() {
                    String::new()
                } else {
                    format!(", {ctype}")
                }
            ))
        })?;

        if body.get("ok").and_then(Value::as_bool).unwrap_or(false) {
            return Ok(body.get("data").cloned().unwrap_or(Value::Null));
        }
        let err = body.get("error").cloned().unwrap_or(Value::Null);
        let code = err
            .get("code")
            .and_then(Value::as_str)
            .unwrap_or("unknown")
            .to_string();
        let msg = err
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("未知错误")
            .to_string();
        Err(map_remote_error(status.as_u16(), &code, &msg))
    }
}

/// 把对端错误翻译成用户能照着做的事。
fn map_remote_error(status: u16, code: &str, msg: &str) -> AppError {
    if status == 401 {
        return AppError::Forbidden(format!(
            "盒子拒绝了这次连接：{msg}。\
             请确认令牌有效 —— 平台令牌用盒子上的 `hc api_auth_token gen` 生成，\
             应用令牌在盒子版「设置 → 同步」里查看"
        ));
    }
    AppError::Internal(format!("盒子返回错误 [{code}] {msg}"))
}

/// 把「回了个重定向」翻译成用户能照着做的事。
///
/// 这个函数存在的理由：桌面版和浏览器**唯一的**差别就是没有登录态，而这一点在
/// 默认的 HTTP 客户端行为下是看不见的（重定向会被跟掉，最后只看到「响应解不出来」）。
/// 所以宁可这里多写几行，也要把「你被登录门挡了、该去点哪个按钮」直接说出来。
fn gate_error(base: &str, status: u16, location: &str) -> AppError {
    if location.contains("/sys/login") {
        return AppError::Forbidden(format!(
            "被懒猫平台的登录门挡下了（HTTP {status} → 登录页）。\
             桌面版没有浏览器的登录态，所以光填地址连不上 —— \
             请在懒猫客户端里打开 NexTerm 应用窗口（保持开着），\
             然后点上面的「从懒猫客户端获取票据」。\
             如果之前取过票据，就是它过期了（客户端重开窗口会换），重新取一次即可。"
        ));
    }
    AppError::Internal(format!(
        "{base} 回了 HTTP {status} 重定向到 {location}，这不像是盒子上的 NexTerm 入口"
    ))
}

/// 从一个 URL 里取小写主机名（`https://a.b/c` → `a.b`）。
pub fn host_of_url(url: &str) -> String {
    let s = url.trim();
    let s = s
        .strip_prefix("https://")
        .or_else(|| s.strip_prefix("http://"))
        .unwrap_or(s);
    s.split(['/', ':'])
        .next()
        .unwrap_or("")
        .to_ascii_lowercase()
}

/// 归到「盒子域」：`nexterm.lazycore.heiyu.space` → `lazycore.heiyu.space`。
///
/// 为什么按盒子域匹配而不是按完整主机名：懒猫的 `subdomain` **首装即固化**
/// （§15.3），装的时候叫什么就一直叫什么。所以用户手里的地址和客户端窗口里的
/// `appUrl` 完全可能不是一个名字（本项目就是：dev 包的窗口开在 `nexterm.*` 上）。
/// 只按完整名匹配会得到一个「明明开着窗口却说没找到」的怪现象。
fn box_domain(host: &str) -> &str {
    host.split_once('.').map(|(_, rest)| rest).unwrap_or(host)
}

/// 从 `ps` 输出里挑出「开着目标应用窗口」的那条命令行，返回 `(票据, 窗口主机)`。
///
/// 做成纯函数是为了能单测：真实的窗口命令行很长、字段顺序不保证，所以这里按
/// 「逐个 `--k=v`」解析，而不是靠位置或正则去截。
///
/// 匹配分**两轮**，顺序有意义：
/// 1. **主机名完全相同** —— 正常情况，精确且不会误伤别的盒子；
/// 2. 退一步比**盒子域**（`a.b.c` 的 `b.c`）—— 只用于「地址里的子域和窗口里的
///    不一样」这一种情况（懒猫 `subdomain` 首装固化，见 `box_domain`）。
///    如果放进同一轮，`nexterm.heiyu.space` 这种短名会跟别的盒子的窗口撞上。
fn session_token_from_ps(ps_output: &str, target_host: &str) -> Option<(String, String)> {
    // 收「主机名」也收「整条 URL」—— 调用方很容易顺手把地址整个传进来，
    // 而那种错法的表现是「明明开着窗口却说没找到」，很难查。这里统一归一。
    let want_host = host_of_url(target_host);
    let want_box = box_domain(&want_host).to_string();

    // 先把所有「开着某个应用的窗口」解析出来，再按两轮筛。
    let mut windows: Vec<(String, String)> = Vec::new();
    for line in ps_output.lines() {
        if !line.contains(CLIENT_WINDOW_ACTION) {
            continue;
        }
        let mut app_url = None;
        let mut token = None;
        for field in line.split_whitespace() {
            if let Some(v) = field.strip_prefix(CLIENT_ARG_APP_URL) {
                app_url = Some(v);
            } else if let Some(v) = field.strip_prefix(CLIENT_ARG_AUTH_TOKEN) {
                token = Some(v);
            }
        }
        let (Some(url), Some(tok)) = (app_url, token) else {
            continue;
        };
        if tok.is_empty() {
            continue;
        }
        windows.push((tok.to_string(), host_of_url(url)));
    }

    windows
        .iter()
        .find(|(_, h)| h.as_str() == want_host.as_str())
        .or_else(|| {
            windows
                .iter()
                .find(|(_, h)| box_domain(h) == want_box.as_str())
        })
        .cloned()
}

/// 读本机进程列表，找出「开着目标应用」的懒猫客户端窗口，取它的会话票据。
///
/// # 为什么读进程命令行
///
/// 这是平台自己给浏览器窗口下发票据的方式（客户端把 `--authToken=` 写在窗口进程的
/// 命令行上），所以不是绕过鉴权，而是**用同一个凭据**。用户因此不必在盒子上做任何事。
///
/// 读的是**同用户**进程的命令行（`ps -ww -ax`），不需要任何特权：这些票据本来就是
/// 给这个用户的客户端用的。能读到它的程序，本来也能读这个用户自己的凭据库。
pub fn find_session_token(base: &str) -> AppResult<(String, String)> {
    let host = host_of_url(base);
    let out = std::process::Command::new("ps")
        .args(["-ww", "-ax", "-o", "command="])
        .output()
        .map_err(|e| {
            AppError::Unsupported(format!(
                "读不到本机进程列表（{e}）。\
                 请在懒猫客户端里打开 NexTerm 窗口，把它命令行里的 `--authToken=` 值\
                 手动填到「访问令牌」里（令牌类型选「懒猫客户端票据」）"
            ))
        })?;
    let text = String::from_utf8_lossy(&out.stdout);
    session_token_from_ps(&text, &host).ok_or_else(|| {
        AppError::NotFound(format!(
            "没找到开着「{host}」的懒猫客户端窗口。\
             请先在懒猫客户端里打开这个微服的任意应用窗口（打开 NexTerm 最稳）并保持开着，\
             再点一次「从懒猫客户端获取票据」"
        ))
    })
}

/// 校验并规范化地址；**挡住「把令牌明文发到公网」**。
pub fn normalize_base(raw: &str) -> AppResult<String> {
    let trimmed = raw.trim().trim_end_matches('/');
    if trimmed.is_empty() {
        return Err(AppError::param("还没填盒子地址"));
    }
    let lower = trimmed.to_ascii_lowercase();
    let (scheme, rest) = if let Some(r) = lower.strip_prefix("https://") {
        ("https", r)
    } else if let Some(r) = lower.strip_prefix("http://") {
        ("http", r)
    } else {
        return Err(AppError::param(
            "地址要以 https:// 开头，例如 https://nexterm.heiyu.space",
        ));
    };
    let host = rest.split(['/', ':']).next().unwrap_or("");
    if host.is_empty() {
        return Err(AppError::param("地址里没有主机名"));
    }
    // 访问令牌走明文 HTTP 等于把它交给同链路上的任何人。只对本机与内网开口子
    // ——那也是唯一「不出公网」的场合（调试与同网段直连）。
    if scheme == "http" && !is_local_or_private(host) {
        return Err(AppError::param(format!(
            "{host} 是公网地址，明文 http 会把访问令牌暴露在链路上；请改用 https://"
        )));
    }
    Ok(trimmed.to_string())
}

fn is_local_or_private(host: &str) -> bool {
    if host == "localhost" || host.ends_with(".local") {
        return true;
    }
    match host.parse::<std::net::IpAddr>() {
        Ok(std::net::IpAddr::V4(v4)) => v4.is_private() || v4.is_loopback() || v4.is_link_local(),
        Ok(std::net::IpAddr::V6(v6)) => v6.is_loopback(),
        Err(_) => false,
    }
}

/// 探连通性 + 拿对端摘要（一次调用同时验地址、令牌、协议版本）。
pub async fn remote_digest(link: &SyncLink) -> AppResult<crate::sync::SyncDigest> {
    let client = SyncClient::new(link)?;
    let data = client.call("sync_digest", json!({})).await?;
    serde_json::from_value(data)
        .map_err(|e| AppError::Internal(format!("对端摘要格式不对（版本差异？）: {e}")))
}

/// 推：本地导出 → 对端导入。
pub async fn push(
    store: &Store,
    vault: &crate::vault::Vault,
    link: &SyncLink,
    asset_ids: &[String],
    with_creds: bool,
    force: bool,
) -> AppResult<crate::sync::ImportReport> {
    let client = SyncClient::new(link)?;
    let bundle = crate::sync::export(store, vault, asset_ids, with_creds).await?;
    if bundle.is_empty() {
        return Ok(crate::sync::ImportReport::default());
    }
    let data = client
        .call(
            "sync_import",
            json!({ "args": { "bundle": bundle, "force": force } }),
        )
        .await?;
    serde_json::from_value(data)
        .map_err(|e| AppError::Internal(format!("对端导入报告格式不对: {e}")))
}

/// 拉：对端导出 → 本地导入。
pub async fn pull(
    store: &Store,
    vault: &crate::vault::Vault,
    link: &SyncLink,
    asset_ids: &[String],
    with_creds: bool,
    force: bool,
) -> AppResult<crate::sync::ImportReport> {
    let client = SyncClient::new(link)?;
    let data = client
        .call(
            "sync_export",
            json!({ "args": { "assetIds": asset_ids, "withCreds": with_creds } }),
        )
        .await?;
    let bundle: crate::sync::SyncBundle = serde_json::from_value(data)
        .map_err(|e| AppError::Internal(format!("对端同步包格式不对: {e}")))?;
    crate::sync::apply(store, vault, &bundle, force).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base_url_normalization() {
        assert_eq!(
            normalize_base("https://nexterm.heiyu.space/").unwrap(),
            "https://nexterm.heiyu.space"
        );
        assert_eq!(
            normalize_base("  https://box.example.com  ").unwrap(),
            "https://box.example.com"
        );
        assert!(normalize_base("").is_err());
        assert!(
            normalize_base("nexterm.heiyu.space").is_err(),
            "缺 scheme 该拒"
        );
        assert!(normalize_base("https://").is_err(), "缺主机该拒");
    }

    /// 明文 HTTP 只对本机 / 内网开口子 —— 这条防的是「把令牌交给公网链路上的
    /// 任何人」，是个安全默认值，必须钉住。
    #[test]
    fn plaintext_http_only_for_local_and_private() {
        assert!(normalize_base("http://127.0.0.1:8080").is_ok());
        assert!(normalize_base("http://localhost:8080").is_ok());
        assert!(normalize_base("http://192.168.1.10:8080").is_ok());
        assert!(normalize_base("http://10.0.0.5").is_ok());
        assert!(normalize_base("http://172.16.3.4").is_ok());
        assert!(normalize_base("http://nexterm.heiyu.space").is_err());
        assert!(normalize_base("http://8.8.8.8").is_err());
        // https 一律放行
        assert!(normalize_base("https://8.8.8.8").is_ok());
    }

    /// 令牌按种类挂到正确的头上；没填就不挂；认不出的种类落到应用令牌。
    #[test]
    fn token_header_follows_kind() {
        let mk = |kind: &str, token: &str| SyncLink {
            url: "https://box.example.com".into(),
            token_kind: kind.into(),
            token: token.into(),
            insecure: false,
            verified_at: 0,
            last_error: None,
        };
        let session = SyncClient::new(&mk(TOKEN_KIND_SESSION, "t0")).unwrap();
        assert_eq!(session.headers[0].0, crate::sync::SESSION_TOKEN_HEADER);
        let platform = SyncClient::new(&mk(TOKEN_KIND_PLATFORM, "t1")).unwrap();
        assert_eq!(platform.headers[0].0, crate::sync::PLATFORM_TOKEN_HEADER);
        let app = SyncClient::new(&mk(TOKEN_KIND_APP, "t2")).unwrap();
        assert_eq!(app.headers[0].0, crate::sync::TOKEN_HEADER);
        let unknown = SyncClient::new(&mk("nonsense", "t3")).unwrap();
        assert_eq!(unknown.headers[0].0, crate::sync::TOKEN_HEADER);
        let none = SyncClient::new(&mk(TOKEN_KIND_APP, "   ")).unwrap();
        assert!(none.headers.is_empty(), "空白令牌不该挂头");
    }

    /// 懒猫客户端窗口命令行的真实形状（本机实测原文，只截掉了无关参数）。
    const REAL_PS_LINE: &str = "/Applications/懒猫微服.app/Contents/MacOS/懒猫微服 \
--action=open_web_app_window --appUrl=https://nexterm.lazycore.heiyu.space/ \
--appId=cloud.lazycat.app.nexterm.dev \
--boxId=12D3KooWBMT3z6FB9KTGTDG1oGmcF4XPdRj464bfkj3S9MvGe2J6 \
--socksaddr=127.0.0.1:31085 --authToken=df903244-4f20-48eb-a17d-81e782e03aa4 \
--theme=dark --themeOnlyForClient=false";

    /// 从窗口命令行里取出票据，并认得出是哪台主机。
    #[test]
    fn session_token_is_read_from_client_window_args() {
        let got = session_token_from_ps(REAL_PS_LINE, "nexterm.lazycore.heiyu.space");
        assert_eq!(
            got,
            Some((
                "df903244-4f20-48eb-a17d-81e782e03aa4".to_string(),
                "nexterm.lazycore.heiyu.space".to_string()
            ))
        );
    }

    /// 地址里的子域和窗口里的不一样时，靠**盒子域**兜住。
    ///
    /// 这条不是构造出来的场景：懒猫的 `subdomain` 首装即固化，本项目 dev 包的窗口
    /// 就开在 `nexterm.*` 上而 manifest 里写的是 `nexterm-dev`（§15.3）。
    /// 只认完整主机名会报「明明开着窗口却说没找到」。
    #[test]
    fn session_token_falls_back_to_box_domain() {
        let got = session_token_from_ps(REAL_PS_LINE, "https://nexterm-dev.lazycore.heiyu.space/");
        assert!(got.is_some(), "同一盒子的另一个子域也该认");

        let other = session_token_from_ps(REAL_PS_LINE, "https://nexterm.otherbox.heiyu.space");
        assert!(other.is_none(), "另一个盒子不能认");
    }

    /// 精确匹配优先于盒子域兜底 —— 否则短名地址会随手撞上别的窗口。
    #[test]
    fn session_token_prefers_exact_host() {
        let two = format!(
            "{REAL_PS_LINE}\n/Applications/lazycat --action=open_web_app_window \
--appUrl=https://other.otherbox.heiyu.space/ --authToken=exact-match-token"
        );
        let got = session_token_from_ps(&two, "other.otherbox.heiyu.space");
        assert_eq!(got.unwrap().0, "exact-match-token");
    }

    /// 没开窗口 / 不是窗口进程 / 票据为空 ⇒ 都取不到（宁可报错，也不能拿错票据）。
    #[test]
    fn session_token_is_none_when_absent() {
        assert!(session_token_from_ps("", "box.example.com").is_none());
        assert!(session_token_from_ps("sshd\nnginx\n", "box.example.com").is_none());
        // 有窗口但票据字段是空值
        assert!(session_token_from_ps(
            "/Applications/lazycat --action=open_web_app_window \
             --appUrl=https://box.example.com/ --authToken=",
            "box.example.com"
        )
        .is_none());
        // 有票据但窗口开的是别的应用
        assert!(session_token_from_ps(
            "/Applications/lazycat --action=open_web_app_window \
             --appUrl=https://files.otherbox.heiyu.space/ --authToken=xxx",
            "box.example.com"
        )
        .is_none());
    }

    #[test]
    fn host_and_box_domain_parsing() {
        assert_eq!(host_of_url("https://A.B.C/x/y"), "a.b.c");
        assert_eq!(host_of_url("http://127.0.0.1:8080/"), "127.0.0.1");
        assert_eq!(host_of_url("box.example.com"), "box.example.com");
        assert_eq!(host_of_url(""), "");
        assert_eq!(
            box_domain("nexterm.lazycore.heiyu.space"),
            "lazycore.heiyu.space"
        );
        assert_eq!(box_domain("single"), "single");
    }

    /// 登录门必须被**认出来**并给出可执行的下一步，而不是报一句「响应解不出来」。
    #[test]
    fn login_gate_is_reported_with_next_step() {
        let url = "https://lazycore.heiyu.space/sys/login?redirect=https%3A%2F%2Fx%2Fsync%2Frpc";
        let e = gate_error("https://nexterm.lazycore.heiyu.space", 307, url);
        assert_eq!(e.code(), "forbidden");
        let msg = e.to_string();
        assert!(msg.contains("登录门"), "要说清是被门挡了: {msg}");
        assert!(msg.contains("从懒猫客户端获取票据"), "要给下一步: {msg}");
        assert!(msg.contains("307"), "要带上真实状态码: {msg}");
        // 非登录页的重定向走另一支，别把话说错
        let other = gate_error(
            "https://box.example.com",
            302,
            "https://elsewhere.example.com/",
        );
        assert!(other.to_string().contains("302"));
        assert!(!other.to_string().contains("登录门"));
    }

    #[test]
    fn is_configured_requires_both() {
        let mut l = SyncLink::default();
        assert!(!l.is_configured());
        l.url = "https://box.example.com".into();
        assert!(!l.is_configured());
        l.token = "t".into();
        assert!(l.is_configured());
    }

    #[test]
    fn remote_401_is_reported_as_forbidden() {
        let e = map_remote_error(401, "forbidden", "同步令牌不正确");
        assert_eq!(e.code(), "forbidden");
        assert!(e.to_string().contains("hc api_auth_token gen"));
    }

    /// **真机联调**（默认跳过）：拿真实盒子把出站客户端整条路走一遍。
    ///
    /// 为什么要有它：`/sync/rpc` 这条路的两个关键事实——「`Lzc-Auth-Token` 能过
    /// 登录门」与「没过时会以 307 而不是 JSON 报错」——**都只在真盒子上成立**，
    /// 单测只能钉住解析逻辑，钉不住网关行为。GUI 里点按钮最快，但它不可复现、
    /// 也没法进 CI 之外的回归；这个测试把同一件事变成一条命令。
    ///
    /// 跑法（两个环境变量都要给；票据从客户端窗口的命令行里抄，见
    /// [`find_session_token`]）：
    ///
    /// ```bash
    /// cd src-tauri
    /// NEXTERM_SYNC_LIVE_URL=https://nexterm.lazycore.heiyu.space \
    /// NEXTERM_SYNC_LIVE_TOKEN=<--authToken= 的值> \
    ///   cargo test --lib live_box -- --ignored --nocapture
    /// ```
    ///
    /// ⚠️ 只做**读**操作（`sync_digest`），不碰对端数据：真机测试不该改用户的库。
    #[tokio::test]
    #[ignore = "需要真盒子与真票据，见本测试文档注释"]
    async fn live_box_session_token_passes_the_gate() {
        let Ok(url) = std::env::var("NEXTERM_SYNC_LIVE_URL") else {
            panic!("没给 NEXTERM_SYNC_LIVE_URL");
        };
        let Ok(token) = std::env::var("NEXTERM_SYNC_LIVE_TOKEN") else {
            panic!("没给 NEXTERM_SYNC_LIVE_TOKEN");
        };
        let mk = |token: &str| SyncLink {
            url: url.clone(),
            token_kind: TOKEN_KIND_SESSION.to_string(),
            token: token.to_string(),
            // 盒子的公网证书是 Let's Encrypt 签的，走公共信任根即可 ——
            // 顺带把「这里根本不需要 insecure」也钉住。
            insecure: false,
            verified_at: 0,
            last_error: None,
        };

        // ① 带票据：能穿过登录门，并解出对端摘要
        let digest = remote_digest(&mk(&token))
            .await
            .expect("带会话票据应当连得上");
        println!(
            "  对端 origin={} protocol={} version={} 资产={} 条",
            digest.origin,
            digest.protocol,
            digest.app_version,
            digest.assets.len()
        );
        assert!(digest.protocol >= 1);

        // ② 不带票据：必须是**可读的登录门错误**，而不是「响应解不出来」。
        //    这条是回归线 —— 默认跟随重定向时这里会退化成一句无信息量的解析错误。
        let err = remote_digest(&mk("")).await.expect_err("不带票据应当被拒");
        let msg = err.to_string();
        println!("  不带票据时的报错：{msg}");
        assert!(msg.contains("登录门"), "要说清是被登录门挡的，实际：{msg}");
        assert!(
            !msg.contains("返回的不是 NexTerm 的响应"),
            "不该退化成解析错误，实际：{msg}"
        );
    }
}
