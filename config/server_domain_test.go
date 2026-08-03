package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidation_ServerDomain(t *testing.T) {
	total128 := strings.Repeat("a", 63) + "." + strings.Repeat("b", 61) + ".io"
	total129 := strings.Repeat("a", 63) + "." + strings.Repeat("b", 62) + ".io"
	require.Len(t, total128, 128)
	require.Len(t, total129, 129)

	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{name: "empty"},
		{name: "mixed case and hyphenated", domain: "Node-1.Example.COM"},
		{name: "malformed label", domain: "-node.example.com", wantErr: true},
		{name: "malformed TLD", domain: "node.example.c0m", wantErr: true},
		{name: "minimum total length", domain: "a.io"},
		{name: "below minimum total length", domain: "a.c", wantErr: true},
		{name: "maximum total length", domain: total128},
		{name: "above maximum total length", domain: total129, wantErr: true},
		{name: "maximum label length", domain: strings.Repeat("a", 63) + ".example.com"},
		{name: "above maximum label length", domain: strings.Repeat("a", 64) + ".example.com", wantErr: true},
		{name: "maximum TLD length", domain: "a." + strings.Repeat("b", 63)},
		{name: "above maximum TLD length", domain: "a." + strings.Repeat("b", 64), wantErr: true},
		{name: "non ASCII", domain: "nœud.example.com", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validCompleteConfig()
			cfg.ServerDomain = test.domain

			err := ValidateConfig(cfg)
			if test.wantErr {
				require.ErrorContains(t, err, "invalid server_domain")
				return
			}
			require.NoError(t, err)
		})
	}
}
