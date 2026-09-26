//! Windows DPAPI 封装（无主密码模式：KEK 直接来自 OS 凭据库）。

use crate::error::{AppError, AppResult};

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
        let _ = LocalFree(HLOCAL(ptr as *mut _));
    }
}

/// 非 Windows 平台编译桩（macOS/Linux 保持可编译，不验收）。
#[cfg(not(windows))]
pub fn protect(_data: &[u8]) -> AppResult<Vec<u8>> {
    Err(AppError::Unsupported("DPAPI 仅在 Windows 可用".into()))
}

#[cfg(not(windows))]
pub fn unprotect(_data: &[u8]) -> AppResult<Vec<u8>> {
    Err(AppError::Unsupported("DPAPI 仅在 Windows 可用".into()))
}
