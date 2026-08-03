package config

import (
	"strings"
	"testing"
)

func TestNormalizeOriginsRequiresExactHTTPOrigin(t *testing.T) {
	valid, err := NormalizeOrigins([]string{"HTTPS://Example.COM:8443", "https://example.com:8443"})
	if err != nil {
		t.Fatalf("NormalizeOrigins returned error: %v", err)
	}
	if got, want := strings.Join(valid, ","), "https://example.com:8443"; got != want {
		t.Fatalf("normalized origins = %q, want %q", got, want)
	}

	for _, origin := range []string{
		"*",
		"https://example.com/",
		"https://user:pass@example.com",
		"https://example.com/path",
		"https://example.com?query=1",
		"https://example.com?",
		"https://example.com#",
		"https://example.com:bad",
		"https://example.com:",
		"https://example.com:443:444",
		"https://example.com:99999",
		"ftp://example.com",
		"https://",
	} {
		t.Run(origin, func(t *testing.T) {
			if _, err := NormalizeOrigins([]string{origin}); err == nil {
				t.Fatalf("NormalizeOrigins(%q) unexpectedly succeeded", origin)
			}
		})
	}
}

func TestPortValidateRejectsPartialBasicAuth(t *testing.T) {
	p := &PortConfig{Port: 5005, IP: "127.0.0.1", Protocol: "http", User: "operator"}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "user and password") {
		t.Fatalf("Validate error = %v, want partial Basic Auth rejection", err)
	}
}

func TestValidatePortsRejectsCredentiallessBrowserAdmin(t *testing.T) {
	ports := map[string]PortConfig{
		"admin": {
			Port:           5005,
			IP:             "127.0.0.1",
			Protocol:       "http",
			Admin:          []string{"127.0.0.1"},
			AllowedOrigins: []string{"https://admin.example"},
		},
	}
	errs := validatePorts(ports)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "browser listener requires both user and password") {
		t.Fatalf("validatePorts errors = %v, want credentialed browser-admin rejection", errs)
	}
}

func TestValidateConfigRejectsInvalidServerOrigin(t *testing.T) {
	cfg := validCompleteConfig()
	cfg.Server.AllowedOrigins = []string{"*"}

	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "server allowed_origins") {
		t.Fatalf("ValidateConfig error = %v, want invalid server allowed_origins", err)
	}
}
