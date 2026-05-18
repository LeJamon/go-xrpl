package sqlite

import (
	"context"
	"database/sql"
)

// executor interface allows using both sql.DB and sql.Tx
type executor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
