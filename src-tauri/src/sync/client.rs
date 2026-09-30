//! 桌面 → 盒子的出站客户端。
//!
//! # 通道为什么是 HTTP 而不是别的
//!
//! 两端已经在 `/sync/rpc` 上有一套完整的命令分发（`server::rpc`），桌面端
//! 只要会用同一份契约说话，就复用了全部错误语义（`{ok,error}` 信封、
//! `AppError` 的稳定 code）。换成自造协议等于把这套东西再实现一遍。
//!
//! # 认证：两种令牌，一条通道
//!
//! 盒子的公网入口有平台登录门，**桌面版不是浏览器、过不了那道门**。官方为此
//! 留了 [`Lzc-Api-Auth-Token`](PLATFORM_TOKEN_HEADER)（`hc api_auth_token gen`
//! 生成，文档明说是给「脚本或命令行」用的），走它时平台网关放行、并注入
//! `X-HC-User-ID`。
//!
//! 本应用另有一层自己的同步令牌（`X-NexTerm-Sync-Token`），用于「同网段直连」
//! 或 `public_path` 放行这类**不经过平台网关**的场合。两种在服务端都认
//! （见 `server::authorize_sync`），这里按配置选一个发。
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

/// 令牌种类：平台 API Token（`Lzc-Api-Auth-Token`）。
pub const TOKEN_KIND_PLATFORM: &str = "platform";
/// 令牌种类：本应用同步令牌（`X-NexTerm-Sync-Token`）。
pub const TOKEN_KIND_APP: &str = "app";

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
        let http = reqwest::Client::builder()
            .danger_accept_invalid_certs(link.insecure)
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(60))
            .build()
            .map_err(|e| AppError::internal(format!("HTTP 客户端构建失败: {e}")))?;

        let mut headers = Vec::new();
        let token = link.token.trim();
        if !token.is_empty() {
            let name = if link.token_kind == TOKEN_KIND_PLATFORM {
                crate::sync::PLATFORM_TOKEN_HEADER
            } else {
                crate::sync::TOKEN_HEADER
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
        let body: Value = resp.json().await.map_err(|e| {
            AppError::Internal(format!(
                "{} 返回的不是 NexTerm 的响应（HTTP {status}）：{e}。\
                 确认地址填的是盒子上的 NexTerm 应用入口、且令牌有效",
                self.base
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

/// 校验并规范化地址；**挡住「把令牌明文发到公网」**。
fn normalize_base(raw: &str) -> AppResult<String> {
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

    /// 令牌按种类挂到正确的头上；没填就不挂。
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
        let platform = SyncClient::new(&mk(TOKEN_KIND_PLATFORM, "t1")).unwrap();
        assert_eq!(platform.headers[0].0, crate::sync::PLATFORM_TOKEN_HEADER);
        let app = SyncClient::new(&mk(TOKEN_KIND_APP, "t2")).unwrap();
        assert_eq!(app.headers[0].0, crate::sync::TOKEN_HEADER);
        let none = SyncClient::new(&mk(TOKEN_KIND_APP, "   ")).unwrap();
        assert!(none.headers.is_empty(), "空白令牌不该挂头");
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
}
