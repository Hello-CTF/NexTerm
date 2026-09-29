//! Windows DPAPI 封装（无主密码模式：KEK 直接来自 OS 凭据库）。

use crate::error::AppResult;
// macOS 分支只做委托，不用 AppError；Windows / Linux 桩的报错需要它。
#[cfg(not(target_os = "macos"))]
use crate::error::AppError;

/// DPAPI Protect（当前用户作用域）。
#[cfg(windows)]
pub fn protect(data: &[u8]) -> AppResult<Vec<u8>> {
    use windows::Win32::Security::Cryptography::{CryptProtectData, CRYPT_INTEGER_BLOB};
    unsafe {
        let input = CRYPT_INTEGER_BLOB {
            cbData: data.len() as u32,
            pbData: data.as_ptr() as *mut u8,
        };
        let mut out = CRYPT_INTEGER_BLOB::default();
        CryptProtectData(&input, None, None, None, None, 0, &mut out)
            .map_err(|e| AppError::Crypto(format!("DPAPI protect 失败: {e}")))?;
        let slice = std::slice::from_raw_parts(out.pbData, out.cbData as usize);
        let result = slice.to_vec();
        local_free_blob(out.pbData);
        Ok(result)
    }
}

/// DPAPI Unprotect。
#[cfg(windows)]
pub fn unprotect(data: &[u8]) -> AppResult<Vec<u8>> {
    use windows::Win32::Security::Cryptography::{CryptUnprotectData, CRYPT_INTEGER_BLOB};
    unsafe {
        let input = CRYPT_INTEGER_BLOB {
            cbData: data.len() as u32,
            pbData: data.as_ptr() as *mut u8,
        };
        let mut out = CRYPT_INTEGER_BLOB::default();
        CryptUnprotectData(&input, None, None, None, None, 0, &mut out)
            .map_err(|e| AppError::Crypto(format!("DPAPI unprotect 失败: {e}")))?;
        let slice = std::slice::from_raw_parts(out.pbData, out.cbData as usize);
        let result = slice.to_vec();
        local_free_blob(out.pbData);
        Ok(result)
    }
}

#[cfg(windows)]
unsafe fn local_free_blob(ptr: *mut u8) {
    use windows::Win32::Foundation::{LocalFree, HLOCAL};
    if !ptr.is_null() {
        let _ = LocalFree(Some(HLOCAL(ptr as *mut _)));
    }
}

/// macOS：委托登录钥匙串（见 [`crate::vault::keychain`]）。
#[cfg(target_os = "macos")]
pub fn protect(data: &[u8]) -> AppResult<Vec<u8>> {
    crate::vault::keychain::protect(data)
}

/// macOS：委托登录钥匙串。
#[cfg(target_os = "macos")]
pub fn unprotect(data: &[u8]) -> AppResult<Vec<u8>> {
    crate::vault::keychain::unprotect(data)
}

/// 其余平台（Linux 等）编译桩，保持可编译，运行时明确报不支持。
#[cfg(not(any(windows, target_os = "macos")))]
pub fn protect(_data: &[u8]) -> AppResult<Vec<u8>> {
    Err(AppError::Unsupported(
        "系统级免密保护仅支持 Windows / macOS".into(),
    ))
}

#[cfg(not(any(windows, target_os = "macos")))]
pub fn unprotect(_data: &[u8]) -> AppResult<Vec<u8>> {
    Err(AppError::Unsupported(
        "系统级免密保护仅支持 Windows / macOS".into(),
    ))
}
