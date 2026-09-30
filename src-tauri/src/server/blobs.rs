//! 浏览器版的文件中转站（受控暂存区）。
//!
//! # 为什么需要它
//!
//! 桌面版「上传」的源是用户磁盘上的一个**路径**（原生对话框给的），「下载」的落点同理。
//! 浏览器里没有这个路径 —— 用户要么从懒猫网盘选、要么从本机选，拿到的都是**字节**。
//! 于是服务端提供一个受控暂存区，把「字节」与「盒子上的路径」互相兑换：
//!
//! | 方向 | 做法 |
//! |---|---|
//! | 上传 | 浏览器 `POST /files/blob` 落盘 → 拿到盒子路径 → 照旧 `fs_upload(local, remote)` |
//! | 下载 | `POST /files/blob/reserve` 拿空路径 → `fs_download(remote, local)` → `GET /files/blob` 回读 |
//!
//! 好处是传输内核（断点续传、进度事件、tar 打包、终端日志导出）**一行都不用改** ——
//! 浏览器与桌面走的是同一条已经测过的搬运路径。
//!
//! # 磁盘布局：为什么每个暂存项是一个目录
//!
//! ```text
//! <data>/blobs/<id>/<原始文件名>     ← 会被巡检清理
//! <data>/files/<id>/<原始文件名>     ← 私钥这类长期落点，不清理
//! ```
//!
//! 文件名**必须是纯原名**，不能挂 `<id>-` 之类的前缀：上传那条路上，前端是拿
//! 「本地路径的 basename」当远端文件名的（`FileTree.upload`），
//! 前缀会一路变成远端服务器上的文件名。把 id 放到**目录**上就没有这个问题，
//! 同时仍然保证了「一个 id 唯一对应一个落点」。
//!
//! # 为什么还要接平台的文件选择器
//!
//! 单靠这个中转站，用户仍然只能从**本机**选文件，而商店硬性要求「有上传/下载就必须
//! 接入懒猫网盘的自动拦截文件选择器」。平台那份注入脚本拦的是浏览器**原生文件入口**
//! （`showOpenFilePicker()` / `<input type="file">` / `showSaveFilePicker()` / 带 `download`
//! 的 `blob:` / `data:` 链接），所以前端只要老老实实用这些原生 API，
//! 注入就会把「网盘文件 / 本地文件」的统一窗口顶上来。
//!
//! **这也正是本项目没有引官方 `@lazycatcloud/lzc-file-pickers` 的原因**：那是个 Vue
//! 组件，而本前端是 React，为了它嵌一层 Vue 运行时不划算；而注入路线的官方说明里
//! 本来就写着「你愿意改业务前端源码，那可以自己接库，不必走 inject」——
//! 我们选的是第三条路：**保持原生 API，让平台的注入来完成升级**。
//! 代价是必须真的走原生 API：在 web 分支里打字输路径是拦不到的（见 `src/ui/dialogs.ts`）。
//!
//! # 边界
//!
//! 所有落点都在数据目录下的两个子目录里，`id` 只接受 26 位字母数字，文件名里的路径
//! 分隔符会被替换 ⇒ **不可能借这几个接口读写盒子上的任意路径**。容器里数据目录是
//! `/lzcapp/var/nexterm`，卸载应用即随卷清空。
//!
//! ⚠️ 这个数据目录取自 `ServerCtx.data_dir`（即 `--data-dir` / `NEXTERM_DATA_DIR`
//! 解析后的**那一份**），不再自己读环境变量：否则命令行传了 `--data-dir` 时，
//! 库落在一个目录、暂存区落在另一个目录，浏览器上传的文件会凭空消失。
//!
//! 这几个接口都**不额外做鉴权**，与 `/rpc` 一致：应用本身没有登录页，
//! 鉴权整体交给平台 ingress（见 `docs/LAZYCAT-PORT.md` §7.1）。

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::extract::{Query, State as AxumState};
use axum::http::{header, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use futures::stream::{self, Stream, StreamExt};
use serde::Deserialize;
use serde_json::json;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

use crate::ids::new_id;
use crate::server::ServerCtx;

/// 单次搬运的块大小，与 `fs` 内核保持一致。
const CHUNK: usize = 256 * 1024;

/// 暂存区巡检周期。
const SWEEP_EVERY: Duration = Duration::from_secs(10 * 60);

/// 暂存文件的存活上限。上传的源文件、下载的落点都是「用完即弃」，
/// 给足两小时是为了容忍「选完文件、发了一会儿呆再点开始」。
///
/// 唯一会被长期引用的落点是**私钥**（`persist=1`，落 `files/` 而非 `blobs/`），
/// 它不参与巡检 —— 否则用户配好的密钥认证会在几小时后自己失效。
const STAGE_TTL: Duration = Duration::from_secs(2 * 60 * 60);

/// 接口的查询参数（几个接口共用一份，字段都可选）。
#[derive(Debug, Deserialize)]
pub struct BlobQuery {
    /// `POST` / `reserve`：原始文件名。**只取基名**，用于给上传推出远端文件名。
    #[serde(default)]
    name: Option<String>,
    /// `GET` / `DELETE`：暂存项 id。
    #[serde(default)]
    id: Option<String>,
    /// `POST`：`true` 时落进长期目录（私钥这类需要跨会话保留的落点）。
    #[serde(default)]
    persist: Option<bool>,
}

/// 暂存根目录（会被巡检清理）。
fn stage_root(data_dir: &Path) -> PathBuf {
    data_dir.join("blobs")
}

/// 长期根目录（不清理）。私钥落这里。
fn keep_root(data_dir: &Path) -> PathBuf {
    data_dir.join("files")
}

/// 请求要落进哪个根目录。
fn target_root(data_dir: &Path, persist: bool) -> PathBuf {
    if persist {
        keep_root(data_dir)
    } else {
        stage_root(data_dir)
    }
}

/// 从文件名里取一个**只能当文件名用**的版本。
///
/// 关键点不是「保留原名好不好看」，而是**不能带路径分隔符** ——
/// 否则 `../` 就能把这些接口变成「往盒子任意路径写文件」。
fn safe_name(raw: Option<&str>) -> String {
    let base = raw
        .unwrap_or("")
        .rsplit(['/', '\\'])
        .next()
        .unwrap_or("")
        .trim();
    let cleaned: String = base
        .chars()
        .map(|c| if c == '\0' || c.is_control() { '_' } else { c })
        .collect();
    let cleaned = cleaned.trim_start_matches('.').trim();
    if cleaned.is_empty() {
        "blob".to_string()
    } else {
        // 名字只用于「可读性」和「给上传当远端默认名」，长度给个上限。
        cleaned.chars().take(120).collect()
    }
}

/// id 的形状校验：26 位字母数字（ULID 的 `to_string()`）。
///
/// 这里**故意只查字符集**、不去解析 ULID：目的是保证它拼进路径后不可能越出根目录，
/// 而不是做业务校验。允许的字符里没有 `.`、`/`、`\`，这一条就足够了。
fn is_blob_id(s: &str) -> bool {
    s.len() == 26 && s.chars().all(|c| c.is_ascii_alphanumeric())
}

/// 暂存项目录：`<root>/<id>/`。
fn item_dir(root: &Path, id: &str) -> PathBuf {
    root.join(id)
}

/// 在暂存项目录里找那一个文件，返回真实路径。
///
/// 不做「id 拼成文件名」的反查：文件名是**纯原名**（见模块文档的布局说明），
/// 只有扫目录才拿得到。目录里只会有这一个文件。
async fn find_item(root: &Path, id: &str) -> Option<PathBuf> {
    let mut entries = tokio::fs::read_dir(item_dir(root, id)).await.ok()?;
    while let Ok(Some(entry)) = entries.next_entry().await {
        if entry
            .file_type()
            .await
            .map(|t| t.is_file())
            .unwrap_or(false)
        {
            return Some(entry.path());
        }
    }
    None
}

/// 把请求体原样流到一个新文件里。返回落盘字节数。
///
/// 流式而不是 `axum::body::to_bytes`：上传动辄几百 MB，
/// 先整个读进内存再落盘会在盒子上留下一根与文件同大的内存尖峰。
async fn drain_to_file(body: Body, path: &Path) -> std::io::Result<u64> {
    let mut file = tokio::fs::File::create(path).await?;
    let mut written: u64 = 0;
    let mut stream = body.into_data_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|e| std::io::Error::other(e.to_string()))?;
        file.write_all(&chunk).await?;
        written += chunk.len() as u64;
    }
    file.flush().await?;
    Ok(written)
}

