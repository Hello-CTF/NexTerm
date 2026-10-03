package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var secretPattern = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key)(\s*[:=]\s*|\s+)([^\s,;]+)`)
var authorizationPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)([^\s,;]+)`)
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)([^\s,;]+)`)
var mysqlPasswordPattern = regexp.MustCompile(`(?i)(mysql(?:\.exe)?\s+[^\r\n]*?-p)([^\s]+)`)

func RedactText(value string) string {
	value = authorizationPattern.ReplaceAllString(value, "$1<redacted>")
	value = bearerPattern.ReplaceAllString(value, "$1<redacted>")
	value = mysqlPasswordPattern.ReplaceAllString(value, "$1<redacted>")
	return secretPattern.ReplaceAllString(value, "$1$2<redacted>")
}

func redactArguments(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "<invalid>"
	}
	return redactValue("", value)
}

func redactValue(key string, value any) any {
	lower := strings.ToLower(key)
	switch lower {
	case "content", "old_string", "new_string", "keys", "password", "passwd", "token", "secret", "api_key", "apikey", "authorization":
		if text, ok := value.(string); ok {
			return fmt.Sprintf("<redacted:%d bytes>", len(text))
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = redactValue(key, item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(key, item)
		}
		return result
	case string:
		return RedactText(typed)
	default:
		return value
	}
}
