package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeSQLState struct {
	query func(context.Context, string) (driver.Rows, error)
	exec  func(context.Context, string) (driver.Result, error)

	mu      sync.Mutex
	queries []string
	execs   []string
}

func (s *fakeSQLState) recordQuery(statement string) {
	s.mu.Lock()
	s.queries = append(s.queries, statement)
	s.mu.Unlock()
}

func (s *fakeSQLState) recordExec(statement string) {
	s.mu.Lock()
	s.execs = append(s.execs, statement)
	s.mu.Unlock()
}

type fakeSQLDriver struct {
	state *fakeSQLState
}

func (d *fakeSQLDriver) Open(string) (driver.Conn, error) {
	return &fakeSQLConn{state: d.state}, nil
}

type fakeSQLConn struct {
	state *fakeSQLState
}

func (c *fakeSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by the fake driver")
}

func (c *fakeSQLConn) Close() error {
	return nil
}

func (c *fakeSQLConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by the fake driver")
}

func (c *fakeSQLConn) Ping(context.Context) error {
	return nil
}

func (c *fakeSQLConn) QueryContext(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.recordQuery(statement)
	if c.state.query == nil {
		return nil, errors.New("unexpected query: " + statement)
	}
	return c.state.query(ctx, statement)
}

func (c *fakeSQLConn) ExecContext(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.recordExec(statement)
	if c.state.exec == nil {
		return nil, errors.New("unexpected exec: " + statement)
	}
	return c.state.exec(ctx, statement)
}

var fakeSQLDriverSequence atomic.Uint64

func newFakeSQLDB(t *testing.T, state *fakeSQLState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("nexterm-db-fake-%d", fakeSQLDriverSequence.Add(1))
	sql.Register(name, &fakeSQLDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open fake database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

type fakeRows struct {
	columns   []string
	typeNames []string
	values    func(int) []driver.Value
	total     int
	index     int
	delivered int
	closed    bool
}

func newFakeRows(columns []string, typeNames []string, values [][]driver.Value) *fakeRows {
	return &fakeRows{
		columns:   columns,
		typeNames: typeNames,
		total:     len(values),
		values: func(index int) []driver.Value {
			return values[index]
		},
	}
}

func (r *fakeRows) Columns() []string {
	return r.columns
}

func (r *fakeRows) Close() error {
	r.closed = true
	return nil
}

func (r *fakeRows) Next(destination []driver.Value) error {
	if r.index >= r.total {
		return io.EOF
	}
	copy(destination, r.values(r.index))
	r.index++
	r.delivered++
	return nil
}

func (r *fakeRows) ColumnTypeDatabaseTypeName(index int) string {
	if index >= len(r.typeNames) {
		return ""
	}
	return r.typeNames[index]
}