/// 把一个已打开的文件变成响应体流。
///
/// 同样是为了不在盒子上把整个文件读进内存。
fn file_stream(file: tokio::fs::File) -> impl Stream<Item = Result<Bytes, std::io::Error>> {
    stream::try_unfold(file, move |mut f| async move {
        let mut buf = vec![0u8; CHUNK];
        let n = f.read(&mut buf).await?;
        if n == 0 {
            return Ok(None);
        }
        buf.truncate(n);
        Ok(Some((Bytes::from(buf), f)))
    })
}

/// 错误响应。这几个接口是给 `fetch` 直接用的字节通道，回真正的状态码更省事
/// （前端只要判 `res.ok`）。
fn err(code: StatusCode, msg: &str) -> Response {
    (code, msg.to_string()).into_response()
}

/// 一个够用的文件名编码：非 ASCII / 特殊字符全部 `%XX`，
/// 配合 `filename*=UTF-8''` 让浏览器能还原出中文名。
fn content_disposition(name: &str) -> String {
    let mut out = String::with_capacity(name.len() * 3);
    for b in name.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(*b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    format!("attachment; filename*=UTF-8''{out}")
}

// ── 处理器 ────────────────────────────────────────────────────────────

/// `POST /files/blob?name=&persist=` —— 浏览器把字节交上来，换一个盒子上的路径。
///
/// 上传与「选私钥」都走这里：上传要的就是一个能喂给 `fs_upload` 的本地路径，
/// 私钥要的是一个能让内核按路径读到的落点。
pub async fn post_blob(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    Query(q): Query<BlobQuery>,
    body: Body,
) -> Response {
    let dir = item_dir(
        &target_root(&ctx.data_dir, q.persist.unwrap_or(false)),
        &new_id(),
    );
    if let Err(e) = tokio::fs::create_dir_all(&dir).await {
        tracing::warn!(target: "blob", dir = %dir.display(), error = %e, "暂存目录创建失败");
        return err(StatusCode::INTERNAL_SERVER_ERROR, "暂存目录不可用");
    }
    let path = dir.join(safe_name(q.name.as_deref()));
    match drain_to_file(body, &path).await {
        Ok(written) => {
            tracing::debug!(target: "blob", bytes = written, "已暂存");
            Json(json!({
                "ok": true,
                "data": { "id": dir_id(&dir), "path": path.to_string_lossy(), "bytes": written }
            }))
            .into_response()
        }
        Err(e) => {
            let _ = tokio::fs::remove_dir_all(&dir).await;
            tracing::warn!(target: "blob", error = %e, "暂存写入失败");
            err(StatusCode::INTERNAL_SERVER_ERROR, "暂存写入失败")
        }
    }
}

/// `POST /files/blob/reserve?name=` —— 先要一个空路径，由服务端自己往里写。
///
/// 「下载」用：浏览器没有字节可交，但需要一个落点让 `fs_download` 去写，
/// 写完再 `GET` 回读。`fs_download` 用的是 `File::create`（截断），
/// 所以预建的空文件不会被当成「已存在的内容」。
pub async fn post_reserve(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    Query(q): Query<BlobQuery>,
) -> Response {
    let dir = item_dir(&stage_root(&ctx.data_dir), &new_id());
    if let Err(e) = tokio::fs::create_dir_all(&dir).await {
        tracing::warn!(target: "blob", dir = %dir.display(), error = %e, "暂存目录创建失败");
        return err(StatusCode::INTERNAL_SERVER_ERROR, "暂存目录不可用");
    }
    let path = dir.join(safe_name(q.name.as_deref()));
    match tokio::fs::File::create(&path).await {
        Ok(_) => Json(json!({
            "ok": true,
            "data": { "id": dir_id(&dir), "path": path.to_string_lossy() }
        }))
        .into_response(),
        Err(e) => {
            let _ = tokio::fs::remove_dir_all(&dir).await;
            tracing::warn!(target: "blob", error = %e, "预留失败");
            err(StatusCode::INTERNAL_SERVER_ERROR, "预留失败")
        }
    }
}

/// `GET /files/blob?id=` —— 把暂存内容流回浏览器。
pub async fn get_blob(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    Query(q): Query<BlobQuery>,
) -> Response {
    let Some(id) = q.id.as_deref().filter(|s| is_blob_id(s)) else {
        return err(StatusCode::BAD_REQUEST, "id 非法");
    };
    let root = stage_root(&ctx.data_dir);
    let Some(path) = find_item(&root, id).await else {
        return err(StatusCode::NOT_FOUND, "暂存项不存在或已过期");
    };
    match tokio::fs::File::open(&path).await {
        Ok(file) => {
            let name = path
                .file_name()
                .map(|n| n.to_string_lossy().to_string())
                .unwrap_or_else(|| "download.bin".to_string());
            let mut resp = Body::from_stream(file_stream(file)).into_response();
            resp.headers_mut().insert(
                header::CONTENT_TYPE,
                HeaderValue::from_static("application/octet-stream"),
            );
            if let Ok(v) = HeaderValue::from_str(&content_disposition(&name)) {
                resp.headers_mut().insert(header::CONTENT_DISPOSITION, v);
            }
            resp
        }
        Err(e) => {
            tracing::warn!(target: "blob", path = %path.display(), error = %e, "暂存读取失败");
            err(StatusCode::NOT_FOUND, "暂存项不可读")
        }
    }
}

/// `DELETE /files/blob?id=` —— 浏览器用完就删。
///
/// 前端不删也不会漏：巡检任务兜底。但正常路径上前端**必须**删，
/// 否则一次几 GB 的上传会在盒子上留一份无主副本直到两小时后。
pub async fn delete_blob(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    Query(q): Query<BlobQuery>,
) -> Response {
    let Some(id) = q.id.as_deref().filter(|s| is_blob_id(s)) else {
        return err(StatusCode::BAD_REQUEST, "id 非法");
    };
    let dir = item_dir(&stage_root(&ctx.data_dir), id);
    match tokio::fs::remove_dir_all(&dir).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            err(StatusCode::NOT_FOUND, "暂存项不存在或已过期")
        }
        Err(e) => {
            tracing::warn!(target: "blob", dir = %dir.display(), error = %e, "暂存删除失败");
            err(StatusCode::INTERNAL_SERVER_ERROR, "删除失败")
        }
    }
}

