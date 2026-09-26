//! 终端编码转换（§12.2）：环形缓冲按字节存，解码在边界对齐时做增量处理。
//!
//! > 命名说明：本模块曾叫 `screen.rs`，但它**不是**内核 VT 状态机 ——
//! > 状态机（`vt100::Parser` + 网格/光标/滚动缓冲）在 `terminal/mod.rs` 的 `TerminalTab` 上。
//! > 这里只把远端字节流转成 UTF-8 字节流，喂给状态机与前端。改名以免后续找错文件。
//!
//! - UTF-8 之外支持 GB18030 / GBK / Big5 / Latin-1；
//! - 多字节字符被切在块边界时，由 encoding_rs 增量解码器缓存尾字节；
//! - 运行中切换编码：换解码器并丢弃残留（新字节按新编码解释）；
//! - 顺路扫描 DECSET 2004（bracketed paste）序列供终端状态使用。

use encoding_rs::{CoderResult, Decoder};

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TerminalEncoding {
    #[default]
    Utf8,
    Gbk,
    Gb18030,
    Big5,
    Latin1,
}

impl TerminalEncoding {
    pub fn from_str_opt(s: &str) -> Option<Self> {
        match s.to_ascii_lowercase().as_str() {
            "utf-8" | "utf8" => Some(Self::Utf8),
            "gbk" => Some(Self::Gbk),
            "gb18030" => Some(Self::Gb18030),
            "big5" => Some(Self::Big5),
            "latin1" | "iso-8859-1" => Some(Self::Latin1),
            _ => None,
        }
    }

    fn encoding_rs(self) -> &'static encoding_rs::Encoding {
        match self {
            Self::Utf8 => encoding_rs::UTF_8,
            Self::Gbk => encoding_rs::GBK,
            Self::Gb18030 => encoding_rs::GB18030,
            Self::Big5 => encoding_rs::BIG5,
            Self::Latin1 => encoding_rs::WINDOWS_1252,
        }
    }
}

const PASTE_SEQ: &[u8] = b"\x1b[?2004";

/// 增量转码器：把远端字节流转成 UTF-8 字节流（喂给 vt100 与前端）。
#[derive(Debug)]
pub struct Transcoder {
    encoding: TerminalEncoding,
    decoder: Decoder,
    /// 上块的尾部（用于 bracketed-paste 序列跨块匹配）。
    carry: Vec<u8>,
    paste_on: bool,
}

impl Transcoder {
    pub fn new(encoding: TerminalEncoding) -> Self {
        let decoder = encoding.encoding_rs().new_decoder();
        Self {
            encoding,
            decoder,
            carry: Vec::new(),
            paste_on: false,
        }
    }

    pub fn encoding(&self) -> TerminalEncoding {
        self.encoding
    }

    /// 切换编码：残留不再按旧编码解释，直接换解码器。
    pub fn switch(&mut self, encoding: TerminalEncoding) {
        if encoding == self.encoding {
            return;
        }
        self.encoding = encoding;
        self.decoder = encoding.encoding_rs().new_decoder();
        self.carry.clear();
    }

    /// 喂入原始字节，返回 UTF-8 字节。
    pub fn feed(&mut self, data: &[u8]) -> Vec<u8> {
        let mut out = Vec::with_capacity(data.len() * 2 + 8);
        let mut input = data;
        loop {
            // 最坏情况：每个坏字节替换为 3 字节 U+FFFD
            let mut tmp = String::with_capacity(input.len() * 3 + 4);
            // decode_to_string 以 String 容量为输出上限（不会扩容）；
            // decode_to_str 的 &mut str 长度为 0 时会立即 OutputFull，不能用。
            let (result, read, _had_errors) = self.decoder.decode_to_string(input, &mut tmp, false);
            out.extend_from_slice(tmp.as_bytes());
            input = &input[read..];
            match result {
                CoderResult::InputEmpty => break,
                CoderResult::OutputFull => continue,
            }
        }
        self.scan_bracketed_paste(&out);
        out
    }

