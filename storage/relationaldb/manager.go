package relationaldb

import (
	"context"
	"fmt"
	"sync"
	"time"

	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// Logger interface for dependency injection
type Logger interface {
	Debug(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Error(msg string, fields ...interface{})
}

// DefaultLogger provides a basic logger implementation backed by the global xrpllog root.
type DefaultLogger struct{}

func NewDefaultLogger() *DefaultLogger { return &DefaultLogger{} }

func (l *DefaultLogger) Debug(msg string, fields ...interface{}) {
	xrpllog.Named(xrpllog.PartitionNodeStore).Debug(msg, fields...)
}

func (l *DefaultLogger) Info(msg string, fields ...interface{}) {
	xrpllog.Named(xrpllog.PartitionNodeStore).Info(msg, fields...)
}

func (l *DefaultLogger) Warn(msg string, fields ...interface{}) {
	xrpllog.Named(xrpllog.PartitionNodeStore).Warn(msg, fields...)
}

func (l *DefaultLogger) Error(msg string, fields ...interface{}) {
	xrpllog.Named(xrpllog.PartitionNodeStore).Error(msg, fields...)
}

// HealthChecker defines interface for health checking
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// Metrics interface for monitoring
type Metrics interface {
	IncrementCounter(name string, tags map[string]string)
	RecordDuration(name string, duration time.Duration, tags map[string]string)
	SetGauge(name string, value float64, tags map[string]string)
}

// NoOpMetrics provides a no-op metrics implementation
type NoOpMetrics struct{}

func (m *NoOpMetrics) IncrementCounter(name string, tags map[string]string)                       {}
func (m *NoOpMetrics) RecordDuration(name string, duration time.Duration, tags map[string]string) {}
func (m *NoOpMetrics) SetGauge(name string, value float64, tags map[string]string)                {}

// Manager provides lifecycle management and utilities for database operations
type Manager struct {
	repoManager RepositoryManager
	config      *Config
	logger      Logger
	metrics     Metrics

	// Health checking
	healthCheckInterval time.Duration
	healthCtx           context.Context
	healthCancel        context.CancelFunc
	healthWg            sync.WaitGroup

	// Connection state
	mu        sync.RWMutex
	connected bool
	lastError error

	// Maintenance
	maintenanceInterval time.Duration
	maintenanceCtx      context.Context
	maintenanceCancel   context.CancelFunc
	maintenanceWg       sync.WaitGroup
}

// ManagerOption defines functional options for Manager
type ManagerOption func(*Manager)

// WithLogger sets the logger for the manager
func WithLogger(logger Logger) ManagerOption {
	return func(m *Manager) {
		m.logger = logger
	}
}

// WithMetrics sets the metrics collector for the manager
func WithMetrics(metrics Metrics) ManagerOption {
	return func(m *Manager) {
		m.metrics = metrics
	}
}

// WithHealthCheckInterval sets the health check interval
func WithHealthCheckInterval(interval time.Duration) ManagerOption {
	return func(m *Manager) {
		m.healthCheckInterval = interval
	}
}

// WithMaintenanceInterval sets the maintenance interval
func WithMaintenanceInterval(interval time.Duration) ManagerOption {
	return func(m *Manager) {
		m.maintenanceInterval = interval
	}
}

// NewManager creates a new database manager
func NewManager(repoManager RepositoryManager, config *Config, options ...ManagerOption) *Manager {
	manager := &Manager{
		repoManager:         repoManager,
		config:              config,
		logger:              NewDefaultLogger(),
		metrics:             &NoOpMetrics{},
		healthCheckInterval: time.Minute,
		maintenanceInterval: time.Hour * 24,
	}

	// Apply options
	for _, option := range options {
		option(manager)
	}

	return manager
}

// Open opens the database connection and starts background services
func (m *Manager) Open(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connected {
		return nil
	}

	// Open database connection
	if err := m.repoManager.Open(ctx); err != nil {
		m.lastError = err
		m.logger.Error("Failed to open database connection", "error", err)
		m.metrics.IncrementCounter("db.connection.failed", map[string]string{
			"driver": m.config.Driver,
		})
		return WrapError(err, "open_database")
	}

	// Perform initial health check
	if err := m.repoManager.System().Ping(ctx); err != nil {
		m.lastError = err
		m.logger.Error("Database health check failed", "error", err)
		m.metrics.IncrementCounter("db.health_check.failed", map[string]string{
			"driver": m.config.Driver,
		})
		return WrapError(err, "initial_health_check")
	}

	m.connected = true
	m.lastError = nil

	// Start background services
	m.startHealthChecker()
	m.startMaintenance()

	m.logger.Info("Database manager opened successfully",
		"driver", m.config.Driver,
		"database", m.config.Database)

	m.metrics.IncrementCounter("db.connection.opened", map[string]string{
		"driver": m.config.Driver,
	})

	return nil
}

// Close closes the database connection and stops background services
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.connected {
		return nil
	}

	// Stop background services
	m.stopHealthChecker()
	m.stopMaintenance()

	// Close database connection
	if err := m.repoManager.Close(ctx); err != nil {
		m.logger.Error("Failed to close database connection", "error", err)
		m.metrics.IncrementCounter("db.connection.close_failed", map[string]string{
			"driver": m.config.Driver,
		})
		return WrapError(err, "close_database")
	}

	m.connected = false
	m.lastError = nil

	m.logger.Info("Database manager closed successfully")
	m.metrics.IncrementCounter("db.connection.closed", map[string]string{
		"driver": m.config.Driver,
	})

	return nil
}

// IsConnected returns whether the database is connected
func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

// LastError returns the last error encountered
func (m *Manager) LastError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastError
}

