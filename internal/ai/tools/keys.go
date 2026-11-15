package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var specialKeys = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "backspace": "\x7f", "delete": "\x1b[3~",
	"escape": "\x1b", "esc": "\x1b", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C",
	"left": "\x1b[D", "home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
	"insert": "\x1b[2~", "space": " ",
	"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS",
	"f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
	"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
}

// SendKeysTagHelp is the model-facing list of key tags EncodeKeys accepts.
const SendKeysTagHelp = `Supported key tags (case-insensitive): <enter> <return> <tab> <space> <backspace> <delete> <escape> <esc> <up> <down> <left> <right> <home> <end> <pageup> <pagedown> <insert> <f1> to <f12>; <ctrl+X> or <c-X> where X is a letter or one of [ \ ] ^ _ ?; <lt> for a literal "<". Any other <...> text is typed exactly as written; key chords and key names outside this list (for example <alt+b>, <shift+tab>, <ctrl-c>, <f13>) are rejected without sending anything.`

var keyModifierNames = map[string]bool{
	"ctrl": true, "control": true, "alt": true, "meta": true, "shift": true, "super": true,
	"cmd": true, "command": true, "opt": true, "option": true, "win": true,
}

var keyNameAliases = map[string]bool{
	"pgup": true, "pgdn": true, "pgdown": true, "cr": true, "bs": true, "backtab": true,
	"arrowup": true, "arrowdown": true, "arrowleft": true, "arrowright": true,
}

// unsupportedKeyTag reports tags that name a key or key chord this encoder
// cannot send. Typing them literally would report success while the program
// never received the intended key, so they are refused instead. Single-letter
// modifier forms only count with a single key after them, so tag-shaped text
// such as <a-button> stays literal.
func unsupportedKeyTag(tag string) bool {
	if keyModifierNames[tag] || keyNameAliases[tag] {
		return true
	}
	if len(tag) > 1 && tag[0] == 'f' && strings.Trim(tag[1:], "0123456789") == "" {
		return true
	}
	separator := strings.IndexAny(tag, "-+")
	if separator <= 0 || separator == len(tag)-1 {
		return false
	}
	prefix, key := tag[:separator], tag[separator+1:]
	if strings.ContainsAny(key, " \t") {
		return false
	}
	if keyModifierNames[prefix] {
		return true
	}
	switch prefix {
	case "c", "m", "a", "s":
		_, special := specialKeys[key]
		return special || utf8.RuneCountInString(key) == 1
	}
	return false
}

// EncodeKeys turns send_keys text into terminal bytes. Supported tags are
// the names in specialKeys, <lt> for a literal '<', and <ctrl+X>/<c-X> for
// a letter or one of [ \ ] ^ _ ?. Any other <...> text is written exactly
// as given, so the bytes sent equal the text the guard classifies.
func EncodeKeys(input string, enter bool) ([]byte, error) {
	var output strings.Builder
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '<' {
			output.WriteRune(runes[i])
			continue
		}
		end := indexRune(runes[i+1:], '>')
		if end < 0 {
			output.WriteRune(runes[i])
			continue
		}
		end += i + 1
		raw := string(runes[i+1 : end])
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "lt" {
			output.WriteRune('<')
		} else if strings.HasPrefix(tag, "ctrl+") || strings.HasPrefix(tag, "c-") {
			value, ok := controlKey(tag)
			if !ok {
				return nil, fmt.Errorf("未知控制键 <%s>；ctrl+ 仅支持字母与 [ \\ ] ^ _ ?，字面文本请用 <lt> 代替 <，或设置 paste=true 按字面发送", raw)
			}
			output.WriteByte(value)
		} else if value, ok := specialKeys[tag]; ok {
			output.WriteString(value)
		} else if unsupportedKeyTag(tag) {
			return nil, fmt.Errorf("不支持的按键 <%s>；请使用 send_keys 说明中列出的按键名，字面文本请用 <lt> 代替 <，或设置 paste=true 按字面发送", raw)
		} else {
			output.WriteString(string(runes[i : end+1]))
		}
		i = end
	}
	if enter {
		output.WriteByte('\r')
	}
	return []byte(output.String()), nil
}

func controlKey(tag string) (byte, bool) {
	letter := strings.TrimPrefix(tag, "c-")
	if strings.HasPrefix(tag, "ctrl+") {
		letter = tag[len("ctrl+"):]
	}
	if len(letter) != 1 {
		return 0, false
	}
	value := letter[0]
	switch {
	case value >= 'a' && value <= 'z':
		return value - 'a' + 1, true
	case value >= 'A' && value <= 'Z':
		return value - 'A' + 1, true
	}
	switch value {
	case '[':
		return 27, true
	case '\\':
		return 28, true
	case ']':
		return 29, true
	case '^':
		return 30, true
	case '_':
		return 31, true
	case '?':
		return 127, true
	}
	return 0, false
}

func indexRune(values []rune, target rune) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}
