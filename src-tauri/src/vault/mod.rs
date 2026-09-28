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

/// 读一个 setting：**读失败只记 WARN 并当作「没有这个键」**。
///
/// 启动路径上不能因为"某个可选配置读不出来"就把整个应用判死 —— 详见 `Vault::load`。
async fn read_setting(store: &Store, key: &str, what: &str) -> Option<String> {
    match store.setting_get(key).await {
        Ok(v) => v,
        Err(e) => {
            tracing::warn!(target: "vault", setting = key, error = %e, "读取{what}失败，按缺省处理");
            None
        }
    }
}

impl Vault {
    /// 启动时加载。
    ///
    /// **刻意不返回 `Result`** —— 它曾经返回，于是"凭据库处于坏状态"会一路冒到 tauri 的
    /// setup 钩子，被当成致命错误直接 panic：现象是**双击程序一闪就没了**，用户连界面都进不去，
    /// 更不可能进设置页去修它。凭据库是**可选组件**（不初始化时其余功能完全可用），
    /// 它坏掉不该拦着应用启动 —— 这和本文件对 model_profile / ai.permission 的兜底是同一个原则。
    ///
    /// 坏状态只在**内存里**降级为「未初始化」，**不回写数据库**：db 里的脏值是可查的证据，
    /// 写回等于把用户的原始状态悄悄抹掉。降级会打 WARN，排查时一眼能看到。
    pub async fn load(store: Arc<Store>) -> Arc<Self> {
        let mode = match read_setting(&store, SETTING_MODE, "凭据库模式")
            .await
            .as_deref()
        {
            None | Some("") => VaultMode::NotInit,
            Some("dpapi") => VaultMode::Dpapi,
            Some("master") => VaultMode::Master,
            Some(other) => {
                // 未知模式同样降级：宁可让用户重新初始化，也不能让应用起不来。
                tracing::warn!(
                    target: "vault",
                    value = other,
                    "凭据库模式无法识别，本次按「未初始化」处理"
                );
                VaultMode::NotInit
            }
        };
        let auto_lock_ms = read_setting(&store, SETTING_AUTOLOCK, "自动锁时长")
            .await
            .and_then(|v| v.parse().ok())
            .unwrap_or(30 * 60 * 1000u64);
        let salt = read_setting(&store, SETTING_SALT, "主密码盐").await.and_then(|s| {
            base64::engine::general_purpose::STANDARD
                .decode(s)
                .map_err(|e| tracing::warn!(target: "vault", error = %e, "salt 解码失败，按无盐处理"))
                .ok()
        });
        let vault = Arc::new(Self {
            store,
            mode: RwLock::new(mode),
            dek: RwLock::new(None),
            kek: RwLock::new(None),
            salt: RwLock::new(salt),
            last_used_at: AtomicU64::new(now_ms()),
            auto_lock_ms: AtomicU64::new(auto_lock_ms),
        });
        // DPAPI 模式可以无感解锁；解不开（信封丢失 / 换机器 / 换用户）只是"这个功能不可用"，
        // 不是"应用不能启动"。
        if mode == VaultMode::Dpapi {
            if let Err(e) = vault.unlock_dpapi().await {
                tracing::warn!(
                    target: "vault",
                    error = %e,
                    "凭据库自动解锁失败，本次按「未初始化」处理（可到设置页重新初始化）"
                );
                *vault.mode.write().await = VaultMode::NotInit;
            }
        }
        vault
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
    ///
    /// ⚠️ 这里曾经写成「先 `setting_get(...)?.ok_or(VaultNotInit)?`，再判 `is_empty()` 决定
    /// 要不要生成」。问题在于**真实首次初始化拿到的就是 `None`**（键压根不存在），
    /// `ok_or` 先一步把它拒了 —— 那个"生成信封"的分支**永远走不到**。
    ///
    /// 后果远不止"初始化失败"：`init_dpapi` 是**先落库 `mode=dpapi`、再解锁**的，
    /// 所以失败之后数据库里留下一个"自称 dpapi、却没有信封"的死状态，
    /// 从此每次启动都在 load 阶段解锁失败 → setup 返回 Err → tauri panic ——
    /// **应用再也打不开（双击一闪就没）**。所以 `None` 与 `Some("")` 必须走同一条路。
    async fn unlock_dpapi(&self) -> AppResult<()> {
        let envelope = self.store.setting_get(SETTING_ENVELOPE).await?;
        let dek = match envelope.as_deref() {
            // 首次（或信封为空）：生成一把新 DEK，用 DPAPI 包住后落库
            None | Some("") => {
                // 唯一的例外：信封没了、但库里还躺着密文。这时重建密钥等于把这些密文
                // 永久废掉 —— 宁可让这次初始化失败并说清原因，也不静默毁数据。
                let existing = self.store.credential_list().await?;
                if !existing.is_empty() {
                    return Err(AppError::Crypto(format!(
                        "凭据库的密钥信封已丢失，但库内仍有 {} 条密文；重建密钥会让它们永远无法解密，已拒绝。",
                        existing.len()
                    )));
                }
                let dek = crypto::generate_dek();
                let protected = dpapi::protect(dek.0.as_ref())?;
                self.store
                    .setting_set(
                        SETTING_ENVELOPE,
                        &base64::engine::general_purpose::STANDARD.encode(&protected),
                    )
                    .await?;
                dek
            }
            Some(envelope) => {
                let raw = base64::engine::general_purpose::STANDARD
                    .decode(envelope)
                    .map_err(|e| AppError::Crypto(format!("信封解码失败: {e}")))?;
                let plain = dpapi::unprotect(&raw)?;
                if plain.len() != crypto::DEK_LEN {
                    return Err(AppError::Decrypt("DPAPI 信封长度异常".into()));
                }
                let mut key = [0u8; crypto::DEK_LEN];
                key.copy_from_slice(&plain);
                crypto::Dek(Zeroizing::new(key))
            }
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

    /// 全新的内存库 → 一个「未初始化」的凭据库。
    /// （`load` 已经不返回 `Result` 了：它永远不会失败，见上面的注释。）
    async fn vault() -> Arc<Vault> {
        let store = Arc::new(Store::open_in_memory().await.unwrap());
        Vault::load(store).await
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

    /* ── 回归：凭据库坏掉，绝不能让应用起不来 ──────────────────────────────
     *
     * 线上事故（2026-09-28）：`Vault::load` 当时返回 Result，坏状态一路冒到 tauri 的
     * setup 钩子 → tauri panic → **双击程序一闪就没**（退出码 101），用户连设置页都进不去，
     * 也就永远没机会自己修。
     *
     * 下面这组测试把三条策略钉死：
     * ① 能自愈就自愈（缺信封但无密文）；② 不能自愈就降级（信封解不开 / 模式写坏），
     * 且**绝不 panic**；③ 会毁数据的事（有密文却要重建密钥）**宁可失败并说清原因**。
     */

    /// 事故现场（`mode=dpapi`、没有信封）在**没有遗留密文**时应当**自愈**：
    /// 直接补一把新信封并解锁 —— 用户双击就能正常用，不必自己去设置页重新初始化。
    ///
    /// 这是修 `None | Some("")` 带来的关键变化：「键不存在」从**永久死状态**
    /// 变成了「首次初始化 / 可自愈」。
    #[cfg(windows)]
    #[tokio::test]
    async fn load_heals_dpapi_mode_without_envelope() {
        let store = Arc::new(Store::open_in_memory().await.unwrap());
        store.setting_set(SETTING_MODE, "dpapi").await.unwrap();

        let v = Vault::load(Arc::clone(&store)).await; // ← 这一行以前直接 panic 退出

        let st = v.status().await;
        assert!(st.initialized, "无遗留密文时应自愈，不该降级成未初始化");
        assert!(st.unlocked);
        assert_eq!(st.mode, "dpapi");
        assert!(
            store
                .setting_get(SETTING_ENVELOPE)
                .await
                .unwrap()
                .is_some_and(|s| !s.is_empty()),
            "自愈时要把新信封落库，否则下次启动又解不开"
        );
    }

    /// 信封在、但**解不开**（换了机器 / 换了 Windows 用户 / 数据被损坏）
    /// → 降级为「未初始化」，**不崩**。
    #[tokio::test]
    async fn load_survives_undecryptable_envelope() {
        let store = Arc::new(Store::open_in_memory().await.unwrap());
        store.setting_set(SETTING_MODE, "dpapi").await.unwrap();
        // 一段合法 base64，但绝对不是 DPAPI 能解的密文
        store
            .setting_set(
                SETTING_ENVELOPE,
                &base64::engine::general_purpose::STANDARD.encode([7u8; 64]),
            )
            .await
            .unwrap();

        let v = Vault::load(Arc::clone(&store)).await;

        let st = v.status().await;
        assert!(!st.initialized, "解不开的信封应降级为「未初始化」");
        assert_eq!(st.mode, "not_init");
    }

    /// 模式字段被写坏（手改 / 未来版本降级残留）同样不能拦死启动。
    #[tokio::test]
    async fn load_survives_unknown_mode() {
        let store = Arc::new(Store::open_in_memory().await.unwrap());
        store.setting_set(SETTING_MODE, "who-knows").await.unwrap();
        let v = Vault::load(Arc::clone(&store)).await;
        assert!(!v.status().await.initialized);
    }

    /// 「初始化（Windows DPAPI）」这条路必须真的能跑通 —— 它以前**从来没有成功过**。
    ///
    /// 反向验证：把 `unlock_dpapi` 的 `None | Some("")` 改回旧的
    /// `ok_or(AppError::VaultNotInit)?`，本测试立刻失败，且报错原文就是线上那句
    /// 「凭据库尚未初始化」。
    #[cfg(windows)]
    #[tokio::test]
    async fn init_dpapi_works_on_first_run() {
        let v = vault().await;
        assert!(!v.status().await.initialized);

        v.init_dpapi().await.expect("首次初始化 DPAPI 必须成功");

        let st = v.status().await;
        assert!(st.initialized);
        assert_eq!(st.mode, "dpapi");
        assert!(st.unlocked, "DPAPI 模式初始化后应直接是解锁态");
        assert!(v.dek().await.is_ok());
        // 信封必须落库 —— 否则下次启动又解不开
        assert!(v
            .store
            .setting_get(SETTING_ENVELOPE)
            .await
            .unwrap()
            .is_some_and(|s| !s.is_empty()));
    }

    /// 信封丢了、但库里还躺着密文：必须**拒绝**重建密钥。
    /// 重建 = 生成一把新 DEK，那些密文会永远解不开 —— 静默毁数据比报错严重得多。
    #[cfg(windows)]
    #[tokio::test]
    async fn refuses_to_rebuild_key_when_secrets_remain() {
        let store = Arc::new(Store::open_in_memory().await.unwrap());
        store.setting_set(SETTING_MODE, "dpapi").await.unwrap();
        store
            .credential_put(crate::store::CredentialInput {
                id: None,
                name: "遗留密文".into(),
                kind: "password".into(),
                nonce: vec![0u8; 24],
                blob: vec![1, 2, 3],
                kek_hint: "dpapi".into(),
            })
            .await
            .unwrap();

        // 启动不崩，只是降级
        let v = Vault::load(Arc::clone(&store)).await;
        assert!(!v.status().await.initialized);

        // 用户主动初始化时，才把话说清楚
        let err = v.init_dpapi().await.expect_err("有密文时不许重建密钥");
        assert!(
            format!("{err}").contains("已拒绝"),
            "错误信息要说清为什么拒绝，实际是：{err}"
        );
    }
}
