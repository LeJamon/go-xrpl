package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/txq"
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
			if tc.network == "devnet" {
				if !strings.Contains(content, "--standalone") {
					t.Error("devnet config does not document standalone startup")
				}
			} else if !strings.Contains(content, `validators_file = "validators.toml"`) {
				t.Error("public-network config does not reference validators.toml")
			}
		})
	}
}

func TestRunGenerateConfig(t *testing.T) {
	out := filepath.Join(t.TempDir(), "xrpld.toml")
	options := &generateOptions{network: "testnet", output: out}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runGenerateConfig(cmd, options); err != nil {
		t.Fatalf("runGenerateConfig: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if string(data) != generateConfigContent("testnet") {
		t.Error("written config does not match generateConfigContent output")
	}
	validatorsData, err := os.ReadFile(filepath.Join(filepath.Dir(out), generatedValidatorsFilename))
	if err != nil {
		t.Fatalf("validators config not written: %v", err)
	}
	wantValidators, ok := generateValidatorsContent("testnet")
	if !ok {
		t.Fatal("testnet validators config is unavailable")
	}
	if string(validatorsData) != wantValidators {
		t.Error("written validators config does not match generateValidatorsContent output")
	}
	if strings.Contains(string(validatorsData), "vl.ripple.com") ||
		!strings.Contains(string(validatorsData), "https://vl.altnet.rippletest.net") ||
		!strings.Contains(string(validatorsData), "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860") {
		t.Error("testnet validators config does not contain only the altnet trust anchor")
	}
	configInfo, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := configInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("main config permissions = %o, want 600", got)
	}
	validatorsInfo, err := os.Stat(filepath.Join(filepath.Dir(out), generatedValidatorsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got := validatorsInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("validators permissions = %o, want 644", got)
	}
}

func TestRunGenerateConfig_DoesNotOverwriteExistingOutput(t *testing.T) {
	dir := t.TempDir()
	validatorsPath := filepath.Join(dir, generatedValidatorsFilename)
	if err := os.WriteFile(validatorsPath, []byte("operator-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := &generateOptions{network: "testnet", output: filepath.Join(dir, "xrpld.toml")}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runGenerateConfig(cmd, options); err == nil {
		t.Fatal("expected existing validators output to be rejected")
	}
	data, err := os.ReadFile(validatorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "operator-owned\n" {
		t.Fatal("existing validators file was modified")
	}
	if _, err := os.Stat(options.output); !os.IsNotExist(err) {
		t.Fatalf("config output exists after rejected generation: %v", err)
	}
}

func TestRunGenerateConfig_RecoversIdenticalValidatorsOutput(t *testing.T) {
	dir := t.TempDir()
	validatorsContent, ok := generateValidatorsContent("testnet")
	if !ok {
		t.Fatal("testnet validators config is unavailable")
	}
	if err := os.WriteFile(filepath.Join(dir, generatedValidatorsFilename), []byte(validatorsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	options := &generateOptions{network: "testnet", output: filepath.Join(dir, "xrpld.toml")}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runGenerateConfig(cmd, options); err != nil {
		t.Fatalf("runGenerateConfig: %v", err)
	}
	if _, err := os.Stat(options.output); err != nil {
		t.Fatalf("config output was not recovered: %v", err)
	}
}

func TestRunGenerateConfig_TightensIdenticalMainConfigPermissions(t *testing.T) {
	output := filepath.Join(t.TempDir(), "xrpld.toml")
	if err := os.WriteFile(output, []byte(generateConfigContent("devnet")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runGenerateConfig(cmd, &generateOptions{network: "devnet", output: output}); err != nil {
		t.Fatalf("runGenerateConfig: %v", err)
	}

	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("main config permissions = %o, want 600", got)
	}
}

func TestPublishGeneratedFiles_RollsBackPartialPublish(t *testing.T) {
	dir := t.TempDir()
	files := []generatedFile{
		{path: filepath.Join(dir, "validators.toml"), data: []byte("validators"), mode: 0o644},
		{path: filepath.Join(dir, "xrpld.toml"), data: []byte("config"), mode: 0o600},
	}
	links := 0
	err := publishGeneratedFiles(files, func(oldPath, newPath string) error {
		links++
		if links == 2 {
			return errors.New("injected publish failure")
		}
		return os.Link(oldPath, newPath)
	})
	if err == nil {
		t.Fatal("expected publish failure")
	}
	for _, file := range files {
		if _, err := os.Stat(file.path); !os.IsNotExist(err) {
			t.Fatalf("published output %s was not rolled back: %v", file.path, err)
		}
	}
}

// TestGenerateConfigContent_LoadsCleanly round-trips every generated
// template through the strict loader so the generate-config output is
// guaranteed to pass validation.
func TestGenerateConfigContent_LoadsCleanly(t *testing.T) {
	for _, network := range []string{"main", "testnet", "devnet"} {
		t.Run(network, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "xrpld.toml")
			if validatorsContent, ok := generateValidatorsContent(network); ok {
				if err := os.WriteFile(filepath.Join(dir, generatedValidatorsFilename), []byte(validatorsContent), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(p, []byte(generateConfigContent(network)), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(config.Paths{Main: p})
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

			pcfg := peermanagement.DefaultConfig()
			for _, opt := range adaptor.OverlayOptionsFromConfig(cfg) {
				opt(&pcfg)
			}
			if err := pcfg.Validate(); err != nil {
				t.Fatalf("generated %s peer config failed validation: %v", network, err)
			}

			switch network {
			case "main":
				wantSites := "https://vl.ripple.com,https://unl.xrplf.org"
				wantKeys := "ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734,ED42AEC58B701EEBB77356FFFEC26F83C1F0407263530F068C7C73D392C7E06FD1"
				if got := strings.Join(cfg.Validators.ValidatorListSites, ","); got != wantSites {
					t.Fatalf("generated main validator sites = %q, want %q", got, wantSites)
				}
				if got := strings.Join(cfg.Validators.ValidatorListKeys, ","); got != wantKeys {
					t.Fatalf("generated main validator keys = %q, want %q", got, wantKeys)
				}
			case "testnet":
				wantSite := "https://vl.altnet.rippletest.net"
				wantKey := "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"
				if got := strings.Join(cfg.Validators.ValidatorListSites, ","); got != wantSite {
					t.Fatalf("generated testnet validator sites = %q, want %q", got, wantSite)
				}
				if got := strings.Join(cfg.Validators.ValidatorListKeys, ","); got != wantKey {
					t.Fatalf("generated testnet validator keys = %q, want %q", got, wantKey)
				}
			case "devnet":
				if len(cfg.Validators.Validators) != 0 || len(cfg.Validators.ValidatorListKeys) != 0 {
					t.Fatal("devnet config unexpectedly contains public trust anchors")
				}
			}
		})
	}
}
