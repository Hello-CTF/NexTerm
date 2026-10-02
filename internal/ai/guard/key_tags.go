package guard

import "strings"

func terminalActionTag(tag string) bool {
	if strings.HasPrefix(tag, "ctrl+") || strings.HasPrefix(tag, "c-") {
		return true
	}
	switch tag {
	case "backspace", "delete", "escape", "esc", "up", "down", "right", "left", "home", "end", "pageup", "pagedown", "insert",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return true
	default:
		return false
	}
}
