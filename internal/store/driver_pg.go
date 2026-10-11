package store

import (
	"context"
	"database/sql/driver"
)

type rebindConnector struct {
	inner driver.Connector
}

func (c *rebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &rebindConn{inner: conn}, nil
}

func (c *rebindConnector) Driver() driver.Driver { return c.inner.Driver() }

type rebindConn struct {
	inner driver.Conn
}

func (c *rebindConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.inner.Prepare(rebindDollarPlaceholders(query))
	if err != nil {
		return nil, err
	}
	return &rebindStmt{inner: stmt}, nil
}

func (c *rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if inner, ok := c.inner.(driver.ConnPrepareContext); ok {
		stmt, err := inner.PrepareContext(ctx, rebindDollarPlaceholders(query))
		if err != nil {
			return nil, err
		}
		return &rebindStmt{inner: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *rebindConn) Close() error { return c.inner.Close() }

func (c *rebindConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if inner, ok := c.inner.(driver.ConnBeginTx); ok {
		return inner.BeginTx(ctx, opts)
	}
	return c.inner.Begin()
}

func (c *rebindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if inner, ok := c.inner.(driver.ExecerContext); ok {
		return inner.ExecContext(ctx, rebindDollarPlaceholders(query), args)
	}
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	return stmt.Exec(namedToValues(args))
}

func (c *rebindConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if inner, ok := c.inner.(driver.QueryerContext); ok {
		return inner.QueryContext(ctx, rebindDollarPlaceholders(query), args)
	}
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	return stmt.Query(namedToValues(args))
}

func (c *rebindConn) Ping(ctx context.Context) error {
	if inner, ok := c.inner.(driver.Pinger); ok {
		return inner.Ping(ctx)
	}
	return nil
}

func (c *rebindConn) CheckNamedValue(value *driver.NamedValue) error {
	if inner, ok := c.inner.(driver.NamedValueChecker); ok {
		return inner.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *rebindConn) ResetSession(ctx context.Context) error {
	if inner, ok := c.inner.(driver.SessionResetter); ok {
		return inner.ResetSession(ctx)
	}
	return nil
}

type rebindStmt struct {
	inner driver.Stmt
}

func (s *rebindStmt) Close() error  { return s.inner.Close() }
func (s *rebindStmt) NumInput() int { return s.inner.NumInput() }

func (s *rebindStmt) Exec(args []driver.Value) (driver.Result, error) { return s.inner.Exec(args) }
func (s *rebindStmt) Query(args []driver.Value) (driver.Rows, error)  { return s.inner.Query(args) }

func (s *rebindStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if inner, ok := s.inner.(driver.StmtExecContext); ok {
		return inner.ExecContext(ctx, args)
	}
	return s.inner.Exec(namedToValues(args))
}

func (s *rebindStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if inner, ok := s.inner.(driver.StmtQueryContext); ok {
		return inner.QueryContext(ctx, args)
	}
	return s.inner.Query(namedToValues(args))
}

func namedToValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}