/// 从 `<root>/<id>/` 反推 id。
fn dir_id(dir: &Path) -> String {
    dir.file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default()
}

/// 巡检任务：清掉过期的暂存项。
///
/// 为什么不靠「请求边界」顺手清：一次上传失败、浏览器被关掉、
/// 用户点完选文件就跑了 —— 这几种都不会再发任何请求，
/// 只有按时间扫才收得回来。只扫 `blobs/`，`files/` 里的私钥不碰。
pub async fn sweep_task(data_dir: PathBuf) {
    let root = stage_root(&data_dir);
    loop {
        tokio::time::sleep(SWEEP_EVERY).await;
        let removed = sweep_once(&root, STAGE_TTL).await;
        if removed > 0 {
            tracing::info!(target: "blob", removed, "已清理过期暂存项");
        }
    }
}

/// 扫一遍并删除过期项，返回删除个数（拆出来是为了能单独测）。
async fn sweep_once(root: &Path, ttl: Duration) -> usize {
    let Ok(mut entries) = tokio::fs::read_dir(root).await else {
        return 0;
    };
    let mut removed = 0;
    while let Ok(Some(entry)) = entries.next_entry().await {
        let Ok(meta) = entry.metadata().await else {
            continue;
        };
        let Ok(modified) = meta.modified() else {
            continue;
        };
        let expired = modified
            .elapsed()
            .map(|age| age > ttl)
            // 修改时间落在未来（时钟被改过）时按「没过期」处理，宁可留着。
            .unwrap_or(false);
        if expired && tokio::fs::remove_dir_all(entry.path()).await.is_ok() {
            removed += 1;
        }
    }
    removed
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 文件名必须不可能带出目录 —— 这是这套接口唯一的越界面。
    #[test]
    fn safe_name_cannot_escape_dir() {
        assert_eq!(safe_name(Some("report.txt")), "report.txt");
        assert_eq!(safe_name(Some("../../etc/passwd")), "passwd");
        assert_eq!(safe_name(Some(r"..\..\win.ini")), "win.ini");
        assert_eq!(safe_name(Some("/absolute/path.log")), "path.log");
        assert_eq!(safe_name(Some("..")), "blob");
        assert_eq!(safe_name(None), "blob");
        assert!(!safe_name(Some("a\u{0}b")).contains('\u{0}'));
    }

    #[test]
    fn blob_id_shape_is_enforced() {
        assert!(is_blob_id(&new_id()));
        assert!(!is_blob_id("../../etc"));
        assert!(!is_blob_id("01ARZ3NDEKTSV4RRFFQ69G5FA")); // 25 位，少一位
        assert!(!is_blob_id("01ARZ3NDEKTSV4RRFFQ69G5FAVW-")); // 27 位
    }

    /// 暂存项的 basename 必须是**用户选的原始文件名**。
    ///
    /// 这不是审美问题：上传那条路是拿本地路径的 basename 当远端文件名的，
    /// 一旦这里变成 `<id>-名字`，远端服务器上就会出现带 ULID 前缀的文件。
    #[test]
    fn staged_basename_is_the_original_name() {
        let root = PathBuf::from("/data/blobs");
        let id = new_id();
        let path = item_dir(&root, &id).join(safe_name(Some("报表 2026.xlsx")));
        assert_eq!(
            path.file_name().and_then(|n| n.to_str()),
            Some("报表 2026.xlsx")
        );
        assert_eq!(dir_id(&item_dir(&root, &id)), id);
    }

    /// 落盘 → 找得到 → 流回来，字节必须一致。
    #[tokio::test]
    async fn stage_then_stream_roundtrip() {
        let root = std::env::temp_dir().join(format!("nexterm-blob-test-{}", new_id()));
        let id = new_id();
        let dir = item_dir(&root, &id);
        tokio::fs::create_dir_all(&dir).await.expect("建目录");

        let path = dir.join(safe_name(Some("报表 2026.bin")));
        let payload: Vec<u8> = (0..5000u32).map(|i| (i % 251) as u8).collect();
        tokio::fs::write(&path, &payload).await.expect("写");

        let found = find_item(&root, &id).await.expect("应能找到");
        assert_eq!(found, path);

        let file = tokio::fs::File::open(&found).await.expect("打开");
        let collected: Vec<u8> = file_stream(file)
            .map(|chunk| chunk.expect("读块"))
            .fold(Vec::new(), |mut acc, chunk| async move {
                acc.extend_from_slice(&chunk);
                acc
            })
            .await;
        assert_eq!(collected, payload, "回读字节必须与落盘一致");

        // 未知 id 找不到，且不应该抛。
        assert!(find_item(&root, &new_id()).await.is_none());

        tokio::fs::remove_dir_all(&root).await.ok();
    }

    /// 巡检只删过期的，不碰新鲜的。
    #[tokio::test]
    async fn sweep_removes_only_expired() {
        let root = std::env::temp_dir().join(format!("nexterm-blob-sweep-{}", new_id()));
        let dir = item_dir(&root, &new_id());
        tokio::fs::create_dir_all(&dir).await.expect("建目录");
        let fresh = dir.join("fresh.txt");
        tokio::fs::write(&fresh, b"x").await.expect("写");

        // ttl 给足 ⇒ 刚建的项不该被删。
        assert_eq!(sweep_once(&root, Duration::from_secs(3600)).await, 0);
        assert!(fresh.is_file(), "新鲜文件不该被删");

        // ttl 为 0 ⇒ 任何已存在的项都过期（elapsed() > 0）。
        assert_eq!(sweep_once(&root, Duration::from_secs(0)).await, 1);
        assert!(!dir.exists(), "过期项应被整目录删除");

        tokio::fs::remove_dir_all(&root).await.ok();
    }
}
