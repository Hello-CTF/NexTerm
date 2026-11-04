// Package sqlitedriver registers the SQLite driver the application opens. It
// wraps github.com/glebarez/go-sqlite so a statement executed under a
// cancelled context reports the cancellation instead of success.
//
// The wrapped driver's exec loop stops as soon as its context watcher marks
// the call done, and then returns a nil error: a statement whose context is
// cancelled just before or while the call starts is skipped without being run,
// yet reports success. modernc.org/sqlite fixed this upstream by checking the
// context before the call and reporting ctx.Err() once the watcher fired; this
// wrapper restores that contract on top of the fork GORM's dialector uses.
package sqlitedriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	// Registers the "sqlite" driver this package wraps.
	_ "github.com/glebarez/go-sqlite"
)

// Name is the database/sql driver name to open SQLite with.
const Name = "nexterm-sqlite"

// goSQLiteName is the name go-sqlite registers itself under.
const goSQLiteName = "sqlite"

func init() {
	// The registered go-sqlite driver, not a new one: functions registered
	// with go-sqlite live on that instance. sql.Open does not connect.
	registered, err := sql.Open(goSQLiteName, "")
	if err != nil {
		panic(err)
	}
	base := registered.Driver()
	_ = registered.Close()
	sql.Register(Name, Wrap(base))
}

// Driver wraps a SQLite driver; see the package comment.
type Driver struct {
	base driver.Driver
}

// Wrap returns base with the cancellation contract applied. base's
// connections must implement the context-aware exec, query, prepare and
// begin interfaces, as go-sqlite's do.
func Wrap(base driver.Driver) *Driver {
	return &Driver{base: base}
}

// Open implements driver.Driver.
func (d *Driver) Open(name string) (driver.Conn, error) {
	raw, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	inner, ok := raw.(innerConn)
	if !ok {
		_ = raw.Close()
		return nil, fmt.Errorf("sqlitedriver: %T lacks the context-aware connection interfaces", raw)
	}
	return &conn{inner: inner}, nil
}

type innerConn interface {
	driver.Conn
	driver.Pinger
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
}

type conn struct {
	inner innerConn
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	raw, err := c.inner.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return wrapStmt(raw), nil
}

func (c *conn) Close() error { return c.inner.Close() }

//nolint:staticcheck // driver.Conn requires Begin; database/sql calls BeginTx.
func (c *conn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.inner.BeginTx(ctx, opts)
}

func (c *conn) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return execChecked(ctx, func() (driver.Result, error) { return c.inner.ExecContext(ctx, query, args) })
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.inner.QueryContext(ctx, query, args)
}

// execChecked runs an exec that may skip its statements without an error
// once ctx ends. A context already ended never reaches the driver; one that
// ends during the call turns a nil error into ctx.Err(), since the driver may
// have skipped the statement. A statement that completed just before the
// cancellation is reported as cancelled too, as upstream modernc does: the
// caller learns the outcome is not confirmed rather than a false success.
func execChecked(ctx context.Context, run func() (driver.Result, error)) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := run()
	if err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	return result, err
}

type stmt struct {
	inner driver.Stmt
}

type execStmt struct {
	*stmt
	exec driver.StmtExecContext
}

func wrapStmt(raw driver.Stmt) driver.Stmt {
	base := &stmt{inner: raw}
	if exec, ok := raw.(driver.StmtExecContext); ok {
		if query, ok := raw.(driver.StmtQueryContext); ok {
			return &fullStmt{execStmt: execStmt{stmt: base, exec: exec}, query: query}
		}
		return &execStmt{stmt: base, exec: exec}
	}
	return base
}

func (s *stmt) Close() error  { return s.inner.Close() }
func (s *stmt) NumInput() int { return s.inner.NumInput() }

//nolint:staticcheck // driver.Stmt requires Exec and Query.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) { return s.inner.Exec(args) }

//nolint:staticcheck // driver.Stmt requires Exec and Query.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) { return s.inner.Query(args) }

func (s *execStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return execChecked(ctx, func() (driver.Result, error) { return s.exec.ExecContext(ctx, args) })
}

type fullStmt struct {
	execStmt
	query driver.StmtQueryContext
}

func (s *fullStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.query.QueryContext(ctx, args)
}
