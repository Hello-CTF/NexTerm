package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestInlineConnectPostgresAndDispatch(t *testing.T) {
	service := NewService(nil)
	service.newID = func() (string, error) { return "postgres-id", nil }
	state := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
		switch {
		case strings.Contains(statement, "information_schema.schemata"):
			return newFakeRows([]string{"schema_name"}, nil, [][]driver.Value{{"public"}}), nil
		default:
			return nil, errors.New("unexpected metadata query")
		}
	}}
	database := newFakeSQLDB(t, state)
	var captured PostgresConfig
	service.openPostgres = func(_ context.Context, config PostgresConfig) (*postgresConnection, error) {
		captured = config
		return &postgresConnection{db: database}, nil
	}
	result, err := service.Connect(context.Background(), ConnectArgs{Inline: &InlineConnection{
		Kind: "postgres", Host: "pg.internal", Username: "app", Password: "secret", Database: "shop",
		TLS: &TLSOptions{Mode: "skip-verify"},
	}})
	if err != nil || result.ConnID != "postgres-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if captured.Host != "pg.internal" || captured.Port != 5432 || captured.Username != "app" || captured.Password != "secret" || captured.Database != "shop" || captured.TLSConfig == nil || !captured.TLSConfig.InsecureSkipVerify {
		t.Fatalf("captured config: %+v", captured)
	}

	schemas, err := service.Schemas(context.Background(), result.ConnID)
	if err != nil || !reflect.DeepEqual(schemas, []string{"public"}) {
		t.Fatalf("schemas=%v err=%v", schemas, err)
	}
	if len(state.queries) != 1 || !strings.Contains(state.queries[0], "information_schema.schemata") {
		t.Fatalf("queries=%v", state.queries)
	}
	if _, err := service.mysqlOf(result.ConnID); err == nil {
		t.Fatal("mysqlOf accepted a postgres connection")
	} else {
		var ipcErr *ipc.Error
		if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeBadParam {
			t.Fatalf("mysqlOf error=%#v", err)
		}
	}
	if _, err := service.redisOf(result.ConnID); err == nil {
		t.Fatal("redisOf accepted a postgres connection")
	}
	connection, err := service.postgresOf(result.ConnID)
	if err != nil || connection.db != database {
		t.Fatalf("postgresOf=%+v err=%v", connection, err)
	}
	if err := service.Disconnect(result.ConnID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
}

func TestAssetConnectPostgresUsesResolvedCredentialAndOptions(t *testing.T) {
	host, username := "pg.internal", "app"
	port := 5433
	resolver := AssetResolverFunc(func(_ context.Context, id string) (Asset, error) {
		return Asset{
			Kind: "postgres", Host: &host, Port: &port, Username: &username, Password: "decrypted",
			OptionsJSON: `{"database":"shop","tls":{"mode":"required"}}`,
		}, nil
	})
	service := NewService(resolver)
	service.newID = func() (string, error) { return "pg-id", nil }
	var captured PostgresConfig
	service.openPostgres = func(_ context.Context, config PostgresConfig) (*postgresConnection, error) {
		captured = config
		return &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{})}, nil
	}
	if _, err := service.Connect(context.Background(), ConnectArgs{AssetID: "asset-pg"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if captured.Host != host || captured.Port != 5433 || captured.Username != username || captured.Password != "decrypted" || captured.Database != "shop" || captured.TLSConfig == nil || captured.TLSConfig.InsecureSkipVerify {
		t.Fatalf("captured config: %+v", captured)
	}
}

func TestPostgresQueryRowsAffected(t *testing.T) {
	state := &fakeSQLState{
		exec: func(context.Context, string) (driver.Result, error) {
			return driver.RowsAffected(7), nil
		},
	}
	result, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "/* leading */ UPDATE items SET enabled = 0", 0, time.Second)
	if err != nil {
		t.Fatalf("query returned error: %v", err)
	}
	if result.RowsAffected != 7 || result.Error != nil {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Columns) != 0 || len(result.Rows) != 0 {
		t.Fatalf("exec returned row data: %+v", result)
	}
	if len(state.execs) != 1 || len(state.queries) != 0 {
		t.Fatalf("wrong database/sql path: exec=%v query=%v", state.execs, state.queries)
	}
}

func TestPostgresQueryEmptyRowsKeepsColumns(t *testing.T) {
	rows := newFakeRows([]string{"id", "name"}, []string{"INT8", "TEXT"}, nil)
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
	result, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "SELECT id, name FROM items WHERE 0", 0, time.Second)
	if err != nil {
		t.Fatalf("query returned error: %v", err)
	}
	if !reflect.DeepEqual(result.Columns, []string{"id", "name"}) || len(result.Rows) != 0 || result.Truncated {
		t.Fatalf("unexpected result: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(encoded), `"rows":[]`) || !strings.Contains(string(encoded), `"error":null`) {
		t.Fatalf("wrong DTO arrays/null: %s", encoded)
	}
}