// HealthCheck performs a manual health check
func (m *Manager) HealthCheck(ctx context.Context) error {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		m.metrics.RecordDuration("db.health_check.duration", duration, map[string]string{
			"driver": m.config.Driver,
		})
	}()

	if !m.IsConnected() {
		err := ErrDatabaseClosed
		m.metrics.IncrementCounter("db.health_check.failed", map[string]string{
			"driver": m.config.Driver,
			"reason": "not_connected",
		})
		return err
	}

	if err := m.repoManager.System().Ping(ctx); err != nil {
		m.mu.Lock()
		m.lastError = err
		m.mu.Unlock()

		m.logger.Error("Health check failed", "error", err)
		m.metrics.IncrementCounter("db.health_check.failed", map[string]string{
			"driver": m.config.Driver,
			"reason": "ping_failed",
		})
		return WrapError(err, "health_check")
	}

	m.metrics.IncrementCounter("db.health_check.success", map[string]string{
		"driver": m.config.Driver,
	})

	return nil
}

// ExecuteWithRetry executes a function with retry logic
func (m *Manager) ExecuteWithRetry(ctx context.Context, operation func() error) error {
	var lastErr error

	for attempt := 0; attempt <= m.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate delay with exponential backoff
			delay := time.Duration(attempt) * m.config.RetryDelay
			if delay > m.config.RetryMaxDelay {
				delay = m.config.RetryMaxDelay
			}

			m.logger.Debug("Retrying operation",
				"attempt", attempt,
				"delay", delay,
				"last_error", lastErr)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		start := time.Now()
		err := operation()
		duration := time.Since(start)

		m.metrics.RecordDuration("db.operation.duration", duration, map[string]string{
			"driver":  m.config.Driver,
			"attempt": fmt.Sprintf("%d", attempt),
		})

		if err == nil {
			if attempt > 0 {
				m.logger.Info("Operation succeeded after retry",
					"attempt", attempt,
					"total_duration", time.Since(start))

				m.metrics.IncrementCounter("db.operation.retry_success", map[string]string{
					"driver":   m.config.Driver,
					"attempts": fmt.Sprintf("%d", attempt),
				})
			}
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !IsRetryable(err) {
			m.logger.Debug("Operation failed with non-retryable error", "error", err)
			m.metrics.IncrementCounter("db.operation.non_retryable_error", map[string]string{
				"driver": m.config.Driver,
			})
			break
		}

		m.logger.Debug("Operation failed with retryable error",
			"error", err,
			"attempt", attempt)

		m.metrics.IncrementCounter("db.operation.retryable_error", map[string]string{
			"driver":  m.config.Driver,
			"attempt": fmt.Sprintf("%d", attempt),
		})
	}

	m.logger.Error("Operation failed after all retries",
		"attempts", m.config.MaxRetries+1,
		"last_error", lastErr)

	m.metrics.IncrementCounter("db.operation.max_retries_exceeded", map[string]string{
		"driver": m.config.Driver,
	})

	return WrapError(lastErr, "execute_with_retry")
}

