// Package iotdbsql provides a deliberately small database/sql bridge over a
// shared official IoTDB client pool.
package iotdbsql

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/HY-805/iotdb-gorm/internal/backend"
)

// ErrTransactionsUnsupported reports relational transaction usage against IoTDB.
var ErrTransactionsUnsupported = errors.New("iotdb: transactions are not supported; configure GORM SkipDefaultTransaction=true")

// ErrPreparedStatementsUnsupported reports prepared-statement usage.
var ErrPreparedStatementsUnsupported = errors.New("iotdb: prepared statements are not supported by this adapter")

// Connector creates lightweight database/sql connections sharing one backend pool.
type Connector struct {
	backend backend.Backend
	binder  Binder
}

// NewConnector creates a bridge connector around an already-owned backend.
func NewConnector(runtime backend.Backend, binder Binder) *Connector {
	return &Connector{backend: runtime, binder: binder}
}

// Connect returns a logical connection; it never allocates another IoTDB pool.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return &conn{backend: c.backend, binder: c.binder}, nil
	}
}

// Driver returns the connector's non-DSN driver facade.
func (c *Connector) Driver() driver.Driver {
	return connectorDriver{}
}

// connectorDriver exists only to satisfy database/sql's Connector contract.
type connectorDriver struct{}

// Open rejects legacy DSN opening because a shared backend is required.
func (connectorDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("iotdb: use iotdbsql.NewConnector with sql.OpenDB")
}

// conn is a stateless logical database/sql connection.
type conn struct {
	backend backend.Backend
	binder  Binder
}

// Prepare returns an explicit unsupported error instead of emulating a statement.
func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, ErrPreparedStatementsUnsupported
}

// PrepareContext returns an explicit unsupported error instead of emulating a statement.
func (c *conn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	return nil, ErrPreparedStatementsUnsupported
}

// Close releases only the logical connection; backend ownership stays with Dialector.
func (c *conn) Close() error {
	return nil
}

// Begin rejects transaction emulation.
func (c *conn) Begin() (driver.Tx, error) {
	return nil, ErrTransactionsUnsupported
}

// BeginTx rejects transaction emulation.
func (c *conn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, ErrTransactionsUnsupported
}

// Ping delegates a real server round trip to the official client.
func (c *conn) Ping(ctx context.Context) error {
	return c.backend.Ping(ctx)
}

// ExecContext binds values and delegates a non-query statement.
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	bound, err := c.binder.Bind(query, args)
	if err != nil {
		return nil, err
	}
	if err := c.backend.Exec(ctx, bound); err != nil {
		return nil, err
	}
	return driver.RowsAffected(0), nil
}

// QueryContext binds values and returns a result set backed by an official session.
func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	bound, err := c.binder.Bind(query, args)
	if err != nil {
		return nil, err
	}
	result, err := c.backend.Query(ctx, bound)
	if err != nil {
		return nil, err
	}
	return newRows(ctx, result), nil
}

// CheckNamedValue lets the binder validate the concrete value after GORM expansion.
func (c *conn) CheckNamedValue(*driver.NamedValue) error {
	return nil
}

var _ driver.Connector = (*Connector)(nil)
var _ driver.Conn = (*conn)(nil)
var _ driver.ConnPrepareContext = (*conn)(nil)
var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.Pinger = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.QueryerContext = (*conn)(nil)
var _ driver.NamedValueChecker = (*conn)(nil)
