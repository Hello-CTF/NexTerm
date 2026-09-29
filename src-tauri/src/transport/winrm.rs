//! WinRM 传输（§5.3 / §5.4）：非交互式，每条命令一个新 shell。
//!
//! - 每条远程 PowerShell 前置 `[Console]::OutputEncoding = UTF8`；
//! - 解码 UTF-8 失败时回退 GB18030（中文 Windows 必备）；
//! - cwd 用哨兵标记跟随（RainsIR 的工程经验）；
//! - `open_pty` 返回 Unsupported —— UI 显示「非交互模式」。

use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;

use super::{ExecResult, FileEntry, FileSystem, PtyHandle, Transport};
use crate::error::{AppError, AppResult};

const CWD_SENTINEL_HEAD: &str = "<<NEXTERM_CWD>>";
const CWD_SENTINEL_TAIL: &str = "<<EOF>>";
/// WinRM 单命令默认超时。
pub const DEFAULT_EXEC_TIMEOUT: Duration = Duration::from_secs(60);
/// WinRM 文件写上限（受信封大小约束）。
pub const WINRM_WRITE_MAX: usize = 48 * 1024;

#[derive(Debug, Clone)]
pub struct WinRmConnectParams {
    pub host: String,
    pub port: u16,
    pub username: String,
    /// `域\用户` 直接写全量名。
    pub password: String,
    pub domain: String,
    /// basic | ntlm（默认 ntlm）
    pub auth: String,
    pub use_tls: bool,
    pub accept_invalid_certs: bool,
    /// 显式代理；None = 直连（绝不读系统代理，§12.1 血泪坑）。
    pub proxy: Option<String>,
}

pub struct WinRmTransport {
    #[allow(dead_code)]
    params: WinRmConnectParams,
    client: Arc<winrm_rs::WinrmClient>,
    host: String,
    cwd: Arc<std::sync::Mutex<String>>,
}

fn build_client(params: &WinRmConnectParams) -> AppResult<winrm_rs::WinrmClient> {
    use winrm_rs::{WinrmConfig, WinrmCredentials};
    let config = WinrmConfig {
        port: params.port,
        use_tls: params.use_tls,
        accept_invalid_certs: params.accept_invalid_certs,
        ..Default::default()
    };
    let credentials = WinrmCredentials::new(
        params.username.clone(),
        params.password.clone(),
        params.domain.clone(),
    );
    winrm_rs::WinrmClientBuilder::new(config)
        .credentials(credentials)
        .build()
        .map_err(|e| AppError::WinRm(format!("WinRM 客户端构建失败: {e}")))
}

/// §5.4：命令前注入 cwd 恢复，命令后捕获新 cwd。
pub fn wrap_cmd(cwd: &str, cmd: &str) -> String {
    format!(
        "Set-Location -LiteralPath '{cwd}' 2>$null; {cmd}; Write-Output \"`n{CWD_SENTINEL_HEAD}$(Get-Location){CWD_SENTINEL_TAIL}\""
    )
}

/// 从 stdout 尾部剥离哨兵并返回 (正文, 新cwd)。
pub fn unwrap_cwd(stdout: &str) -> (String, Option<String>) {
    if let Some(pos) = stdout.rfind(CWD_SENTINEL_HEAD) {
        let head = &stdout[..pos];
        let rest = &stdout[pos + CWD_SENTINEL_HEAD.len()..];
        let body = rest.trim_start_matches(['\r', '\n']);
        if let Some(end) = body.find(CWD_SENTINEL_TAIL) {
            let new_cwd = body[..end].trim().to_string();
            let mut text = head.to_string();
            let tail_after = &body[end + CWD_SENTINEL_TAIL.len()..];
            text.push_str(tail_after.trim_start_matches('\n'));
            return (text, (!new_cwd.is_empty()).then_some(new_cwd));
        }
    }
    (stdout.to_string(), None)
}

/// §5.3：解码顺序 UTF-8 → GB18030（出现替换字符再试）。
pub fn decode_bytes(raw: &[u8]) -> String {
    match std::str::from_utf8(raw) {
        Ok(s) => s.to_string(),
        Err(_) => {
            let (cow, _, had_errors) = encoding_rs::GB18030.decode(raw);
            if had_errors {
                String::from_utf8_lossy(raw).into_owned()
            } else {
                cow.into_owned()
            }
        }
    }
}