func TestPostgresQueryLimitAndTruncation(t *testing.T) {
	rows := &fakeRows{
		columns:   []string{"id"},
		typeNames: []string{"INT8"},
		total:     MaxRows + 1,
		values: func(index int) []driver.Value {
			return []driver.Value{int64(1)}
		},
	}
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
	result, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "SELECT id FROM generated", 0, time.Second)
	if err != nil {
		t.Fatalf("query returned error: %v", err)
	}
	if len(result.Rows) != MaxRows || !result.Truncated || rows.delivered != MaxRows+1 || !rows.closed {
		t.Fatalf("rows=%d truncated=%v delivered=%d closed=%v", len(result.Rows), result.Truncated, rows.delivered, rows.closed)
	}
}

func TestPostgresQueryTimeoutShape(t *testing.T) {
	state := &fakeSQLState{query: func(ctx context.Context, _ string) (driver.Rows, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "SELECT pg_sleep(10)", 0, 20*time.Millisecond)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeTimeout || !strings.Contains(ipcErr.Message, "SQL 超时") {
		t.Fatalf("wrong timeout error: %#v", err)
	}
}

func TestPostgresQueryServerErrorStaysInDTO(t *testing.T) {
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return nil, errors.New(`ERROR: syntax error at or near "broken"`)
	}}
	result, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "SELECT broken", 0, time.Second)
	if err != nil || result.Error == nil || *result.Error != `ERROR: syntax error at or near "broken"` {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPostgresJSONValuesAreLossless(t *testing.T) {
	columns := []string{"safe", "min", "numeric", "binary", "text", "double", "nan", "flag", "ts", "json", "invalid"}
	types := []string{"INT8", "INT8", "NUMERIC", "BYTEA", "TEXT", "FLOAT8", "FLOAT8", "BOOL", "TIMESTAMPTZ", "JSONB", "TEXT"}
	values := [][]driver.Value{{
		int64(42),
		int64(math.MinInt64),
		"1234567890.012300",
		[]byte{0, 255, 1},
		"hello",
		1.25,
		math.NaN(),
		true,
		time.Date(2026, 10, 2, 15, 4, 5, 123456789, time.UTC),
		[]byte(`{"a":1}`),
		[]byte{255},
	}}
	rows := newFakeRows(columns, types, values)
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
	result, err := queryPostgres(context.Background(), newFakeSQLDB(t, state), "SELECT values", 0, time.Second)
	if err != nil {
		t.Fatalf("query returned error: %v", err)
	}
	want := []any{int64(42), "-9223372036854775808", "1234567890.012300", "AP8B", "hello", 1.25, "NaN", true, "2026-10-02 15:04:05.123456789Z", `{"a":1}`, "/w=="}
	if !reflect.DeepEqual(result.Rows[0], want) {
		t.Fatalf("got  %#v\nwant %#v", result.Rows[0], want)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("marshal values: %v", err)
	}
}

func TestPostgresDescribeMarksPrimaryKeyAndIdentity(t *testing.T) {
	state := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
		switch {
		case strings.Contains(statement, "pg_constraint"):
			return newFakeRows([]string{"attname"}, nil, [][]driver.Value{{"id"}}), nil
		case strings.Contains(statement, "information_schema.columns"):
			return newFakeRows(
				[]string{"column_name", "data_type", "is_nullable", "column_default", "is_identity"},
				nil,
				[][]driver.Value{{"id", "integer", "NO", nil, "NO"}, {"code", "text", "YES", nil, "YES"}},
			), nil
		case strings.Contains(statement, "pg_index"):
			return newFakeRows(
				[]string{"relname", "indisunique", "ord", "attname"},
				nil,
				[][]driver.Value{{"items_pkey", true, int64(1), "id"}, {"idx_lower_code", false, int64(1), ""}},
			), nil
		default:
			return nil, errors.New("unexpected metadata query")
		}
	}}
	service := NewService(nil)
	service.conns["postgres"] = &postgresConnection{db: newFakeSQLDB(t, state)}
	description, err := service.Describe(context.Background(), "postgres", "public", "items")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if len(description.Columns) != 2 || description.Columns[0].Key != "PRI" || description.Columns[0].Extra != "" || description.Columns[1].Key != "" || description.Columns[1].Extra != "identity" {
		t.Fatalf("description=%+v", description)
	}
	if len(description.Indexes) != 2 || !description.Indexes[0].Unique || description.Indexes[0].Column != "id" || description.Indexes[1].Name != "idx_lower_code" || description.Indexes[1].Column != "" {
		t.Fatalf("description=%+v", description)
	}
	encoded, err := json.Marshal(description.Indexes[1])
	if err != nil || !strings.Contains(string(encoded), `"column":""`) {
		t.Fatalf("expression index DTO=%s err=%v", encoded, err)
	}
}

func TestPostgresSchemaResolution(t *testing.T) {
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return newFakeRows([]string{"current_schema"}, nil, [][]driver.Value{{"public"}}), nil
	}}
	connection := &postgresConnection{db: newFakeSQLDB(t, state)}
	schema, err := postgresSchema(context.Background(), connection, "")
	if err != nil || schema != "public" {
		t.Fatalf("schema=%q err=%v", schema, err)
	}

	empty := &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return newFakeRows([]string{"current_schema"}, nil, [][]driver.Value{{nil}}), nil
	}})}
	_, err = postgresSchema(context.Background(), empty, "")
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeBadParam || !strings.Contains(ipcErr.Message, "未选择 schema") {
		t.Fatalf("empty schema error=%#v", err)
	}
}
