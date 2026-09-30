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

pub mod bundle;
pub mod client;

pub use bundle::{
    AssetPayload, CredPayload, DigestEntry, GroupPayload, ImportReport, SyncBundle, SyncDigest,
    PROTOCOL,
};

use std::collections::{HashMap, HashSet};

use crate::error::{AppError, AppResult};
use crate::store::models::AssetRow;
use crate::store::Store;
use crate::vault::Vault;

/// 实例标识所在的 setting 键。
const SETTING_ORIGIN: &str = "sync.origin";

/// 同步令牌所在的 setting 键（仅服务端使用）。
const SETTING_TOKEN: &str = "sync.token";

/// **应用层**同步令牌的 HTTP 头名。
pub const TOKEN_HEADER: &str = "x-nexterm-sync-token";

/// 懒猫平台 API 令牌的头名。
///
/// ⚠️ 这个头**由平台网关消费**，不会转发进容器（官方 `/advanced-api-auth-token`：
/// 「该 Header 只用于系统鉴权，转发到应用时会被移除」）。所以应用**看不到它**，
/// 只能靠下面那个 `X-HC-User-ID` 判断「网关已经放行过」。
///
/// 它的价值在于：桌面版不是浏览器、进不了微服的虚拟网络，而
/// `Lzc-Api-Auth-Token` 正是官方给「脚本或命令行访问」留的口子。
pub const PLATFORM_TOKEN_HEADER: &str = "lzc-api-auth-token";

/// 平台鉴权通过后注入的用户标识头（官方 `/http-request-headers`）。
pub const PLATFORM_USER_HEADER: &str = "x-hc-user-id";

/// 懒猫**客户端会话票据**的头名（`Lzc-Auth-Token`）。
///
/// 平台登录门认的就是这个头（§15.3.1 实测）。它和 [`PLATFORM_TOKEN_HEADER`]
/// 是同一道门的两种钥匙，区别在**谁去开**：
///
/// - `Lzc-Api-Auth-Token`：要给盒子开个口子（`hc api_auth_token gen`），
///   而那个命令需要**盒子上的 shell** —— 开发者侧唯一能进去的入口是
///   `debug.bridge`，它没有 `hc`（实测）。所以这条路对普通用户等于不存在。
/// - `Lzc-Auth-Token`：懒猫客户端**每次打开 Web 应用窗口**时自己下发的，
///   就写在窗口进程的命令行上（`--authToken=`）。桌面端读得到
///   （见 [`client::find_session_token`]），**用户不必在盒子上做任何事**。
///
/// 代价是它是**会话级**的：窗口关掉、客户端重启都会换。失效后的表现是被登录门
/// 307（见 `client::gate_error`），重新取一次即可 —— 同步本来就是手动动作。
pub const SESSION_TOKEN_HEADER: &str = "lzc-auth-token";

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

/// 把选中的资产打成一包。
///
/// 自动带上两样用户没直接勾选、但不带就会坏的东西：
/// - **祖先分组**：`asset.group_id` 是外键，分组不过去的话导入侧只能把它扔进
///   「未分组」—— 用户看到的是「同步成功了，但目录结构全平了」。
/// - **被引用的凭据**（`with_creds` 时）：没有凭据的密码类资产在另一端是死的
///   （点连接就报「凭据不存在」）。
///
/// 内置资产（「当前设备」）**一律不导出**：它是「本机」这个概念在**每台设备上
/// 各自的锚点**，搬到另一边只会多出一台连不上的假机器。
pub async fn export(
    store: &Store,
    vault: &Vault,
    asset_ids: &[String],
    with_creds: bool,
) -> AppResult<SyncBundle> {
    let mut out = SyncBundle::new(origin(store).await?);
    let mut group_ids: HashSet<String> = HashSet::new();
    let mut cred_ids: HashSet<String> = HashSet::new();

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
        if with_creds {
            if let Some(cid) = row.cred_id.clone() {
                cred_ids.insert(cid);
            }
        }
        out.assets.push((&row).into());
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
            out.creds.push(CredPayload {
                id: row.id.clone(),
                name: row.name.clone(),
                kind: row.kind.clone(),
                secret,
            });
        }
    }

    Ok(out)
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
