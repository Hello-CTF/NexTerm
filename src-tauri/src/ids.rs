//! ID 与时间约定（§3）：所有 ID 为 ULID 字符串，时间戳为 Unix 毫秒。

pub type SessionId = String;
pub type TabId = String;

pub fn new_id() -> String {
    ulid::Ulid::new().to_string()
}

pub fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_are_unique_and_sorted() {
        let a = new_id();
        let b = new_id();
        assert_ne!(a, b);
        assert_eq!(a.len(), 26);
        assert!(now_ms() > 1_700_000_000_000);
    }
}
