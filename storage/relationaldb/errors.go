package relationaldb

import (
	"errors"
	"fmt"
)

var (
	// ErrMissingHost indicates that a PostgreSQL host was not configured.
	ErrMissingHost = errors.New("database host is required")
	// ErrMissingDatabase indicates that a PostgreSQL database was not configured.
	ErrMissingDatabase = errors.New("database name is required")
	// ErrMissingUsername indicates that a PostgreSQL username was not configured.
	ErrMissingUsername = errors.New("database username is required")
	// ErrInvalidPort indicates that the configured database port is invalid.
	ErrInvalidPort = errors.New("invalid database port")
	// ErrInvalidMaxOpenConns indicates an invalid maximum-open-connection count.
	ErrInvalidMaxOpenConns = errors.New("max open connections must be >= 0")
	// ErrInvalidMaxIdleConns indicates an invalid maximum-idle-connection count.
	ErrInvalidMaxIdleConns = errors.New("max idle connections must be >= 0")
	// ErrMaxIdleExceedsMaxOpen indicates an inconsistent connection pool size.
	ErrMaxIdleExceedsMaxOpen = errors.New("max idle connections cannot exceed max open connections")
	// ErrInvalidTimeout indicates a non-positive connection timeout.
	ErrInvalidTimeout = errors.New("timeout must be positive")
	// ErrInvalidConnMaxLifetime indicates a negative connection lifetime.
	ErrInvalidConnMaxLifetime = errors.New("connection max lifetime must be >= 0")
	// ErrInvalidConnMaxIdleTime indicates a negative connection idle duration.
	ErrInvalidConnMaxIdleTime = errors.New("connection max idle time must be >= 0")

	// ErrDatabaseClosed indicates an operation on a closed repository manager.
	ErrDatabaseClosed = errors.New("database connection is closed")
	// ErrTransactionClosed indicates an operation on a completed transaction.
	ErrTransactionClosed = errors.New("transaction is closed")
	// ErrLedgerNotFound indicates that no ledger matched the requested key.
	ErrLedgerNotFound = errors.New("ledger not found")
	// ErrInvalidData indicates malformed relational data.
	ErrInvalidData = errors.New("invalid relational data")
	// ErrInvalidSchema indicates an unsupported or inconsistent schema version.
	ErrInvalidSchema = errors.New("invalid relational schema")
)

// DatabaseError adds an operation and category to a relational database error.
type DatabaseError struct {
	Operation string
	Message   string
	Cause     error
}

func (e *DatabaseError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Operation, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Operation, e.Message, e.Cause)
}

func (e *DatabaseError) Unwrap() error {
	return e.Cause
}

func newDatabaseError(operation, message string, cause error) *DatabaseError {
	return &DatabaseError{Operation: operation, Message: message, Cause: cause}
}

// NewConfigurationError creates a configuration database error.
func NewConfigurationError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

// NewConnectionError creates a connection database error.
func NewConnectionError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

// NewTransactionError creates a transaction database error.
func NewTransactionError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

// NewDataError creates a malformed-data database error.
func NewDataError(operation, message string, cause error) *DatabaseError {
	if cause == nil {
		cause = ErrInvalidData
	}
	return newDatabaseError(operation, message, cause)
}

// NewQueryError creates a query database error.
func NewQueryError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

// NewSchemaError creates a schema database error.
func NewSchemaError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}
