//! 桌面 → 盒子的出站客户端。
//!
//! # 通道为什么是 HTTP 而不是别的
//!
//! 两端已经在 `/sync/rpc` 上有一套完整的命令分发（`server::rpc`），桌面端
//! 只要会用同一份契约说话，就复用了全部错误语义（`{ok,error}` 信封、
//! `AppError` 的稳定 code）。换成自造协议等于把这套东西再实现一遍。
//!
//! # 认证：一把钥匙，两个部署位置
//!
//! 连接**只有一种凭据**：服务端自己生成的同步令牌，挂在
//! [`crate::sync::TOKEN_HEADER`]（`X-NexTerm-Sync-Token`）上。服务端装在哪，
//! 令牌就从哪拿 —— [`TOKEN_KIND_BOX`]（懒猫微服）与 [`TOKEN_KIND_SERVER`]
//! （自建服务器）**只是界面上的两个位置提示**，走的是同一个头、同一套校验。
//!
//! ## 为什么能只靠这一个令牌（懒猫的平台登录门去哪了）
//!
//! 微服公网入口对所有请求一律 `307` → `/sys/login`，而且**不认第三方头**
//! （实测：带本应用的令牌请求，响应与不带逐字节相同）。所以「把令牌放进请求头」
//! 本身过不去那道门 —— 需要平台那边**把这条路径放出来**：
//! `lzc-manifest.yml` 的 `application.public_path` 里声明了 `/sync/rpc`
//! （官方 `/advanced-public-api` 就是为此设计的：应用自带独立鉴权时可关闭强制鉴权）。
//!
//! 于是整条链变成两段，各管各的：
//!
//! ```text
//! 平台网关  ── public_path 放行 /sync/rpc ──▶  容器内的 authorize_sync()
//! （不再要求登录）                              （要求令牌一致，否则 401）
//! ```
//!
//! 曾经有另外两种钥匙，都已删除，记在这里免得再走一遍：
//!
//! - **`Lzc-Api-Auth-Token`（平台 API 令牌）**：网关认它，但要用
//!   `hc api_auth_token gen` 生成，而 `hc` 只在**盒子的 shell** 上
//!   （开发者侧能进的 `debug.bridge` 子命令里没有它）⇒ 对普通用户等于不存在。
//! - **`Lzc-Auth-Token`（客户端会话票据）**：从懒猫客户端窗口进程的
//!   `--authToken=` 里读得到，确实能过门，但它是**会话级**的（重开窗口就换），
//!   把「配一次」变成「过期就再点一次」。`public_path` 落地后它没有存在价值。
//!
//! ⚠️ **不跟随重定向**是这条通道的关键细节，见 [`gate_error`]。
//!
//! # TLS
//!
//! 微服自动签发的 `*.lazycore.heiyu.space` 是**公共 CA** 签的（实测
//! `CN=*.lazycore.heiyu.space`，issuer Let's Encrypt，verify ok）⇒ 正常不需要
//! 动 TLS 设置。`insecure` 开关留给「自己配了自签证书的域名 / 内网直连」。

use std::time::Duration;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use ts_rs::TS;

use crate::error::{AppError, AppResult};
use crate::store::Store;

/// 部署位置：**懒猫微服**上的 NexTerm 服务端。
pub const TOKEN_KIND_BOX: &str = "box";
/// 部署位置：**自建服务器**上的 NexTerm 服务端（公网 VPS、自己的机器……）。
pub const TOKEN_KIND_SERVER: &str = "server";

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
    /// 对端服务端地址，如 `https://nexterm.heiyu.space`（微服）或
    /// `https://sync.example.com`（自建）。
    pub url: String,
    /// 对端服务端装在哪儿：`box`（懒猫微服）| `server`（自建服务器）。
    ///
    /// **只影响界面提示，不影响协议** —— 两个位置发的是同一个头、同一套校验。
    /// 留着它是因为「令牌去哪儿拿」这件事两者完全不同，而这正是用户最容易卡住的地方。
    pub token_kind: String,
    /// 服务端自己生成的那串令牌。
    pub token: String,
    /// 跳过 TLS 证书校验（自签证书的内网/自建地址才需要）。
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

