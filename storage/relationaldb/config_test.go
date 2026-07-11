package relationaldb

import (
	"strings"
	"testing"
)

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "url userinfo password",
			dsn:  "postgres://xrpl:s3cr3t@dbhost:5432/xrpl?sslmode=require",
			want: "postgres://xrpl:xxxxx@dbhost:5432/xrpl?sslmode=require",
		},
		{
			name: "url without password unchanged",
			dsn:  "postgres://xrpl@dbhost/xrpl?sslmode=require",
			want: "postgres://xrpl@dbhost/xrpl?sslmode=require",
		},
		{
			name: "url password query parameter",
			dsn:  "postgres://dbhost/xrpl?password=s3cr3t&sslmode=require",
			want: "postgres://dbhost/xrpl?password=xxxxx&sslmode=require",
		},
		{
			name: "url userinfo with malformed path escape",
			dsn:  "postgres://xrpl:s3cr3t@dbhost/%ZZ",
			want: "xxxxx",
		},
		{
			name: "url password query parameter with malformed escape",
			dsn:  "postgres://dbhost/xrpl?password=s3cr3t%ZZ&sslmode=require",
			want: "xxxxx",
		},
		{
			name: "key/value password",
			dsn:  "host=dbhost user=xrpl password=s3cr3t dbname=xrpl",
			want: "host=dbhost user=xrpl password=xxxxx dbname=xrpl",
		},
		{
			name: "key/value quoted password",
			dsn:  "host=dbhost password='se cr et' dbname=xrpl",
			want: "host=dbhost password=xxxxx dbname=xrpl",
		},
		{
			name: "key/value password with ampersand",
			dsn:  "host=dbhost password=abc&def dbname=xrpl",
			want: "host=dbhost password=xxxxx dbname=xrpl",
		},
		{
			name: "key/value password with escaped spaces",
			dsn:  `host=dbhost password=se\ cr\ et dbname=xrpl`,
			want: "host=dbhost password=xxxxx dbname=xrpl",
		},
		{
			name: "key/value quoted password with escaped quote",
			dsn:  `host=dbhost password='it\'s secret' dbname=xrpl`,
			want: "host=dbhost password=xxxxx dbname=xrpl",
		},
		{
			name: "key/value unterminated quoted password",
			dsn:  "host=dbhost password='se cr et",
			want: "host=dbhost password=xxxxx",
		},
		{
			name: "key/value unicode whitespace",
			dsn:  "host=dbhost password\u00a0=\u00a0s3cr3t dbname=xrpl",
			want: "host=dbhost password\u00a0=\u00a0xxxxx dbname=xrpl",
		},
		{
			name: "password text in another value unchanged",
			dsn:  "user=password=s3cr3t host=dbhost",
			want: "user=password=s3cr3t host=dbhost",
		},
		{
			name: "sqlite dsn unchanged",
			dsn:  "xrpl.db?foreign_keys=1&journal_mode=WAL",
			want: "xrpl.db?foreign_keys=1&journal_mode=WAL",
		},
		{
			name: "empty",
			dsn:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactDSN(tt.dsn); got != tt.want {
				t.Errorf("redactDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestConfigStringRedactsPassword(t *testing.T) {
	t.Run("connection string DSN", func(t *testing.T) {
		cfg := NewConfig().WithConnectionString("postgres://xrpl:s3cr3t@dbhost/xrpl?sslmode=require")
		s := cfg.String()
		if strings.Contains(s, "s3cr3t") {
			t.Errorf("String() leaked DSN password: %s", s)
		}
		if !strings.Contains(s, "xxxxx") {
			t.Errorf("String() missing redaction marker: %s", s)
		}
	})

	t.Run("password field", func(t *testing.T) {
		cfg := NewConfig()
		cfg.Password = "s3cr3t"
		s := cfg.String()
		if strings.Contains(s, "s3cr3t") {
			t.Errorf("String() leaked password field: %s", s)
		}
		if !strings.Contains(s, "xxxxx") {
			t.Errorf("String() missing redaction marker: %s", s)
		}
	})
}