    pub fn bracketed_paste(&self) -> bool {
        self.paste_on
    }

    /// 扫描 DECSET 2004 h/l。carry 保留尾部，处理跨块序列。
    fn scan_bracketed_paste(&mut self, chunk: &[u8]) {
        let mut hay = std::mem::take(&mut self.carry);
        hay.extend_from_slice(chunk);
        let mut rest: &[u8] = &hay;
        while let Some(pos) = find_subslice(rest, PASTE_SEQ) {
            match rest.get(pos + PASTE_SEQ.len()) {
                Some(b'h') => self.paste_on = true,
                Some(b'l') => self.paste_on = false,
                _ => {}
            }
            rest = &rest[(pos + PASTE_SEQ.len()).min(rest.len())..];
            if rest.is_empty() {
                break;
            }
        }
        // 保留尾部（最长 PASTE_SEQ.len()-1 字节即可能的不完整序列）
        let keep = PASTE_SEQ.len() - 1;
        self.carry = if rest.len() > keep {
            rest[rest.len() - keep..].to_vec()
        } else {
            rest.to_vec()
        };
    }
}

fn find_subslice(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() || haystack.len() < needle.len() {
        return None;
    }
    haystack.windows(needle.len()).position(|w| w == needle)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn utf8_multibyte_across_chunks() {
        let mut t = Transcoder::new(TerminalEncoding::Utf8);
        let bytes = "中文输出测试".as_bytes();
        let half = bytes.len() - 1; // 切在多字节字符中间
        let mut all = t.feed(&bytes[..half]);
        all.extend_from_slice(&t.feed(&bytes[half..]));
        assert_eq!(String::from_utf8(all).unwrap(), "中文输出测试");
    }

    #[test]
    fn gbk_decode() {
        let mut t = Transcoder::new(TerminalEncoding::Gbk);
        let (cow, _, _) = encoding_rs::GBK.encode("中文目录");
        let out = t.feed(&cow);
        assert_eq!(String::from_utf8(out).unwrap(), "中文目录");
    }

    #[test]
    fn gbk_multibyte_split_across_feed() {
        let mut t = Transcoder::new(TerminalEncoding::Gbk);
        let (cow, _, _) = encoding_rs::GBK.encode("服务器编码");
        let mut all = t.feed(&cow[..3]);
        all.extend_from_slice(&t.feed(&cow[3..]));
        assert_eq!(String::from_utf8(all).unwrap(), "服务器编码");
    }

    #[test]
    fn invalid_byte_replaced_not_dropped() {
        let mut t = Transcoder::new(TerminalEncoding::Utf8);
        let out = t.feed(&[b'a', 0xFF, b'b']);
        assert_eq!(String::from_utf8_lossy(&out), "a\u{FFFD}b");
    }

    #[test]
    fn switch_encoding_midstream() {
        let mut t = Transcoder::new(TerminalEncoding::Gbk);
        let (cow, _, _) = encoding_rs::GBK.encode("部分");
        let _ = t.feed(&cow);
        t.switch(TerminalEncoding::Utf8);
        let out = t.feed("切换编码".as_bytes());
        assert_eq!(String::from_utf8(out).unwrap(), "切换编码");
    }

    #[test]
    fn bracketed_paste_detection_including_split() {
        let mut t = Transcoder::new(TerminalEncoding::Utf8);
        t.feed(b"\x1b[?2004hhello");
        assert!(t.bracketed_paste());
        t.feed(b"\x1b[?2004lworld");
        assert!(!t.bracketed_paste());
        // 跨块：序列被切开
        let seq = b"\x1b[?2004h";
        let mut t2 = Transcoder::new(TerminalEncoding::Utf8);
        t2.feed(&seq[..4]);
        assert!(!t2.bracketed_paste());
        t2.feed(&seq[4..]);
        assert!(t2.bracketed_paste());
    }
}
