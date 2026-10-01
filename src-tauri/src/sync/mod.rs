//! 跨实例资产同步（§12.4 / M5）。
//!
//! # 它解决什么
//!
//! 桌面版和盒子上的服务端是**两个独立实例**，各持一份 SQLite。日常用法是
//! 「在桌面上把主机配好，之后手机上用浏览器接着连」—— 没有同步，用户就得在
//! 两边各配一遍，而且改了一边另一边立刻变旧。
//!
//! # 为什么只有两个原语
//!
//! 这里只提供 `export`（从本地库取一批资产打包）与 `apply`（把一包落进本地库）。
//! 方向由调用方决定：推 = 「本地 export → 远端 apply」，拉 = 「远端 export → 本地 apply」。
//!
//! **内核刻意不猜方向。** 自动双向合并要求三路 diff 加冲突策略，而 SSH 私钥这类
//! 载荷根本不可合并（§12.4 已定 last-write-wins + 墓碑）。与其做个半吊子的自动
//! 合并让用户不敢用，不如让用户在界面上逐条选方向 —— 「同步哪些资产」本来就说好了
//! 要可选，方向的显式化是顺带的收益。
//!
//! # 冲突：默认不覆盖更新的那一份
//!
//! `apply` 在 `force = false` 时**跳过「本地这份更新」的条目**，并在报告里逐条说明。
//! 理由是同步最常见的误操作是「拿一台旧机器的包盖掉新改动」，静默覆盖会直接丢数据；
//! 而「跳过了什么」是用户能看懂、能补救的（看到报告 → 勾强制覆盖 → 重来）。
//!
//! # 私钥正文要跟着资产走，不能只搬路径
//!
//! `key_path` 只是**源端**的文件路径；到了对端那个路径根本不存在 —— 资产看似
//! 同步成功，一连就报「私钥文件不存在」。引用型私钥凭据（`PrivateKeyPayload.file`）
//! 同理，库里只有路径、没有正文。所以导出时必须把**正文**读进来：
//!
//! - `auth_kind == "key"` 且 `key_path` 非空：读文件 → 造一条**内容型**私钥凭据，
//!   把资产的 `cred_id` 指向它、`key_path` 置空。导入侧因此自然落进
//!   「`key_path` 为空 + `cred_id` 指向 `private_key` 凭据」这条
//!   `session::build_ssh_params` 已经支持的分支，一行都不用改；
//! - `private_key` 凭据本身是引用型（`file` 有值）：读文件 → 补进 `key` 并把
//!   `file` 置空。**必须置空** —— `build_ssh_params` 解出来是 `file` 优先于
//!   `key`（`(Some(path), _) => SshAuth::Key{path}`），只带正文不清 `file`
//!   等于没带。
//!
//! 读不到文件时**不能静默**：导出侧把原因记进警告（走 `tracing`，见 `export`），
//! 对端导入时还会按「本机路径是否存在」再兜一条**用户可见**的警告
//! （`ImportReport.warnings`，见 `apply_assets` / `apply_creds`）—— 那条路
//! push / pull 两个方向都能到达界面，是「别让用户以为同步了其实没有」的兜底。

pub mod bundle;
pub mod client;

pub use bundle::{
    AssetPayload, CredPayload, DigestEntry, GroupPayload, ImportReport, SyncBundle, SyncDigest,
    PROTOCOL,
};

use std::collections::{HashMap, HashSet};

use zeroize::Zeroizing;

use crate::error::{AppError, AppResult};
use crate::store::models::{AssetRow, CredentialRow};
use crate::store::Store;
use crate::vault::payload::{self, PrivateKeyPayload};
use crate::vault::Vault;

/// 实例标识所在的 setting 键。
const SETTING_ORIGIN: &str = "sync.origin";

/// 同步令牌所在的 setting 键（仅服务端使用）。
const SETTING_TOKEN: &str = "sync.token";

/// **应用层**同步令牌的 HTTP 头名 —— 这条路**只有这一把钥匙**。
///
/// 令牌由**服务端自己**生成（微服上的 NexTerm：设置 → 资产同步；自建服务器同理），
/// 客户端只负责把它带上。两个部署位置（懒猫微服 / 自建服务器）用的是同一个头、
/// 同一套校验，差别只在「平台网关那一段有没有」——见 `client` 的模块文档。
pub const TOKEN_HEADER: &str = "x-nexterm-sync-token";

/// 平台鉴权通过后注入的用户标识头（官方 `/http-request-headers`）。
///
/// `/sync/rpc` 的准入判定里除了令牌还认它：**这个头存在 = 平台网关已经鉴过权**。
/// 它不是给桌面端用的（桌面端走令牌），而是留给「经由平台登录门进来的请求」
/// 那条路，见 `server::authorize_sync`。
pub const PLATFORM_USER_HEADER: &str = "x-hc-user-id";

/// 本实例的标识（持久化，首次调用时生成）。
///
/// 用 8 位十六进制而不是 ULID：它是要显示在人眼前、口头念得出来的东西
/// （「这批是从 `3f9a1c02` 推来的」），不是主键，短一点更好用。
pub async fn origin(store: &Store) -> AppResult<String> {
    if let Some(v) = store.setting_get(SETTING_ORIGIN).await? {
        if !v.trim().is_empty() {
            return Ok(v);
        }
    }
    let mut raw = [0u8; 4];
    use rand::RngCore;
    rand::rngs::OsRng.fill_bytes(&mut raw);
    let id: String = raw.iter().map(|b| format!("{b:02x}")).collect();
    store.setting_set(SETTING_ORIGIN, &id).await?;
    Ok(id)
}

/// 取本机的同步令牌，没有就生成一个（幂等）。
///
/// # 这个令牌是什么权限
///
/// 它落在 `/sync/rpc` 上，而那个端点**调的是同一张 RPC 表** —— 所以令牌等价于
/// 「对该实例的完全控制权」，不只是「能同步资产」。界面文案必须据实说明，
/// 不能做成看起来像只读配对码的样子。
///
/// 之所以还需要它：服务端的 `/rpc` 靠懒猫平台登录门挡在公网后面，而**桌面版
/// 不是浏览器、过不了那道门**。没有令牌，同步接口就只能裸奔在公网上。
pub async fn ensure_token(store: &Store) -> AppResult<String> {
    if let Some(v) = store.setting_get(SETTING_TOKEN).await? {
        if !v.trim().is_empty() {
            return Ok(v);
        }
    }
    let token = new_token();
    store.setting_set(SETTING_TOKEN, &token).await?;
    Ok(token)
}

/// 换一个令牌（旧令牌立即失效）。用于「令牌不小心贴到别处了」。
pub async fn rotate_token(store: &Store) -> AppResult<String> {
    let token = new_token();
    store.setting_set(SETTING_TOKEN, &token).await?;
    Ok(token)
}

fn new_token() -> String {
    let mut raw = [0u8; 32];
    use rand::RngCore;
    rand::rngs::OsRng.fill_bytes(&mut raw);
    use base64::Engine;
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(raw)
}

