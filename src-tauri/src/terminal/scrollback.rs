//! 原始字节环形缓冲（§4.2 / §12.2）。
//!
//! 按**字节**存储（不是按行），避免多字节字符被从中间丢弃；
//! 解码交由消费方在边界对齐时做增量处理。

use std::collections::VecDeque;

#[derive(Debug)]
pub struct Scrollback {
    buf: VecDeque<u8>,
    cap: usize,
    total_in: u64,
    dropped: u64,
}

impl Scrollback {
    pub fn new(cap_bytes: usize) -> Self {
        Self {
            buf: VecDeque::with_capacity(cap_bytes.min(1 << 20)),
            cap: cap_bytes,
            total_in: 0,
            dropped: 0,
        }
    }

    pub fn push(&mut self, data: &[u8]) {
        self.total_in += data.len() as u64;
        let overflow = (self.buf.len() + data.len()).saturating_sub(self.cap);
        if overflow > 0 {
            if overflow >= self.buf.len() {
                self.dropped += self.buf.len() as u64;
                self.buf.clear();
            } else {
                self.dropped += overflow as u64;
                self.buf.drain(..overflow);
            }
        }
        self.buf.extend(data);
        // 收尾防御：极端情况下（data > cap）只留尾部
        if self.buf.len() > self.cap {
            let excess = self.buf.len() - self.cap;
            self.dropped += excess as u64;
            self.buf.drain(..excess);
        }
    }

    /// 当前缓冲内字节数。
    pub fn len(&self) -> usize {
        self.buf.len()
    }

    pub fn is_empty(&self) -> bool {
        self.buf.is_empty()
    }

    /// 累计写入字节。
    pub fn total_in(&self) -> u64 {
        self.total_in
    }

    /// 因容量被丢弃的字节数。
    pub fn dropped(&self) -> u64 {
        self.dropped
    }

    /// 导出最近的 `max_bytes` 字节（用于 terminal_dump / 导出）。
    pub fn dump(&self, max_bytes: usize) -> Vec<u8> {
        let start = self.buf.len().saturating_sub(max_bytes);
        self.buf.iter().skip(start).copied().collect()
    }

    /// 全量快照（测试与导出用）。
    pub fn snapshot(&self) -> Vec<u8> {
        self.buf.iter().copied().collect()
    }

    pub fn clear(&mut self) {
        self.buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_recent_bytes_only() {
        let mut sb = Scrollback::new(16);
        sb.push(b"0123456789");
        sb.push(b"ABCDEFGHIJ");
        let snap = sb.snapshot();
        assert_eq!(snap.len(), 16);
        assert_eq!(sb.total_in(), 20);
        assert_eq!(sb.dropped(), 4);
        assert_eq!(snap, b"456789ABCDEFGHIJ".to_vec());
    }

    #[test]
    fn chunk_larger_than_cap_keeps_tail() {
        let mut sb = Scrollback::new(8);
        sb.push(b"a very long stream of bytes exceeding cap");
        assert_eq!(sb.len(), 8);
        assert_eq!(sb.snapshot(), b"ding cap".to_vec());
        assert_eq!(
            sb.dropped(),
            ("a very long stream of bytes exceeding cap".len() - 8) as u64
        );
    }

    #[test]
    fn dump_respects_max() {
        let mut sb = Scrollback::new(1024);
        sb.push(&vec![b'x'; 600]);
        sb.push(&vec![b'y'; 300]);
        let d = sb.dump(100);
        assert_eq!(d.len(), 100);
        assert_eq!(d[0], b'y');
        assert_eq!(*d.last().unwrap(), b'y');
    }
}