impl WinRmTransport {
    pub async fn connect(params: WinRmConnectParams) -> AppResult<Arc<Self>> {
        let client = build_client(&params)?;
        let host = params.host.clone();
        Ok(Arc::new(Self {
            params,
            client: Arc::new(client),
            host,
            cwd: Arc::new(std::sync::Mutex::new("C:\\".to_string())),
        }))
    }

    pub fn host(&self) -> &str {
        &self.host
    }

    pub async fn run_ps_raw(&self, script: &str, timeout: Duration) -> AppResult<ExecResult> {
        let start = Instant::now();
        let out = tokio::time::timeout(timeout, self.client.run_powershell(&self.host, script))
            .await
            .map_err(|_| AppError::Timeout(format!("WinRM 命令超时（{}s）", timeout.as_secs())))?
            .map_err(|e| AppError::WinRm(format!("执行失败: {e}")))?;
        let stdout = decode_bytes(&out.stdout);
        let stderr = decode_bytes(&out.stderr);
        Ok(ExecResult {
            stdout,
            stderr,
            exit_code: Some(out.exit_code),
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: false,
        })
    }
}

#[async_trait]
impl Transport for WinRmTransport {
    fn kind(&self) -> &'static str {
        "winrm"
    }

    /// WinRM 是非交互式（§5.3）。
    async fn open_pty(&self, _cols: u16, _rows: u16) -> AppResult<PtyHandle> {
        Err(AppError::Unsupported(
            "WinRM 不支持交互式 PTY（非交互模式）".into(),
        ))
    }

    async fn exec(&self, cmd: &str, timeout: Duration) -> AppResult<ExecResult> {
        let start = Instant::now();
        let cwd = self.cwd.lock().unwrap_or_else(|e| e.into_inner()).clone();
        // §5.3：前置 UTF-8 输出编码
        let script = format!(
            "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; {}",
            wrap_cmd(&cwd, cmd)
        );
        let out = tokio::time::timeout(timeout, self.client.run_powershell(&self.host, &script))
            .await
            .map_err(|_| AppError::Timeout(format!("WinRM 命令超时（{}s）", timeout.as_secs())))?
            .map_err(|e| AppError::WinRm(format!("执行失败: {e}")))?;
        let raw_text = decode_bytes(&out.stdout);
        let (text, new_cwd) = unwrap_cwd(&raw_text);
        if let Some(c) = new_cwd {
            *self.cwd.lock().unwrap_or_else(|e| e.into_inner()) = c;
        }
        let err_text = decode_bytes(&out.stderr);
        // 不做 §6.3 裁剪（原因见 `ExecResult` 注释）：限预算归喂模型的那一侧。
        // WinRM 的输出规模由协议自身的消息上限兜住，这里不再二次裁剪。
        Ok(ExecResult {
            stdout: text,
            stderr: err_text,
            exit_code: Some(out.exit_code),
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: false,
        })
    }

    /// WinRM 文件子系统：PowerShell 实现（受限能力面）。
    async fn fs(&self) -> AppResult<Arc<dyn FileSystem>> {
        Ok(Arc::new(WinRmFs {
            client: Arc::clone(&self.client),
            host: self.host.clone(),
            cwd: Arc::clone(&self.cwd),
        }))
    }

    async fn ping(&self) -> AppResult<Duration> {
        let start = Instant::now();
        self.exec("Write-Output ok", Duration::from_secs(10))
            .await?;
        Ok(start.elapsed())
    }

    async fn close(&self) {}

    fn as_any(&self) -> &dyn std::any::Any {
        self
    }

    fn as_any_arc(self: Arc<Self>) -> Arc<dyn std::any::Any + Send + Sync> {
        self
    }

    fn cwd(&self) -> Option<String> {
        Some(self.cwd.lock().unwrap_or_else(|e| e.into_inner()).clone())
    }

    fn set_cwd(&self, cwd: &str) {
        *self.cwd.lock().unwrap_or_else(|e| e.into_inner()) = cwd.to_string();
    }
}

/// WinRM 文件子系统：PowerShell 实现。
pub struct WinRmFs {
    client: Arc<winrm_rs::WinrmClient>,
    host: String,
    cwd: Arc<std::sync::Mutex<String>>,
}

impl WinRmFs {
    async fn ps(&self, script: &str) -> AppResult<String> {
        let full = format!("[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; {script}");
        let out = self
            .client
            .run_powershell(&self.host, &full)
            .await
            .map_err(|e| AppError::WinRm(format!("文件操作失败: {e}")))?;
        if out.exit_code != 0 {
            let err = decode_bytes(&out.stderr);
            return Err(AppError::Sftp(if err.is_empty() {
                format!("WinRM 文件操作返回码 {}", out.exit_code)
            } else {
                err
            }));
        }
        Ok(decode_bytes(&out.stdout).trim().to_string())
    }

