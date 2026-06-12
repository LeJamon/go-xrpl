package relationaldb

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the relational database layer.
var (
	// Configuration errors
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
	ErrInvalidMaxRetries      = errors.New("max retries must be >= 0")
	ErrInvalidRetryDelay      = errors.New("retry delay must be >= 0")
	ErrInvalidRetryMaxDelay   = errors.New("retry max delay must be >= retry delay")
	ErrInvalidMinFreeSpace    = errors.New("minimum free space must be >= 100MB")

	// ErrDatabaseClosed is returned when an operation runs against a closed connection.
	ErrDatabaseClosed = errors.New("database connection is closed")

	// ErrTransactionClosed is returned when a transaction context is reused
	// after Commit or Rollback.
	ErrTransactionClosed = errors.New("transaction is closed")

	// ErrLedgerNotFound is returned when a requested ledger is not stored.
	ErrLedgerNotFound = errors.New("ledger not found")
)

// ErrorType represents different categories of database errors
type ErrorType int

// ErrorType values categorize database errors.
const (
	ErrorTypeUnknown ErrorType = iota
	ErrorTypeConfiguration
	ErrorTypeConnection
	ErrorTypeTransaction
	ErrorTypeData
	ErrorTypeQuery
	ErrorTypeSchema
)

// DatabaseError provides detailed information about database errors
type DatabaseError struct {
	Type      ErrorType `json:"type"`
	Operation string    `json:"operation"`
	Message   string    `json:"message"`
	Cause     error     `json:"cause,omitempty"`
}

// Error implements the error interface
func (e *DatabaseError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Operation, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Operation, e.Message)
}

// Unwrap returns the underlying cause error
func (e *DatabaseError) Unwrap() error {
	return e.Cause
}

// NewDatabaseError creates a new DatabaseError
func NewDatabaseError(errorType ErrorType, operation, message string, cause error) *DatabaseError {
	return &DatabaseError{
		Type:      errorType,
		Operation: operation,
		Message:   message,
		Cause:     cause,
	}
}

// NewConfigurationError creates a configuration error
func NewConfigurationError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeConfiguration, operation, message, cause)
}

// NewConnectionError creates a connection error
func NewConnectionError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeConnection, operation, message, cause)
}

// NewTransactionError creates a transaction error
func NewTransactionError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeTransaction, operation, message, cause)
}

// NewDataError creates a data error
func NewDataError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeData, operation, message, cause)
}

// NewQueryError creates a query error
func NewQueryError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeQuery, operation, message, cause)
}

// NewSchemaError creates a schema error
func NewSchemaError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeSchema, operation, message, cause)
}
