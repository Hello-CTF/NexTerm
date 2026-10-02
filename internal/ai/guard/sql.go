package guard

import (
	"strconv"
	"strings"
	"unicode"
)

type sqlToken struct {
	text    string
	keyword bool
}

func ClassifySQL(statement string, rules []string) Ruling {
	result := Allow()
	if matchDangerRule(statement, rules) {
		result = Dangerous("命中自定义危险规则")
	}
	statements, err := splitSQL(statement)
	if err != nil {
		return Worst(result, Confirm(KindDBWrite, "SQL 引号或注释未闭合，无法安全分析"))
	}
	for _, tokens := range statements {
		if len(tokens) == 0 {
			continue
		}
		result = Worst(result, classifySQLStatement(tokens))
		if result.Risk == Forbidden {
			return result
		}
	}
	if len(statements) == 0 {
		return Worst(result, Confirm(KindDBWrite, "空 SQL 或无有效语句"))
	}
	if result.Risk == Safe && strings.TrimSpace(statement) != "" {
		return result
	}
	return result
}

func classifySQLStatement(tokens []sqlToken) Ruling {
	first := strings.ToUpper(tokens[0].text)
	switch first {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "VALUES", "TABLE":
		return classifyReadSQL(tokens)
	case "WITH":
		return classifyReadSQL(tokens)
	case "EXPLAIN":
		for i, token := range tokens {
			if token.keyword && token.text == "ANALYZE" {
				if i+1 < len(tokens) {
					return classifySQLStatement(tokens[i+1:])
				}
				return Confirm(KindDBWrite, "EXPLAIN ANALYZE 目标不明确")
			}
		}
		if len(tokens) > 1 {
			next := strings.ToUpper(tokens[1].text)
			if next != "SELECT" && next != "WITH" && next != "SHOW" && next != "DESC" && next != "DESCRIBE" {
				return Confirm(KindDBWrite, "EXPLAIN 非只读语句需要确认")
			}
		}
		return classifyReadSQL(tokens)
	case "DROP":
		if hasKeywordSequence(tokens, "DATABASE") || hasKeywordSequence(tokens, "SCHEMA") {
			return Deny("禁止 DROP DATABASE / DROP SCHEMA")
		}
		return Confirm(KindDBWrite, "DROP 会修改数据库对象")
	case "TRUNCATE":
		return Dangerous("TRUNCATE 会清空数据表")
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE", "CREATE", "ALTER", "RENAME", "GRANT", "REVOKE", "SET", "USE", "CALL", "DO", "LOAD", "IMPORT", "ATTACH", "DETACH", "VACUUM", "OPTIMIZE", "ANALYZE", "LOCK", "UNLOCK", "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return Confirm(KindDBWrite, "SQL 写入、会话或事务控制")
	default:
		return Confirm(KindDBWrite, "未知 SQL 语句需要确认")
	}
}

func classifyReadSQL(tokens []sqlToken) Ruling {
	if hasUnknownSQLFunction(tokens) {
		return Confirm(KindDBWrite, "未知 SQL 函数可能有写入或锁副作用")
	}
	for i, token := range tokens {
		if i+1 < len(tokens) && tokens[i+1].text == "(" {
			if _, ok := readOnlySQLFunctions[token.text]; ok {
				continue
			}
		}
		if !token.keyword {
			continue
		}
		switch token.text {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "CALL", "LOAD", "SET", "USE":
			if i == 0 && (token.text == "SET" || token.text == "USE") {
				return Confirm(KindDBWrite, "SQL 会话状态变更")
			}
			return Confirm(KindDBWrite, "只读前缀中包含写操作")
		case "OUTFILE", "DUMPFILE":
			return Confirm(KindWriteFS, "SELECT 会写入服务器文件")
		case "INTO":
			if i+1 < len(tokens) && tokens[i+1].keyword && (tokens[i+1].text == "OUTFILE" || tokens[i+1].text == "DUMPFILE") {
				return Confirm(KindWriteFS, "SELECT 会写入服务器文件")
			}
			if startsWithKeyword(tokens[0], "SELECT") {
				return Confirm(KindDBWrite, "SELECT INTO 会创建或修改表")
			}
		case "FOR":
			if i+1 < len(tokens) && tokens[i+1].keyword && tokens[i+1].text == "UPDATE" {
				return Confirm(KindDBWrite, "锁行查询需要确认")
			}
		case "LOCK":
			return Confirm(KindDBWrite, "锁表或锁行查询需要确认")
		}
	}
	return Allow()
}

func startsWithKeyword(token sqlToken, keyword string) bool {
	return token.keyword && token.text == keyword
}

