//! 日志脱敏（§9.2）：所有进入 tracing 的敏感字符串先过 [`redact`]。

use std::sync::OnceLock;

fn patterns() -> &'static [(regex::Regex, &'static str)] {
    static RES: OnceLock<Vec<(regex::Regex, &'static str)>> = OnceLock::new();
    RES.get_or_init(|| {
        [
            // key=value 形态：保留键名
            (
                r#"(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[=:]\s*)\S+"#,
                "${1}***",
            ),
            // mysql -pXXX / --password XXX
            (r"(?i)((?:-p|--password[= ]))\S+", "${1}***"),
            // redis -a XXX
            (r"(?i)(-a\s+)\S+", "${1}***"),
            // Authorization 头（整行）
            (r"(?i)(authorization\s*:\s*).*", "${1}***"),
            (r"(?i)(bearer\s+)[A-Za-z0-9\-._~+/]+=*", "${1}***"),
            // 私钥块（多行）
            (
                r"(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----",
                "***",
            ),
            // URL 中的 userinfo：保留 scheme 与 user
            (r"(?i)((?:\w+)://(?:[^:/\s@]+):)[^@\s/]+@", "${1}***@"),
        ]
        .iter()
        .filter_map(|(p, rep)| regex::Regex::new(p).ok().map(|re| (re, *rep)))
        .collect()
    })
}

/// 遮蔽字符串中的密码 / token / 私钥等内容。
pub fn redact(s: &str) -> String {
    patterns()
        .iter()
        .fold(s.to_string(), |acc, (re, replacement)| {
            re.replace_all(&acc, *replacement).to_string()
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn masks_kv_secrets() {
        assert_eq!(redact("password=hunter2 x=1"), "password=*** x=1");
        let r = redact("api_key: sk-123456");
        assert!(r.contains("***"));
        assert!(!r.contains("sk-123456"));
    }

    #[test]
    fn masks_cli_passwords() {
        let r = redact("mysql -uroot -pSup3rSecret db");
        assert!(!r.contains("Sup3rSecret"));
    }

    #[test]
    fn masks_auth_headers() {
        let r = redact("Authorization: Bearer abc.def.ghi");
        assert!(!r.contains("abc.def.ghi"));
    }

    #[test]
    fn masks_private_key_blocks() {
        let key = "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----";
        assert_eq!(redact(key), "***");
    }

    #[test]
    fn masks_url_userinfo() {
        let r = redact("redis://admin:Secret1@host:6379/0");
        assert!(!r.contains("Secret1"));
        assert!(r.contains(":***@"));
    }

    #[test]
    fn leaves_normal_text() {
        assert_eq!(redact("docker ps -a"), "docker ps -a");
    }
}
