package terminal

import "strings"

func EncodePaste(text string, bracketed bool) []byte {
	if bracketed {
		return []byte("\x1b[200~" + text + "\x1b[201~")
	}
	return []byte(strings.NewReplacer("\r\n", "\r", "\n", "\r").Replace(text))
}