// ExecuteInTransaction executes a function within a transaction with retry logic
func (m *Manager) ExecuteInTransaction(ctx context.Context, operation func(TransactionContext) error) error {
	return m.ExecuteWithRetry(ctx, func() error {
		return m.repoManager.WithTransaction(ctx, operation)
	})
}

// GetRepositoryManager returns the underlying repository manager
func (m *Manager) GetRepositoryManager() RepositoryManager {
	return m.repoManager
}

// GetConfig returns the configuration
func (m *Manager) GetConfig() *Config {
	return m.config
}

// startHealthChecker starts the background health checker
func (m *Manager) startHealthChecker() {
	m.healthCtx, m.healthCancel = context.WithCancel(context.Background())

	m.healthWg.Add(1)
	go func() {
		defer m.healthWg.Done()

		ticker := time.NewTicker(m.healthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.healthCtx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(m.healthCtx, time.Second*10)
				if err := m.HealthCheck(ctx); err != nil {
					m.logger.Error("Background health check failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

// stopHealthChecker stops the background health checker
func (m *Manager) stopHealthChecker() {
	if m.healthCancel != nil {
		m.healthCancel()
		m.healthWg.Wait()
	}
}

// startMaintenance starts the background maintenance tasks
func (m *Manager) startMaintenance() {
	m.maintenanceCtx, m.maintenanceCancel = context.WithCancel(context.Background())

	m.maintenanceWg.Add(1)
	go func() {
		defer m.maintenanceWg.Done()

		ticker := time.NewTicker(m.maintenanceInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.maintenanceCtx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(m.maintenanceCtx, time.Minute*30)
				m.performMaintenance(ctx)
				cancel()
			}
		}
	}()
}

// stopMaintenance stops the background maintenance tasks
func (m *Manager) stopMaintenance() {
	if m.maintenanceCancel != nil {
		m.maintenanceCancel()
		m.maintenanceWg.Wait()
	}
}

// performMaintenance performs routine maintenance tasks
func (m *Manager) performMaintenance(ctx context.Context) {
	m.logger.Info("Starting database maintenance")

	start := time.Now()
	defer func() {
		duration := time.Since(start)
		m.logger.Info("Database maintenance completed", "duration", duration)
		m.metrics.RecordDuration("db.maintenance.duration", duration, map[string]string{
			"driver": m.config.Driver,
		})
	}()

	// Check space
	if hasSpace, err := m.repoManager.Ledger().HasLedgerSpace(ctx); err != nil {
		m.logger.Error("Failed to check ledger space", "error", err)
	} else if !hasSpace {
		m.logger.Warn("Low ledger database space")
		m.metrics.IncrementCounter("db.maintenance.low_space", map[string]string{
			"type": "ledger",
		})
	}

	if hasSpace, err := m.repoManager.Transaction().HasTransactionSpace(ctx); err != nil {
		m.logger.Error("Failed to check transaction space", "error", err)
	} else if !hasSpace {
		m.logger.Warn("Low transaction database space")
		m.metrics.IncrementCounter("db.maintenance.low_space", map[string]string{
			"type": "transaction",
		})
	}

	// Collect usage statistics
	if kbUsed, err := m.repoManager.System().GetKBUsedAll(ctx); err != nil {
		m.logger.Error("Failed to get database usage", "error", err)
	} else {
		m.metrics.SetGauge("db.space.used_kb", float64(kbUsed), map[string]string{
			"driver": m.config.Driver,
			"type":   "all",
		})
	}

	if kbUsed, err := m.repoManager.Ledger().GetKBUsedLedger(ctx); err != nil {
		m.logger.Error("Failed to get ledger database usage", "error", err)
	} else {
		m.metrics.SetGauge("db.space.used_kb", float64(kbUsed), map[string]string{
			"driver": m.config.Driver,
			"type":   "ledger",
		})
	}

	if kbUsed, err := m.repoManager.Transaction().GetKBUsedTransaction(ctx); err != nil {
		m.logger.Error("Failed to get transaction database usage", "error", err)
	} else {
		m.metrics.SetGauge("db.space.used_kb", float64(kbUsed), map[string]string{
			"driver": m.config.Driver,
			"type":   "transaction",
		})
	}
}