    async fn ps_cwd(&self, script: &str) -> AppResult<String> {
        let cwd = self.cwd.lock().unwrap_or_else(|e| e.into_inner()).clone();
        let wrapped = wrap_cmd(&cwd, script);
        let out = self
            .client
            .run_powershell(
                &self.host,
                &format!("[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; {wrapped}"),
            )
            .await
            .map_err(|e| AppError::WinRm(format!("文件操作失败: {e}")))?;
        let (text, new_cwd) = unwrap_cwd(&decode_bytes(&out.stdout));
        if let Some(c) = new_cwd {
            *self.cwd.lock().unwrap_or_else(|e| e.into_inner()) = c;
        }
        if out.exit_code != 0 {
            let err = decode_bytes(&out.stderr);
            return Err(AppError::Sftp(if err.is_empty() {
                format!("返回码 {}", out.exit_code)
            } else {
                err
            }));
        }
        Ok(text.trim().to_string())
    }

    fn ps_quote(s: &str) -> String {
        format!("'{}'", s.replace('\'', "''"))
    }
}

#[async_trait]
impl FileSystem for WinRmFs {
    async fn list(&self, path: &str) -> AppResult<Vec<FileEntry>> {
        let p = Self::ps_quote(path);
        let script = format!(
            "$ErrorActionPreference='Stop'; $items = @(Get-ChildItem -Force -LiteralPath {p} | ForEach-Object {{ [PSCustomObject]@{{ n=$_.Name; d=$_.PSIsContainer; l=$_.Length; m=$_.LastWriteTimeUtc.ToString('o'); }} }}); $items | ConvertTo-Json -Compress"
        );
        let out = self.ps_cwd(&script).await?;
        let mut entries = Vec::new();
        if out.is_empty() {
            return Ok(entries);
        }
        let parsed: serde_json::Value = serde_json::from_str(&out)
            .map_err(|e| AppError::Sftp(format!("目录 JSON 解析失败: {e}")))?;
        let list = match parsed {
            serde_json::Value::Array(a) => a,
            obj @ serde_json::Value::Object(_) => vec![obj],
            _ => vec![],
        };
        for item in list {
            let name = item
                .get("n")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            let is_dir = item.get("d").and_then(|v| v.as_bool()).unwrap_or(false);
            let size = item.get("l").and_then(|v| v.as_i64()).unwrap_or(0);
            let mtime = item
                .get("m")
                .and_then(|v| v.as_str())
                .and_then(|s| chrono::DateTime::parse_from_rfc3339(s).ok())
                .map(|d| d.timestamp_millis())
                .unwrap_or(0);
            let full = if path.ends_with('\\') || path.ends_with('/') {
                format!("{path}{name}")
            } else {
                format!("{path}\\{name}")
            };
            entries.push(FileEntry {
                name,
                path: full,
                kind: if is_dir { "dir".into() } else { "file".into() },
                size: size.max(0) as u64,
                mode: String::new(),
                owner: None,
                group: None,
                mtime,
                symlink_target: None,
            });
        }
        entries.sort_by(|a, b| {
            (b.kind == "dir")
                .cmp(&(a.kind == "dir"))
                .then(a.name.cmp(&b.name))
        });
        Ok(entries)
    }

    async fn read_file(&self, path: &str, max_bytes: u64) -> AppResult<Vec<u8>> {
        let p = Self::ps_quote(path);
        let script = format!(
            "$ErrorActionPreference='Stop'; $f = Get-Item -LiteralPath {p}; if ($f.Length -gt {max_bytes}) {{ throw '文件超过读取上限' }}; [Convert]::ToBase64String([IO.File]::ReadAllBytes({p}))"
        );
        let b64 = self.ps(&script).await?;
        use base64::Engine;
        base64::engine::general_purpose::STANDARD
            .decode(b64.trim())
            .map_err(|e| AppError::Sftp(format!("base64 解码失败: {e}")))
    }

