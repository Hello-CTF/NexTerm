//! 同步载荷（唯一权威定义）：跨实例搬什么、长什么样、怎么演进。
//!
//! # 为什么字段与表列一一对应
//!
//! 载荷是「某几行数据的快照」，不是给界面用的 DTO。这里刻意**不做字段改名或
//! 嵌套重排** —— 一旦载荷与表结构分叉，每加一列都要在「表 / 载荷 / 导出 /
//! 导入」四处同步改，漏一处就是**静默丢字段**（导入不报错，只是那个字段没了）。
//! 所以载荷直接对齐 `store::models` 的行结构，转换用 `From` 显式写出来，
//! 让「哪些字段不过去」变成一个看得见的决定。
//!
//! # 什么不搬
//!
//! - **`builtin`**：内置与否是本机属性。远端说「这条是内置的」在本地没有意义，
//!   而且导入侧会拒绝一切对内置资产的写入（见 `repo::asset_upsert`）。
//! - **`ai_conversation` / `audit_log` / `terminal_recording` / `known_host`**：
//!   前三个是本地行为的记录（搬到别的设备上不是「同一件事」），已知主机指纹
//!   更是**必须以目标端实际握手结果为准**，接受了远端指纹等于把 TOFU 保护关掉。

use serde::{Deserialize, Serialize};
use ts_rs::TS;
use zeroize::Zeroizing;

use crate::error::{AppError, AppResult};
use crate::store::models::{AssetGroupRow, AssetRow};

/// 协议版本。两端必须一致才允许搬运。
///
/// 同步是**双端参与**的协议：一侧字段语义变了而另一侧不认，最坏情况是把
/// 「凭据」当成「备注」写进去 —— 这种静默错位比直接拒绝难查得多。所以宁可
/// 整体拒绝，并把两侧版本号都摆进错误信息里，让人一眼看出是谁旧了。
pub const PROTOCOL: u32 = 1;

/// 一次同步的完整载荷。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncBundle {
    pub protocol: u32,
    /// 来源实例标识（`sync::origin`），用于界面显示「这批是从哪台来的」。
    pub origin: String,
    /// 导出时刻（Unix 毫秒）。仅供展示与排查，**不参与合并判断** ——
    /// 合并一律按每条的 `updated_at`，用整包时间戳会把「一批里只改了一条」
    /// 放大成「全都比对面新」。
    pub exported_at: i64,
    #[serde(default)]
    pub groups: Vec<GroupPayload>,
    #[serde(default)]
    pub assets: Vec<AssetPayload>,
    /// 凭据。**`secret` 是落库明文**（见 `CredPayload` 注释）。
    #[serde(default)]
    pub creds: Vec<CredPayload>,
}

impl SyncBundle {
    pub fn new(origin: String) -> Self {
        Self {
            protocol: PROTOCOL,
            origin,
            exported_at: crate::ids::now_ms() as i64,
            groups: Vec::new(),
            assets: Vec::new(),
            creds: Vec::new(),
        }
    }

    /// 版本闸门。不匹配一律拒收，不做「尽量兼容」。
    pub fn verify_protocol(&self) -> AppResult<()> {
        if self.protocol == PROTOCOL {
            return Ok(());
        }
        Err(AppError::Unsupported(format!(
            "同步协议版本不一致：本机 {PROTOCOL}，对端 {}。请把两端升到同一版本后再同步。",
            self.protocol
        )))
    }

    pub fn is_empty(&self) -> bool {
        self.groups.is_empty() && self.assets.is_empty() && self.creds.is_empty()
    }
}

/// 分组载荷。字段与 `asset_group` 表一一对应。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GroupPayload {
    pub id: String,
    pub parent_id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub sort: i64,
    #[serde(default)]
    pub created_at: i64,
    #[serde(default)]
    pub updated_at: i64,
}

impl From<AssetGroupRow> for GroupPayload {
    fn from(r: AssetGroupRow) -> Self {
        Self {
            id: r.id,
            parent_id: r.parent_id,
            name: r.name,
            sort: r.sort,
            created_at: r.created_at,
            updated_at: r.updated_at,
        }
    }
}

/// 资产载荷。字段与 `asset` 表一一对应，**唯独没有 `builtin`**（见模块文档）。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetPayload {
    pub id: String,
    pub group_id: Option<String>,
    pub kind: String,
    pub name: String,
    pub host: Option<String>,
    pub port: Option<i32>,
    pub username: Option<String>,
    pub auth_kind: Option<String>,
    pub key_path: Option<String>,
    pub cred_id: Option<String>,
    #[serde(default = "empty_obj")]
    pub options_json: String,
    #[serde(default)]
    pub tags: String,
    #[serde(default)]
    pub note: String,
    #[serde(default)]
    pub sort: i64,
    #[serde(default)]
    pub created_at: i64,
    #[serde(default)]
    pub updated_at: i64,
    /// 墓碑。**必须搬运**：不传的话「对端删了这条」在本地永远删不掉，
    /// 下次同步还会被当成「对面有、我没有」重新推回来，变成打不死的东西。
    #[serde(default)]
    pub deleted_at: Option<i64>,
}

