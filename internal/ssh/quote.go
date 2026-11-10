package ssh

import (
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func quotePowerShell(value string) string {
	return psquote.Quote(value)
}

func powerShellCommand(shell, script string) string {
	return psquote.Command(shell, script)
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
