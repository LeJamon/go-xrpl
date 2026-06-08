package relationaldb

import (
	"fmt"
	"net/url"
	"time"
)

// Config contains database configuration settings
type Config struct {
	// Database connection settings
	Driver           string `json:"driver" yaml:"driver"`
	ConnectionString string `json:"connection_string" yaml:"connection_string"`
	Host             string `json:"host" yaml:"host"`
	Port             int    `json:"port" yaml:"port"`
	Database         string `json:"database" yaml:"database"`
	Username         string `json:"username" yaml:"username"`
	Password         string `json:"password" yaml:"password"`
	SSLMode          string `json:"ssl_mode" yaml:"ssl_mode"`

	// Connection pool settings
	MaxOpenConns    int           `json:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime" yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time" yaml:"conn_max_idle_time"`

	// Transaction settings
	DefaultTimeout time.Duration `json:"default_timeout" yaml:"default_timeout"`

	// Retry settings
	MaxRetries    int           `json:"max_retries" yaml:"max_retries"`
	RetryDelay    time.Duration `json:"retry_delay" yaml:"retry_delay"`
	RetryMaxDelay time.Duration `json:"retry_max_delay" yaml:"retry_max_delay"`

	// Maintenance settings
	MinFreeSpaceMB     uint64 `json:"min_free_space_mb" yaml:"min_free_space_mb"`
	VacuumIntervalDays int    `json:"vacuum_interval_days" yaml:"vacuum_interval_days"`

	// Feature flags
	UseTxTables       bool `json:"use_tx_tables" yaml:"use_tx_tables"`
	EnableWALMode     bool `json:"enable_wal_mode" yaml:"enable_wal_mode"`
	EnableForeignKeys bool `json:"enable_foreign_keys" yaml:"enable_foreign_keys"`
}

// NewConfig creates a new Config with sensible defaults
func NewConfig() *Config {
	return &Config{
		Driver:             "postgres",
		Host:               "localhost",
		Port:               5432,
		Database:           "xrpl",
		Username:           "xrpl",
		SSLMode:            "prefer",
		MaxOpenConns:       25,
		MaxIdleConns:       5,
		ConnMaxLifetime:    time.Hour,
		ConnMaxIdleTime:    time.Minute * 15,
		DefaultTimeout:     time.Second * 30,
		MaxRetries:         3,
		RetryDelay:         time.Millisecond * 100,
		RetryMaxDelay:      time.Second * 5,
		MinFreeSpaceMB:     512,
		VacuumIntervalDays: 7,
		UseTxTables:        true,
		EnableWALMode:      true,
		EnableForeignKeys:  true,
	}
}

// Validate checks the configuration for common errors
func (c *Config) Validate() error {
	// Validate driver
	switch c.Driver {
	case "postgres", "postgresql":
		c.Driver = "postgres"
	case "sqlite3", "sqlite":
		c.Driver = "sqlite3"
	default:
		return fmt.Errorf("unsupported database driver: %s", c.Driver)
	}

	// Validate required fields based on driver
	if c.Driver == "postgres" {
		if c.Host == "" {
			return ErrMissingHost
		}
		if c.Port <= 0 || c.Port > 65535 {
			return ErrInvalidPort
		}
		if c.Database == "" {
			return ErrMissingDatabase
		}
		if c.Username == "" {
			return ErrMissingUsername
		}
		// Validate SSL mode
		switch c.SSLMode {
		case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
			// Valid SSL modes
		default:
			return fmt.Errorf("invalid SSL mode: %s", c.SSLMode)
		}
	} else if c.Driver == "sqlite3" {
		if c.Database == "" {
			return ErrMissingDatabase
		}
	}

	// Validate connection pool settings
	if c.MaxOpenConns < 0 {
		return ErrInvalidMaxOpenConns
	}
	if c.MaxIdleConns < 0 {
		return ErrInvalidMaxIdleConns
	}
	if c.MaxIdleConns > c.MaxOpenConns && c.MaxOpenConns > 0 {
		return ErrMaxIdleExceedsMaxOpen
	}

	// Validate timeouts
	if c.DefaultTimeout <= 0 {
		return ErrInvalidTimeout
	}
	if c.ConnMaxLifetime < 0 {
		return ErrInvalidConnMaxLifetime
	}
	if c.ConnMaxIdleTime < 0 {
		return ErrInvalidConnMaxIdleTime
	}

	// Validate retry settings
	if c.MaxRetries < 0 {
		return ErrInvalidMaxRetries
	}
	if c.RetryDelay < 0 {
		return ErrInvalidRetryDelay
	}
	if c.RetryMaxDelay < c.RetryDelay {
		return ErrInvalidRetryMaxDelay
	}

	// Validate maintenance settings
	if c.MinFreeSpaceMB < 100 {
		return ErrInvalidMinFreeSpace
	}

	return nil
}

