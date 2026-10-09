package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type postgresConnection struct {
	db *sql.DB
}

func (c *postgresConnection) Close() error {
	return c.db.Close()
}

func connectPostgres(ctx context.Context, config PostgresConfig) (*postgresConnection, error) {
	if config.Port == 0 {
		config.Port = 5432
	}
	dsn := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))),
		Path:   "/" + config.Database,
	}
	if config.Username != "" || config.Password != "" {
		dsn.User = url.UserPassword(config.Username, config.Password)
	}
	query := dsn.Query()
	query.Set("sslmode", "disable")
	dsn.RawQuery = query.Encode()

	parsed, err := pgx.ParseConfig(dsn.String())
	if err != nil {
		return nil, badParam(fmt.Errorf("PostgreSQL 配置无效: %w", err))
	}
	parsed.Password = config.Password
	if config.Database == "" {
		parsed.Database = parsed.User
	}
	if config.TLSConfig != nil {
		parsed.TLSConfig = config.TLSConfig.Clone()
	}
	parsed.Fallbacks = nil
	parsed.ConnectTimeout = 10 * time.Second
	parsed.RuntimeParams = map[string]string{}

	database := sql.OpenDB(stdlib.GetConnector(*parsed))
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	database.SetConnMaxIdleTime(5 * time.Minute)
	database.SetConnMaxLifetime(time.Hour)

	pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = database.PingContext(pingContext)
	cancel()
	if err != nil {
		_ = database.Close()
		return nil, internalError("PostgreSQL 连接失败", err)
	}
	return &postgresConnection{db: database}, nil
}

func postgresSchemas(ctx context.Context, connection *postgresConnection) ([]string, error) {
	schemas, err := queryStrings(ctx, connection.db, "SELECT schema_name FROM information_schema.schemata ORDER BY schema_name")
	if err != nil {
		return nil, internalError("读取 PostgreSQL schema 列表", err)
	}
	return schemas, nil
}

func postgresTables(ctx context.Context, connection *postgresConnection, schema string) ([]string, error) {
	schema, err := postgresSchema(ctx, connection, schema)
	if err != nil {
		return nil, err
	}
	tables, err := queryStrings(ctx, connection.db, "SELECT table_name FROM information_schema.tables WHERE table_schema = $1 ORDER BY table_name", schema)
	if err != nil {
		return nil, internalError("读取 PostgreSQL 表列表", err)
	}
	return tables, nil
}

func postgresDescribe(ctx context.Context, connection *postgresConnection, schema, table string) (TableDescribe, error) {
	schema, err := postgresSchema(ctx, connection, schema)
	if err != nil {
		return TableDescribe{}, err
	}
	if table == "" {
		return TableDescribe{}, badParam(errors.New("table 不能为空"))
	}

	primaryKeys, err := queryStrings(ctx, connection.db, `SELECT a.attname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'p' AND n.nspname = $1 AND t.relname = $2`, schema, table)
	if err != nil {
		return TableDescribe{}, internalError("读取 PostgreSQL 主键", err)
	}
	primaryKeySet := make(map[string]struct{}, len(primaryKeys))
	for _, name := range primaryKeys {
		primaryKeySet[name] = struct{}{}
	}

	description := TableDescribe{Columns: []TableColumn{}, Indexes: []TableIndex{}}
	columns, err := connection.db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, column_default, is_identity
		FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return TableDescribe{}, internalError("读取 PostgreSQL 字段列表", err)
	}
	for columns.Next() {
		var column TableColumn
		var nullable string
		var defaultValue sql.NullString
		var identity string
		if err := columns.Scan(&column.Name, &column.Type, &nullable, &defaultValue, &identity); err != nil {
			_ = columns.Close()
			return TableDescribe{}, internalError("读取 PostgreSQL 字段列表", err)
		}
		column.Nullable = nullable == "YES"
		if defaultValue.Valid {
			value := defaultValue.String
			column.Default = &value
		}
		if identity == "YES" {
			column.Extra = "identity"
		}
		if _, ok := primaryKeySet[column.Name]; ok {
			column.Key = "PRI"
		}
		description.Columns = append(description.Columns, column)
	}
	if err := errors.Join(columns.Err(), columns.Close()); err != nil {
		return TableDescribe{}, internalError("读取 PostgreSQL 字段列表", err)
	}

	indexes, err := connection.db.QueryContext(ctx, `SELECT ic.relname, i.indisunique, k.ord, COALESCE(a.attname, '')
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		WHERE n.nspname = $1 AND t.relname = $2
		ORDER BY ic.relname, k.ord`, schema, table)
	if err != nil {
		return TableDescribe{}, internalError("读取 PostgreSQL 索引列表", err)
	}
	for indexes.Next() {
		var index TableIndex
		if err := indexes.Scan(&index.Name, &index.Unique, &index.Seq, &index.Column); err != nil {
			_ = indexes.Close()
			return TableDescribe{}, internalError("读取 PostgreSQL 索引列表", err)
		}
		description.Indexes = append(description.Indexes, index)
	}
	if err := errors.Join(indexes.Err(), indexes.Close()); err != nil {
		return TableDescribe{}, internalError("读取 PostgreSQL 索引列表", err)
	}
	return description, nil
}

func queryPostgres(ctx context.Context, database *sql.DB, statement string, limit uint64, timeout time.Duration) (QueryResult, error) {
	return querySQL(ctx, database, statement, limit, timeout, postgresJSONValue)
}

func postgresSchema(ctx context.Context, connection *postgresConnection, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	var current sql.NullString
	if err := connection.db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&current); err != nil {
		return "", internalError("读取当前 PostgreSQL schema", err)
	}
	if !current.Valid || current.String == "" {
		return "", badParam(errors.New("未选择 schema，请提供 schema"))
	}
	return current.String, nil
}
