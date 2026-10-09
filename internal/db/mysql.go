package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		return TableDescribe{}, badParam(errors.New("table 不能为空"))
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
	return querySQL(ctx, database, statement, limit, timeout, mysqlJSONValue)
}

func querySQL(ctx context.Context, database *sql.DB, statement string, limit uint64, timeout time.Duration, jsonValue func(any, string) any) (result QueryResult, returnedErr error) {
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
	if statementReturnsRows(statement) {
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
	if errors.Is(queryContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return result, ipc.WrapError(ipc.CodeTimeout, fmt.Sprintf("操作超时: SQL 超时（%s）", formatDuration(timeout)), err)
	}
	if errors.Is(queryContext.Err(), context.Canceled) {
		return result, internalError("SQL 执行已取消", queryContext.Err())
	}
	message := err.Error()
	result.Error = &message
	return result, nil
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
		return "", badParam(errors.New("未选择数据库，请提供 schema"))
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

func statementReturnsRows(statement string) bool {
	keyword := firstSQLKeyword(statement)
	if keyword == "WITH" {
		keyword = withMainKeyword(statement)
	}
	switch keyword {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "WITH", "CALL", "VALUES", "TABLE", "HELP", "ANALYZE", "OPTIMIZE", "REPAIR", "CHECK", "CHECKSUM", "(":
		return true
	default:
		return false
	}
}

func withMainKeyword(statement string) string {
	depth := 0
	for i := 0; i < len(statement); {
		switch {
		case i+1 < len(statement) && statement[i:i+2] == "/*":
			end := strings.Index(statement[i+2:], "*/")
			if end < 0 {
				return "WITH"
			}
			i += end + 4
		case statement[i] == '#' || (i+1 < len(statement) && statement[i:i+2] == "--" && (i+2 == len(statement) || unicode.IsSpace(rune(statement[i+2])))):
			end := strings.IndexByte(statement[i:], '\n')
			if end < 0 {
				return "WITH"
			}
			i += end + 1
		case statement[i] == '\'' || statement[i] == '"' || statement[i] == '`':
			quote := statement[i]
			i++
			for i < len(statement) {
				if quote != '`' && statement[i] == '\\' && i+1 < len(statement) {
					i += 2
					continue
				}
				if statement[i] == quote {
					if i+1 < len(statement) && statement[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case statement[i] == '(':
			depth++
			i++
		case statement[i] == ')':
			if depth > 0 {
				depth--
			}
			i++
		case statement[i] == ';' && depth == 0:
			return "WITH"
		case (statement[i] >= 'a' && statement[i] <= 'z') || (statement[i] >= 'A' && statement[i] <= 'Z'):
			start := i
			for i < len(statement) && ((statement[i] >= 'a' && statement[i] <= 'z') || (statement[i] >= 'A' && statement[i] <= 'Z')) {
				i++
			}
			if depth == 0 {
				keyword := strings.ToUpper(statement[start:i])
				if keyword == "SELECT" || keyword == "UPDATE" || keyword == "DELETE" {
					return keyword
				}
			}
		default:
			i++
		}
	}
	return "WITH"
}

func firstSQLKeyword(statement string) string {
	remaining := strings.TrimSpace(strings.TrimPrefix(statement, "\ufeff"))
	for remaining != "" {
		switch {
		case strings.HasPrefix(remaining, "/*"):
			end := strings.Index(remaining[2:], "*/")
			if end < 0 {
				return ""
			}
			remaining = strings.TrimSpace(remaining[end+4:])
		case strings.HasPrefix(remaining, "--") && (len(remaining) == 2 || unicode.IsSpace(rune(remaining[2]))):
			end := strings.IndexByte(remaining, '\n')
			if end < 0 {
				return ""
			}
			remaining = strings.TrimSpace(remaining[end+1:])
		case strings.HasPrefix(remaining, "#"):
			end := strings.IndexByte(remaining, '\n')
			if end < 0 {
				return ""
			}
			remaining = strings.TrimSpace(remaining[end+1:])
		default:
			if remaining[0] == '(' {
				return "("
			}
			end := 0
			for end < len(remaining) && ((remaining[end] >= 'a' && remaining[end] <= 'z') || (remaining[end] >= 'A' && remaining[end] <= 'Z')) {
				end++
			}
			return strings.ToUpper(remaining[:end])
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
