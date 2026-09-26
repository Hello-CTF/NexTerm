//! 两级密钥加密（§9.1）：
//!
//! ```text
//! 主密码 ──Argon2id(m=64MB,t=3,p=4)──► KEK
//! DEK(随机32B) ──ChaCha20-Poly1305(KEK)──► 信封
//! 凭据明文 ──XChaCha20-Poly1305(DEK)──► credential.blob
//! ```
//!
//! 明文只在内存短暂存在，用完 zeroize。

use argon2::{Algorithm, Argon2, Params, Version};
use chacha20poly1305::{
    aead::{Aead, AeadCore, KeyInit, Payload},
    ChaCha20Poly1305, XChaCha20Poly1305, XNonce,
};
use zeroize::Zeroizing;

use crate::error::{AppError, AppResult};

pub const DEK_LEN: usize = 32;
/// 当前凭据加密算法标识，写入 `credential.cipher`。
pub const CIPHER_XCHACHA: &str = "xchacha20poly1305-v1";
/// DEK 信封算法标识，写入 setting 表。
pub const CIPHER_CHACHA: &str = "chacha20poly1305-v1";

/// KEK：主密钥加密密钥，仅存在于内存。
#[derive(Clone)]
pub struct Kek(pub(crate) Zeroizing<[u8; 32]>);

/// DEK：数据加密密钥，随机生成、用 KEK 加密后落盘。
#[derive(Clone)]
pub struct Dek(pub(crate) Zeroizing<[u8; 32]>);

/// Argon2id 参数：m=64MB, t=3, p=4（§9.1）。
fn argon2_instance() -> Argon2<'static> {
    // 64 MiB = 65536 KiB；参数为编译期常量，属于 §0.3 允许的例外范围
    let params = Params::new(65_536, 3, 4, Some(32)).expect("argon2 参数为编译期常量，必然合法");
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
}

/// 由主密码派生 KEK。`salt` 至少 16 字节随机数据。
pub fn derive_kek_from_master(password: &str, salt: &[u8]) -> AppResult<Kek> {
    if salt.len() < 16 {
        return Err(AppError::Crypto("salt 过短".into()));
    }
    let mut out = Zeroizing::new([0u8; 32]);
    argon2_instance()
        .hash_password_into(password.as_bytes(), salt, out.as_mut())
        .map_err(|e| AppError::Crypto(format!("argon2 派生失败: {e}")))?;
    Ok(Kek(out))
}

/// 测试专用：低参数快速派生（m=64KB, t=1）——生产路径不走这里。
#[cfg(test)]
pub fn derive_kek_fast(password: &str, salt: &[u8]) -> AppResult<Kek> {
    let params = Params::new(64, 1, 1, Some(32)).expect("测试参数合法");
    let mut out = Zeroizing::new([0u8; 32]);
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(password.as_bytes(), salt, out.as_mut())
        .map_err(|e| AppError::Crypto(format!("argon2 派生失败: {e}")))?;
    Ok(Kek(out))
}

/// 随机生成 DEK。
pub fn generate_dek() -> Dek {
    use rand::RngCore;
    let mut key = Zeroizing::new([0u8; DEK_LEN]);
    rand::rngs::OsRng.fill_bytes(key.as_mut());
    Dek(key)
}

/// 用 KEK 加密 DEK，产出信封（nonce ‖ ciphertext）。
pub fn seal_dek(kek: &Kek, dek: &Dek) -> AppResult<Vec<u8>> {
    let cipher = ChaCha20Poly1305::new_from_slice(kek.0.as_ref())
        .map_err(|_| AppError::Crypto("KEK 长度异常".into()))?;
    let nonce = ChaCha20Poly1305::generate_nonce(&mut rand::rngs::OsRng);
    let ct = cipher
        .encrypt(&nonce, dek.0.as_ref())
        .map_err(|_| AppError::Crypto("DEK 信封加密失败".into()))?;
    let mut out = nonce.to_vec();
    out.extend_from_slice(&ct);
    Ok(out)
}