/// 把连接配置收敛到当前定义上（历史取值在这里被消化掉）。
///
/// 旧版有三种钥匙（`session` / `platform` / `app`），现在只剩通用的服务端令牌。
/// `app` 直接就是今天这一种；另外两种所对应的请求头**已经不再发送**，所以
/// 顺手把存的令牌清空 —— 留着一条永远连不上的值，只会让用户以为「我配过了」。
pub fn normalize_link(mut link: SyncLink) -> SyncLink {
    match link.token_kind.as_str() {
        TOKEN_KIND_BOX | TOKEN_KIND_SERVER => {}
        "app" => link.token_kind = default_kind_for(&link.url),
        _ => {
            if !link.token.trim().is_empty() {
                tracing::info!(
                    target: "sync",
                    old_kind = %link.token_kind,
                    "连接配置用的是已废弃的令牌类型，已清空所存令牌（需要重新粘一次）"
                );
            }
            link.token_kind = default_kind_for(&link.url);
            link.token.clear();
        }
    }
    link
}

/// 没得选时按地址猜部署位置：微服自动签发的应用域名一定是 `*.heiyu.space`。
fn default_kind_for(url: &str) -> String {
    if url.to_ascii_lowercase().contains("heiyu.space") {
        TOKEN_KIND_BOX.to_string()
    } else {
        TOKEN_KIND_SERVER.to_string()
    }
}

pub async fn load_link(store: &Store) -> AppResult<SyncLink> {
    Ok(normalize_link(
        store
            .setting_get(SETTING_LINK)
            .await?
            .and_then(|s| serde_json::from_str::<SyncLink>(&s).ok())
            .unwrap_or_default(),
    ))
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
            headers.push((crate::sync::TOKEN_HEADER.to_string(), token.to_string()));
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
            // 后者只可能出现在自签证书的地址上，所以提示里直接点出来。
            AppError::Disconnected(format!(
                "连不上 {}：{e}。若提示证书相关错误，请在下方勾选「跳过证书校验」\
                 （只有自签证书的地址才需要；微服自动签发的地址是公共 CA）",
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
                 确认地址填的是 NexTerm 服务端的入口 —— 微服上就是应用的应用域名，\
                 自建就是你自己服务器的地址",
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
            "对端拒绝了这次连接：{}。令牌在「对端服务端的设置 → 资产同步」里查\
             （微服上就是那个应用的设置页），两边对不上就重新复制一次",
            strip_display_prefix(msg)
        ));
    }
    AppError::Internal(format!(
        "对端返回错误 [{code}] {}",
        strip_display_prefix(msg)
    ))
}

/// 去掉对端 `AppError` 的 Display 前缀，免得嵌进我们自己的句子里重复一遍。
///
/// 对端的 `{ok:false, error}` 信封里 `message` 已经是**展示过的**形态
/// （如 `操作被拒绝: 缺少同步令牌`），直接拼进来会变成
/// 「操作被拒绝: 对端拒绝了这次连接：操作被拒绝: 缺少同步令牌」。
///
/// 只在**已知前缀**上剥，不做通用解析：前缀属于展示层，改文案时这里同步改，
/// 漏改也只是啰嗦一点，不会丢信息。
fn strip_display_prefix(msg: &str) -> &str {
    msg.strip_prefix("操作被拒绝: ").unwrap_or(msg)
}

/// 把「回了个重定向」翻译成用户能照着做的事。
///
/// 这个函数存在的理由：默认的 HTTP 客户端会**跟掉重定向**，最后只看到一句
/// 「响应解不出来」，真实成因（被平台的登录门挡了）完全看不见 —— 用户报的就是
/// 这个现象。所以宁可多写几行，也要把「被谁挡了」直接说出来。
///
/// ⚠️ 正常情况下**不该**走到这里：`/sync/rpc` 已经在 manifest 的 `public_path`
/// 里放行了，平台的登录门不拦它。真走到这里只有一种解释 —— **对端那个应用是旧版**
/// （还没声明 `public_path`）或者平台没接住这条配置。所以文案要指向「升级对端」，
/// 而不是指向「换个令牌试试」（换什么都没用）。
fn gate_error(base: &str, status: u16, location: &str) -> AppError {
    if location.contains("/sys/login") {
        return AppError::Forbidden(format!(
            "被懒猫微服的登录门挡下了（HTTP {status} → 登录页）。\
             同步入口本该由应用自己放行（manifest 的 public_path），\
             出现这条说明「微服上装的 NexTerm 还是旧版」：\
             请在微服的应用商店/开发者后台把它更新到最新版后重试。\
             地址：{base}"
        ));
    }
    AppError::Internal(format!(
        "{base} 回了 HTTP {status} 重定向到 {location}，这不像是 NexTerm 服务端的入口"
    ))
}