fn empty_obj() -> String {
    "{}".to_string()
}

impl From<&AssetRow> for AssetPayload {
    fn from(r: &AssetRow) -> Self {
        Self {
            id: r.id.clone(),
            group_id: r.group_id.clone(),
            kind: r.kind.clone(),
            name: r.name.clone(),
            host: r.host.clone(),
            port: r.port,
            username: r.username.clone(),
            auth_kind: r.auth_kind.clone(),
            key_path: r.key_path.clone(),
            cred_id: r.cred_id.clone(),
            options_json: r.options_json.clone(),
            tags: r.tags.clone(),
            note: r.note.clone(),
            sort: r.sort,
            created_at: r.created_at,
            updated_at: r.updated_at,
            deleted_at: r.deleted_at,
        }
    }
}

impl From<AssetPayload> for AssetRow {
    fn from(p: AssetPayload) -> Self {
        Self {
            id: p.id,
            group_id: p.group_id,
            kind: p.kind,
            name: p.name,
            host: p.host,
            port: p.port,
            username: p.username,
            auth_kind: p.auth_kind,
            key_path: p.key_path,
            cred_id: p.cred_id,
            options_json: p.options_json,
            tags: p.tags,
            note: p.note,
            sort: p.sort,
            created_at: p.created_at,
            updated_at: p.updated_at,
            deleted_at: p.deleted_at,
            // 载荷里没有这一列；导入侧也从不写它（`asset_upsert` 硬编码 0）。
            builtin: false,
        }
    }
}

/// 凭据载荷。
///
/// # `secret` 是**落库明文**
///
/// 刻意传明文而不是密文：两端各有一套独立密钥（桌面是 DPAPI / 主密码，
/// 盒子是平台注入的根密钥），**密文搬过去解不开**。所以只能是「源端解出来 →
/// 走 TLS → 目标端用自己的密钥重新加密」。这意味着：
///
/// - 源端凭据库必须处于**解锁态**（否则拿不到明文，直接报错，不静默跳过）；
/// - 明文只在 HTTPS 通道里存在，**绝不能走明文 HTTP**（见 `client` 模块的强制校验）；
/// - 这里的 `Zeroizing` 只是让本进程尽量不留残影 —— 序列化成 JSON 时必然
///   还会产生副本，别把它当成「内存里绝对没有明文」（§12.4 已认下这个代价）。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CredPayload {
    pub id: String,
    pub name: String,
    pub kind: String,
    #[serde(rename = "secret")]
    pub secret: Zeroizing<String>,
}

/// 本机摘要：给界面做「哪边有、哪边新」的对比，**不含任何密文**。
#[derive(Debug, Clone, Serialize, Deserialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct SyncDigest {
    pub origin: String,
    pub protocol: u32,
    pub app_version: String,
    /// 本实例是桌面版还是服务端（界面据此区分「这台 / 盒子」）。
    pub desktop: bool,
    pub assets: Vec<DigestEntry>,
}

#[derive(Debug, Clone, Serialize, Deserialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct DigestEntry {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub host: Option<String>,
    pub username: Option<String>,
    #[ts(type = "number")]
    pub updated_at: i64,
    #[ts(type = "number | null")]
    pub deleted_at: Option<i64>,
    /// 引用了凭据（界面据此提示「这条带密码」）。
    pub has_cred: bool,
    pub group_id: Option<String>,
}

/// 导入结果。界面拿它报告「建了几条、更新了几条、什么被跳过了」。
#[derive(Debug, Clone, Default, Serialize, Deserialize, TS)]
#[ts(export, export_to = "../../src/ipc/types.ts")]
#[serde(rename_all = "camelCase")]
pub struct ImportReport {
    #[ts(type = "number")]
    pub groups_created: i64,
    #[ts(type = "number")]
    pub groups_updated: i64,
    #[ts(type = "number")]
    pub assets_created: i64,
    #[ts(type = "number")]
    pub assets_updated: i64,
    #[ts(type = "number")]
    pub creds_created: i64,
    #[ts(type = "number")]
    pub creds_updated: i64,
    /// 因为「本地这份更新」而跳过的条数（未开强制覆盖时）。
    #[ts(type = "number")]
    pub skipped_newer: i64,
    /// 被拒的条数（内置资产、名称非法等）。
    #[ts(type = "number")]
    pub refused: i64,
    /// 人话警告。界面必须展示 —— 这里的每一条都对应一个「用户以为同步了，
    /// 其实没有」的坑（引用的私钥路径失效、凭据没跟过来、分组不存在…）。
    pub warnings: Vec<String>,
}

