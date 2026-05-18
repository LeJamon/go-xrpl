package relationaldb

import (
	"errors"
	"fmt"
)

// Error types for different categories of database errors
var (
	// Configuration errors
	ErrMissingHost            = errors.New("database host is required")
	ErrMissingDatabase        = errors.New("database name is required")
	ErrMissingUsername        = errors.New("database username is required")
	ErrInvalidPort            = errors.New("invalid database port")
	ErrInvalidDriver          = errors.New("invalid database driver")
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

	// Connection errors
	ErrDatabaseClosed          = errors.New("database connection is closed")
	ErrConnectionFailed        = errors.New("failed to connect to database")
	ErrConnectionTimeout       = errors.New("database connection timeout")
	ErrNoConnectionAvailable   = errors.New("no database connection available")
	ErrTransactionInProgress   = errors.New("transaction already in progress")
	ErrNoTransactionInProgress = errors.New("no transaction in progress")

	// Transaction errors
	ErrTransactionClosed       = errors.New("transaction is closed")
	ErrTransactionRollback     = errors.New("transaction was rolled back")
	ErrTransactionCommitFailed = errors.New("transaction commit failed")
	ErrDeadlock                = errors.New("database deadlock detected")
	ErrLockTimeout             = errors.New("database lock timeout")

	// Data errors
	ErrLedgerNotFound         = errors.New("ledger not found")
	ErrTransactionNotFound    = errors.New("transaction not found")
	ErrAccountNotFound        = errors.New("account not found")
	ErrDuplicateEntry         = errors.New("duplicate entry")
	ErrInvalidLedgerSequence  = errors.New("invalid ledger sequence")
	ErrInvalidTransactionHash = errors.New("invalid transaction hash")
	ErrInvalidAccountID       = errors.New("invalid account ID")
	ErrDataCorruption         = errors.New("data corruption detected")
	ErrInvalidDataFormat      = errors.New("invalid data format")

	// Constraint errors
	ErrConstraintViolation = errors.New("database constraint violation")
	ErrForeignKeyViolation = errors.New("foreign key constraint violation")
	ErrUniqueViolation     = errors.New("unique constraint violation")
	ErrNotNullViolation    = errors.New("not null constraint violation")
	ErrCheckViolation      = errors.New("check constraint violation")

	// Resource errors
	ErrInsufficientSpace      = errors.New("insufficient database space")
	ErrDatabaseFull           = errors.New("database is full")
	ErrMemoryExhausted        = errors.New("database memory exhausted")
	ErrConnectionLimitReached = errors.New("connection limit reached")

	// Query errors
	ErrInvalidQuery   = errors.New("invalid SQL query")
	ErrQueryTimeout   = errors.New("query execution timeout")
	ErrQueryCancelled = errors.New("query was cancelled")
	ErrTooManyResults = errors.New("query returned too many results")
	ErrInvalidLimit   = errors.New("invalid query limit")
	ErrInvalidOffset  = errors.New("invalid query offset")

	// Schema errors
	ErrSchemaVersion  = errors.New("unsupported database schema version")
	ErrTableNotFound  = errors.New("database table not found")
	ErrColumnNotFound = errors.New("database column not found")
	ErrIndexNotFound  = errors.New("database index not found")

	// Maintenance errors
	ErrVacuumFailed    = errors.New("database vacuum failed")
	ErrBackupFailed    = errors.New("database backup failed")
	ErrRestoreFailed   = errors.New("database restore failed")
	ErrMigrationFailed = errors.New("database migration failed")
)

// ErrorType represents different categories of database errors
type ErrorType int

const (
	ErrorTypeUnknown ErrorType = iota
	ErrorTypeConfiguration
	ErrorTypeConnection
	ErrorTypeTransaction
	ErrorTypeData
	ErrorTypeConstraint
	ErrorTypeResource
	ErrorTypeQuery
	ErrorTypeSchema
	ErrorTypeMaintenance
)

