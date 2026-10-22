package ssh

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func powerShellCommand(shell, script string) string {
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	return shell + " -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(encoded)
}

func quoteWindowsArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\"") {
		return value
	}
	var quoted strings.Builder
	quoted.WriteByte('"')
	backslashes := 0
	for _, char := range value {
		switch char {
		case '\\':
			backslashes++
		case '"':
			quoted.WriteString(strings.Repeat(`\`, backslashes*2+1))
			quoted.WriteRune(char)
			backslashes = 0
		default:
			quoted.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			quoted.WriteRune(char)
		}
	}
	quoted.WriteString(strings.Repeat(`\`, backslashes*2))
	quoted.WriteByte('"')
	return quoted.String()
}
