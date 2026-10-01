//! 统一错误类型。所有内核错误归一到 [`AppError`]，
//! 跨 IPC 时序列化为 `{ code, message }`。

use serde::Serialize;

#[derive(Debug, thiserror::Error)]
pub enum AppError {
    #[error("IO 错误: {0}")]
    Io(#[from] std::io::Error),

    #[error("数据库错误: {0}")]
    Db(#[from] sqlx::Error),

    #[error("数据库迁移错误: {0}")]
    Migrate(#[from] sqlx::migrate::MigrateError),

    #[error("SSH 错误: {0}")]
    Ssh(String),

    #[error("主机指纹待确认 {host}:{port} {key_type} {fingerprint}")]
    HostKeyPending {
        host: String,
        port: u16,
        key_type: String,
        fingerprint: String,
    },

    #[error("SFTP 错误: {0}")]
    Sftp(String),

    #[error("WinRM 错误: {0}")]
    WinRm(String),

    #[error("连接已断开，请停止操作并提示用户重新连接。({0})")]
    Disconnected(String),

    #[error("不支持的操作: {0}")]
    Unsupported(String),

    #[error("未找到: {0}")]
    NotFound(String),

    #[error("参数错误: {0}")]
    BadParam(String),

    #[error("凭据库已锁定，请先解锁")]
    VaultLocked,

    #[error("凭据库尚未初始化")]
    VaultNotInit,

    #[error("凭据库已初始化，不能重复初始化")]
    VaultAlreadyInit,

    #[error("主密码错误")]
    BadMasterPassword,

    #[error("凭据解密失败: {0}")]
    Decrypt(String),

    // 曾经写作「危险操作已被拒绝」。实际用它的地方全是**鉴权**（同步令牌校验、
    // 懒猫平台登录门），没有一处是「危险操作」，于是那句前缀在界面上变成了一句
    // 误导：用户报的「被登录门挡下」会显示成「危险操作已被拒绝: 被登录门挡下」。
    // 改成中性措辞 —— 它对两类场合都说得通，而名字（Forbidden）本来也就是这个意思。
    #[error("操作被拒绝: {0}")]
    Forbidden(String),

    /// 终端输入被拒：这会儿控制权在别的设备手里（单点模式）。
    ///
    /// 单独开一个变体而不是复用 `Forbidden`：界面要对它做**特定**处理 ——
    /// 弹「接管控制」而不是当成错误报出来。靠通用的 `forbidden` 码前端分不出来。
    ///
    /// 刻意**不带上持有者的标识**：那是订阅者通道 id，对用户没有意义，
    /// 而且渲染出来只会像串乱码。
    #[error("终端正在其他设备上操作中")]
    NotController,

    #[error("该操作需要用户确认: {0}")]
    NeedsConfirm(String),

    #[error("操作超时: {0}")]
    Timeout(String),

    #[error("AI 提供方错误: {0}")]
    AiProvider(String),

    #[error("加密错误: {0}")]
    Crypto(String),

    #[error("内部错误: {0}")]
    Internal(String),
}

pub type AppResult<T> = Result<T, AppError>;

impl AppError {
    /// 快捷构造内部错误。
    pub fn internal(msg: impl Into<String>) -> Self {
        Self::Internal(msg.into())
    }

    /// 快捷构造参数错误。
    pub fn param(msg: impl Into<String>) -> Self {
        Self::BadParam(msg.into())
    }

    /// 稳定错误码，供前端分支处理。
    pub fn code(&self) -> &'static str {
        match self {
            Self::Io(_) => "io",
            Self::Db(_) => "db",
            Self::Migrate(_) => "db_migrate",
            Self::Ssh(_) => "ssh",
            Self::HostKeyPending { .. } => "host_key_pending",
            Self::Sftp(_) => "sftp",
            Self::WinRm(_) => "winrm",
            Self::Disconnected(_) => "disconnected",
            Self::Unsupported(_) => "unsupported",
            Self::NotFound(_) => "not_found",
            Self::BadParam(_) => "bad_param",
            Self::VaultLocked => "vault_locked",
            Self::VaultNotInit => "vault_not_init",
            Self::VaultAlreadyInit => "vault_already_init",
            Self::BadMasterPassword => "bad_master_password",
            Self::Decrypt(_) => "decrypt",
            Self::Forbidden(_) => "forbidden",
            Self::NotController => "not_controller",
            Self::NeedsConfirm(_) => "needs_confirm",
            Self::Timeout(_) => "timeout",
            Self::AiProvider(_) => "ai_provider",
            Self::Crypto(_) => "crypto",
            Self::Internal(_) => "internal",
        }
    }

    /// 是否由对端连接失效导致 —— AI 工具层用它给模型明确的停止信号。
    pub fn is_disconnected(&self) -> bool {
        matches!(self, Self::Disconnected(_) | Self::Ssh(_) | Self::WinRm(_))
    }

    /// 附加信息（host_key_pending 携带指纹详情给前端弹窗）。
    pub fn detail(&self) -> Option<serde_json::Value> {
        match self {
            Self::HostKeyPending {
                host,
                port,
                key_type,
                fingerprint,
            } => Some(serde_json::json!({
                "host": host,
                "port": port,
                "keyType": key_type,
                "fingerprint": fingerprint,
            })),
            _ => None,
        }
    }
}

impl From<serde_json::Error> for AppError {
    fn from(e: serde_json::Error) -> Self {
        Self::Internal(format!("JSON: {e}"))
    }
}

impl From<russh::Error> for AppError {
    fn from(e: russh::Error) -> Self {
        Self::Ssh(e.to_string())
    }
}

/// 跨 IPC 的错误形态。
#[derive(Debug, Serialize)]
pub struct SerializedError {
    pub code: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<serde_json::Value>,
}

impl Serialize for AppError {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        SerializedError {
            code: self.code().to_string(),
            message: self.to_string(),
            detail: self.detail(),
        }
        .serialize(serializer)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn error_codes_are_stable() {
        assert_eq!(AppError::VaultLocked.code(), "vault_locked");
        assert_eq!(AppError::Disconnected("x".into()).code(), "disconnected");
        assert!(AppError::Ssh("conn reset".into()).is_disconnected());
        assert!(!AppError::BadParam("x".into()).is_disconnected());
    }
}
