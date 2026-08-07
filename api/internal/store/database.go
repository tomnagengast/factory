package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// database and transaction keep PostgreSQL placeholder binding in one place.
// Factory's queries stay readable with '?' placeholders while pgx receives
// PostgreSQL's numbered form.
type database struct {
	inner *sql.DB
}

func (d *database) Exec(query string, args ...any) (sql.Result, error) {
	return d.inner.Exec(rebind(query), args...)
}

func (d *database) Query(query string, args ...any) (*sql.Rows, error) {
	return d.inner.Query(rebind(query), args...)
}

func (d *database) QueryRow(query string, args ...any) *sql.Row {
	return d.inner.QueryRow(rebind(query), args...)
}

func (d *database) Begin() (*transaction, error) {
	return d.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

func (d *database) BeginTx(ctx context.Context, options *sql.TxOptions) (*transaction, error) {
	tx, err := d.inner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &transaction{inner: tx}, nil
}

func (d *database) Close() error { return d.inner.Close() }

type transaction struct {
	inner *sql.Tx
}

func (t *transaction) Exec(query string, args ...any) (sql.Result, error) {
	return t.inner.Exec(rebind(query), args...)
}

func (t *transaction) Query(query string, args ...any) (*sql.Rows, error) {
	return t.inner.Query(rebind(query), args...)
}

func (t *transaction) QueryRow(query string, args ...any) *sql.Row {
	return t.inner.QueryRow(rebind(query), args...)
}

func (t *transaction) Commit() error   { return t.inner.Commit() }
func (t *transaction) Rollback() error { return t.inner.Rollback() }

func rebind(query string) string { return sqlx.Rebind(sqlx.DOLLAR, query) }
