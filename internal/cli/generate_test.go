package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/txq"
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
	restore := silenceStdout(t)
	defer restore()

	prevNet, prevOut := generateNetwork, generateOutput
	defer func() { generateNetwork, generateOutput = prevNet, prevOut }()

	out := filepath.Join(t.TempDir(), "xrpld.toml")
	generateNetwork = "testnet"
	generateOutput = out

	// Valid network: writes the file and returns without exiting.
	runGenerateConfig(nil, nil)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if string(data) != generateConfigContent("testnet") {
		t.Error("written config does not match generateConfigContent output")
	}
}

// TestGenerateConfigContent_LoadsCleanly round-trips every generated
// template through the strict loader so the generate-config output is
// guaranteed to pass validation.
func TestGenerateConfigContent_LoadsCleanly(t *testing.T) {
	for _, network := range []string{"main", "testnet", "devnet"} {
		t.Run(network, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "xrpld.toml")
			if err := os.WriteFile(p, []byte(generateConfigContent(network)), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(config.ConfigPaths{Main: p})
			if err != nil {
				t.Fatalf("generated %s config failed to load: %v", network, err)
			}
			// The template's [transaction_queue] values must match the
			// built-in defaults so a generated config changes nothing.
			txqCfg, err := service.TxQConfigFromTuning(cfg.TransactionQueue, false)
			if err != nil {
				t.Fatal(err)
			}
			if txqCfg != txq.DefaultConfig() {
				t.Errorf("generated [transaction_queue] diverges from txq defaults:\n got %+v\nwant %+v", txqCfg, txq.DefaultConfig())
			}
		})
	}
}
