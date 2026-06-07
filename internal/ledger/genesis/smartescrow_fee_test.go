package genesis

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func readGenesisFeeSettings(t *testing.T, cfg Config) *state.FeeSettings {
	t.Helper()
	g, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis failed: %v", err)
	}
	item, found, err := g.StateMap.Get(keylet.Fees().Key)
	if err != nil || !found {
		t.Fatalf("FeeSettings not found in genesis state map (err=%v found=%v)", err, found)
	}
	fs, err := state.ParseFeeSettings(item.Data())
	if err != nil {
		t.Fatalf("Failed to parse FeeSettings: %v", err)
	}
	return fs
}

// TestGenesisFeeSettings_NoSmartEscrow guards conformance: without the
// SmartEscrow amendment the FeeSettings entry must NOT carry the WASM extension
// fee fields, so the genesis state hash is unchanged for existing networks.
func TestGenesisFeeSettings_NoSmartEscrow(t *testing.T) {
	t.Parallel()
	fs := readGenesisFeeSettings(t, DefaultConfig())
	if fs.HasExtensionFees {
		t.Fatalf("FeeSettings unexpectedly carries extension fees without SmartEscrow")
	}
}

// TestGenesisFeeSettings_SmartEscrow verifies that enabling SmartEscrow at
// genesis seeds the WASM extension fee fields from the FeeSetup defaults.
// Reference: rippled Config.h FeeSetup (1,000,000 / 100,000 / 1,000,000).
func TestGenesisFeeSettings_SmartEscrow(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Amendments = append(append([][32]byte{}, cfg.Amendments...), amendment.FeatureSmartEscrow)

	fs := readGenesisFeeSettings(t, cfg)
	if !fs.HasExtensionFees {
		t.Fatalf("FeeSettings missing extension fees with SmartEscrow enabled")
	}
	if got := fs.GetExtensionComputeLimit(); got != state.DefaultExtensionComputeLimit {
		t.Errorf("ExtensionComputeLimit = %d, want %d", got, state.DefaultExtensionComputeLimit)
	}
	if got := fs.GetExtensionSizeLimit(); got != state.DefaultExtensionSizeLimit {
		t.Errorf("ExtensionSizeLimit = %d, want %d", got, state.DefaultExtensionSizeLimit)
	}
	if got := fs.GetGasPrice(); got != state.DefaultGasPrice {
		t.Errorf("GasPrice = %d, want %d", got, state.DefaultGasPrice)
	}
}
