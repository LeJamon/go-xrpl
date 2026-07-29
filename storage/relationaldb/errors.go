package relationaldb

import (
	"errors"
	"fmt"
)

var (
	ErrMissingHost            = errors.New("database host is required")
	ErrMissingDatabase        = errors.New("database name is required")
	ErrMissingUsername        = errors.New("database username is required")
	ErrInvalidPort            = errors.New("invalid database port")
	ErrInvalidMaxOpenConns    = errors.New("max open connections must be >= 0")
	ErrInvalidMaxIdleConns    = errors.New("max idle connections must be >= 0")
	ErrMaxIdleExceedsMaxOpen  = errors.New("max idle connections cannot exceed max open connections")
	ErrInvalidTimeout         = errors.New("timeout must be positive")
	ErrInvalidConnMaxLifetime = errors.New("connection max lifetime must be >= 0")
	ErrInvalidConnMaxIdleTime = errors.New("connection max idle time must be >= 0")

	ErrDatabaseClosed    = errors.New("database connection is closed")
	ErrTransactionClosed = errors.New("transaction is closed")
	ErrLedgerNotFound    = errors.New("ledger not found")
	ErrInvalidData       = errors.New("invalid relational data")
	ErrInvalidSchema     = errors.New("invalid relational schema")
)

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

func NewConfigurationError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

func NewConnectionError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

func NewTransactionError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

func NewDataError(operation, message string, cause error) *DatabaseError {
	if cause == nil {
		cause = ErrInvalidData
	}
	return newDatabaseError(operation, message, cause)
}

func NewQueryError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}

func NewSchemaError(operation, message string, cause error) *DatabaseError {
	return newDatabaseError(operation, message, cause)
}
