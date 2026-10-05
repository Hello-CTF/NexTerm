package memory

import "regexp"

const redactedValue = "[REDACTED]"

var redactionRules = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*(?:basic|digest|negotiate)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b`),
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s/:@]+:)[^\s/@]+@`),
	regexp.MustCompile(`(?i)mysql(?:\.exe)?\s+[^\r\n]*?-p[^\s]+`),

	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bgh[opusr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),

	regexp.MustCompile(`(?i)(?:[a-z0-9]+_)*(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|session[_-]?token|auth[_-]?token|id[_-]?token|access[_-]?key|secret[_-]?key|private[_-]?key|client[_-]?secret|password|passwd|passphrase|secret|token|authorization)(?:_(?:access|key|keys))*["']?\s*[:=]\s*(\"[^\"\r\n]*\"|'[^'\r\n]*'|[^\s,;]+)`),
}

func RedactText(content string) (string, bool) {
	redacted := content
	for _, rule := range redactionRules {
		redacted = rule.ReplaceAllString(redacted, redactedValue)
	}
	return redacted, redacted != content
}
