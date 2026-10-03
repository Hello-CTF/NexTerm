package tools

import (
	"fmt"
	"strings"
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
		tag := strings.ToLower(strings.TrimSpace(string(runes[i+1 : end])))
		if tag == "lt" {
			output.WriteRune('<')
		} else if strings.HasPrefix(tag, "ctrl+") || strings.HasPrefix(tag, "c-") {
			letter := tag[strings.LastIndex(tag, "-")+1:]
			if strings.HasPrefix(tag, "ctrl+") {
				letter = tag[5:]
			}
			if len(letter) != 1 {
				return nil, fmt.Errorf("未知控制键 <%s>", tag)
			}
			value := letter[0]
			if value >= 'a' && value <= 'z' {
				value -= 'a' - 1
			} else if value >= 'A' && value <= 'Z' {
				value -= 'A' - 1
			} else {
				switch value {
				case '[':
					value = 27
				case '\\':
					value = 28
				case ']':
					value = 29
				case '^':
					value = 30
				case '_':
					value = 31
				case '?':
					value = 127
				default:
					return nil, fmt.Errorf("未知控制键 <%s>", tag)
				}
			}
			output.WriteByte(value)
		} else if value, ok := specialKeys[tag]; ok {
			output.WriteString(value)
		} else {
			output.WriteString("<" + tag + ">")
		}
		i = end
	}
	if enter {
		output.WriteByte('\r')
	}
	return []byte(output.String()), nil
}

func indexRune(values []rune, target rune) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}
