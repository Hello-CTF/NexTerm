//! 按键编码（§8.6 接管模式 send_keys）：把语义键名转成 PTY 字节序列。

/// 特殊键名 → 字节序列。不在此表中的输入按原样发送。
pub fn encode_key(name: &str) -> Option<&'static [u8]> {
    let n = name.trim().to_ascii_lowercase();
    Some(match n.as_str() {
        "enter" | "return" => b"\r",
        "linefeed" | "lf" => b"\n",
        "tab" => b"\t",
        "shift+tab" | "backtab" => b"\x1b[Z",
        "esc" | "escape" => b"\x1b",
        "space" => b" ",
        "backspace" => b"\x7f",
        "delete" | "del" => b"\x1b[3~",
        "insert" => b"\x1b[2~",
        "home" => b"\x1b[H",
        "end" => b"\x1b[F",
        "pageup" => b"\x1b[5~",
        "pagedown" => b"\x1b[6~",
        "up" => b"\x1b[A",
        "down" => b"\x1b[B",
        "right" => b"\x1b[C",
        "left" => b"\x1b[D",
        "ctrl+@" | "ctrl+space" => b"\x00",
        "ctrl+a" => b"\x01",
        "ctrl+b" => b"\x02",
        "ctrl+c" => b"\x03",
        "ctrl+d" => b"\x04",
        "ctrl+e" => b"\x05",
        "ctrl+f" => b"\x06",
        "ctrl+g" => b"\x07",
        "ctrl+h" => b"\x08",
        "ctrl+i" => b"\x09",
        "ctrl+j" => b"\x0a",
        "ctrl+k" => b"\x0b",
        "ctrl+l" => b"\x0c",
        "ctrl+m" => b"\x0d",
        "ctrl+n" => b"\x0e",
        "ctrl+o" => b"\x0f",
        "ctrl+p" => b"\x10",
        "ctrl+q" => b"\x11",
        "ctrl+r" => b"\x12",
        "ctrl+s" => b"\x13",
        "ctrl+t" => b"\x14",
        "ctrl+u" => b"\x15",
        "ctrl+v" => b"\x16",
        "ctrl+w" => b"\x17",
        "ctrl+x" => b"\x18",
        "ctrl+y" => b"\x19",
        "ctrl+z" => b"\x1a",
        "ctrl+\\" => b"\x1c",
        "ctrl+]" => b"\x1d",
        "ctrl+^" => b"\x1e",
        "ctrl+_" => b"\x1f",
        "f1" => b"\x1bOP",
        "f2" => b"\x1bOQ",
        "f3" => b"\x1bOR",
        "f4" => b"\x1bOS",
        "f5" => b"\x1b[15~",
        "f6" => b"\x1b[17~",
        "f7" => b"\x1b[18~",
        "f8" => b"\x1b[19~",
        "f9" => b"\x1b[20~",
        "f10" => b"\x1b[21~",
        "f11" => b"\x1b[23~",
        "f12" => b"\x1b[24~",
        _ => return None,
    })
}

/// 组合发送：文本（可含特殊键名以 `<...>` 引用）+ 可选回车。
/// 例：`encode_send("y", true)` → `y\r`；
///     `encode_send("<ctrl+c>", false)` → `\x03`；
///     `encode_send("ls<enter>", false)` → `ls\r`。
pub fn encode_send(text: &str, enter: bool) -> Vec<u8> {
    let mut out = Vec::with_capacity(text.len() + 1);
    let mut rest = text;
    while let (Some(start), Some(end)) = (rest.find('<'), rest.find('>')) {
        if start < end {
            out.extend_from_slice(&rest.as_bytes()[..start]);
            let name = &rest[start + 1..end];
            match encode_key(name) {
                Some(seq) => out.extend_from_slice(seq),
                None => out.extend_from_slice(&rest.as_bytes()[start..=end]),
            }
            rest = &rest[end + 1..];
        } else {
            out.extend_from_slice(&rest.as_bytes()[..start]);
            rest = &rest[start..];
            break;
        }
    }
    out.extend_from_slice(rest.as_bytes());
    if enter {
        out.extend_from_slice(b"\r");
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn simple_keys() {
        assert_eq!(encode_send("y", true), b"y\r".to_vec());
        assert_eq!(encode_send("", true), b"\r".to_vec());
        assert_eq!(encode_send("ls", false), b"ls".to_vec());
    }

    #[test]
    fn special_keys() {
        assert_eq!(encode_send("<ctrl+c>", false), b"\x03".to_vec());
        assert_eq!(
            encode_send("<up><up><enter>", false),
            b"\x1b[A\x1b[A\r".to_vec()
        );
        assert_eq!(encode_send("sudo <tab>", false), b"sudo \t".to_vec());
    }

    #[test]
    fn mixed_text_and_keys() {
        assert_eq!(
            encode_send("vim /etc/hosts<enter>", false),
            b"vim /etc/hosts\r".to_vec()
        );
        assert_eq!(encode_send(":wq<enter>", true), b":wq\r\r".to_vec());
    }

    #[test]
    fn unknown_tag_passthrough() {
        assert_eq!(encode_send("<notakey>", false), b"<notakey>".to_vec());
    }

    #[test]
    fn case_insensitive_keys() {
        assert_eq!(encode_send("<CTRL+C>", false), b"\x03".to_vec());
        assert_eq!(encode_key("Enter"), Some(b"\r".as_ref()));
    }
}
