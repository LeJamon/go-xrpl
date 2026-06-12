package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGenerateConfigContent(t *testing.T) {
	cases := []struct {
		network string
		wantIPs string // a substring expected only in that network's ips block
	}{
		{"main", "r.ripple.com 51235"},
		{"testnet", "r.altnet.rippletest.net 51235"},
		{"devnet", "ips = []"},
	}
	for _, tc := range cases {
		t.Run(tc.network, func(t *testing.T) {
			content := generateConfigContent(tc.network)
			if !strings.Contains(content, tc.wantIPs) {
				t.Errorf("missing ips marker %q for %s", tc.wantIPs, tc.network)
			}
			if !strings.Contains(content, `network_id = "`+tc.network+`"`) {
				t.Errorf("missing network_id for %s", tc.network)
			}
			// A few required structural sections every generated file must carry.
			for _, section := range []string{"[logging]", "[server]", "[node_db]", "[transaction_queue]"} {
				if !strings.Contains(content, section) {
					t.Errorf("%s: generated config missing section %s", tc.network, section)
				}
			}
		})
	}
}

func TestRunGenerateConfig(t *testing.T) {
	prevNet, prevOut := generateNetwork, generateOutput
	defer func() { generateNetwork, generateOutput = prevNet, prevOut }()

	out := filepath.Join(t.TempDir(), "xrpld.toml")
	generateNetwork = "testnet"
	generateOutput = out

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runGenerateConfig(cmd, nil); err != nil {
		t.Fatalf("runGenerateConfig: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if string(data) != generateConfigContent("testnet") {
		t.Error("written config does not match generateConfigContent output")
	}
}