func hasKeywordSequence(tokens []sqlToken, keyword string) bool {
	for _, token := range tokens[1:] {
		if token.keyword && token.text == keyword {
			return true
		}
	}
	return false
}

func splitSQL(input string) ([][]sqlToken, error) {
	var result [][]sqlToken
	var current []sqlToken
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		value := word.String()
		upper := strings.ToUpper(value)
		current = append(current, sqlToken{text: upper, keyword: isSQLKeyword(upper)})
		word.Reset()
	}
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if r == '-' && i+1 < len(runes) && runes[i+1] == '-' {
			flush()
			i += 2
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			continue
		}
		if r == '#' {
			flush()
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
			}
			continue
		}
		if r == '/' && i+1 < len(runes) && runes[i+1] == '*' {
			flush()
			executable := i+2 < len(runes) && runes[i+2] == '!'
			start := i + 2
			if executable {
				start++
			}
			end := -1
			quote := rune(0)
			escaped := false
			for i = start; i+1 < len(runes); i++ {
				current := runes[i]
				if escaped {
					escaped = false
					continue
				}
				if current == '\\' {
					escaped = true
					continue
				}
				if quote != 0 {
					if current == quote {
						quote = 0
					}
					continue
				}
				if current == '\'' || current == '"' || current == '`' {
					quote = current
					continue
				}
				if current == '*' && runes[i+1] == '/' {
					end = i
					break
				}
			}
			if end < 0 {
				return nil, strconv.ErrSyntax
			}
			if executable {
				nested, err := splitSQL(string(runes[start:end]))
				if err != nil {
					return nil, err
				}
				for _, tokens := range nested {
					if len(tokens) > 0 {
						version := true
						for _, r := range tokens[0].text {
							if r < '0' || r > '9' {
								version = false
								break
							}
						}
						if version {
							tokens = tokens[1:]
						}
					}
					current = append(current, tokens...)
				}
			}
			i = end + 1
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			flush()
			quote := r
			closed := false
			for i+1 < len(runes) {
				i++
				if runes[i] == '\\' && quote != '`' && i+1 < len(runes) {
					i++
					continue
				}
				if runes[i] == quote {
					if i+1 < len(runes) && runes[i+1] == quote {
						i++
						continue
					}
					closed = true
					break
				}
			}
			if !closed {
				return nil, strconv.ErrSyntax
			}
			current = append(current, sqlToken{text: "<literal>"})
			continue
		}
		if r == '$' {
			flush()
			tagEnd := i + 1
			for tagEnd < len(runes) && (unicode.IsLetter(runes[tagEnd]) || unicode.IsDigit(runes[tagEnd]) || runes[tagEnd] == '_') {
				tagEnd++
			}
			if tagEnd < len(runes) && runes[tagEnd] == '$' {
				tag := string(runes[i : tagEnd+1])
				remainder := string(runes[tagEnd+1:])
				end := strings.Index(remainder, tag)
				if end < 0 {
					return nil, strconv.ErrSyntax
				}
				consumed := tagEnd + 1 + end + len(tag) - 1
				i = consumed
				current = append(current, sqlToken{text: "<literal>"})
				continue
			}
		}
		if r == ';' {
			flush()
			if len(current) != 0 {
				result = append(result, current)
				current = nil
			}
			continue
		}
		if strings.ContainsRune("(),.*=<>!+-/%", r) {
			flush()
			current = append(current, sqlToken{text: string(r)})
			continue
		}
		word.WriteRune(r)
	}
	flush()
	if len(current) != 0 {
		result = append(result, current)
	}
	return result, nil
}

func isSQLKeyword(value string) bool {
	_, ok := sqlKeywords[value]
	return ok
}

var sqlKeywords = func() map[string]struct{} {
	values := strings.Fields(`SELECT SHOW DESC DESCRIBE VALUES TABLE WITH EXPLAIN ANALYZE INSERT UPDATE DELETE REPLACE MERGE CREATE ALTER DROP TRUNCATE GRANT REVOKE SET USE CALL DO LOAD IMPORT ATTACH DETACH VACUUM OPTIMIZE LOCK UNLOCK BEGIN START COMMIT ROLLBACK SAVEPOINT RELEASE DATABASE SCHEMA OUTFILE DUMPFILE INTO FOR SHARE MODE RENAME AS FROM WHERE JOIN INNER LEFT RIGHT FULL OUTER CROSS ON GROUP ORDER BY HAVING LIMIT OFFSET UNION ALL DISTINCT CASE WHEN THEN ELSE END CAST COLLATE ASC NULL NOT AND OR IS IN BETWEEN LIKE OVER PARTITION WINDOW`)
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}()
