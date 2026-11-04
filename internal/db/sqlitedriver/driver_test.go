package sqlitedriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
)

// fakeConn stands in for go-sqlite: its exec runs hook and then reports
// success, the way the wrapped driver does when its watcher skipped the
// statement.
type fakeConn struct {
	execs int
	hook  func()
	err   error
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return &fakeStmt{conn: c}, nil }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *fakeConn) Ping(context.Context) error          { return nil }
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, errors.New("unused")
}
func (c *fakeConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	return c.Prepare(query)
}
func (c *fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return c.run()
}
func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("unused")
}

func (c *fakeConn) run() (driver.Result, error) {
	c.execs++
	if c.hook != nil {
		c.hook()
	}
	if c.err != nil {
		return nil, c.err
	}
	return driver.RowsAffected(1), nil
}

type fakeStmt struct{ conn *fakeConn }

func (s *fakeStmt) Close() error                               { return nil }
func (s *fakeStmt) NumInput() int                              { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return s.conn.run() }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, errors.New("unused") }
func (s *fakeStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	return s.conn.run()
}

type fakeDriver struct{ conn *fakeConn }

func (d fakeDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

func openFake(t *testing.T, inner *fakeConn) *conn {
	t.Helper()
	opened, err := Wrap(fakeDriver{conn: inner}).Open("")
	if err != nil {
		t.Fatal(err)
	}
	return opened.(*conn)
}

func TestExecReportsCancellationInsteadOfSkippedSuccess(t *testing.T) {
	type execFunc func(context.Context, *conn) (driver.Result, error)
	paths := map[string]execFunc{
		"connection": func(ctx context.Context, c *conn) (driver.Result, error) {
			return c.ExecContext(ctx, "INSERT", nil)
		},
		"prepared statement": func(ctx context.Context, c *conn) (driver.Result, error) {
			prepared, err := c.PrepareContext(ctx, "INSERT")
			if err != nil {
				return nil, err
			}
			return prepared.(driver.StmtExecContext).ExecContext(ctx, nil)
		},
	}
	for name, exec := range paths {
		t.Run(name, func(t *testing.T) {
			t.Run("context ended before the call never reaches the driver", func(t *testing.T) {
				inner := &fakeConn{}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := exec(ctx, openFake(t, inner)); !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", err)
				}
				if inner.execs != 0 {
					t.Fatalf("the driver ran %d statements under an ended context", inner.execs)
				}
			})
			t.Run("context ending during the call turns a nil error into the cancellation", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				inner := &fakeConn{hook: cancel}
				result, err := exec(ctx, openFake(t, inner))
				if !errors.Is(err, context.Canceled) || result != nil {
					t.Fatalf("result, err = %v, %v; want nil, context.Canceled", result, err)
				}
			})
			t.Run("a driver error is kept when the context also ended", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := errors.New("disk I/O error")
				inner := &fakeConn{hook: cancel, err: want}
				if _, err := exec(ctx, openFake(t, inner)); !errors.Is(err, want) {
					t.Fatalf("err = %v, want the driver error", err)
				}
			})
			t.Run("a live context passes the result through", func(t *testing.T) {
				result, err := exec(context.Background(), openFake(t, &fakeConn{}))
				if err != nil {
					t.Fatal(err)
				}
				if rows, _ := result.RowsAffected(); rows != 1 {
					t.Fatalf("rows affected = %d, want 1", rows)
				}
			})
		})
	}
}

// Against the real driver: a statement under an ended context never runs and
// never reports success. Unwrapped, go-sqlite returns nil for some of these
// calls (its watcher wins the race and the exec loop is skipped).
func TestRegisteredDriverNeverReportsASkippedStatementAsSuccess(t *testing.T) {
	db, err := sql.Open(Name, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "probe.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for attempt := range 200 {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := conn.ExecContext(cancelled, "INSERT INTO probe (id) VALUES (?)", attempt); !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt %d: err = %v, want context.Canceled", attempt, err)
		}
	}
	var rows int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM probe").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d cancelled inserts were written", rows)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO probe (id) VALUES (1)"); err != nil {
		t.Fatalf("a live insert after the cancelled ones: %v", err)
	}
}
