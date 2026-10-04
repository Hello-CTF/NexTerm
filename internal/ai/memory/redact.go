package memory

import "regexp"

const redactedValue = "[REDACTED]"

var redactionRules = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b`),
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s/:@]+:)[^\s/@]+@`),

	regexp.MustCompile(`(?i)(?:[a-z0-9]+_)*(?:api[_-]?key|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|password|passwd|secret|token|authorization)(?:_(?:access|key|keys))*["']?\s*[:=]\s*(\"[^\"\r\n]*\"|'[^'\r\n]*'|[^\s,;]+)`),
}

func RedactText(content string) (string, bool) {
	redacted := content
	for _, rule := range redactionRules {
		redacted = rule.ReplaceAllString(redacted, redactedValue)
	}
	return redacted, redacted != content
}
