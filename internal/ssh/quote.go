package ssh

import "strings"

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