    async fn write_file(&self, path: &str, data: &[u8], backup: bool) -> AppResult<()> {
        if data.len() > WINRM_WRITE_MAX {
            return Err(AppError::param(format!(
                "WinRM 写入上限 {WINRM_WRITE_MAX} 字节，当前 {}",
                data.len()
            )));
        }
        use base64::Engine;
        let b64 = base64::engine::general_purpose::STANDARD.encode(data);
        let p = Self::ps_quote(path);
        let backup_clause = if backup {
            format!("if (Test-Path -LiteralPath {p}) {{ Copy-Item -LiteralPath {p} \"{path}.nexterm-bak\" -Force }};")
        } else {
            String::new()
        };
        let script = format!(
            "$ErrorActionPreference='Stop'; {backup_clause} [IO.File]::WriteAllBytes({p}, [Convert]::FromBase64String('{b64}'))"
        );
        self.ps(&script).await?;
        Ok(())
    }

    async fn mkdir(&self, path: &str) -> AppResult<()> {
        let p = Self::ps_quote(path);
        self.ps(&format!(
            "$ErrorActionPreference='Stop'; New-Item -ItemType Directory -Force -Path {p} | Out-Null"
        ))
        .await?;
        Ok(())
    }

    async fn rename(&self, from: &str, to: &str) -> AppResult<()> {
        let (f, t) = (Self::ps_quote(from), Self::ps_quote(to));
        self.ps(&format!(
            "$ErrorActionPreference='Stop'; Move-Item -LiteralPath {f} -Destination {t} -Force"
        ))
        .await?;
        Ok(())
    }

    async fn delete(&self, path: &str, is_dir: bool) -> AppResult<()> {
        let p = Self::ps_quote(path);
        let cmd = if is_dir {
            format!("Remove-Item -LiteralPath {p} -Recurse -Force")
        } else {
            format!("Remove-Item -LiteralPath {p} -Force")
        };
        self.ps(&format!("$ErrorActionPreference='Stop'; {cmd}"))
            .await?;
        Ok(())
    }

    async fn chmod(&self, _path: &str, _mode: u32) -> AppResult<()> {
        Err(AppError::Unsupported("Windows 目标不支持 chmod".into()))
    }

    async fn checksum(&self, path: &str, algo: &str) -> AppResult<String> {
        let p = Self::ps_quote(path);
        let a = match algo.to_ascii_lowercase().as_str() {
            "md5" => "MD5",
            "sha256" => "SHA256",
            _ => return Err(AppError::param("不支持的校验算法（md5/sha256）")),
        };
        let out = self
            .ps(&format!("$ErrorActionPreference='Stop'; (Get-FileHash -LiteralPath {p} -Algorithm {a}).Hash"))
            .await?;
        Ok(out.to_lowercase())
    }

    async fn exists(&self, path: &str) -> AppResult<bool> {
        let p = Self::ps_quote(path);
        let out = self
            .ps(&format!(
                "if (Test-Path -LiteralPath {p}) {{ 'yes' }} else {{ 'no' }}"
            ))
            .await?;
        Ok(out == "yes")
    }

    async fn size(&self, path: &str) -> AppResult<u64> {
        let p = Self::ps_quote(path);
        let out = self
            .ps(&format!(
                "$ErrorActionPreference='Stop'; (Get-Item -LiteralPath {p}).Length"
            ))
            .await?;
        out.trim()
            .parse::<u64>()
            .map_err(|e| AppError::Sftp(format!("大小解析失败: {e}")))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wrap_and_unwrap_cwd() {
        let wrapped = wrap_cmd("C:\\Users", "Get-ChildItem");
        assert!(wrapped.contains("Set-Location -LiteralPath 'C:\\Users'"));
        assert!(wrapped.contains("Get-ChildItem"));
        assert!(wrapped.contains(CWD_SENTINEL_HEAD));

        let simulated = "some output\n<<NEXTERM_CWD>>C:\\Users\\test<<EOF>>\n";
        let (text, new_cwd) = unwrap_cwd(simulated);
        assert_eq!(new_cwd.as_deref(), Some("C:\\Users\\test"));
        assert!(!text.contains("NEXTERM_CWD"));
    }

    #[test]
    fn unwrap_without_sentinel() {
        let (text, cwd) = unwrap_cwd("plain output");
        assert_eq!(text, "plain output");
        assert!(cwd.is_none());
    }

    #[test]
    fn decode_prefers_utf8_falls_back_gb18030() {
        // 合法 UTF-8 中文
        assert_eq!(decode_bytes("中文".as_bytes()), "中文");
        // GBK 字节流（"中文" 的 GBK 编码非合法 UTF-8）
        let (cow, _, _) = encoding_rs::GBK.encode("中文目录");
        assert_eq!(decode_bytes(&cow), "中文目录");
    }
}
