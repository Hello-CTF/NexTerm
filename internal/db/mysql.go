package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

type mysqlConnection struct {
	db       *sql.DB
	database string
}

func (c *mysqlConnection) Close() error {
	return c.db.Close()
}

func connectMySQL(ctx context.Context, config MySQLConfig) (*mysqlConnection, error) {
	if config.Port == 0 {
		config.Port = 3306
	}
	driverConfig := mysqlDriver.NewConfig()
	driverConfig.User = config.Username
	driverConfig.Passwd = config.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port)))
	driverConfig.DBName = config.Database
	driverConfig.Timeout = 10 * time.Second
	driverConfig.MultiStatements = false
	driverConfig.ParseTime = false
	driverConfig.InterpolateParams = false
	driverConfig.DialFunc = config.DialContext
	if config.TLSConfig != nil {
		driverConfig.TLS = config.TLSConfig.Clone()
	}

	connector, err := mysqlDriver.NewConnector(driverConfig)
	if err != nil {
		return nil, badParam(fmt.Errorf("MySQL 配置无效: %w", err))
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	database.SetConnMaxIdleTime(5 * time.Minute)
	database.SetConnMaxLifetime(time.Hour)

	pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = database.PingContext(pingContext)
	cancel()
	if err != nil {
		_ = database.Close()
		return nil, internalError("MySQL 连接失败", err)
	}
	return &mysqlConnection{db: database, database: config.Database}, nil
}

func mysqlSchemas(ctx context.Context, connection *mysqlConnection) ([]string, error) {
	schemas, err := queryStrings(ctx, connection.db, "SHOW DATABASES")
	if err != nil {
		return nil, internalError("读取 MySQL 数据库列表", err)
	}
	return schemas, nil
}

func mysqlTables(ctx context.Context, connection *mysqlConnection, schema string) ([]string, error) {
	schema, err := mysqlSchema(ctx, connection, schema)
	if err != nil {
		return nil, err
	}
	tables, err := queryStrings(ctx, connection.db, "SELECT table_name FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name", schema)
	if err != nil {
		return nil, internalError("读取 MySQL 表列表", err)
	}
	return tables, nil
}

func mysqlDescribe(ctx context.Context, connection *mysqlConnection, schema, table string) (TableDescribe, error) {
	schema, err := mysqlSchema(ctx, connection, schema)
	if err != nil {
		return TableDescribe{}, err
	}
	if table == "" {
		return TableDescribe{}, badParam(errors.New("表名不能为空"))
	}

	description := TableDescribe{Columns: []TableColumn{}, Indexes: []TableIndex{}}
	columns, err := connection.db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, column_key, column_default, extra
		FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return TableDescribe{}, internalError("读取 MySQL 字段列表", err)
	}
	for columns.Next() {
		var column TableColumn
		var nullable string
		var defaultValue sql.NullString
		if err := columns.Scan(&column.Name, &column.Type, &nullable, &column.Key, &defaultValue, &column.Extra); err != nil {
			_ = columns.Close()
			return TableDescribe{}, internalError("读取 MySQL 字段列表", err)
		}
		column.Nullable = nullable == "YES"
		if defaultValue.Valid {
			value := defaultValue.String
			column.Default = &value
		}
		description.Columns = append(description.Columns, column)
	}
	if err := errors.Join(columns.Err(), columns.Close()); err != nil {
		return TableDescribe{}, internalError("读取 MySQL 字段列表", err)
	}

	indexes, err := connection.db.QueryContext(ctx, `SELECT index_name, non_unique, seq_in_index, column_name
		FROM information_schema.statistics WHERE table_schema = ? AND table_name = ? ORDER BY index_name, seq_in_index`, schema, table)
	if err != nil {
		return TableDescribe{}, internalError("读取 MySQL 索引列表", err)
	}
	for indexes.Next() {
		var index TableIndex
		var nonUnique int64
		var columnName sql.NullString
		if err := indexes.Scan(&index.Name, &nonUnique, &index.Seq, &columnName); err != nil {
			_ = indexes.Close()
			return TableDescribe{}, internalError("读取 MySQL 索引列表", err)
		}
		index.Unique = nonUnique == 0
		index.Column = columnName.String
		description.Indexes = append(description.Indexes, index)
	}
	if err := errors.Join(indexes.Err(), indexes.Close()); err != nil {
		return TableDescribe{}, internalError("读取 MySQL 索引列表", err)
	}
	return description, nil
}

func queryMySQL(ctx context.Context, database *sql.DB, statement string, limit uint64, timeout time.Duration) (QueryResult, error) {
	return querySQL(ctx, database, statement, sqlDialectMySQL, limit, timeout, mysqlJSONValue)
}

