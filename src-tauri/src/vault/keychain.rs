//! macOS 登录钥匙串封装（无主密码模式：DEK 托管给系统钥匙串）。
//!
//! 与 Windows DPAPI 的语义对齐：
//! - 密钥绑定当前登录用户，免主密码；
//! - 换用户 / 重装系统后取不回（`Vault::load` 会降级为「未初始化」，不拦启动）；
//! - 条目由本应用创建，正常访问不弹窗。未签名的开发版每次重编译代码签名
//!   都会变，首次访问可能弹一次「允许」——正式签名版没有这个问题。
//!
//! 信封策略：数据库 `vault.dek_envelope` 里只存「托管标记」（[`KEYCHAIN_MARKER`]），
//! 真正的 DEK 在钥匙串里 —— 钥匙串本身才是信封。这样 `vault/mod.rs` 的共用流程
//! （首次生成 / 后续解信封 / 自愈与拒绝重建）完全不用感知平台差异。

use crate::error::{AppError, AppResult};

const SERVICE: &str = "com.nexterm.desktop";
const ACCOUNT: &str = "vault-dek";
/// 写进数据库信封字段的标记：见到它就知道密钥在钥匙串里。
const KEYCHAIN_MARKER: &[u8] = b"keychain:v1";

/// 托管：密钥写入登录钥匙串，返回应落库的信封标记。
pub fn protect(data: &[u8]) -> AppResult<Vec<u8>> {
    security_framework::passwords::set_generic_password(SERVICE, ACCOUNT, data)
        .map_err(|e| AppError::Crypto(format!("钥匙串写入失败: {e}")))?;
    Ok(KEYCHAIN_MARKER.to_vec())
}

/// 取回：校验信封标记后从钥匙串读出密钥。
///
/// 钥匙串条目被删 / 换用户导致取不回时返回 Err —— 上层（`Vault::load`）
/// 会降级为「未初始化」，不会拦启动。
pub fn unprotect(envelope: &[u8]) -> AppResult<Vec<u8>> {
    if envelope != KEYCHAIN_MARKER {
        return Err(AppError::Crypto("信封不是钥匙串托管标记".into()));
    }
    security_framework::passwords::get_generic_password(SERVICE, ACCOUNT)
        .map_err(|e| AppError::Crypto(format!("钥匙串读取失败: {e}")))
}

/// 测试辅助：删除托管条目（条目不存在也算成功）。
#[cfg(test)]
pub fn cleanup_for_tests() -> AppResult<()> {
    use security_framework::passwords::delete_generic_password;
    match delete_generic_password(SERVICE, ACCOUNT) {
        Ok(()) => Ok(()),
        // errSecItemNotFound —— 本来就没有，视作已清理
        Err(e) if e.code() == -25300 => Ok(()),
        Err(e) => Err(AppError::Crypto(format!("钥匙串清理失败: {e}"))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 真钥匙串往返。会动本机登录钥匙串（测试完清理），用环境变量显式开启：
    /// CI 的 macos 任务与本地验收时设 `NEXTERM_TEST_KEYCHAIN=1`。
    #[test]
    fn keychain_roundtrip() {
        if std::env::var("NEXTERM_TEST_KEYCHAIN").ok().as_deref() != Some("1") {
            eprintln!("跳过：未设置 NEXTERM_TEST_KEYCHAIN=1");
            return;
        }
        let dek = crypto_test_dek();
        let envelope = protect(&dek).expect("写入钥匙串");
        assert_eq!(envelope, KEYCHAIN_MARKER);
        let back = unprotect(&envelope).expect("读回");
        assert_eq!(back, dek);
        // 清理，不留测试条目
        let _ = security_framework::passwords::delete_generic_password(SERVICE, ACCOUNT);
    }

    /// 非托管标记的信封直接拒绝，不碰钥匙串。
    #[test]
    fn rejects_foreign_envelope() {
        assert!(unprotect(b"dpapi-blob-or-garbage").is_err());
    }

    fn crypto_test_dek() -> Vec<u8> {
        (0u8..32).collect()
    }
}
