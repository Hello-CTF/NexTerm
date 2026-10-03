package guard

func hasUnknownSQLFunction(tokens []sqlToken) bool {
	for i := 1; i < len(tokens); i++ {
		if tokens[i].text != "(" {
			continue
		}
		name := tokens[i-1]
		if name.quoted {
			return true
		}
		if name.keyword || name.text == "<literal>" || name.text == "." {
			continue
		}
		if _, ok := readOnlySQLFunctions[name.text]; !ok {
			return true
		}
	}
	return false
}

var readOnlySQLFunctions = func() map[string]struct{} {
	values := []string{
		"ABS", "ACOS", "ADDDATE", "ADDTIME", "ASCII", "ASIN", "ATAN", "ATAN2", "AVG",
		"BIN", "BIT_COUNT", "BIT_LENGTH", "CEIL", "CEILING", "CHAR", "CHAR_LENGTH", "CHARACTER_LENGTH",
		"COALESCE", "CONCAT", "CONCAT_WS", "CONV", "CONVERT", "COS", "COT", "COUNT", "CRC32",
		"CURDATE", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "CURTIME",
		"DATABASE", "DATE", "DATEDIFF", "DATE_ADD", "DATE_FORMAT", "DATE_SUB", "DAY", "DAYNAME",
		"DAYOFMONTH", "DAYOFWEEK", "DAYOFYEAR", "DEGREES", "ELT", "EXP", "EXPORT_SET", "EXTRACT",
		"FIELD", "FIND_IN_SET", "FLOOR", "FORMAT", "FROM_BASE64", "FROM_DAYS", "FROM_UNIXTIME",
		"GREATEST", "GROUP_CONCAT", "HEX", "HOUR", "IF", "IFNULL", "INET_ATON", "INET_NTOA",
		"INET6_ATON", "INET6_NTOA", "INSERT", "INSTR", "INTERVAL", "ISNULL", "JSON_ARRAY",
		"JSON_ARRAYAGG", "JSON_CONTAINS", "JSON_CONTAINS_PATH", "JSON_DEPTH", "JSON_EXTRACT", "JSON_KEYS",
		"JSON_LENGTH", "JSON_OBJECT", "JSON_OBJECTAGG", "JSON_PRETTY", "JSON_QUOTE", "JSON_SEARCH",
		"JSON_TYPE", "JSON_UNQUOTE", "JSON_VALID", "LAST_DAY", "LAST_INSERT_ID", "LCASE", "LEAST",
		"LEFT", "LENGTH", "LN", "LOCALTIME", "LOCALTIMESTAMP", "LOCATE", "LOG", "LOG10", "LOG2",
		"LOWER", "LPAD", "LTRIM", "MAKEDATE", "MAKETIME", "MAX", "MD5", "MICROSECOND", "MID",
		"MIN", "MINUTE", "MOD", "MONTH", "MONTHNAME", "NOW", "NULLIF", "OCT", "OCTET_LENGTH",
		"ORD", "PERIOD_ADD", "PERIOD_DIFF", "PI", "POSITION", "POW", "POWER", "QUARTER", "QUOTE",
		"RADIANS", "RAND", "REGEXP_INSTR", "REGEXP_LIKE", "REGEXP_REPLACE", "REGEXP_SUBSTR", "REPEAT",
		"REPLACE", "REVERSE", "RIGHT", "ROUND", "ROW_COUNT", "RPAD", "RTRIM", "SCHEMA", "SECOND",
		"SEC_TO_TIME", "SHA", "SHA1", "SHA2", "SIGN", "SIN", "SOUNDEX", "SPACE", "SQRT", "STDDEV",
		"STDDEV_POP", "STDDEV_SAMP", "STR_TO_DATE", "STRCMP", "SUBDATE", "SUBSTR", "SUBSTRING",
		"SUBSTRING_INDEX", "SUBTIME", "SUM", "SYSDATE", "TAN", "TIME", "TIMEDIFF", "TIMESTAMP",
		"TIMESTAMPADD", "TIMESTAMPDIFF", "TIME_FORMAT", "TIME_TO_SEC", "TO_BASE64", "TO_DAYS",
		"TO_SECONDS", "TRIM", "TRUNCATE", "UCASE", "UNHEX", "UNIX_TIMESTAMP", "UPPER", "USER",
		"UTC_DATE", "UTC_TIME", "UTC_TIMESTAMP", "UUID", "UUID_SHORT", "VALUES", "VAR_POP", "VAR_SAMP",
		"VARIANCE", "VERSION", "WEEK", "WEEKDAY", "WEEKOFYEAR", "YEAR", "YEARWEEK",
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}()
