package relationaldb

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config contains database configuration settings
type Config struct {
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

	DefaultTimeout time.Duration `json:"default_timeout" yaml:"default_timeout"`
}

// NewConfig creates a new Config with sensible defaults
func NewConfig() *Config {
	return &Config{
		Host:            "localhost",
		Port:            5432,
		Database:        "xrpl",
		Username:        "xrpl",
		SSLMode:         "prefer",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: time.Minute * 15,
		DefaultTimeout:  time.Second * 30,
	}
}

// Validate checks the configuration for common errors
func (c *Config) Validate() error {
	if c.ConnectionString == "" {
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

	return nil
}

// BuildConnectionString builds a connection string from the config
func (c *Config) BuildConnectionString() (string, error) {
	if c.ConnectionString != "" {
		return c.ConnectionString, nil
	}

	return c.buildPostgresConnectionString()
}

// buildPostgresConnectionString builds a PostgreSQL connection string,
// URL-escaping credentials and the database name.
func (c *Config) buildPostgresConnectionString() (string, error) {
	params := url.Values{}
	params.Set("sslmode", c.SSLMode)
	params.Set("connect_timeout", "30")
	params.Set("application_name", "xrpl-relational-db")

	host := c.Host
	if c.Port != 0 && c.Port != 5432 {
		host = fmt.Sprintf("%s:%d", c.Host, c.Port)
	}

	u := &url.URL{
		Scheme:   "postgres",
		Host:     host,
		Path:     "/" + c.Database,
		RawQuery: params.Encode(),
	}

	if c.Username != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		} else {
			u.User = url.User(c.Username)
		}
	}

	return u.String(), nil
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

// String returns a string representation of the config (with password redacted)
func (c *Config) String() string {
	connStr, _ := c.BuildConnectionString()
	return fmt.Sprintf("Config{Host: %s, Port: %d, Database: %s, Connection: %s}",
		c.Host, c.Port, c.Database, redactDSN(connStr))
}

// redactedPassword matches the placeholder net/url's URL.Redacted() uses; it
// survives URL re-encoding unescaped.
const redactedPassword = "xxxxx"

func redactDSN(dsn string) string {
	if strings.Contains(dsn, "://") {
		return redactURLDSN(dsn)
	}
	return redactKeywordDSN(dsn)
}

func redactURLDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || !u.IsAbs() {
		return redactedPassword
	}

	redacted := false
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), redactedPassword)
		redacted = true
	}

	if u.RawQuery != "" {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return redactedPassword
		}
		queryRedacted := false
		for key := range query {
			if strings.EqualFold(key, "password") {
				query.Set(key, redactedPassword)
				queryRedacted = true
			}
		}
		if queryRedacted {
			u.RawQuery = query.Encode()
			redacted = true
		}
	}

	if redacted {
		return u.String()
	}
	return dsn
}

func redactKeywordDSN(dsn string) string {
	var result strings.Builder
	last := 0
	redacted := false
	for pos := 0; pos < len(dsn); {
		pos = skipDSNSpace(dsn, pos)
		keyStart := pos
		for pos < len(dsn) {
			r, size := utf8.DecodeRuneInString(dsn[pos:])
			if unicode.IsSpace(r) || r == '=' {
				break
			}
			pos += size
		}
		keyEnd := pos
		pos = skipDSNSpace(dsn, pos)
		if pos >= len(dsn) {
			break
		}
		if dsn[pos] != '=' {
			continue
		}
		pos++
		pos = skipDSNSpace(dsn, pos)
		valueStart := pos
		pos = scanDSNValue(dsn, pos)
		if strings.EqualFold(dsn[keyStart:keyEnd], "password") {
			if !redacted {
				result.Grow(len(dsn))
			}
			result.WriteString(dsn[last:valueStart])
			result.WriteString(redactedPassword)
			last = pos
			redacted = true
		}
	}

	if !redacted {
		return dsn
	}
	result.WriteString(dsn[last:])
	return result.String()
}

func skipDSNSpace(dsn string, pos int) int {
	for pos < len(dsn) {
		r, size := utf8.DecodeRuneInString(dsn[pos:])
		if !unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	return pos
}

func scanDSNValue(dsn string, pos int) int {
	if pos >= len(dsn) {
		return pos
	}

	if dsn[pos] == '\'' {
		pos++
		for pos < len(dsn) {
			r, size := utf8.DecodeRuneInString(dsn[pos:])
			pos += size
			if r == '\\' && pos < len(dsn) {
				_, size = utf8.DecodeRuneInString(dsn[pos:])
				pos += size
				continue
			}
			if r == '\'' {
				break
			}
		}
		return pos
	}

	for pos < len(dsn) {
		r, size := utf8.DecodeRuneInString(dsn[pos:])
		if unicode.IsSpace(r) {
			break
		}
		pos += size
		if r == '\\' && pos < len(dsn) {
			_, size = utf8.DecodeRuneInString(dsn[pos:])
			pos += size
		}
	}
	return pos
}
