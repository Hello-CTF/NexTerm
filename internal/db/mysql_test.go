package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestMySQLQueryRowsAffected(t *testing.T) {
	state := &fakeSQLState{
		exec: func(context.Context, string) (driver.Result, error) {
			return driver.RowsAffected(7), nil
		},
	}
	result, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "/* leading */ UPDATE items SET enabled = 0", 0, time.Second)
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

func TestMySQLQueryEmptyRowsKeepsColumns(t *testing.T) {
	rows := newFakeRows([]string{"id", "name"}, []string{"INT", "VARCHAR"}, nil)
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
	result, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "SELECT id, name FROM items WHERE 0", 0, time.Second)
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

func TestMySQLQueryLimitAndTruncation(t *testing.T) {
	for _, test := range []struct {
		name          string
		limit         uint64
		total         int
		wantRows      int
		wantTruncated bool
		wantDelivered int
	}{
		{name: "caller limit", limit: 2, total: 4, wantRows: 2, wantTruncated: true, wantDelivered: 3},
		{name: "five thousand cap", limit: 9000, total: MaxRows + 1, wantRows: MaxRows, wantTruncated: true, wantDelivered: MaxRows + 1},
		{name: "exact cap is not truncated", limit: 0, total: MaxRows, wantRows: MaxRows, wantTruncated: false, wantDelivered: MaxRows},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := &fakeRows{
				columns:   []string{"id"},
				typeNames: []string{"INT"},
				total:     test.total,
				values: func(index int) []driver.Value {
					return []driver.Value{[]byte("1")}
				},
			}
			state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
			result, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "SELECT id FROM generated", test.limit, time.Second)
			if err != nil {
				t.Fatalf("query returned error: %v", err)
			}
			if len(result.Rows) != test.wantRows || result.Truncated != test.wantTruncated || rows.delivered != test.wantDelivered || !rows.closed {
				t.Fatalf("rows=%d truncated=%v delivered=%d closed=%v", len(result.Rows), result.Truncated, rows.delivered, rows.closed)
			}
		})
	}
}

func TestMySQLQueryTimeoutShape(t *testing.T) {
	state := &fakeSQLState{query: func(ctx context.Context, _ string) (driver.Rows, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "SELECT SLEEP(10)", 0, 20*time.Millisecond)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeTimeout || !strings.Contains(ipcErr.Message, "SQL 超时") {
		t.Fatalf("wrong timeout error: %#v", err)
	}
}

func TestMySQLQueryServerErrorStaysInDTO(t *testing.T) {
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return nil, errors.New("ERROR 1064 syntax")
	}}
	result, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "SELECT broken", 0, time.Second)
	if err != nil || result.Error == nil || *result.Error != "ERROR 1064 syntax" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestMySQLJSONValuesAreLossless(t *testing.T) {
	columns := []string{"safe", "min", "umax", "decimal", "null", "binary", "text", "double", "invalid", "json"}
	types := []string{"INT", "BIGINT", "UNSIGNED BIGINT", "DECIMAL", "INT", "VARBINARY", "VARCHAR", "DOUBLE", "VARCHAR", "JSON"}
	values := [][]driver.Value{{
		[]byte("42"),
		[]byte("-9223372036854775808"),
		[]byte("18446744073709551615"),
		[]byte("1234567890.012300"),
		nil,
		[]byte{0, 255, 1},
		[]byte("hello"),
		[]byte("1.25"),
		[]byte{255},
		[]byte(`{"a":1}`),
	}}
	rows := newFakeRows(columns, types, values)
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) { return rows, nil }}
	result, err := queryMySQL(context.Background(), newFakeSQLDB(t, state), "SELECT values", 0, time.Second)
	if err != nil {
		t.Fatalf("query returned error: %v", err)
	}
	want := []any{int64(42), "-9223372036854775808", "18446744073709551615", "1234567890.012300", nil, "AP8B", "hello", 1.25, "/w==", `{"a":1}`}
	if !reflect.DeepEqual(result.Rows[0], want) {
		t.Fatalf("got  %#v\nwant %#v", result.Rows[0], want)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("marshal values: %v", err)
	}
}

func TestStatementReturnsRowsSkipsComments(t *testing.T) {
	for statement, want := range map[string]bool{
		" SELECT 1":                                         true,
		"/* hint */ SHOW DATABASES":                         true,
		"-- note\n UPDATE t SET x=1":                        false,
		"# note\nDELETE FROM t":                             false,
		"WITH x AS (SELECT 1) SELECT * FROM x":              true,
		"WITH x AS (SELECT 1) UPDATE items SET enabled = 0": false,
		"WITH RECURSIVE x AS (SELECT 1) DELETE FROM items":  false,
		"WITH x AS (SELECT ')') SELECT * FROM x":            true,
		"INSERT INTO t VALUES (1)":                          false,
	} {
		if got := statementReturnsRows(statement); got != want {
			t.Errorf("statementReturnsRows(%q)=%v want %v", statement, got, want)
		}
	}
}
