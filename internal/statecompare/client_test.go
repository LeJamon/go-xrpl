package statecompare

import (
	"context"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	const key = "STATECOMPARE_TEST_GETENV"
	const def = "fallback"

	t.Run("unset returns default", func(t *testing.T) {
		// key is never set in this subtest's environment.
		if got := getEnvOrDefault(key, def); got != def {
			t.Errorf("getEnvOrDefault(unset) = %q, want %q", got, def)
		}
	})

	t.Run("empty returns default", func(t *testing.T) {
		t.Setenv(key, "")
		if got := getEnvOrDefault(key, def); got != def {
			t.Errorf("getEnvOrDefault(empty) = %q, want %q", got, def)
		}
	})

	t.Run("set returns value", func(t *testing.T) {
		t.Setenv(key, "custom")
		if got := getEnvOrDefault(key, def); got != "custom" {
			t.Errorf("getEnvOrDefault(set) = %q, want %q", got, "custom")
		}
	})
}

func TestConfigFromEnvDefaults(t *testing.T) {
	// getEnvOrDefault treats an empty value the same as unset, so clearing each
	// var exercises the default branch while letting t.Setenv restore the
	// caller's environment afterwards.
	for _, k := range []string{
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_SSLMODE",
	} {
		t.Setenv(k, "")
	}

	want := Config{
		Host:     "localhost",
		Port:     "5432",
		Database: "xrpl_state",
		User:     "postgres",
		Password: "postgres",
		SSLMode:  "disable",
	}
	if got := ConfigFromEnv(); got != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "db.example.com")
	t.Setenv("POSTGRES_PORT", "6543")
	t.Setenv("POSTGRES_DB", "ledgers")
	t.Setenv("POSTGRES_USER", "alice")
	t.Setenv("POSTGRES_PASSWORD", "s3cret")
	t.Setenv("POSTGRES_SSLMODE", "require")

	want := Config{
		Host:     "db.example.com",
		Port:     "6543",
		Database: "ledgers",
		User:     "alice",
		Password: "s3cret",
		SSLMode:  "require",
	}
	if got := ConfigFromEnv(); got != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestValidateRangeFromGreaterThanTo(t *testing.T) {
	// from > to is a degenerate (empty) range: it returns early before any DB
	// access, so a Client with a nil *sql.DB is sufficient and must not panic.
	c := &Client{}
	ok, missing, err := c.ValidateRange(context.Background(), 10, 5)
	if err != nil {
		t.Fatalf("ValidateRange returned error: %v", err)
	}
	if !ok {
		t.Errorf("ValidateRange(10, 5) ok = false, want true for an empty range")
	}
	if missing != 0 {
		t.Errorf("ValidateRange(10, 5) missing = %d, want 0", missing)
	}
}
