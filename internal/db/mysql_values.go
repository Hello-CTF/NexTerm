package db

import (
	"database/sql"
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSafeJSONInteger = uint64(1<<53 - 1)

func mysqlJSONValue(value any, databaseType string) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case sql.RawBytes:
		return mysqlBytesValue([]byte(typed), databaseType)
	case []byte:
		return mysqlBytesValue(typed, databaseType)
	case string:
		return mysqlTextValue(typed, databaseType)
	case int64:
		return signedJSONInteger(typed)
	case uint64:
		return unsignedJSONInteger(typed)
	case int32:
		return typed
	case uint32:
		return typed
	case int16:
		return typed
	case uint16:
		return typed
	case int8:
		return typed
	case uint8:
		return typed
	case int:
		return signedJSONInteger(int64(typed))
	case uint:
		return unsignedJSONInteger(uint64(typed))
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return strconv.FormatFloat(float64(typed), 'g', -1, 32)
		}
		return typed
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

func mysqlBytesValue(value []byte, databaseType string) any {
	if isBinaryMySQLType(databaseType) {
		return base64.StdEncoding.EncodeToString(value)
	}
	text := string(value)
	if !utf8.ValidString(text) {
		return base64.StdEncoding.EncodeToString(value)
	}
	return mysqlTextValue(text, databaseType)
}

func mysqlTextValue(value, databaseType string) any {
	typeName := strings.ToUpper(databaseType)
	switch typeName {
	case "DECIMAL", "NEWDECIMAL", "DEC", "NUMERIC", "FIXED":
		return value
	case "FLOAT", "DOUBLE", "DOUBLE PRECISION", "REAL":
		if number, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
			return number
		}
		return value
	case "BIT":
		if number, err := strconv.ParseUint(value, 10, 64); err == nil {
			return unsignedJSONInteger(number)
		}
		return value
	default:
		if isIntegerMySQLType(typeName) {
			if strings.HasPrefix(typeName, "UNSIGNED ") {
				if number, err := strconv.ParseUint(value, 10, 64); err == nil {
					return unsignedJSONInteger(number)
				}
			} else if number, err := strconv.ParseInt(value, 10, 64); err == nil {
				return signedJSONInteger(number)
			}
		}
		return value
	}
}

func isIntegerMySQLType(typeName string) bool {
	typeName = strings.TrimPrefix(typeName, "UNSIGNED ")
	switch typeName {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR":
		return true
	default:
		return false
	}
}

func isBinaryMySQLType(typeName string) bool {
	switch strings.ToUpper(typeName) {
	case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY":
		return true
	default:
		return false
	}
}

func signedJSONInteger(value int64) any {
	if value >= -int64(maxSafeJSONInteger) && value <= int64(maxSafeJSONInteger) {
		return value
	}
	return strconv.FormatInt(value, 10)
}

func unsignedJSONInteger(value uint64) any {
	if value <= maxSafeJSONInteger {
		return value
	}
	return strconv.FormatUint(value, 10)
}
