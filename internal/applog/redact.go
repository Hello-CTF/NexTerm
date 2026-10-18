package applog

import "regexp"

type redaction struct {
	pattern     *regexp.Regexp
	replacement string
}

var redactions = []redaction{
	{regexp.MustCompile(`(?i)((?:\w+)://(?:[^:/\s@]+):)[^@\s/]+@`), "${1}***@"},
	{regexp.MustCompile(`(?i)("(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)"\s*:\s*")(?:\\.|[^"\\])*(")`), "${1}***${2}"},
	{regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[=:]\s*['"])[^'"]*(['"])`), "${1}***${2}"},
	{regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[=:]\s*)\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)(^|\s)(-p)\S+`), "${1}${2}***"},
	{regexp.MustCompile(`(?i)(^|\s)(--password[= ])\S+`), "${1}${2}***"},
	{regexp.MustCompile(`(?i)(-a\s+)\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)(authorization\s*:\s*).*`), "${1}***"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9\-._~+/]+=*`), "${1}***"},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "***"},
}

func RedactText(input string) string {
	for _, rule := range redactions {
		input = rule.pattern.ReplaceAllString(input, rule.replacement)
	}
	return input
}