// BuildConnectionString builds a connection string from the config
func (c *Config) BuildConnectionString() (string, error) {
	if c.ConnectionString != "" {
		return c.ConnectionString, nil
	}

	switch c.Driver {
	case "postgres":
		return c.buildPostgresConnectionString()
	case "sqlite3":
		return c.buildSQLiteConnectionString()
	default:
		return "", fmt.Errorf("unsupported driver for connection string building: %s", c.Driver)
	}
}

// buildPostgresConnectionString builds a PostgreSQL connection string
func (c *Config) buildPostgresConnectionString() (string, error) {
	// Build using url.Values for proper encoding
	params := url.Values{}
	params.Set("sslmode", c.SSLMode)
	params.Set("connect_timeout", "30")
	params.Set("application_name", "xrpl-relational-db")

	// Build the DSN
	dsn := fmt.Sprintf("postgres://%s", c.Host)

	if c.Port != 0 && c.Port != 5432 {
		dsn += fmt.Sprintf(":%d", c.Port)
	}

	dsn += "/" + c.Database

	if c.Username != "" {
		userInfo := c.Username
		if c.Password != "" {
			userInfo += ":" + c.Password
		}
		// Insert user info into URL
		dsn = fmt.Sprintf("postgres://%s@%s", userInfo, dsn[11:]) // Remove "postgres://" prefix
	}

	if len(params) > 0 {
		dsn += "?" + params.Encode()
	}

	return dsn, nil
}

// buildSQLiteConnectionString builds a SQLite connection string
func (c *Config) buildSQLiteConnectionString() (string, error) {
	dsn := c.Database

	params := url.Values{}
	if c.EnableWALMode {
		params.Set("journal_mode", "WAL")
	}
	if c.EnableForeignKeys {
		params.Set("foreign_keys", "1")
	}
	params.Set("synchronous", "NORMAL")
	params.Set("cache_size", "-64000") // 64MB cache

	if len(params) > 0 {
		dsn += "?" + params.Encode()
	}

	return dsn, nil
}

// Clone creates a deep copy of the configuration
func (c *Config) Clone() *Config {
	clone := *c
	return &clone
}

// WithConnectionString returns a new config with the specified connection string
func (c *Config) WithConnectionString(connStr string) *Config {
	clone := c.Clone()
	clone.ConnectionString = connStr
	return clone
}

// WithDatabase returns a new config with the specified database name
func (c *Config) WithDatabase(database string) *Config {
	clone := c.Clone()
	clone.Database = database
	return clone
}

// WithCredentials returns a new config with the specified credentials
func (c *Config) WithCredentials(username, password string) *Config {
	clone := c.Clone()
	clone.Username = username
	clone.Password = password
	return clone
}

// WithHost returns a new config with the specified host
func (c *Config) WithHost(host string) *Config {
	clone := c.Clone()
	clone.Host = host
	return clone
}

// WithPort returns a new config with the specified port
func (c *Config) WithPort(port int) *Config {
	clone := c.Clone()
	clone.Port = port
	return clone
}

// WithPoolSettings returns a new config with the specified connection pool settings
func (c *Config) WithPoolSettings(maxOpen, maxIdle int, maxLifetime, maxIdleTime time.Duration) *Config {
	clone := c.Clone()
	clone.MaxOpenConns = maxOpen
	clone.MaxIdleConns = maxIdle
	clone.ConnMaxLifetime = maxLifetime
	clone.ConnMaxIdleTime = maxIdleTime
	return clone
}

// WithTimeout returns a new config with the specified default timeout
func (c *Config) WithTimeout(timeout time.Duration) *Config {
	clone := c.Clone()
	clone.DefaultTimeout = timeout
	return clone
}

// WithRetrySettings returns a new config with the specified retry settings
func (c *Config) WithRetrySettings(maxRetries int, delay, maxDelay time.Duration) *Config {
	clone := c.Clone()
	clone.MaxRetries = maxRetries
	clone.RetryDelay = delay
	clone.RetryMaxDelay = maxDelay
	return clone
}

// String returns a string representation of the config (with password redacted)
func (c *Config) String() string {
	clone := c.Clone()
	if clone.Password != "" {
		clone.Password = "***"
	}

	connStr, _ := clone.BuildConnectionString()
	return fmt.Sprintf("Config{Driver: %s, Host: %s, Port: %d, Database: %s, Connection: %s}",
		clone.Driver, clone.Host, clone.Port, clone.Database, connStr)
}