impl ImportReport {
    /// 一条都没动（用于界面判断「无事发生」而不是报「成功」）。
    pub fn touched(&self) -> i64 {
        self.groups_created
            + self.groups_updated
            + self.assets_created
            + self.assets_updated
            + self.creds_created
            + self.creds_updated
    }

    pub fn warn(&mut self, msg: impl Into<String>) {
        self.warnings.push(msg.into());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: &str, updated: i64) -> AssetRow {
        AssetRow {
            id: id.into(),
            group_id: None,
            kind: "ssh".into(),
            name: format!("主机 {id}"),
            host: Some("10.0.0.1".into()),
            port: Some(22),
            username: Some("root".into()),
            auth_kind: Some("password".into()),
            key_path: None,
            cred_id: None,
            options_json: "{}".into(),
            tags: String::new(),
            note: String::new(),
            sort: 0,
            created_at: 1,
            updated_at: updated,
            deleted_at: None,
            builtin: false,
        }
    }

    /// 载荷往返不丢字段 —— 这是「加列时忘了改载荷」的最直接防线。
    #[test]
    fn asset_payload_roundtrip_keeps_every_field() {
        let mut src = row("A", 111);
        src.group_id = Some("G".into());
        src.key_path = Some("/k".into());
        src.cred_id = Some("C".into());
        src.options_json = r#"{"encoding":"utf-8"}"#.into();
        src.tags = "prod,cn".into();
        src.note = "备注".into();
        src.sort = 7;
        src.created_at = 5;
        src.deleted_at = Some(99);

        let p: AssetPayload = (&src).into();
        let back: AssetRow = p.into();

        assert_eq!(back.id, src.id);
        assert_eq!(back.group_id, src.group_id);
        assert_eq!(back.kind, src.kind);
        assert_eq!(back.name, src.name);
        assert_eq!(back.host, src.host);
        assert_eq!(back.port, src.port);
        assert_eq!(back.username, src.username);
        assert_eq!(back.auth_kind, src.auth_kind);
        assert_eq!(back.key_path, src.key_path);
        assert_eq!(back.cred_id, src.cred_id);
        assert_eq!(back.options_json, src.options_json);
        assert_eq!(back.tags, src.tags);
        assert_eq!(back.note, src.note);
        assert_eq!(back.sort, src.sort);
        assert_eq!(back.created_at, src.created_at);
        assert_eq!(back.updated_at, src.updated_at);
        assert_eq!(back.deleted_at, src.deleted_at);
        // builtin 不在载荷里，还原回来一定是 false（导入侧也从不写这一列）
        assert!(!back.builtin);
    }

    /// 缺字段的旧载荷要能读（`#[serde(default)]` 的实际作用），
    /// 否则「对端少发一个字段」会变成整个包解不开。
    #[test]
    fn asset_payload_tolerates_missing_optional_fields() {
        let p: AssetPayload =
            serde_json::from_str(r#"{"id":"A","kind":"ssh","name":"x"}"#).unwrap();
        assert_eq!(p.id, "A");
        assert_eq!(p.options_json, "{}", "缺省的 options 必须是空对象字面量");
        assert_eq!(p.sort, 0);
        assert!(p.deleted_at.is_none());
    }

    #[test]
    fn protocol_mismatch_is_refused() {
        let mut b = SyncBundle::new("dev-a".into());
        assert!(b.verify_protocol().is_ok());
        b.protocol = PROTOCOL + 1;
        let err = b.verify_protocol().expect_err("版本不一致必须拒收");
        let msg = err.to_string();
        assert!(msg.contains("协议版本不一致"), "实际：{msg}");
    }

    /// 凭据明文是 `Zeroizing`：序列化出去是裸字符串，反序列化回来自动包上。
    /// 断言的是**线格式**没变形（对端按普通字符串发也能收）。
    #[test]
    fn cred_payload_wire_format_is_plain_string() {
        let c = CredPayload {
            id: "C".into(),
            name: "密码".into(),
            kind: "password".into(),
            secret: Zeroizing::new("hunter2".into()),
        };
        let v = serde_json::to_value(&c).unwrap();
        assert_eq!(v["secret"], serde_json::json!("hunter2"));
        let back: CredPayload = serde_json::from_value(v).unwrap();
        assert_eq!(back.secret.as_str(), "hunter2");
    }
}
