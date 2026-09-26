//! 凭据与安全（§9）：两级密钥。
//!
//! - 无主密码模式：DEK 直接由 Windows DPAPI 保护（`kek_hint = "dpapi"`）；
//! - 主密码模式：Argon2id 派生 KEK → ChaCha20-Poly1305 信封包 DEK
//!   （`kek_hint = "master:<salt_id>"`），闲置 30 分钟自动锁定；
//! - 凭据明文用 XChaCha20-Poly1305(DEK) 加密，内存中短暂存在（zeroize）。

pub mod crypto;
pub mod dpapi;
pub mod redact;

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use base64::Engine;
use serde::Serialize;
use tokio::sync::RwLock;
use zeroize::Zeroizing;

use crate::error::{AppError, AppResult};
use crate::ids::now_ms;
use crate::store::models::CredentialRow;
use crate::store::Store;

const SETTING_MODE: &str = "vault.mode";
const SETTING_ENVELOPE: &str = "vault.dek_envelope";
const SETTING_SALT: &str = "vault.master_salt";
const SETTING_AUTOLOCK: &str = "vault.autolock_ms";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum VaultMode {
    NotInit,
    Dpapi,
    Master,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VaultStatus {
    pub initialized: bool,
    pub mode: String,
    pub unlocked: bool,
    pub auto_lock_minutes: u64,
}

pub struct Vault {
    store: Arc<Store>,
    mode: RwLock<VaultMode>,
    dek: RwLock<Option<crypto::Dek>>,
    kek: RwLock<Option<crypto::Kek>>,
    salt: RwLock<Option<Vec<u8>>>,
    last_used_at: AtomicU64,
    auto_lock_ms: AtomicU64,
}

impl Vault {
    /// 启动时加载（不自动解锁 DPAPI 之外的形态）。
    pub async fn load(store: Arc<Store>) -> AppResult<Arc<Self>> {
        let mode = match store.setting_get(SETTING_MODE).await?.as_deref() {
            None | Some("") => VaultMode::NotInit,
            Some("dpapi") => VaultMode::Dpapi,
            Some("master") => VaultMode::Master,
            Some(other) => return Err(AppError::Crypto(format!("未知凭据库模式 {other}"))),
        };
        let auto_lock_ms = store
            .setting_get(SETTING_AUTOLOCK)
            .await?
            .and_then(|v| v.parse().ok())
            .unwrap_or(30 * 60 * 1000u64);
        let salt = match store.setting_get(SETTING_SALT).await? {
            Some(s) => Some(
                base64::engine::general_purpose::STANDARD
                    .decode(s)
                    .map_err(|e| AppError::Crypto(format!("salt 解码失败: {e}")))?,
            ),
            None => None,
        };
        let vault = Arc::new(Self {
            store,
            mode: RwLock::new(mode),
            dek: RwLock::new(None),
            kek: RwLock::new(None),
            salt: RwLock::new(salt),
            last_used_at: AtomicU64::new(now_ms()),
            auto_lock_ms: AtomicU64::new(auto_lock_ms),
        });
        // DPAPI 模式可以无感解锁
        if mode == VaultMode::Dpapi {
            vault.unlock_dpapi().await?;
        }
        Ok(vault)
    }

    /// 生产参数（§9.1：m=64MB, t=3, p=4）；测试编译走快速参数。
    fn derive_kek(&self, password: &str, salt: &[u8]) -> AppResult<crypto::Kek> {
        #[cfg(test)]
        {
            let _ = (password, salt);
            crypto::derive_kek_fast(password, salt)
        }
        #[cfg(not(test))]
        {
            crypto::derive_kek_from_master(password, salt)
        }
    }

    pub async fn status(&self) -> VaultStatus {
        let mode = *self.mode.read().await;
        VaultStatus {
            initialized: mode != VaultMode::NotInit,
            mode: serde_json::to_value(mode)
                .ok()
                .and_then(|v| v.as_str().map(|s| s.to_string()))
                .unwrap_or_else(|| "not_init".into()),
            unlocked: self.dek.read().await.is_some(),
            auto_lock_minutes: self.auto_lock_ms.load(Ordering::Relaxed) / 60_000,
        }
    }

    /// 初始化为主密码模式（只能初始化一次）。
    pub async fn init_master(&self, password: &str) -> AppResult<()> {
        if *self.mode.read().await != VaultMode::NotInit {
            return Err(AppError::VaultAlreadyInit);
        }
        if password.len() < 8 {
            return Err(AppError::param("主密码至少 8 位"));
        }
        let mut salt = vec![0u8; 16];
        use rand::RngCore;
        rand::rngs::OsRng.fill_bytes(&mut salt);
        let kek = self.derive_kek(password, &salt)?;
        let dek = crypto::generate_dek();
        let envelope = crypto::seal_dek(&kek, &dek)?;
        self.store.setting_set(SETTING_MODE, "master").await?;
        self.store
            .setting_set(
                SETTING_ENVELOPE,
                &base64::engine::general_purpose::STANDARD.encode(&envelope),
            )
            .await?;
        self.store
            .setting_set(
                SETTING_SALT,
                &base64::engine::general_purpose::STANDARD.encode(&salt),
            )
            .await?;
        *self.mode.write().await = VaultMode::Master;
        *self.salt.write().await = Some(salt);
        *self.kek.write().await = Some(kek);
        *self.dek.write().await = Some(dek);
        self.last_used_at.store(now_ms(), Ordering::Relaxed);
        Ok(())
    }

    /// 初始化为 DPAPI 模式（无主密码，Windows）。
    pub async fn init_dpapi(&self) -> AppResult<()> {
        if *self.mode.read().await != VaultMode::NotInit {
            return Err(AppError::VaultAlreadyInit);
        }
        self.store.setting_set(SETTING_MODE, "dpapi").await?;
        *self.mode.write().await = VaultMode::Dpapi;
        self.unlock_dpapi().await
    }

    /// DPAPI 解锁：用 DPAPI 直接包住随机 KEK → 信封里的 DEK。
    async fn unlock_dpapi(&self) -> AppResult<()> {
        let envelope = self
            .store
            .setting_get(SETTING_ENVELOPE)
            .await?
            .ok_or(AppError::VaultNotInit)?;
        let dek = if envelope.is_empty() {
            // 首次：生成 DEK 并用 DPAPI 包起来
            let dek = crypto::generate_dek();
            let protected = dpapi::protect(dek.0.as_ref())?;
            self.store
                .setting_set(
                    SETTING_ENVELOPE,
                    &base64::engine::general_purpose::STANDARD.encode(&protected),
                )
                .await?;
            dek
        } else {
            let raw = base64::engine::general_purpose::STANDARD
                .decode(envelope)
                .map_err(|e| AppError::Crypto(format!("信封解码失败: {e}")))?;
            let plain = dpapi::unprotect(&raw)?;
            let mut key = [0u8; crypto::DEK_LEN];
            if plain.len() != crypto::DEK_LEN {
                return Err(AppError::Decrypt("DPAPI 信封长度异常".into()));
            }
            key.copy_from_slice(&plain);
            crypto::Dek(Zeroizing::new(key))
        };
        *self.dek.write().await = Some(dek);
        self.last_used_at.store(now_ms(), Ordering::Relaxed);
        Ok(())
    }

    /// 主密码解锁。
    pub async fn unlock_master(&self, password: &str) -> AppResult<()> {
        if *self.mode.read().await != VaultMode::Master {
            return Err(AppError::Unsupported("凭据库不是主密码模式".into()));
        }
        let salt = self
            .salt
            .read()
            .await
            .clone()
            .ok_or(AppError::VaultNotInit)?;
        let kek = self.derive_kek(password, &salt)?;
        let envelope_b64 = self
            .store
            .setting_get(SETTING_ENVELOPE)
            .await?
            .ok_or(AppError::VaultNotInit)?;
        let envelope = base64::engine::general_purpose::STANDARD
            .decode(envelope_b64)
            .map_err(|e| AppError::Crypto(format!("信封解码失败: {e}")))?;
        let dek = crypto::open_dek(&kek, &envelope)?;
        *self.kek.write().await = Some(kek);
        *self.dek.write().await = Some(dek);
        self.last_used_at.store(now_ms(), Ordering::Relaxed);
        Ok(())
    }

    pub async fn lock(&self) {
        *self.dek.write().await = None;
        *self.kek.write().await = None;
    }

    pub async fn set_auto_lock(&self, minutes: u64) -> AppResult<()> {
        let ms = minutes * 60_000;
        self.auto_lock_ms.store(ms, Ordering::Relaxed);
        self.store
            .setting_set(SETTING_AUTOLOCK, &ms.to_string())
            .await
    }

    /// 取 DEK（解锁态），刷新空闲计时。
    pub async fn dek(&self) -> AppResult<crypto::Dek> {
        self.last_used_at.store(now_ms(), Ordering::Relaxed);
        self.dek.read().await.clone().ok_or(AppError::VaultLocked)
    }

    /// 闲置自动锁定（由后台任务调用）。
    pub async fn auto_lock_if_idle(&self) {
        let idle = now_ms().saturating_sub(self.last_used_at.load(Ordering::Relaxed));
        if idle > self.auto_lock_ms.load(Ordering::Relaxed)
            && *self.mode.read().await == VaultMode::Master
        {
            self.lock().await;
        }
    }

    /// 加密凭据明文 → (nonce, blob)。
    pub async fn encrypt_credential(
        dek: &crypto::Dek,
        plaintext: &str,
    ) -> AppResult<(Vec<u8>, Vec<u8>)> {
        crypto::seal_credential(dek, plaintext.as_bytes())
    }

    /// 解密凭据行 → 明文。
    pub fn decrypt_credential(
        dek: &crypto::Dek,
        row: &CredentialRow,
    ) -> AppResult<Zeroizing<String>> {
        let bytes = crypto::open_credential(dek, &row.nonce, &row.blob)?;
        Ok(Zeroizing::new(String::from_utf8_lossy(&bytes).into_owned()))
    }

    /// 变更主密码：用旧 KEK 解信封、新 KEK 重封（DEK 不变，凭据无需重加密）。
    pub async fn change_master_password(
        &self,
        old_password: &str,
        new_password: &str,
    ) -> AppResult<()> {
        if new_password.len() < 8 {
            return Err(AppError::param("主密码至少 8 位"));
        }
        let salt = self
            .salt
            .read()
            .await
            .clone()
            .ok_or(AppError::VaultNotInit)?;
        let old_kek = self.derive_kek(old_password, &salt)?;
        let envelope_b64 = self
            .store
            .setting_get(SETTING_ENVELOPE)
            .await?
            .ok_or(AppError::VaultNotInit)?;
        let envelope = base64::engine::general_purpose::STANDARD
            .decode(envelope_b64)
            .map_err(|e| AppError::Crypto(format!("信封解码失败: {e}")))?;
        let dek = crypto::open_dek(&old_kek, &envelope)?;
        let mut new_salt = vec![0u8; 16];
        use rand::RngCore;
        rand::rngs::OsRng.fill_bytes(&mut new_salt);
        let new_kek = self.derive_kek(new_password, &new_salt)?;
        let new_envelope = crypto::seal_dek(&new_kek, &dek)?;
        self.store
            .setting_set(
                SETTING_ENVELOPE,
                &base64::engine::general_purpose::STANDARD.encode(&new_envelope),
            )
            .await?;
        self.store
            .setting_set(
                SETTING_SALT,
                &base64::engine::general_purpose::STANDARD.encode(&new_salt),
            )
            .await?;
        *self.kek.write().await = Some(new_kek);
        self.last_used_at.store(now_ms(), Ordering::Relaxed);
        Ok(())
    }
}

/// 后台自动锁任务。
pub async fn auto_lock_task(vault: Arc<Vault>) {
    loop {
        tokio::time::sleep(std::time::Duration::from_secs(60)).await;
        vault.auto_lock_if_idle().await;
    }
}

pub use redact::redact;

#[cfg(test)]
mod tests {
    use super::*;

    async fn vault() -> Arc<Vault> {
        let store = Store::open_in_memory().await.unwrap();
        Vault::load(Arc::new(store)).await.unwrap()
    }

    #[tokio::test]
    async fn master_mode_roundtrip() {
        let v = vault().await;
        assert!(!v.status().await.initialized);
        v.init_master("super-secret-1").await.unwrap();
        let st = v.status().await;
        assert_eq!(st.mode, "master");
        assert!(st.unlocked);

        let dek = v.dek().await.unwrap();
        let (nonce, blob) = Vault::encrypt_credential(&dek, "p@ss 中文").await.unwrap();
        let row = CredentialRow {
            id: "x".into(),
            name: "test".into(),
            kind: "password".into(),
            cipher: crypto::CIPHER_XCHACHA.into(),
            nonce,
            blob,
            kek_hint: "master:0".into(),
            created_at: 0,
            updated_at: 0,
        };
        let pt = Vault::decrypt_credential(&dek, &row).unwrap();
        assert_eq!(pt.as_str(), "p@ss 中文");
    }

    #[tokio::test]
    async fn wrong_password_fails_and_locks() {
        let v = vault().await;
        v.init_master("correct-password").await.unwrap();
        v.lock().await;
        assert!(matches!(
            v.unlock_master("wrong-password").await,
            Err(AppError::BadMasterPassword)
        ));
        assert!(v.unlock_master("correct-password").await.is_ok());
    }

    #[tokio::test]
    async fn change_password_keeps_dek() {
        let v = vault().await;
        v.init_master("old-password-1").await.unwrap();
        let dek_before = v.dek().await.unwrap().0.as_ref().to_vec();
        v.change_master_password("old-password-1", "new-password-2")
            .await
            .unwrap();
        let dek_after = v.dek().await.unwrap().0.as_ref().to_vec();
        assert_eq!(dek_before, dek_after);
    }
}