/// 校验并规范化地址；**挡住「把令牌明文发到公网」**。
pub fn normalize_base(raw: &str) -> AppResult<String> {
    let trimmed = raw.trim().trim_end_matches('/');
    if trimmed.is_empty() {
        return Err(AppError::param("还没填对端地址"));
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

    fn link(kind: &str, token: &str) -> SyncLink {
        SyncLink {
            url: "https://box.example.com".into(),
            token_kind: kind.into(),
            token: token.into(),
            insecure: false,
            verified_at: 0,
            last_error: None,
        }
    }

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

    /// **只有一把钥匙**：两个部署位置发的是同一个头；没填令牌就不挂头。
    ///
    /// 这条钉住的是本轮的收敛结果 —— 以前这里按 `token_kind` 分派到三种不同的头，
    /// 而其中两种（平台令牌 / 会话票据）用户根本拿不到或会过期。
    #[test]
    fn token_header_is_the_same_for_both_places() {
        for kind in [TOKEN_KIND_BOX, TOKEN_KIND_SERVER] {
            let c = SyncClient::new(&link(kind, "t")).unwrap();
            assert_eq!(c.headers.len(), 1, "{kind} 应当只挂一个头");
            assert_eq!(c.headers[0].0, crate::sync::TOKEN_HEADER);
            assert_eq!(c.headers[0].1, "t");
        }
        let blank = SyncClient::new(&link(TOKEN_KIND_BOX, "   ")).unwrap();
        assert!(blank.headers.is_empty(), "空白令牌不该挂头");
    }

    /// 历史配置要被消化掉，而不是让用户对着一串永远连不上的值发呆。
    ///
    /// · `app`（旧版第三种）就是今天这一种 ⇒ 令牌原样保留；
    /// · `session` / `platform`（旧版另外两种）所对应的请求头**已经不再发送** ⇒
    ///   清空令牌，并按键名落地到两个新值之一；空串 / 未知值同样按地址猜。
    #[test]
    fn legacy_token_kinds_are_migrated() {
        let app = normalize_link(link("app", "keep-me"));
        assert_eq!(app.token_kind, TOKEN_KIND_SERVER, "非微服地址猜自建");
        assert_eq!(app.token, "keep-me", "app 令牌就是今天这种，不该丢");

        let app_box = normalize_link(SyncLink {
            url: "https://nexterm.lazycore.heiyu.space".into(),
            ..link("app", "keep-me")
        });
        assert_eq!(app_box.token_kind, TOKEN_KIND_BOX, "微服地址猜微服");

        let session = normalize_link(SyncLink {
            url: "https://nexterm.lazycore.heiyu.space".into(),
            ..link("session", "expired-ticket")
        });
        assert_eq!(session.token_kind, TOKEN_KIND_BOX);
        assert!(session.token.is_empty(), "旧票据当令牌用只会白报错，清掉");

        let platform = normalize_link(link("platform", "old-platform-token"));
        assert_eq!(platform.token_kind, TOKEN_KIND_SERVER);
        assert!(platform.token.is_empty());

        let empty = normalize_link(SyncLink::default());
        assert_eq!(empty.token_kind, TOKEN_KIND_SERVER);
        assert!(empty.token.is_empty());
    }

    /// 登录门必须被**认出来**并给出可执行的下一步，而不是报一句「响应解不出来」。
    ///
    /// ⚠️ 正常情况下走不到这里（`/sync/rpc` 已由 `public_path` 放行），所以文案指向
    /// 「对端是旧版」，而不是「换个令牌试试」—— 后者换什么都没用。
    #[test]
    fn login_gate_is_reported_with_next_step() {
        let url = "https://lazycore.heiyu.space/sys/login?redirect=https%3A%2F%2Fx%2Fsync%2Frpc";
        let e = gate_error("https://nexterm.lazycore.heiyu.space", 307, url);
        assert_eq!(e.code(), "forbidden");
        let msg = e.to_string();
        assert!(msg.contains("登录门"), "要说清是被门挡了: {msg}");
        assert!(msg.contains("更新到最新版"), "要给下一步: {msg}");
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

    /// 401 的文案必须指向「去哪儿抄令牌」，而不是指向一条普通人走不通的命令；
    /// 并且**不能**出现叠加的「操作被拒绝」（对端信封里的 message 自带这个前缀）。
    #[test]
    fn remote_401_points_at_the_server_settings() {
        let e = map_remote_error(401, "forbidden", "操作被拒绝: 同步令牌不正确");
        assert_eq!(e.code(), "forbidden");
        let msg = e.to_string();
        assert!(msg.contains("设置 → 资产同步"), "要指向抄令牌的地方: {msg}");
        assert_eq!(
            msg.matches("操作被拒绝").count(),
            1,
            "只该有一个「操作被拒绝」: {msg}"
        );
        assert!(
            !msg.contains("**"),
            "这里是纯文本展示，别把 Markdown 强调写进来: {msg}"
        );
        assert!(
            !msg.contains("hc api_auth_token"),
            "那条命令普通用户执行不了，不该出现在面向用户的文案里: {msg}"
        );
    }

    /// **真机联调**（默认跳过）：拿真服务端把出站客户端整条路走一遍。
    ///
    /// 为什么要有它：这条路的两个关键事实 —— ①「`public_path` 放行后，只带应用
    /// 令牌就能穿过平台的登录门」②「不带令牌时会得到一条**可读**的拒绝，而不是
    /// 一句没有信息量的解析错误」—— **都只在真服务端上成立**，单测只能钉住解析
    /// 逻辑，钉不住网关行为。GUI 里点按钮最快，但它不可复现；这个测试把同一件事
    /// 变成一条命令。
    ///
    /// 跑法（两个环境变量都要给；令牌从对端「设置 → 资产同步」里复制）：
    ///
    /// ```bash
    /// cd src-tauri
    /// NEXTERM_SYNC_LIVE_URL=https://nexterm.lazycore.heiyu.space \
    /// NEXTERM_SYNC_LIVE_TOKEN=<服务端生成的令牌> \
    ///   cargo test --lib live_server -- --ignored --nocapture
    /// ```
    ///
    /// ⚠️ 只做**读**操作（`sync_digest`），不碰对端数据：真机测试不该改用户的库。
    #[tokio::test]
    #[ignore = "需要真服务端与真令牌，见本测试文档注释"]
    async fn live_server_token_is_enough() {
        let Ok(url) = std::env::var("NEXTERM_SYNC_LIVE_URL") else {
            panic!("没给 NEXTERM_SYNC_LIVE_URL");
        };
        let Ok(token) = std::env::var("NEXTERM_SYNC_LIVE_TOKEN") else {
            panic!("没给 NEXTERM_SYNC_LIVE_TOKEN");
        };
        let mk = |token: &str| SyncLink {
            url: url.clone(),
            token_kind: TOKEN_KIND_BOX.to_string(),
            token: token.to_string(),
            // 微服公网证书是 Let's Encrypt 签的，走公共信任根即可 ——
            // 顺带把「这里根本不需要 insecure」也钉住。
            insecure: false,
            verified_at: 0,
            last_error: None,
        };

        // ① 只带服务端令牌：必须连通，并解出对端摘要。
        let digest = remote_digest(&mk(&token))
            .await
            .expect("只带服务端令牌就应当连得上");
        println!(
            "  对端 origin={} protocol={} version={} 资产={} 条",
            digest.origin,
            digest.protocol,
            digest.app_version,
            digest.assets.len()
        );
        assert!(digest.protocol >= 1);

        // ② 不带令牌：必须是**可读的**拒绝。两种都算通过 ——
        //    「缺少同步令牌」（public_path 生效，被应用自己的门挡）或
        //    「登录门」（对端还是没声明 public_path 的旧版）。
        //    唯一不许出现的，是那句什么都没说清的解析错误。
        let err = remote_digest(&mk("")).await.expect_err("不带令牌应当被拒");
        let msg = err.to_string();
        println!("  不带令牌时的报错：{msg}");
        assert!(
            msg.contains("缺少同步令牌") || msg.contains("登录门"),
            "要说清是谁拒的，实际：{msg}"
        );
        assert!(
            !msg.contains("返回的不是 NexTerm 的响应"),
            "不该退化成解析错误，实际：{msg}"
        );
    }
}