func querySQL(ctx context.Context, database *sql.DB, statement string, dialect sqlDialect, limit uint64, timeout time.Duration, jsonValue func(any, string) any) (result QueryResult, returnedErr error) {
	started := time.Now()
	result.Columns = []string{}
	result.Rows = [][]any{}
	defer func() {
		result.DurationMS = uint64(time.Since(started).Milliseconds())
	}()
	if timeout <= 0 {
		timeout = DefaultQueryTimeoutMS * time.Millisecond
	}
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	if statementReturnsRows(statement, dialect) {
		err = readSQLRows(queryContext, database, statement, normalizeRowLimit(limit), &result, jsonValue)
	} else {
		var execution sql.Result
		execution, err = database.ExecContext(queryContext, statement)
		if err == nil {
			var affected int64
			affected, err = execution.RowsAffected()
			if affected > 0 {
				result.RowsAffected = uint64(affected)
			}
		}
	}
	if err == nil {
		return result, nil
	}
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		return result, ipcErr
	}
	if errors.Is(queryContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return result, ipc.WrapError(ipc.CodeTimeout, fmt.Sprintf("SQL 执行超时（%s）", formatDuration(timeout)), err)
	}
	if errors.Is(queryContext.Err(), context.Canceled) {
		return result, internalError("SQL 执行已取消", queryContext.Err())
	}
	if isSQLTransportError(err) {
		return result, ipc.WrapError(ipc.CodeDisconnected, fmt.Sprintf("SQL 执行时数据库连接已断开: %v", err), err)
	}
	message := err.Error()
	result.Error = &message
	return result, nil
}

func isSQLTransportError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, mysqlDriver.ErrInvalidConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrShortWrite) ||
		errors.Is(err, net.ErrClosed) ||
		errors.As(err, &netErr)
}