// DatabaseError provides detailed information about database errors
type DatabaseError struct {
	Type      ErrorType              `json:"type"`
	Operation string                 `json:"operation"`
	Message   string                 `json:"message"`
	Cause     error                  `json:"cause,omitempty"`
	Code      string                 `json:"code,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool                   `json:"retryable"`
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

// Is reports whether any error in err's chain matches target
func (e *DatabaseError) Is(target error) bool {
	if target == nil {
		return false
	}

	// Check if target is a DatabaseError with the same message
	if dbErr, ok := target.(*DatabaseError); ok {
		return e.Message == dbErr.Message && e.Type == dbErr.Type
	}

	switch target {
	case ErrLedgerNotFound:
		return e.Type == ErrorTypeData && e.Code == "LEDGER_NOT_FOUND"
	case ErrTransactionNotFound:
		return e.Type == ErrorTypeData && e.Code == "TRANSACTION_NOT_FOUND"
	case ErrConnectionFailed:
		return e.Type == ErrorTypeConnection && e.Code == "CONNECTION_FAILED"
	case ErrTransactionClosed:
		return e.Type == ErrorTypeTransaction && e.Code == "TRANSACTION_CLOSED"
	case ErrDuplicateEntry:
		return e.Type == ErrorTypeConstraint && e.Code == "DUPLICATE_ENTRY"
	case ErrInsufficientSpace:
		return e.Type == ErrorTypeResource && e.Code == "INSUFFICIENT_SPACE"
	}

	return false
}

// WithDetail adds a detail to the error
func (e *DatabaseError) WithDetail(key string, value any) *DatabaseError {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

// WithCode sets the error code
func (e *DatabaseError) WithCode(code string) *DatabaseError {
	e.Code = code
	return e
}

// IsRetryable returns whether the error is retryable
func (e *DatabaseError) IsRetryable() bool {
	return e.Retryable
}

// NewDatabaseError creates a new DatabaseError
func NewDatabaseError(errorType ErrorType, operation, message string, cause error) *DatabaseError {
	return &DatabaseError{
		Type:      errorType,
		Operation: operation,
		Message:   message,
		Cause:     cause,
		Retryable: isRetryableError(errorType, cause),
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

// NewConstraintError creates a constraint error
func NewConstraintError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeConstraint, operation, message, cause)
}

// NewResourceError creates a resource error
func NewResourceError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeResource, operation, message, cause)
}

// NewQueryError creates a query error
func NewQueryError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeQuery, operation, message, cause)
}

// NewSchemaError creates a schema error
func NewSchemaError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeSchema, operation, message, cause)
}

// NewMaintenanceError creates a maintenance error
func NewMaintenanceError(operation, message string, cause error) *DatabaseError {
	return NewDatabaseError(ErrorTypeMaintenance, operation, message, cause)
}

// isRetryableError determines if an error is retryable based on its type and cause
func isRetryableError(errorType ErrorType, cause error) bool {
	switch errorType {
	case ErrorTypeConnection:
		return true // Connection errors are usually retryable
	case ErrorTypeTransaction:
		if cause != nil {
			errStr := cause.Error()
			// Common retryable transaction errors
			if contains(errStr, "deadlock") || contains(errStr, "timeout") ||
				contains(errStr, "connection") || contains(errStr, "temporary") {
				return true
			}
		}
		return false
	case ErrorTypeResource:
		// Some resource errors might be temporary
		if cause != nil {
			errStr := cause.Error()
			if contains(errStr, "temporary") || contains(errStr, "busy") {
				return true
			}
		}
		return false
	case ErrorTypeQuery:
		// Query timeouts might be retryable
		if cause != nil {
			errStr := cause.Error()
			if contains(errStr, "timeout") || contains(errStr, "cancelled") {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// IsConfigurationError checks if an error is a configuration error
func IsConfigurationError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeConfiguration
}

// IsConnectionError checks if an error is a connection error
func IsConnectionError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeConnection
}

// IsTransactionError checks if an error is a transaction error
func IsTransactionError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeTransaction
}

// IsDataError checks if an error is a data error
func IsDataError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeData
}

// IsConstraintError checks if an error is a constraint error
func IsConstraintError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeConstraint
}

// IsResourceError checks if an error is a resource error
func IsResourceError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeResource
}

// IsQueryError checks if an error is a query error
func IsQueryError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeQuery
}

// IsSchemaError checks if an error is a schema error
func IsSchemaError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeSchema
}

// IsMaintenanceError checks if an error is a maintenance error
func IsMaintenanceError(err error) bool {
	var dbErr *DatabaseError
	return errors.As(err, &dbErr) && dbErr.Type == ErrorTypeMaintenance
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	var dbErr *DatabaseError
	if errors.As(err, &dbErr) {
		return dbErr.Retryable
	}

	// Check for common retryable patterns in error messages
	if err != nil {
		errStr := err.Error()
		retryablePatterns := []string{
			"connection refused",
			"connection reset",
			"connection timeout",
			"database is locked",
			"temporary failure",
			"deadlock",
			"timeout",
			"busy",
		}

		for _, pattern := range retryablePatterns {
			if contains(errStr, pattern) {
				return true
			}
		}
	}

	return false
}

// WrapError wraps an existing error with database error context
func WrapError(err error, operation string) error {
	if err == nil {
		return nil
	}

	// If it's already a DatabaseError, just update the operation
	var dbErr *DatabaseError
	if errors.As(err, &dbErr) {
		newErr := *dbErr
		newErr.Operation = operation
		return &newErr
	}

	// Classify the error based on its message
	errStr := err.Error()
	var errorType ErrorType
	var retryable bool

	switch {
	case contains(errStr, "connection") || contains(errStr, "connect"):
		errorType = ErrorTypeConnection
		retryable = true
	case contains(errStr, "transaction") || contains(errStr, "deadlock"):
		errorType = ErrorTypeTransaction
		retryable = contains(errStr, "deadlock") || contains(errStr, "timeout")
	case contains(errStr, "constraint") || contains(errStr, "duplicate") || contains(errStr, "unique"):
		errorType = ErrorTypeConstraint
		retryable = false
	case contains(errStr, "not found") || contains(errStr, "no rows"):
		errorType = ErrorTypeData
		retryable = false
	case contains(errStr, "space") || contains(errStr, "full") || contains(errStr, "memory"):
		errorType = ErrorTypeResource
		retryable = false
	case contains(errStr, "syntax") || contains(errStr, "invalid"):
		errorType = ErrorTypeQuery
		retryable = false
	case contains(errStr, "table") || contains(errStr, "column") || contains(errStr, "schema"):
		errorType = ErrorTypeSchema
		retryable = false
	default:
		errorType = ErrorTypeUnknown
		retryable = false
	}

	return &DatabaseError{
		Type:      errorType,
		Operation: operation,
		Message:   errStr,
		Cause:     err,
		Retryable: retryable,
	}
}