/// 校验令牌。**只读不生成** —— 校验路径上偷偷写库，会让「令牌没配好」
/// 看起来像「服务端在正常工作」。
pub async fn verify_token(store: &Store, presented: &str) -> AppResult<()> {
    let expected = store
        .setting_get(SETTING_TOKEN)
        .await?
        .filter(|s| !s.trim().is_empty())
        .ok_or_else(|| AppError::Forbidden("服务端尚未生成同步令牌".into()))?;
    if presented.is_empty() {
        return Err(AppError::Forbidden("缺少同步令牌".into()));
    }
    if !ct_eq(&expected, presented) {
        return Err(AppError::Forbidden("同步令牌不正确".into()));
    }
    Ok(())
}

/// 定长比较，不提前返回。
///
/// 令牌是长期有效的共享秘密，逐字节提前返回会把「对了几个字符」通过响应时间
/// 泄出去。这里不引入 `subtle` 这类依赖，手写一个够用的版本：长度不同直接
/// 判否（长度本身不是秘密），否则把差异累积起来一次判。
fn ct_eq(a: &str, b: &str) -> bool {
    let (a, b) = (a.as_bytes(), b.as_bytes());
    if a.len() != b.len() {
        return false;
    }
    let mut diff = 0u8;
    for (x, y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}

/// 本机摘要 —— 不含任何密文，可以随便传。
///
/// `include_deleted = true` 是同步场景要的：墓碑必须出现在摘要里，否则界面
/// 上看不出「对端删过这条」，拉取时也就无从勾选。
///
/// **内置资产（「当前设备」）不出现在摘要里**：它在每台设备上都是各自的本机，
/// 参与对照表只会让人误以为「可以把它推过去」。`export` 也一并排除它，
/// 两处口径一致。
pub async fn digest(store: &Store) -> AppResult<SyncDigest> {
    let rows = store.asset_list(true).await?;
    let assets = rows
        .iter()
        .filter(|r| !r.builtin)
        .map(|r| DigestEntry {
            id: r.id.clone(),
            name: r.name.clone(),
            kind: r.kind.clone(),
            host: r.host.clone(),
            username: r.username.clone(),
            updated_at: r.updated_at,
            deleted_at: r.deleted_at,
            has_cred: r.cred_id.is_some(),
            group_id: r.group_id.clone(),
        })
        .collect();
    Ok(SyncDigest {
        origin: origin(store).await?,
        protocol: PROTOCOL,
        app_version: env!("CARGO_PKG_VERSION").to_string(),
        desktop: cfg!(feature = "desktop"),
        assets,
    })
}

/// 导出（带「用户可见」的警告列表）。
///
/// # 为什么是这个形状（而不是改 `export` 的返回类型）
///
/// `export` 的调用方分布在 `commands::sync::sync_export` 与 `sync::client`
/// （`push` / `pull`）里，它们各有既定的返回形状（`SyncBundle` / `ImportReport`）。
/// 把它们全改成 `(SyncBundle, Vec<String>)` 会牵动一批文件；而**用户真正需要的
/// 兜底并不在导出侧** —— 见下。
///
/// 所以这里拆成两层：
/// - `export_with_warnings` 返回 `(包, 警告)`，信息完整、可测；
/// - [`export`] 保持原签名（调用方不动），把警告走 `tracing::warn!` 记下来。
///
/// 而**面向用户**的那条警告在**导入侧**：`apply` 会按**目标机的文件系统**再判
/// 一次「引用的私钥文件在不在」，结果进 `ImportReport.warnings`。这条设计比在
/// 导出侧报更结实 —— 它判的是「这台机器连不连得上」，而不是「源端读没读到」；
/// 而且 push / pull 两个方向都会经过 `apply`，警告必然到得了界面。
pub async fn export_with_warnings(
    store: &Store,
    vault: &Vault,
    asset_ids: &[String],
    with_creds: bool,
) -> AppResult<(SyncBundle, Vec<String>)> {
    let mut out = SyncBundle::new(origin(store).await?);
    let mut warnings: Vec<String> = Vec::new();
    let mut group_ids: HashSet<String> = HashSet::new();
    let mut cred_ids: HashSet<String> = HashSet::new();
    // 内容型私钥凭据的 id 已经在包里出现过？见 `synced_key_cred_id`。
    let mut inline_cred_ids: HashSet<String> = HashSet::new();

    for id in asset_ids {
        let row = match store.asset_get(id).await {
            Ok(r) => r,
            // 界面选完到按下同步之间被删掉了：跳过比整批失败好
            Err(AppError::NotFound(_)) => continue,
            Err(e) => return Err(e),
        };
        if row.builtin {
            continue;
        }
        if let Some(gid) = row.group_id.clone() {
            collect_ancestors(store, &gid, &mut group_ids).await;
        }

        let mut payload: AssetPayload = (&row).into();

        // key_path 型资产：把文件正文读进凭据库，资产改指那条新凭据。
        let mut cred_replaced = false;
        if with_creds && payload.auth_kind.as_deref() == Some("key") {
            if let Some(path) = payload.key_path.clone().filter(|p| !p.trim().is_empty()) {
                match std::fs::read_to_string(&path) {
                    Ok(content) => {
                        let cred_id = synced_key_cred_id(&row.id);
                        let passphrase =
                            file_key_passphrase(store, vault, row.cred_id.as_deref()).await?;
                        let secret = PrivateKeyPayload::inline(content, passphrase).encode();
                        if inline_cred_ids.insert(cred_id.clone()) {
                            out.creds.push(CredPayload {
                                id: cred_id.clone(),
                                name: format!("{} 的私钥", row.name),
                                kind: payload::KIND_PRIVATE_KEY.to_string(),
                                secret: Zeroizing::new(secret),
                            });
                        }
                        payload.cred_id = Some(cred_id);
                        payload.key_path = None;
                        cred_replaced = true;
                    }
                    Err(e) => warnings.push(format!(
                        "资产「{}」引用的私钥文件 {} 读不到（{}），私钥正文未能随行：\
                         这次同步到对端后仍然连不上该主机。请把私钥存入凭据库，\
                         或在对端重新指定该文件路径后重试",
                        row.name, path, e
                    )),
                }
            }
        }

        // 原有的「带上 cred_id 指向的凭据」照旧。但路径已被正文取代时不重复带：
        // 此时 `cred_id` 指向的多半是原先那条**口令**凭据，它的值已经并进新的
        // 内容型私钥凭据里，单独带走只会在对端造一条没人引用的孤儿凭据。
        if with_creds && !cred_replaced {
            if let Some(cid) = payload.cred_id.clone() {
                cred_ids.insert(cid);
            }
        }
        out.assets.push(payload);
    }

    for gid in group_ids {
        if let Ok(g) = store.group_get(&gid).await {
            out.groups.push(g.into());
        }
    }

    if !cred_ids.is_empty() {
        // **锁定态直接报错，不静默跳过。** 静默的后果是「资产同步过去了、
        // 密码没过去」，而用户在另一端点连接才发现 —— 那时已经很难联想起
        // 是这次同步的问题。
        let dek = vault.dek().await?;
        for cid in cred_ids {
            // 悬空引用（asset.cred_id 指向已删凭据）：跳过，由导入侧报出来
            let Ok(row) = store.credential_get_row(&cid).await else {
                continue;
            };
            let secret = Vault::decrypt_credential(&dek, &row)?;
            // 引用型私钥凭据：把正文读进来、清掉 `ref`（见模块文档）。
            let secret = inline_referenced_key(secret, &row, &mut warnings);
            out.creds.push(CredPayload {
                id: row.id.clone(),
                name: row.name.clone(),
                kind: row.kind.clone(),
                secret,
            });
        }
    }

    Ok((out, warnings))
}

/// 把选中的资产打成一包（既有签名，调用方在 `commands` / `sync::client`）。
///
/// 自动带上两样用户没直接勾选、但不带就会坏的东西：
/// - **祖先分组**：`asset.group_id` 是外键，分组不过去的话导入侧只能把它扔进
///   「未分组」—— 用户看到的是「同步成功了，但目录结构全平了」。
/// - **被引用的凭据**（`with_creds` 时）：没有凭据的密码类资产在另一端是死的
///   （点连接就报「凭据不存在」）；私钥类还会把**正文**读进来（见模块文档）。
///
/// 内置资产（「当前设备」）**一律不导出**：它是「本机」这个概念在**每台设备上
/// 各自的锚点**，搬到另一边只会多出一台连不上的假机器。
///
/// 本函数只是 [`export_with_warnings`] 的薄包装：真正的警告在这里只走日志
/// （用户可见的那条在导入侧，理由见 `export_with_warnings` 的文档）。
pub async fn export(
    store: &Store,
    vault: &Vault,
    asset_ids: &[String],
    with_creds: bool,
) -> AppResult<SyncBundle> {
    let (bundle, warnings) = export_with_warnings(store, vault, asset_ids, with_creds).await?;
    for w in &warnings {
        tracing::warn!(target: "sync", "导出警告：{w}");
    }
    Ok(bundle)
}

/// 从 `key_path` 型资产派生的**内容型私钥凭据 id**。
///
/// 必须**确定性**：同一条资产每次导出都要得到同一个 id，否则重复同步会在对端
/// 攒下一堆内容相同、id 不同的私钥凭据（每次都新建而非更新）。从 `asset.id`
/// 派生即可 —— 它在一台设备上唯一且稳定。
fn synced_key_cred_id(asset_id: &str) -> String {
    format!("synckey-{asset_id}")
}

/// 解出「文件私钥」所配的口令。
///
/// 与 `session::build_ssh_params` 对「`key_path` 非空」那条支路的判定**逐条对齐**：
/// `cred_id` 指向的凭据只有**不是 `private_key` 时**才被当成口令；指向
/// `private_key` 凭据时它对本文件私钥没有意义，忽略（不当口令用）。
async fn file_key_passphrase(
    store: &Store,
    vault: &Vault,
    cred_id: Option<&str>,
) -> AppResult<Option<String>> {
    let Some(id) = cred_id else {
        return Ok(None);
    };
    // 悬空引用：按「没有口令」处理，由导入侧兜底报。
    let Ok(row) = store.credential_get_row(id).await else {
        return Ok(None);
    };
    if row.kind == payload::KIND_PRIVATE_KEY {
        return Ok(None);
    }
    let dek = vault.dek().await?;
    let plain = Vault::decrypt_credential(&dek, &row)?;
    let text = plain.trim().to_string();
    Ok(if text.is_empty() { None } else { Some(text) })
}

/// 引用型私钥凭据 → 内容型：把 `ref` 指向的文件读进来，清掉 `ref`。
///
/// 读不到就原样返回 + 记一条警告（**不静默**）：这条凭据到了对端仍是引用型，
/// 而对端没有这个文件，连接时会在 `build_ssh_params` 里按路径读文件失败。
fn inline_referenced_key(
    secret: Zeroizing<String>,
    row: &CredentialRow,
    warnings: &mut Vec<String>,
) -> Zeroizing<String> {
    if row.kind != payload::KIND_PRIVATE_KEY {
        return secret;
    }
    let parsed = PrivateKeyPayload::parse(secret.as_str());
    let Some(path) = parsed.file.clone() else {
        return secret; // 已经是内容型（或旧数据），没什么可做
    };
    match std::fs::read_to_string(&path) {
        // 保留口令；`file` 置空 —— `build_ssh_params` 里 `file` 优先于 `key`。
        Ok(content) => {
            Zeroizing::new(PrivateKeyPayload::inline(content, parsed.passphrase).encode())
        }
        Err(e) => {
            warnings.push(format!(
                "凭据「{}」引用的私钥文件 {} 读不到（{}），私钥正文未能随行：\
                 对端连接时会因找不到这个文件而失败。请把私钥存入凭据库后重试",
                row.name, path, e
            ));
            secret
        }
    }
}

/// 把一包落进本地库。
///
/// 写入顺序是**被外键定死的**，不能调换：
/// 分组（`asset.group_id` → `asset_group`）→ 凭据（`asset.cred_id` → `credential`）
/// → 资产。分组之间还要**父先于子**（`asset_group.parent_id` 是自引用外键）。
///
/// `force = false` 时跳过「本地更新」的条目（见模块文档）。
pub async fn apply(
    store: &Store,
    vault: &Vault,
    bundle: &SyncBundle,
    force: bool,
) -> AppResult<ImportReport> {
    bundle.verify_protocol()?;
    let mut report = ImportReport::default();

    apply_groups(store, bundle, &mut report).await?;
    apply_creds(store, vault, bundle, &mut report).await?;
    apply_assets(store, bundle, force, &mut report).await;

    Ok(report)
}

/// 分组：一律 upsert，不做冲突检查。
///
/// 分组只有名字和位置，冲突的后果轻（改回来就是了），而**跳过会造成实伤**：
/// 资产挂在没建出来的分组下，外键直接失败。所以这里的选择是「宁可靠错位置，
/// 也不能让资产落不了地」。
async fn apply_groups(
    store: &Store,
    bundle: &SyncBundle,
    report: &mut ImportReport,
) -> AppResult<()> {
    for idx in topo_order(&bundle.groups) {
        let g = &bundle.groups[idx];
        // 上级不在本机（对端只发了子分组）：降级成顶级，而不是让整批失败
        let parent = match &g.parent_id {
            Some(pid) if store.group_get(pid).await.is_err() => {
                report.warn(format!(
                    "分组「{}」的上级分组不在本机，已作为顶级分组导入",
                    g.name
                ));
                None
            }
            other => other.clone(),
        };
        match store
            .group_upsert(
                &g.id,
                parent.as_deref(),
                &g.name,
                g.sort,
                g.created_at,
                g.updated_at,
            )
            .await
        {
            Ok(true) => report.groups_created += 1,
            Ok(false) => report.groups_updated += 1,
            Err(e) => {
                report.refused += 1;
                report.warn(format!("分组「{}」未能导入：{e}", g.name));
            }
        }
    }
    Ok(())
}

/// 凭据：解不开就整批失败（不能只写一半）。
async fn apply_creds(
    store: &Store,
    vault: &Vault,
    bundle: &SyncBundle,
    report: &mut ImportReport,
) -> AppResult<()> {
    if bundle.creds.is_empty() {
        return Ok(());
    }
    // 与 `export` 同一条理由：锁定态必须报错，不能静默把凭据丢掉后
    // 还告诉用户「同步成功」。
    let dek = vault.dek().await?;
    let kek_hint = kek_hint_of(vault).await;
    for c in &bundle.creds {
        let existed = store.credential_get_row(&c.id).await.is_ok();
        // **用本机的 DEK 重新加密**：对端密文在这边解不开（密钥体系各自独立），
        // 所以明文过河、落地即重封。
        let (nonce, blob) = Vault::encrypt_credential(&dek, c.secret.as_str()).await?;
        match store
            .credential_put(crate::store::CredentialInput {
                id: Some(c.id.clone()),
                name: c.name.clone(),
                kind: c.kind.clone(),
                nonce,
                blob,
                kek_hint: kek_hint.clone(),
            })
            .await
        {
            Ok(_) => {
                if existed {
                    report.creds_updated += 1;
                } else {
                    report.creds_created += 1;
                }
                // 引用型私钥凭据：`ref` 指向的是**源端**路径，到本机多半不存在
                // （Windows 路径搬到 Linux 服务端更是必然不存在）。与资产那条同源 ——
                // 用户需要知道「这条凭据在本机连不上」，只读文件在不在，不读内容。
                if c.kind == payload::KIND_PRIVATE_KEY {
                    let parsed = PrivateKeyPayload::parse(c.secret.as_str());
                    if let Some(path) = parsed.file.as_deref() {
                        if !std::path::Path::new(path).exists() {
                            report.warn(format!(
                                "凭据「{}」引用的私钥文件 {} 在本机不存在，连接会失败",
                                c.name, path
                            ));
                        }
                    }
                }
            }
            Err(e) => {
                report.refused += 1;
                report.warn(format!("凭据「{}」未能导入：{e}", c.name));
            }
        }
    }
    Ok(())
}

/// 资产：逐条 upsert，单条失败不掀桌。
async fn apply_assets(store: &Store, bundle: &SyncBundle, force: bool, report: &mut ImportReport) {
    for a in &bundle.assets {
        let mut row: AssetRow = a.clone().into();

        // 外键兜底。这两条不加的话，`asset_upsert` 会直接抛 DB 错误，
        // 而错误里只有约束名，用户根本看不出是哪条资产、为什么。
        if let Some(gid) = row.group_id.clone() {
            if store.group_get(&gid).await.is_err() {
                report.warn(format!(
                    "资产「{}」原属的分组不在本机，已导入到「未分组」",
                    row.name
                ));
                row.group_id = None;
            }
        }
        if let Some(cid) = row.cred_id.clone() {
            if store.credential_get_row(&cid).await.is_err() {
                report.warn(format!(
                    "资产「{}」关联的凭据不在本机，已解除关联（同步时没勾选「含凭据」？）",
                    row.name
                ));
                row.cred_id = None;
            }
        }

        let existing = store.asset_get(&row.id).await.ok();
        if let Some(cur) = &existing {
            if cur.builtin {
                report.refused += 1;
                continue;
            }
            if cur.updated_at > row.updated_at && !force {
                report.skipped_newer += 1;
                report.warn(format!(
                    "「{}」本机这份更新（{} > 对端 {}），已跳过；要覆盖请勾选「强制覆盖」",
                    cur.name, cur.updated_at, row.updated_at
                ));
                continue;
            }
        }

        match store.asset_upsert(&row).await {
            Ok(true) => report.assets_created += 1,
            Ok(false) => report.assets_updated += 1,
            Err(e) => {
                report.refused += 1;
                report.warn(format!("资产「{}」未能导入：{e}", row.name));
                continue;
            }
        }

        // 私钥「文件型」资产：对端把「本地文件私钥」搬了过来，但 `key_path` 记的是
        // **源端**的路径，在本机几乎一定不存在 ⇒ 这条资产在本机连不上。只看文件在不在，
        // 不读内容。墓碑不查（它马上就要被删掉，报「连不上」只会误导）。
        if row.deleted_at.is_none() && row.auth_kind.as_deref() == Some("key") {
            if let Some(path) = row.key_path.as_deref().filter(|p| !p.trim().is_empty()) {
                if !std::path::Path::new(path).exists() {
                    report.warn(format!(
                        "资产「{}」引用的私钥文件 {} 在本机不存在，连接会失败",
                        row.name, path
                    ));
                }
            }
        }
    }
}

/// 与 `commands::vault::seal` 保持一致的 KEK 标识。
async fn kek_hint_of(vault: &Vault) -> String {
    match vault.status().await.mode.as_str() {
        "dpapi" => "dpapi".to_string(),
        _ => "master:0".to_string(),
    }
}

/// 沿 `parent_id` 往上收尽祖先分组（含起点自身）。
///
/// `guard` 防的是环：`group_update` 已经在写入侧挡了成环，但同步收的是**对端
/// 的数据**——对端可能被手工改过库、或是更老的版本、或是别的实现。这里不能
/// 假设数据是合法的，否则一个环就让同步死循环。
async fn collect_ancestors(store: &Store, start: &str, out: &mut HashSet<String>) {
    let mut cursor = Some(start.to_string());
    let mut guard = 0;
    while let Some(id) = cursor {
        if !out.insert(id.clone()) {
            break; // 已经收过 —— 有环，停
        }
        guard += 1;
        if guard > 64 {
            break;
        }
        cursor = match store.group_get(&id).await {
            Ok(g) => g.parent_id,
            Err(_) => None,
        };
    }
}

/// 按「在包内的深度」升序排序分组下标 —— 保证父先于子写入。
///
/// 包内没出现的父（对端没发）深度按 0 算：那些会被 `apply_groups` 降级成
/// 顶级分组，放到最前面正好。
fn topo_order(groups: &[GroupPayload]) -> Vec<usize> {
    let index: HashMap<&str, usize> = groups
        .iter()
        .enumerate()
        .map(|(i, g)| (g.id.as_str(), i))
        .collect();
    let mut depths: Vec<usize> = Vec::with_capacity(groups.len());
    for g in groups {
        let mut depth = 0usize;
        let mut cursor = g.parent_id.as_deref();
        let mut guard = 0;
        while let Some(pid) = cursor {
            match index.get(pid) {
                Some(&pi) => {
                    depth += 1;
                    cursor = groups[pi].parent_id.as_deref();
                }
                None => break,
            }
            guard += 1;
            if guard > 64 {
                break; // 环：停在当前深度，稳定排序会给出确定顺序
            }
        }
        depths.push(depth);
    }
    let mut order: Vec<usize> = (0..groups.len()).collect();
    // 稳定排序：同深度的保持原顺序，测试与实现都确定性
    order.sort_by_key(|&i| depths[i]);
    order
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::{AssetInput, CredentialInput, GroupInput};

    /// 只要一个空库（不涉及凭据库的用例）。
    async fn fresh_store() -> Store {
        Store::open_in_memory().await.unwrap()
    }

    /// 一起建库 + 建凭据库（两者共用同一个 store，这才是真实装配）。
    async fn pair() -> (std::sync::Arc<Store>, std::sync::Arc<Vault>) {
        let store = std::sync::Arc::new(Store::open_in_memory().await.unwrap());
        let vault = Vault::load(std::sync::Arc::clone(&store)).await;
        vault.init_master("test-password-1").await.unwrap();
        (store, vault)
    }

    async fn mk_asset(store: &Store, name: &str, host: &str) -> AssetRow {
        store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: name.into(),
                host: Some(host.into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("password".into()),
                key_path: None,
                cred_id: None,
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap()
    }

    async fn mk_cred(store: &Store, vault: &Vault, name: &str, secret: &str) -> String {
        let dek = vault.dek().await.unwrap();
        let (nonce, blob) = Vault::encrypt_credential(&dek, secret).await.unwrap();
        store
            .credential_put(CredentialInput {
                id: None,
                name: name.into(),
                kind: "password".into(),
                nonce,
                blob,
                kek_hint: "master:0".into(),
            })
            .await
            .unwrap()
    }

    /// 造一个「用本地文件私钥」的资产（`auth_kind == "key"` + `key_path`）。
    async fn mk_key_asset(
        store: &Store,
        name: &str,
        key_path: &str,
        cred_id: Option<String>,
    ) -> AssetRow {
        store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: name.into(),
                host: Some("10.0.0.1".into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("key".into()),
                key_path: Some(key_path.into()),
                cred_id,
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap()
    }

    /// 写一个真实临时文件并返回路径（用完删；名字带随机 id 防碰撞）。
    fn tmp_key_file(tag: &str, body: &str) -> std::path::PathBuf {
        let mut p = std::env::temp_dir();
        p.push(format!(
            "nexterm-sync-test-{tag}-{}.key",
            crate::ids::new_id()
        ));
        std::fs::write(&p, body).unwrap();
        p
    }

    /// `key_path` 型资产：导出要把**文件正文**读成内容型私钥凭据，并把资产改成
    /// 「`cred_id` 指向它 + `key_path` 为空」；再落到另一台机器上仍能解出正文 ——
    /// 这就是「服务端能连上」的内核证据。
    #[tokio::test]
    async fn key_path_asset_carries_key_body() {
        let (a_store, a_vault) = pair().await;
        let body = "-----BEGIN OPENSSH PRIVATE KEY-----\nSECRET\n-----END OPENSSH PRIVATE KEY-----";
        let file = tmp_key_file("kp", body);
        let a = mk_key_asset(&a_store, "用文件的", &file.to_string_lossy(), None).await;

        let (bundle, warnings) =
            export_with_warnings(&a_store, &a_vault, std::slice::from_ref(&a.id), true)
                .await
                .unwrap();
        assert!(warnings.is_empty(), "能读到文件时不该有警告: {warnings:?}");

        let asset = &bundle.assets[0];
        assert!(asset.key_path.is_none(), "key_path 必须被清空");
        let cid = asset
            .cred_id
            .as_deref()
            .expect("必须指向新凭据")
            .to_string();
        assert_eq!(cid, synced_key_cred_id(&a.id), "cred id 要确定性派生");
        let cred = bundle
            .creds
            .iter()
            .find(|c| c.id == cid)
            .expect("包内要有这条内容型私钥凭据");
        assert_eq!(cred.kind, payload::KIND_PRIVATE_KEY);
        let parsed = PrivateKeyPayload::parse(cred.secret.as_str());
        assert_eq!(parsed.key.as_deref(), Some(body), "正文要与文件逐字一致");
        assert!(parsed.file.is_none(), "内容型不该带 ref");

        // 端到端：B 侧用自己的密钥重新加密后仍能解出正确正文。
        let (b_store, b_vault) = pair().await;
        let report = apply(&b_store, &b_vault, &bundle, false).await.unwrap();
        assert_eq!(report.assets_created, 1);
        assert_eq!(report.creds_created, 1);
        assert!(
            report.warnings.is_empty(),
            "不该有警告: {:?}",
            report.warnings
        );

        let got = b_store.asset_get(&a.id).await.unwrap();
        assert!(got.key_path.is_none(), "对端落地后 key_path 也该为空");
        assert_eq!(got.cred_id.as_deref(), Some(cid.as_str()));
        let row = b_store.credential_get_row(&cid).await.unwrap();
        let dek = b_vault.dek().await.unwrap();
        let plain = Vault::decrypt_credential(&dek, &row).unwrap();
        let parsed = PrivateKeyPayload::parse(plain.as_str());
        assert_eq!(parsed.key.as_deref(), Some(body), "对端要能解出同一把私钥");

        std::fs::remove_file(&file).ok();
    }

    /// 文件口令凭据要跟着私钥一起过去（并进内容型凭据的 `passphrase`），
    /// 否则带口令的私钥到了对端照样解不开。
    #[tokio::test]
    async fn key_path_asset_keeps_passphrase_cred() {
        let (a_store, a_vault) = pair().await;
        let body = "PRIVATE-KEY-WITH-PASSPHRASE";
        let file = tmp_key_file("kp-pass", body);
        // 口令存在另一条非 private_key 凭据里（与 build_ssh_params 的判定一致）
        let pass_id = mk_cred(&a_store, &a_vault, "私钥口令", "p@ss").await;
        let a = mk_key_asset(
            &a_store,
            "带口令的文件私钥",
            &file.to_string_lossy(),
            Some(pass_id.clone()),
        )
        .await;

        let (bundle, warnings) =
            export_with_warnings(&a_store, &a_vault, std::slice::from_ref(&a.id), true)
                .await
                .unwrap();
        assert!(warnings.is_empty(), "{warnings:?}");
        let cid = bundle.assets[0].cred_id.clone().unwrap();
        let cred = bundle.creds.iter().find(|c| c.id == cid).unwrap();
        let parsed = PrivateKeyPayload::parse(cred.secret.as_str());
        assert_eq!(parsed.key.as_deref(), Some(body));
        assert_eq!(parsed.passphrase.as_deref(), Some("p@ss"), "口令不能丢");
        // 原口令凭据不该再作为孤儿被单独带走
        assert!(
            !bundle.creds.iter().any(|c| c.id == pass_id),
            "口令已并进新凭据，不该再造一条孤儿凭据"
        );

        std::fs::remove_file(&file).ok();
    }

    /// 文件读不到时**不能静默**：资产原样保留（不被偷偷改成空私钥），
    /// 导入侧还会按目标机文件系统给出一条面向用户的警告。
    #[tokio::test]
    async fn missing_key_file_warns_instead_of_silently_dropping() {
        let (a_store, a_vault) = pair().await;
        let missing = std::env::temp_dir().join(format!("nexterm-nope-{}", crate::ids::new_id()));
        let missing_s = missing.to_string_lossy().to_string();
        let a = mk_key_asset(&a_store, "文件丢了", &missing_s, None).await;

        let (bundle, warnings) =
            export_with_warnings(&a_store, &a_vault, std::slice::from_ref(&a.id), true)
                .await
                .unwrap();
        assert_eq!(warnings.len(), 1, "导出侧要记一条: {warnings:?}");
        assert!(warnings[0].contains("读不到"), "{}", warnings[0]);

        let asset = &bundle.assets[0];
        assert_eq!(
            asset.key_path.as_deref(),
            Some(missing_s.as_str()),
            "读不到时资产必须原样保留，不能被改坏"
        );
        assert!(asset.cred_id.is_none());
        assert!(bundle.creds.is_empty(), "不该凭空造一条空私钥凭据");

        // 用户可见的那条在导入侧（push / pull 都会经过它）
        let (b_store, b_vault) = pair().await;
        let report = apply(&b_store, &b_vault, &bundle, false).await.unwrap();
        assert_eq!(report.assets_created, 1);
        assert!(
            report.warnings.iter().any(|w| w.contains("在本机不存在")),
            "导入侧要给出人话警告: {:?}",
            report.warnings
        );
    }

    /// 引用型私钥凭据在**源端**也读不到时：正文带不过去，但导入侧要给出人话警告
    /// （否则用户看到「同步成功」，对端点连接却是死的）。
    #[tokio::test]
    async fn missing_referenced_key_cred_warns_on_import() {
        let (a_store, a_vault) = pair().await;
        let missing =
            std::env::temp_dir().join(format!("nexterm-nope-ref-{}", crate::ids::new_id()));
        let missing_s = missing.to_string_lossy().to_string();
        let refd = PrivateKeyPayload::referenced(missing_s.clone(), None);
        let dek = a_vault.dek().await.unwrap();
        let (nonce, blob) = Vault::encrypt_credential(&dek, &refd.encode())
            .await
            .unwrap();
        let cid = a_store
            .credential_put(CredentialInput {
                id: None,
                name: "引用型私钥(丢了)".into(),
                kind: payload::KIND_PRIVATE_KEY.into(),
                nonce,
                blob,
                kek_hint: "master:0".into(),
            })
            .await
            .unwrap();
        let a = a_store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: "引用型".into(),
                host: Some("10.0.0.1".into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("key".into()),
                key_path: None,
                cred_id: Some(cid),
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap();

        let (bundle, warnings) =
            export_with_warnings(&a_store, &a_vault, std::slice::from_ref(&a.id), true)
                .await
                .unwrap();
        assert_eq!(warnings.len(), 1, "源端读不到要记一条: {warnings:?}");

        let (b_store, b_vault) = pair().await;
        let report = apply(&b_store, &b_vault, &bundle, false).await.unwrap();
        assert!(
            report.warnings.iter().any(|w| w.contains("在本机不存在")),
            "导入侧要给人话警告: {:?}",
            report.warnings
        );
    }

    /// 引用型私钥凭据：导出要把 `ref` 读成正文并**清空 `file`**
    /// （否则 `build_ssh_params` 里 `file` 优先于 `key`，带正文也白搭），口令保留。
    #[tokio::test]
    async fn referenced_private_key_cred_is_inlined() {
        let (store, vault) = pair().await;
        let body = "REFERENCED-KEY-BODY";
        let file = tmp_key_file("ref", body);
        let refd =
            PrivateKeyPayload::referenced(file.to_string_lossy().to_string(), Some("pw".into()));
        let dek = vault.dek().await.unwrap();
        let (nonce, blob) = Vault::encrypt_credential(&dek, &refd.encode())
            .await
            .unwrap();
        let cid = store
            .credential_put(CredentialInput {
                id: None,
                name: "引用型私钥".into(),
                kind: payload::KIND_PRIVATE_KEY.into(),
                nonce,
                blob,
                kek_hint: "master:0".into(),
            })
            .await
            .unwrap();
        // 资产 key_path 为空 → 私钥完全来自这条凭据（引用型）
        let a = store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: "引用型私钥资产".into(),
                host: Some("10.0.0.1".into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("key".into()),
                key_path: None,
                cred_id: Some(cid.clone()),
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap();

        let (bundle, warnings) =
            export_with_warnings(&store, &vault, std::slice::from_ref(&a.id), true)
                .await
                .unwrap();
        assert!(warnings.is_empty(), "{warnings:?}");
        let cred = bundle.creds.iter().find(|c| c.id == cid).unwrap();
        let parsed = PrivateKeyPayload::parse(cred.secret.as_str());
        assert_eq!(parsed.key.as_deref(), Some(body), "正文要读进来");
        assert!(parsed.file.is_none(), "file 必须清空，否则仍按路径读");
        assert_eq!(parsed.passphrase.as_deref(), Some("pw"), "口令保留");

        std::fs::remove_file(&file).ok();
    }

    #[tokio::test]
    async fn origin_is_stable_and_persisted() {
        let store = fresh_store().await;
        let a = origin(&store).await.unwrap();
        let b = origin(&store).await.unwrap();
        assert_eq!(
            a, b,
            "同一实例的 origin 必须稳定，否则每次同步都像换了一台设备"
        );
        assert_eq!(a.len(), 8);
    }

    /// 导出必须带上祖先分组 —— 否则导入后资产全掉进「未分组」，
    /// 用户看到的是「同步成功但目录没了」。
    #[tokio::test]
    async fn export_collects_ancestor_groups() {
        let (store, vault) = pair().await;
        let root = store
            .group_create(GroupInput {
                parent_id: None,
                name: "生产".into(),
                sort: 0,
            })
            .await
            .unwrap();
        let child = store
            .group_create(GroupInput {
                parent_id: Some(root.id.clone()),
                name: "华东".into(),
                sort: 0,
            })
            .await
            .unwrap();
        let mut a = mk_asset(&store, "web-1", "10.0.0.1").await;
        a = store
            .asset_update(
                &a.id,
                Some(Some(child.id.clone())),
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
            )
            .await
            .unwrap();
        assert_eq!(a.group_id.as_deref(), Some(child.id.as_str()));

        let bundle = export(&store, &vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        let ids: Vec<&str> = bundle.groups.iter().map(|g| g.id.as_str()).collect();
        assert!(
            ids.contains(&root.id.as_str()) && ids.contains(&child.id.as_str()),
            "两级祖先都该在包里，实际: {ids:?}"
        );
    }

    /// 内置资产（「当前设备」）不该被搬到别的设备上。
    #[tokio::test]
    async fn export_skips_builtin_local_asset() {
        let (store, vault) = pair().await;
        let builtin = store.asset_ensure_builtin_local().await.unwrap();
        let bundle = export(&store, &vault, std::slice::from_ref(&builtin.id), true)
            .await
            .unwrap();
        assert!(bundle.assets.is_empty(), "内置资产不该出现在包里");
    }

    /// 凭据库锁定时导出必须**报错**，不能静默把密码丢掉 ——
    /// 静默的结果是「另一端资产是死的」，而用户很难联想到是同步的问题。
    #[tokio::test]
    async fn export_fails_when_vault_locked() {
        let (store, vault) = pair().await;
        let cid = mk_cred(&store, &vault, "口令", "s3cret").await;
        let a = store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: "带密码的".into(),
                host: Some("10.0.0.9".into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("password".into()),
                key_path: None,
                cred_id: Some(cid),
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap();

        vault.lock().await;
        let err = export(&store, &vault, std::slice::from_ref(&a.id), true)
            .await
            .expect_err("锁定态导出必须失败");
        assert_eq!(err.code(), "vault_locked");
    }

    /// 端到端：A 导出 → B 导入，资产/凭据/分组都到位，且 B 侧凭据**用自己的密钥**
    /// 重新加密（能解出原值）。
    #[tokio::test]
    async fn bundle_travels_and_creds_are_reencrypted() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;

        let g = a_store
            .group_create(GroupInput {
                parent_id: None,
                name: "生产".into(),
                sort: 1,
            })
            .await
            .unwrap();
        let cid = mk_cred(&a_store, &a_vault, "root 口令", "p@ss-中文").await;
        let a = a_store
            .asset_create(AssetInput {
                group_id: Some(g.id.clone()),
                kind: "ssh".into(),
                name: "web-1".into(),
                host: Some("10.0.0.1".into()),
                port: Some(2222),
                username: Some("root".into()),
                auth_kind: Some("password".into()),
                key_path: None,
                cred_id: Some(cid.clone()),
                options_json: r#"{"keepalive":30}"#.into(),
                tags: "prod".into(),
                note: "主力".into(),
                sort: 3,
            })
            .await
            .unwrap();

        let bundle = export(&a_store, &a_vault, std::slice::from_ref(&a.id), true)
            .await
            .unwrap();
        let report = apply(&b_store, &b_vault, &bundle, false).await.unwrap();

        assert_eq!(report.assets_created, 1);
        assert_eq!(report.creds_created, 1);
        assert_eq!(report.groups_created, 1);
        assert!(
            report.warnings.is_empty(),
            "不该有警告: {:?}",
            report.warnings
        );

        let got = b_store.asset_get(&a.id).await.unwrap();
        assert_eq!(got.name, "web-1");
        assert_eq!(got.host.as_deref(), Some("10.0.0.1"));
        assert_eq!(got.port, Some(2222));
        assert_eq!(got.group_id.as_deref(), Some(g.id.as_str()));
        assert_eq!(got.cred_id.as_deref(), Some(cid.as_str()));
        assert_eq!(got.options_json, r#"{"keepalive":30}"#);
        assert_eq!(got.note, "主力");
        assert_eq!(got.sort, 3);

        // B 侧凭据用 B 的密钥加密：能解出原值
        let b_dek = b_vault.dek().await.unwrap();
        let row = b_store.credential_get_row(&cid).await.unwrap();
        let plain = Vault::decrypt_credential(&b_dek, &row).unwrap();
        assert_eq!(plain.as_str(), "p@ss-中文");
    }

    /// 再一次同步：同一 ID **更新**而不是新增（这是「两端认同 ID」的全部意义）。
    #[tokio::test]
    async fn second_sync_updates_instead_of_duplicating() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;
        let a = mk_asset(&a_store, "web-1", "10.0.0.1").await;

        let b1 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        let r1 = apply(&b_store, &b_vault, &b1, false).await.unwrap();
        assert_eq!(r1.assets_created, 1);

        // A 改个名再同步
        tokio::time::sleep(std::time::Duration::from_millis(5)).await;
        a_store
            .asset_update(
                &a.id,
                None,
                Some("web-1-改名".into()),
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
            )
            .await
            .unwrap();
        let b2 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        let r2 = apply(&b_store, &b_vault, &b2, false).await.unwrap();

        assert_eq!(r2.assets_created, 0, "第二次该是更新不是新增");
        assert_eq!(r2.assets_updated, 1);
        assert_eq!(
            b_store.asset_list(false).await.unwrap().len(),
            1,
            "不该多出一条"
        );
        assert_eq!(b_store.asset_get(&a.id).await.unwrap().name, "web-1-改名");
    }

    /// 默认跳过「本地更新」的那一份，并把原因说出来；`force` 时才覆盖。
    #[tokio::test]
    async fn newer_local_is_skipped_unless_forced() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;
        let a = mk_asset(&a_store, "web-1", "10.0.0.1").await;

        // 先同步一份到 B
        let b1 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        apply(&b_store, &b_vault, &b1, false).await.unwrap();

        // B 本地改得更新
        tokio::time::sleep(std::time::Duration::from_millis(5)).await;
        b_store
            .asset_update(
                &a.id,
                None,
                Some("B 改的".into()),
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
                None,
            )
            .await
            .unwrap();

        // A 那边（旧包）再推一次
        let b2 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        let r = apply(&b_store, &b_vault, &b2, false).await.unwrap();
        assert_eq!(r.skipped_newer, 1, "本地更新时默认该跳过");
        assert_eq!(r.assets_updated, 0);
        assert_eq!(
            b_store.asset_get(&a.id).await.unwrap().name,
            "B 改的",
            "默认不能覆盖掉本地的新改动"
        );
        assert!(r.warnings.iter().any(|w| w.contains("强制覆盖")));

        // 强制覆盖才真的盖掉
        let r2 = apply(&b_store, &b_vault, &b2, true).await.unwrap();
        assert_eq!(r2.assets_updated, 1);
        assert_eq!(b_store.asset_get(&a.id).await.unwrap().name, "web-1");
    }

    /// 墓碑要能传播：对端删了，本地跟着软删（否则下次又被推回来）。
    #[tokio::test]
    async fn tombstone_propagates() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;
        let a = mk_asset(&a_store, "web-1", "10.0.0.1").await;

        let b1 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        apply(&b_store, &b_vault, &b1, false).await.unwrap();
        assert_eq!(b_store.asset_list(false).await.unwrap().len(), 1);

        // A 删掉（软删）
        tokio::time::sleep(std::time::Duration::from_millis(5)).await;
        a_store.asset_delete(&a.id).await.unwrap();

        let b2 = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        assert_eq!(b2.assets.len(), 1, "墓碑也要在包里");
        assert!(b2.assets[0].deleted_at.is_some());

        let r = apply(&b_store, &b_vault, &b2, false).await.unwrap();
        assert_eq!(r.assets_updated, 1);
        assert!(
            b_store.asset_list(false).await.unwrap().is_empty(),
            "删除应随墓碑传播过去"
        );
        // 但记录还在（墓碑），能看见
        assert_eq!(b_store.asset_list(true).await.unwrap().len(), 1);
    }

    /// 内置「当前设备」永远拒绝被同步写入。
    #[tokio::test]
    async fn builtin_is_never_overwritten() {
        let (b_store, b_vault) = pair().await;
        b_store.asset_ensure_builtin_local().await.unwrap();
        let builtin_id = crate::store::BUILTIN_LOCAL_ASSET_ID.to_string();

        // 伪造一个针对内置 ID 的包（正常 export 不会产生）
        let mut bundle = SyncBundle::new("evil".into());
        bundle.assets.push(AssetPayload {
            id: builtin_id.clone(),
            group_id: None,
            kind: "ssh".into(),
            name: "冒充的".into(),
            host: Some("1.2.3.4".into()),
            port: Some(22),
            username: None,
            auth_kind: None,
            key_path: None,
            cred_id: None,
            options_json: "{}".into(),
            tags: String::new(),
            note: String::new(),
            sort: 0,
            created_at: 0,
            updated_at: i64::MAX,
            deleted_at: None,
        });
        let r = apply(&b_store, &b_vault, &bundle, true).await.unwrap();
        assert_eq!(r.refused, 1);
        assert_eq!(r.assets_updated, 0);
        let row = b_store.asset_get(&builtin_id).await.unwrap();
        assert_eq!(row.name, crate::store::BUILTIN_LOCAL_ASSET_NAME);
        assert!(row.builtin);
    }

    /// 悬空引用要降级 + 报警告，而不是抛一个只有约束名的 DB 错误。
    #[tokio::test]
    async fn dangling_refs_are_dropped_with_warning() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;
        let cid = mk_cred(&a_store, &a_vault, "口令", "x").await;
        let g = a_store
            .group_create(GroupInput {
                parent_id: None,
                name: "组".into(),
                sort: 0,
            })
            .await
            .unwrap();
        let a = a_store
            .asset_create(AssetInput {
                group_id: Some(g.id.clone()),
                kind: "ssh".into(),
                name: "web-1".into(),
                host: Some("10.0.0.1".into()),
                port: Some(22),
                username: None,
                auth_kind: Some("password".into()),
                key_path: None,
                cred_id: Some(cid),
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .unwrap();

        // 只导资产、不带凭据；分组也手工剔掉，模拟「对端只发了资产」
        let mut bundle = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        bundle.creds.clear();
        bundle.groups.clear();

        let r = apply(&b_store, &b_vault, &bundle, false).await.unwrap();
        assert_eq!(r.assets_created, 1);
        assert_eq!(
            r.warnings.len(),
            2,
            "分组与凭据各该有一条警告: {:?}",
            r.warnings
        );
        let got = b_store.asset_get(&a.id).await.unwrap();
        assert!(got.group_id.is_none(), "分组不存在时该落到未分组");
        assert!(got.cred_id.is_none(), "凭据不存在时该解除关联");
    }

    /// 父分组必须先于子分组写入（自引用外键定死）。
    #[test]
    fn topo_order_puts_parents_first() {
        let mk = |id: &str, parent: Option<&str>| GroupPayload {
            id: id.into(),
            parent_id: parent.map(|s| s.to_string()),
            name: id.into(),
            sort: 0,
            created_at: 0,
            updated_at: 0,
        };
        // 故意把子放在前面
        let groups = vec![
            mk("child", Some("mid")),
            mk("leaf", Some("child")),
            mk("mid", Some("root")),
            mk("root", None),
        ];
        let order = topo_order(&groups);
        let pos = |name: &str| {
            order
                .iter()
                .position(|&i| groups[i].id == name)
                .expect("应在排序结果里")
        };
        assert!(pos("root") < pos("mid"));
        assert!(pos("mid") < pos("child"));
        assert!(pos("child") < pos("leaf"));
    }

    /// 成环的坏数据不能让排序死循环（对端可能是被手改过的库）。
    #[test]
    fn topo_order_survives_cycle() {
        let mk = |id: &str, parent: &str| GroupPayload {
            id: id.into(),
            parent_id: Some(parent.into()),
            name: id.into(),
            sort: 0,
            created_at: 0,
            updated_at: 0,
        };
        let groups = vec![mk("a", "b"), mk("b", "a")];
        let order = topo_order(&groups);
        assert_eq!(order.len(), 2, "环也要给出确定的结果，而不是挂住");
    }

    /// 版本不一致整包拒收。
    #[tokio::test]
    async fn protocol_mismatch_rejects_whole_bundle() {
        let (a_store, a_vault) = pair().await;
        let (b_store, b_vault) = pair().await;
        let a = mk_asset(&a_store, "web-1", "10.0.0.1").await;
        let mut bundle = export(&a_store, &a_vault, std::slice::from_ref(&a.id), false)
            .await
            .unwrap();
        bundle.protocol = PROTOCOL + 1;
        let err = apply(&b_store, &b_vault, &bundle, false)
            .await
            .expect_err("版本不一致必须拒收");
        assert_eq!(err.code(), "unsupported");
        assert!(b_store.asset_list(true).await.unwrap().is_empty());
    }

    /// 摘要必须带墓碑（否则界面看不出对端删过什么），且**不含内置资产** ——
    /// 它出现在对照表里只会让人以为「可以把它推过去」。
    #[tokio::test]
    async fn digest_includes_tombstones_and_excludes_builtin() {
        let (store, _v) = pair().await;
        store.asset_ensure_builtin_local().await.unwrap();
        let a = mk_asset(&store, "web-1", "10.0.0.1").await;
        store.asset_delete(&a.id).await.unwrap();
        let d = digest(&store).await.unwrap();
        assert_eq!(
            d.assets.len(),
            1,
            "内置资产不该出现在摘要里: {:?}",
            d.assets
        );
        assert!(d.assets[0].deleted_at.is_some());
        assert_eq!(d.protocol, PROTOCOL);
    }

    #[tokio::test]
    async fn token_is_stable_until_rotated() {
        let store = fresh_store().await;
        let t1 = ensure_token(&store).await.unwrap();
        let t2 = ensure_token(&store).await.unwrap();
        assert_eq!(
            t1, t2,
            "重复取令牌必须幂等，否则上次配对好的桌面会突然连不上"
        );
        assert!(t1.len() >= 32, "令牌得有足够熵: {t1}");

        let t3 = rotate_token(&store).await.unwrap();
        assert_ne!(t1, t3);
        assert!(verify_token(&store, &t1).await.is_err(), "旧令牌该立刻失效");
        assert!(verify_token(&store, &t3).await.is_ok());
    }

    /// 校验路径**只读不生成**：没配令牌时报错，而不是顺手造一个让调用方以为通了。
    #[tokio::test]
    async fn verify_does_not_generate_token() {
        let store = fresh_store().await;
        assert!(verify_token(&store, "whatever").await.is_err());
        assert!(
            store.setting_get(SETTING_TOKEN).await.unwrap().is_none(),
            "校验不该写库"
        );
    }

    #[tokio::test]
    async fn verify_rejects_wrong_and_empty() {
        let store = fresh_store().await;
        let t = ensure_token(&store).await.unwrap();
        assert!(verify_token(&store, "").await.is_err());
        assert!(verify_token(&store, &t[..t.len() - 1]).await.is_err());
        assert!(verify_token(&store, "x").await.is_err());
        assert!(verify_token(&store, &t).await.is_ok());
    }

    #[test]
    fn ct_eq_matches_equality() {
        assert!(ct_eq("abc", "abc"));
        assert!(!ct_eq("abc", "abd"));
        assert!(!ct_eq("abc", "ab"));
        assert!(ct_eq("", ""));
    }
}
