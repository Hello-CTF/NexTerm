package vault

import "regexp"

type redaction struct {
	pattern     *regexp.Regexp
	replacement string
}

var redactions = []redaction{
	{regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[=:]\s*)\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)((?:-p|--password[= ]))\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)(-a\s+)\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)(authorization\s*:\s*).*`), "${1}***"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9\-._~+/]+=*`), "${1}***"},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "***"},
	{regexp.MustCompile(`(?i)((?:\w+)://(?:[^:/\s@]+):)[^@\s/]+@`), "${1}***@"},
}

func Redact(input string) string {
	for _, rule := range redactions {
		input = rule.pattern.ReplaceAllString(input, rule.replacement)
	}
	return input
}