/// 用 KEK 解开 DEK 信封。
pub fn open_dek(kek: &Kek, envelope: &[u8]) -> AppResult<Dek> {
    if envelope.len() < 12 + 16 {
        return Err(AppError::Decrypt("DEK 信封长度不合法".into()));
    }
    let (nonce, ct) = envelope.split_at(12);
    let cipher = ChaCha20Poly1305::new_from_slice(kek.0.as_ref())
        .map_err(|_| AppError::Crypto("KEK 长度异常".into()))?;
    let pt = cipher
        .decrypt(chacha20poly1305::Nonce::from_slice(nonce), ct)
        .map_err(|_| AppError::BadMasterPassword)?;
    let mut key = [0u8; DEK_LEN];
    if pt.len() != DEK_LEN {
        return Err(AppError::Decrypt("DEK 长度不合法".into()));
    }
    key.copy_from_slice(&pt);
    Ok(Dek(Zeroizing::new(key)))
}

/// 用 DEK 加密凭据明文，返回 (nonce, ciphertext)。
pub fn seal_credential(dek: &Dek, plaintext: &[u8]) -> AppResult<(Vec<u8>, Vec<u8>)> {
    let cipher = XChaCha20Poly1305::new_from_slice(dek.0.as_ref())
        .map_err(|_| AppError::Crypto("DEK 长度异常".into()))?;
    let nonce = XChaCha20Poly1305::generate_nonce(&mut rand::rngs::OsRng);
    let ct = cipher
        .encrypt(
            &nonce,
            Payload {
                msg: plaintext,
                aad: &[],
            },
        )
        .map_err(|_| AppError::Crypto("凭据加密失败".into()))?;
    Ok((nonce.to_vec(), ct))
}

/// 用 DEK 解密凭据，返回 zeroizing 明文。
pub fn open_credential(dek: &Dek, nonce: &[u8], blob: &[u8]) -> AppResult<Zeroizing<Vec<u8>>> {
    if nonce.len() != 24 {
        return Err(AppError::Decrypt("nonce 长度不合法".into()));
    }
    let cipher = XChaCha20Poly1305::new_from_slice(dek.0.as_ref())
        .map_err(|_| AppError::Crypto("DEK 长度异常".into()))?;
    let pt = cipher
        .decrypt(
            XNonce::from_slice(nonce),
            Payload {
                msg: blob,
                aad: &[],
            },
        )
        .map_err(|_| AppError::Decrypt("凭据解密失败（DEK 不匹配或数据损坏）".into()))?;
    Ok(Zeroizing::new(pt))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dek_seal_open_roundtrip() {
        let kek = derive_kek_fast("correct horse", b"salt-salt-salt-01").unwrap();
        let dek = generate_dek();
        let env = seal_dek(&kek, &dek).unwrap();
        let dek2 = open_dek(&kek, &env).unwrap();
        assert_eq!(dek.0.as_ref(), dek2.0.as_ref());
    }

    #[test]
    fn dek_wrong_kek_fails() {
        let kek1 = derive_kek_fast("right", b"salt-salt-salt-01").unwrap();
        let kek2 = derive_kek_fast("wrong", b"salt-salt-salt-01").unwrap();
        let dek = generate_dek();
        let env = seal_dek(&kek1, &dek).unwrap();
        assert!(open_dek(&kek2, &env).is_err());
    }

    #[test]
    fn credential_roundtrip_and_tamper() {
        let dek = generate_dek();
        let (nonce, blob) = seal_credential(&dek, "p@ssw0rd-中文".as_bytes()).unwrap();
        let pt = open_credential(&dek, &nonce, &blob).unwrap();
        assert_eq!(pt.as_slice(), "p@ssw0rd-中文".as_bytes());
        let mut tampered = blob.clone();
        tampered[0] ^= 1;
        assert!(open_credential(&dek, &nonce, &tampered).is_err());
    }

    #[test]
    fn master_password_change_invalidates() {
        let salt = b"0123456789abcdef";
        let k1 = derive_kek_fast("pw1", salt).unwrap();
        let k2 = derive_kek_fast("pw2", salt).unwrap();
        assert_ne!(k1.0.as_ref(), k2.0.as_ref());
    }
}
