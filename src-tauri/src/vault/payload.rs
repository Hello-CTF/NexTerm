//! 凭据载荷：私钥类凭据的密文里存的是**结构化 JSON**，其它类型仍是纯文本。
//!
//! 为什么私钥要结构化 —— 一把私钥有两件事要一起记：它**从哪来**（引用本地文件 / 正文
//! 收进库）和**有没有口令**。口令只对这把私钥有意义，不该在凭据列表里当一条独立凭据
//! （旧版就是那样：列表里孤零零一条「旧机房-口令」，看不出它属于哪把钥匙）。
//!
//! 向后兼容：旧数据里 `private_key` 的明文就是 PEM 原文，既没口令也没记来源 ——
//! [`PrivateKeyPayload::parse`] 遇到非 JSON 一律按"内容型、无口令"处理，**不需要迁移**。

use serde::{Deserialize, Serialize};

/// 私钥类凭据的 kind。
pub const KIND_PRIVATE_KEY: &str = "private_key";
/// 旧版的独立口令凭据。只为兼容老数据与给出可读报错而保留，新建入口已不再提供。
pub const KIND_PASSPHRASE: &str = "passphrase";

/// 私钥凭据的载荷。
///
/// `key` 与 `file` 二选一：前者是私钥正文（收进凭据库），后者是本地文件路径（只记引用，
/// 不复制内容）。两者都空说明这条数据是坏的 —— 由调用方决定怎么报错。
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PrivateKeyPayload {
    /// 私钥正文（内容型）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
    /// 本地文件路径（引用型）。JSON 字段名用 `ref`，Rust 侧避开关键字所以叫 `file`。
    #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
    pub file: Option<String>,
    /// 私钥口令（可选）。空白一律归一成 `None`。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub passphrase: Option<String>,
}

/// 口令归一：空白 = 没有口令。用户清空输入框的语义是"清除口令"，不是存一个空串。
fn norm_pass(p: Option<String>) -> Option<String> {
    p.map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
}

impl PrivateKeyPayload {
    /// 私钥正文收进库。
    pub fn inline(key: impl Into<String>, passphrase: Option<String>) -> Self {
        Self {
            key: Some(key.into()),
            file: None,
            passphrase: norm_pass(passphrase),
        }
    }

    /// 只记本地文件路径，不复制私钥内容。
    pub fn referenced(path: impl Into<String>, passphrase: Option<String>) -> Self {
        Self {
            file: Some(path.into()),
            key: None,
            passphrase: norm_pass(passphrase),
        }
    }

    /// 引用型？引用型连接时按路径读文件，库里只有路径。
    pub fn is_ref(&self) -> bool {
        self.file.is_some()
    }

    /// 编码成落库的明文（随后交给 vault 加密）。
    pub fn encode(&self) -> String {
        // 全是 Option<String>，理论上不会失败。真失败了也别炸：退化成纯正文，
        // 至少"内容型 + 无口令"这条最常用路径还能连上。
        serde_json::to_string(self).unwrap_or_else(|_| self.key.clone().unwrap_or_default())
    }

    /// 解析落库的明文。**非 JSON 一律按旧数据**：内容型私钥、无口令。
    pub fn parse(raw: &str) -> Self {
        if raw.trim_start().starts_with('{') {
            if let Ok(p) = serde_json::from_str::<PrivateKeyPayload>(raw.trim_start()) {
                // 必须真的带 key 或 ref —— 否则 `{}` 这种会被解析成"空私钥"，
                // 连接时报一句莫名其妙的错，不如当成旧数据原样用。
                if p.key.is_some() || p.file.is_some() {
                    return Self {
                        key: p.key,
                        file: p.file,
                        passphrase: norm_pass(p.passphrase),
                    };
                }
            }
        }
        Self::inline(raw, None)
    }

    /// 只换口令，保留私钥本体（改口令不必重新提供私钥）。
    pub fn with_passphrase(mut self, passphrase: Option<String>) -> Self {
        self.passphrase = norm_pass(passphrase);
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn legacy_pem_is_inline_without_passphrase() {
        let pem = "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----";
        let p = PrivateKeyPayload::parse(pem);
        assert_eq!(p.key.as_deref(), Some(pem));
        assert!(p.file.is_none());
        assert!(p.passphrase.is_none());
        assert!(!p.is_ref());
    }

    #[test]
    fn json_roundtrip_keeps_source_and_passphrase() {
        let p = PrivateKeyPayload::referenced("C:/Users/x/.ssh/id_rsa", Some("pw".into()));
        let back = PrivateKeyPayload::parse(&p.encode());
        assert_eq!(back, p);
        assert!(back.is_ref());
        assert_eq!(back.file.as_deref(), Some("C:/Users/x/.ssh/id_rsa"));
    }

    #[test]
    fn blank_passphrase_normalizes_to_none() {
        assert!(PrivateKeyPayload::inline("k", Some("   ".into()))
            .passphrase
            .is_none());
        assert!(PrivateKeyPayload::inline("k", Some(String::new()))
            .passphrase
            .is_none());
        assert!(PrivateKeyPayload::inline("k", None).passphrase.is_none());
    }

    #[test]
    fn json_without_key_or_ref_falls_back_to_raw() {
        // 防呆：`{}` 之类的 JSON 不能被解析成"空私钥"，否则连接时报的错毫无信息量
        let p = PrivateKeyPayload::parse("{}");
        assert_eq!(p.key.as_deref(), Some("{}"));
        assert!(p.file.is_none());
    }

    #[test]
    fn changing_passphrase_keeps_key() {
        let p = PrivateKeyPayload::inline("KEY", Some("old".into()));
        let q = p.with_passphrase(Some("new".into()));
        assert_eq!(q.key.as_deref(), Some("KEY"));
        assert_eq!(q.passphrase.as_deref(), Some("new"));

        // 清除口令
        let r = q.with_passphrase(Some(String::new()));
        assert_eq!(r.key.as_deref(), Some("KEY"));
        assert!(r.passphrase.is_none());
    }

    #[test]
    fn ref_survives_roundtrip_without_passphrase() {
        let p = PrivateKeyPayload::referenced("/home/u/.ssh/id_ed25519", None);
        let back = PrivateKeyPayload::parse(&p.encode());
        assert!(back.is_ref());
        assert!(back.passphrase.is_none());
        assert!(back.key.is_none());
    }
}