func readSQLRows(ctx context.Context, database *sql.DB, statement string, limit uint64, result *QueryResult, jsonValue func(any, string) any) error {
	rows, err := database.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()

	result.Columns, err = rows.Columns()
	if err != nil {
		return err
	}
	if result.Columns == nil {
		result.Columns = []string{}
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	typeNames := make([]string, len(columnTypes))
	for i, columnType := range columnTypes {
		typeNames[i] = columnType.DatabaseTypeName()
	}

	for rows.Next() {
		if uint64(len(result.Rows)) >= limit {
			result.Truncated = true
			break
		}
		values := make([]any, len(result.Columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		row := make([]any, len(values))
		for i, value := range values {
			row[i] = jsonValue(value, typeNames[i])
		}
		result.Rows = append(result.Rows, row)
	}
	return errors.Join(rows.Err(), rows.Close())
}

func normalizeRowLimit(limit uint64) uint64 {
	if limit == 0 || limit > MaxRows {
		return MaxRows
	}
	return limit
}

func mysqlSchema(ctx context.Context, connection *mysqlConnection, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	if connection.database != "" {
		return connection.database, nil
	}
	var current sql.NullString
	if err := connection.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err != nil {
		return "", internalError("读取当前 MySQL 数据库", err)
	}
	if !current.Valid || current.String == "" {
		return "", badParam(errors.New("未选择数据库，请先选择 schema"))
	}
	return current.String, nil
}

func queryStrings(ctx context.Context, database *sql.DB, statement string, args ...any) ([]string, error) {
	rows, err := database.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return values, nil
}

type sqlDialect int

const (
	sqlDialectMySQL sqlDialect = iota
	sqlDialectPostgres
)

func statementReturnsRows(statement string, dialect sqlDialect) bool {
	keyword := firstSQLKeyword(statement, dialect)
	if keyword == "WITH" {
		keyword = withMainKeyword(statement, dialect)
	}
	switch keyword {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "WITH", "CALL", "VALUES", "TABLE", "HELP", "ANALYZE", "OPTIMIZE", "REPAIR", "CHECK", "CHECKSUM", "(":
		return true
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
		return hasTopLevelReturning(statement, dialect)
	default:
		return false
	}
}

// withMainKeyword returns the statement keyword that follows the CTE list,
// or "WITH" when it cannot be determined.
func withMainKeyword(statement string, dialect sqlDialect) string {
	keyword := "WITH"
	scanTopLevelSQLWords(statement, dialect, func(word string) bool {
		switch upper := strings.ToUpper(word); upper {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE", "VALUES", "TABLE":
			keyword = upper
			return false
		}
		return true
	})
	return keyword
}

// hasTopLevelReturning reports whether the first statement carries a
// RETURNING clause (PostgreSQL, MariaDB), which makes a DML statement
// return rows.
func hasTopLevelReturning(statement string, dialect sqlDialect) bool {
	found := false
	scanTopLevelSQLWords(statement, dialect, func(word string) bool {
		found = strings.EqualFold(word, "RETURNING")
		return !found
	})
	return found
}

func isSQLWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// scanTopLevelSQLWords calls visit for every bare word outside quotes,
// comments and parentheses of the first statement. Scanning stops at a
// top-level ';', at an unterminated quote or comment, or when visit
// returns false.
func scanTopLevelSQLWords(statement string, dialect sqlDialect, visit func(string) bool) {
	postgres := dialect == sqlDialectPostgres
	depth := 0
	for i := 0; i < len(statement); {
		c := statement[i]
		switch {
		case sqlCommentStart(statement, i, dialect):
			next, ok := skipSQLComment(statement, i, dialect)
			if !ok {
				return
			}
			i = next
		case c == '\'' || c == '"' || (!postgres && c == '`'):
			next, ok := skipSQLQuoted(statement, i, !postgres && c != '`')
			if !ok {
				return
			}
			i = next
		case postgres && c == '$':
			tagEnd := i + 1
			for tagEnd < len(statement) && isSQLWordByte(statement[tagEnd]) && statement[tagEnd] != '$' {
				tagEnd++
			}
			if tagEnd >= len(statement) || statement[tagEnd] != '$' || (tagEnd > i+1 && statement[i+1] >= '0' && statement[i+1] <= '9') {
				i++
				continue
			}
			tag := statement[i : tagEnd+1]
			end := strings.Index(statement[tagEnd+1:], tag)
			if end < 0 {
				return
			}
			i = tagEnd + 1 + end + len(tag)
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == ';' && depth == 0:
			return
		case isSQLWordByte(c):
			start := i
			for i < len(statement) && isSQLWordByte(statement[i]) {
				i++
			}
			word := statement[start:i]
			if postgres && (word == "E" || word == "e") && i < len(statement) && statement[i] == '\'' {
				next, ok := skipSQLQuoted(statement, i, true)
				if !ok {
					return
				}
				i = next
				continue
			}
			if depth == 0 && !visit(word) {
				return
			}
		default:
			i++
		}
	}
}

// skipSQLQuoted returns the index after the quoted literal or identifier
// starting at start. A doubled quote is an escaped quote; backslash escapes
// apply only when backslashEscapes is set.
func skipSQLQuoted(statement string, start int, backslashEscapes bool) (int, bool) {
	quote := statement[start]
	for i := start + 1; i < len(statement); {
		if backslashEscapes && statement[i] == '\\' && i+1 < len(statement) {
			i += 2
			continue
		}
		if statement[i] == quote {
			if i+1 < len(statement) && statement[i+1] == quote {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return len(statement), false
}

// sqlCommentStart reports whether a comment opens at i under the dialect's
// rules: MySQL needs whitespace after "--" and accepts "#".
func sqlCommentStart(statement string, i int, dialect sqlDialect) bool {
	rest := statement[i:]
	if strings.HasPrefix(rest, "/*") {
		return true
	}
	if dialect == sqlDialectPostgres {
		return strings.HasPrefix(rest, "--")
	}
	return rest[0] == '#' || (strings.HasPrefix(rest, "--") && (len(rest) == 2 || unicode.IsSpace(rune(rest[2]))))
}

// skipSQLComment returns the index after the comment that opens at i.
// PostgreSQL block comments nest. ok is false when the comment does not end.
func skipSQLComment(statement string, i int, dialect sqlDialect) (int, bool) {
	if !strings.HasPrefix(statement[i:], "/*") {
		end := strings.IndexByte(statement[i:], '\n')
		if end < 0 {
			return len(statement), false
		}
		return i + end + 1, true
	}
	if dialect != sqlDialectPostgres {
		end := strings.Index(statement[i+2:], "*/")
		if end < 0 {
			return len(statement), false
		}
		return i + end + 4, true
	}
	nested := 0
	for i < len(statement) {
		switch {
		case strings.HasPrefix(statement[i:], "/*"):
			nested++
			i += 2
		case strings.HasPrefix(statement[i:], "*/"):
			nested--
			i += 2
			if nested == 0 {
				return i, true
			}
		default:
			i++
		}
	}
	return len(statement), false
}

func firstSQLKeyword(statement string, dialect sqlDialect) string {
	remaining := strings.TrimPrefix(statement, "\ufeff")
	for i := 0; i < len(remaining); {
		switch {
		case unicode.IsSpace(rune(remaining[i])):
			i++
		case sqlCommentStart(remaining, i, dialect):
			next, ok := skipSQLComment(remaining, i, dialect)
			if !ok {
				return ""
			}
			i = next
		case remaining[i] == '(':
			return "("
		default:
			end := i
			for end < len(remaining) && ((remaining[end] >= 'a' && remaining[end] <= 'z') || (remaining[end] >= 'A' && remaining[end] <= 'Z')) {
				end++
			}
			return strings.ToUpper(remaining[i:end])
		}
	}
	return ""
}

func formatDuration(duration time.Duration) string {
	if duration%time.Second == 0 {
		return strconv.FormatInt(int64(duration/time.Second), 10) + "s"
	}
	return duration.String()
}
