package config

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/spf13/viper"
)

// CrawlConfig preserves omitted versus explicitly disabled /crawl sections.
type CrawlConfig struct {
	Enabled *bool `toml:"enabled" mapstructure:"enabled"`
	Overlay *bool `toml:"overlay" mapstructure:"overlay"`
	Server  *bool `toml:"server" mapstructure:"server"`
	Counts  *bool `toml:"counts" mapstructure:"counts"`
	UNL     *bool `toml:"unl" mapstructure:"unl"`
}

func (c CrawlConfig) enabled() bool {
	return boolOrDefault(c.Enabled, true)
}

// OverlayEnabled reports whether peer topology is included, defaulting to true.
func (c CrawlConfig) OverlayEnabled() bool {
	return c.enabled() && boolOrDefault(c.Overlay, true)
}

// ServerEnabled reports whether public server state is included, defaulting to true.
func (c CrawlConfig) ServerEnabled() bool {
	return c.enabled() && boolOrDefault(c.Server, true)
}

// CountsEnabled reports whether the optional object counts section is enabled.
func (c CrawlConfig) CountsEnabled() bool {
	return c.enabled() && boolOrDefault(c.Counts, false)
}

// UNLEnabled reports whether validator-list metadata is included, defaulting to true.
func (c CrawlConfig) UNLEnabled() bool {
	return c.enabled() && boolOrDefault(c.UNL, true)
}

// ValidatorListHTTPConfig controls the peer /vl endpoint.
type ValidatorListHTTPConfig struct {
	Enabled *bool `toml:"enabled" mapstructure:"enabled"`
}

// EnabledValue reports whether cached lists are served, defaulting to true.
func (c ValidatorListHTTPConfig) EnabledValue() bool {
	return boolOrDefault(c.Enabled, true)
}

func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// validateHTTPSettings accepts TOML booleans and 0/1 integer spellings.
func validateHTTPSettings(v *viper.Viper) error {
	for _, key := range []string{
		"crawl.enabled",
		"crawl.overlay",
		"crawl.server",
		"crawl.counts",
		"crawl.unl",
		"vl.enabled",
	} {
		if !v.IsSet(key) {
			continue
		}
		if err := validateHTTPBool(key, v.Get(key)); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPBool(key string, value any) error {
	if value == nil {
		return fmt.Errorf("%s must be a boolean or 0/1", key)
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Bool:
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n := rv.Int(); n == 0 || n == 1 {
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n := rv.Uint(); n == 0 || n == 1 {
			return nil
		}
	case reflect.String:
		if _, err := strconv.ParseBool(rv.String()); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s must be a boolean or 0/1", key)
}
