package db

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func postgresJSONValue(value any, databaseType string) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return postgresBytesValue(typed, databaseType)
	case string:
		return typed
	case int64:
		return signedJSONInteger(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return strconv.FormatFloat(typed, 'g', -1, 64)
		}
		return typed
	case bool:
		return typed
	case time.Time:
		return typed.Format("2006-01-02 15:04:05.999999999Z07:00")
	default:
		return typed
	}
}

func postgresBytesValue(value []byte, databaseType string) any {
	if strings.EqualFold(databaseType, "BYTEA") {
		return base64.StdEncoding.EncodeToString(value)
	}
	text := string(value)
	if !utf8.ValidString(text) {
		return base64.StdEncoding.EncodeToString(value)
	}
	return text
}
