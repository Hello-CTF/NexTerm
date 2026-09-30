//! 极简静态托管：把前端 `dist/` 从服务端发出去。
//!
//! # 为什么不用 tower-http 的 ServeDir
//!
//! 只为一件事：**要往 `index.html` 里注入一行标记**，让前端知道
//! 「我是被 nexterm-server 服务的，不是跑在 Tauri 里，也不是演示模式」。
//!
//! 前端本来靠 `window.__TAURI_INTERNALS__` 判断环境，而纯浏览器里它同样是
//! `undefined` —— 与 `pnpm dev` 的演示模式无法区分。加这一行是**确定性**的
//! 判别手段（探测 `/rpc` 是否可达那种做法是异步的，模块求值时就该有答案）。
//!
//! 代价是「发出去的文件 ≠ 磁盘上的文件」，所以这里只做一处最小字符串插入，
//! 不做任何其它改写。

use std::path::{Path, PathBuf};

use axum::body::Body;
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};

/// 注入到 `</head>` 之前的那一行。
const TRANSPORT_MARKER: &str = r#"<script>window.__NEXTERM_TRANSPORT__="web";</script>"#;

pub async fn serve(root: &Path, uri_path: &str) -> Response {
    let Some(rel) = sanitize(uri_path) else {
        return (StatusCode::BAD_REQUEST, "路径非法").into_response();
    };

    let candidate = root.join(&rel);
    let is_index = rel.as_os_str().is_empty() || rel == Path::new("index.html");

    if !is_index && candidate.is_file() {
        return file_response(&candidate, false).await;
    }
    // SPA 兜底：未命中的路径一律回 index.html。
    // 直接访问 `/` 之外的路由、或者在子路径下刷新，都靠这条活着。
    file_response(&root.join("index.html"), true).await
}

/// 归一化请求路径。返回 `None` = 试图越出 root。
fn sanitize(uri_path: &str) -> Option<PathBuf> {
    let trimmed = uri_path.trim_start_matches('/');
    // 先解一层 %XX 再判 —— 只查字面 `..` 会被 `%2e%2e%2f` 绕过。
    let decoded = percent_decode(trimmed);
    if decoded
        .split('/')
        .any(|seg| seg == ".." || seg.contains('\\'))
    {
        return None;
    }
    if decoded.contains('\0') {
        return None;
    }
    Some(PathBuf::from(decoded))
}

fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let hex = std::str::from_utf8(&bytes[i + 1..i + 3]).ok();
            if let Some(v) = hex.and_then(|h| u8::from_str_radix(h, 16).ok()) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

async fn file_response(path: &Path, is_html: bool) -> Response {
    match tokio::fs::read(path).await {
        Ok(bytes) => {
            let ctype = content_type(path);
            if is_html || ctype.starts_with("text/html") {
                let html = String::from_utf8_lossy(&bytes).into_owned();
                let injected = inject_marker(&html);
                // 注入后的长度与原文件不同 ⇒ **必须**用实体长度，不能复刻原文件头。
                let mut resp = Body::from(injected).into_response();
                resp.headers_mut().insert(
                    header::CONTENT_TYPE,
                    header::HeaderValue::from_static("text/html; charset=utf-8"),
                );
                resp.headers_mut().insert(
                    header::CACHE_CONTROL,
                    header::HeaderValue::from_static("no-cache"),
                );
                return resp;
            }
            let len = bytes.len();
            let mut resp = Body::from(bytes).into_response();
            resp.headers_mut().insert(
                header::CONTENT_TYPE,
                header::HeaderValue::from_str(ctype).unwrap_or_else(|_| {
                    header::HeaderValue::from_static("application/octet-stream")
                }),
            );
            resp.headers_mut().insert(
                header::CONTENT_LENGTH,
                header::HeaderValue::from_str(&len.to_string()).unwrap(),
            );
            // 前端资源名带内容哈希，可以长缓存；HTML 不行（上面已单独设 no-cache）。
            if path
                .file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.contains('-') && n.len() > 24)
            {
                resp.headers_mut().insert(
                    header::CACHE_CONTROL,
                    header::HeaderValue::from_static("public, max-age=31536000, immutable"),
                );
            }
            resp
        }
        Err(e) => {
            tracing::warn!(target: "server", path = %path.display(), error = %e, "静态文件读失败");
            (
                StatusCode::NOT_FOUND,
                format!("未找到 {}（web root 配置对不对？）", path.display()),
            )
                .into_response()
        }
    }
}

fn inject_marker(html: &str) -> String {
    match html.find("</head>") {
        Some(i) => format!("{}{}{}", &html[..i], TRANSPORT_MARKER, &html[i..]),
        None => format!("{TRANSPORT_MARKER}{html}"),
    }
}

fn content_type(path: &Path) -> &'static str {
    match path
        .extension()
        .and_then(|e| e.to_str())
        .unwrap_or("")
        .to_ascii_lowercase()
        .as_str()
    {
        "html" | "htm" => "text/html; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" | "map" => "application/json; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "gif" => "image/gif",
        "webp" => "image/webp",
        "ico" => "image/x-icon",
        "woff" => "font/woff",
        "woff2" => "font/woff2",
        "ttf" => "font/ttf",
        "otf" => "font/otf",
        "wasm" => "application/wasm",
        "txt" => "text/plain; charset=utf-8",
        _ => "application/octet-stream",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sanitize_blocks_traversal() {
        assert!(sanitize("/assets/index.js").is_some());
        assert!(sanitize("/../Cargo.toml").is_none());
        assert!(sanitize("/%2e%2e/Cargo.toml").is_none());
        assert!(sanitize("/a/../../b").is_none());
        assert!(sanitize("/").is_some());
    }

    /// 注入必须**真的**落在 `</head>` 之前 —— 否则前端读不到标记，
    /// 会掉进演示模式，然后「所有数据都是假的」这件事没人会立刻发现。
    #[test]
    fn marker_injected_before_head_close() {
        let out = inject_marker("<html><head><title>x</title></head><body></body></html>");
        let marker_at = out.find("__NEXTERM_TRANSPORT__").expect("标记应存在");
        let head_at = out.find("</head>").expect("应保留 head 闭合");
        assert!(marker_at < head_at, "标记必须在 </head> 之前：{out}");
    }

    #[test]
    fn marker_still_applied_without_head() {
        let out = inject_marker("<div>hi</div>");
        assert!(out.contains("__NEXTERM_TRANSPORT__"));
    }

    #[test]
    fn content_types_cover_frontend_assets() {
        assert!(content_type(Path::new("a.js")).starts_with("text/javascript"));
        assert!(content_type(Path::new("a.css")).starts_with("text/css"));
        assert_eq!(content_type(Path::new("a.png")), "image/png");
        assert_eq!(
            content_type(Path::new("a.unknown")),
            "application/octet-stream"
        );
    }
}
