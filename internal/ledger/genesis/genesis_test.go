package genesis

import (
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

func TestGenerateGenesisAccountID(t *testing.T) {
	accountID, address, err := GenerateGenesisAccountID()
	if err != nil {
		t.Fatalf("GenerateGenesisAccountID failed: %v", err)
	}

	// The well-known genesis account address
	expectedAddress := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	if address != expectedAddress {
		t.Errorf("Genesis address mismatch: got %s, expected %s", address, expectedAddress)
	}

	// Account ID should be 20 bytes, not all zeros
	if accountID == [20]byte{} {
		t.Error("Genesis account ID should not be empty")
	}

	t.Logf("Genesis account: %s", address)
	t.Logf("Genesis account ID: %x", accountID)
}

func TestCreateGenesisLedger(t *testing.T) {
	cfg := DefaultConfig()
	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis failed: %v", err)
	}

	// Verify genesis ledger properties
	if genesis.Header.LedgerIndex != GenesisLedgerSequence {
		t.Errorf("Genesis ledger sequence mismatch: got %d, expected %d",
			genesis.Header.LedgerIndex, GenesisLedgerSequence)
	}

	if genesis.Header.Drops != InitialXRP {
		t.Errorf("Genesis XRP mismatch: got %d, expected %d",
			genesis.Header.Drops, InitialXRP)
	}

	// Parent hash should be all zeros
	if genesis.Header.ParentHash != [32]byte{} {
		t.Error("Genesis parent hash should be all zeros")
	}

	// Ledger hash should not be empty
	if genesis.Header.Hash == [32]byte{} {
		t.Error("Genesis ledger hash should not be empty")
	}

	// State map hash should not be empty
	stateHash, err := genesis.StateMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get state map hash: %v", err)
	}
	if stateHash == [32]byte{} {
		t.Error("Genesis state map hash should not be empty")
	}

	// Verify the state hash matches header
	if genesis.Header.AccountHash != stateHash {
		t.Error("Account hash in header should match state map hash")
	}

	// Genesis account should be the well-known address
	expectedAddress := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	if genesis.GenesisAddress != expectedAddress {
		t.Errorf("Genesis address mismatch: got %s, expected %s",
			genesis.GenesisAddress, expectedAddress)
	}

	t.Logf("Genesis ledger hash: %x", genesis.Header.Hash)
	t.Logf("Genesis account hash: %x", genesis.Header.AccountHash)
	t.Logf("Genesis tx hash: %x", genesis.Header.TxHash)
	t.Logf("Genesis account: %s", genesis.GenesisAddress)
}

func TestCreateGenesisLedgerWithAmendments(t *testing.T) {
	cfg := DefaultConfig()

	// Add a fake amendment hash
	fakeAmendment := [32]byte{1, 2, 3, 4}
	cfg.Amendments = [][32]byte{fakeAmendment}

	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis with amendments failed: %v", err)
	}

	// Genesis should still be valid
	if genesis.Header.LedgerIndex != GenesisLedgerSequence {
		t.Errorf("Genesis ledger sequence mismatch: got %d, expected %d",
			genesis.Header.LedgerIndex, GenesisLedgerSequence)
	}

	t.Logf("Genesis with amendments created successfully")
}

func TestCreateGenesisLedgerLegacyFees(t *testing.T) {
	cfg := Config{
		Fees:          StandardFees(),
		UseModernFees: false, // Use legacy fee format
		Amendments:    nil,
	}

	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis with legacy fees failed: %v", err)
	}

	if genesis.Header.Drops != InitialXRP {
		t.Errorf("Genesis XRP mismatch: got %d, expected %d",
			genesis.Header.Drops, InitialXRP)
	}

	t.Logf("Genesis with legacy fees created successfully")
}

func TestStandardFees(t *testing.T) {
	fees := StandardFees()

	expectedBaseFee := drops.NewXRPAmount(10)
	expectedReserveBase := drops.DropsPerXRP * 10
	expectedReserveIncrement := drops.DropsPerXRP * 2

	if fees.BaseFee != expectedBaseFee {
		t.Errorf("Base fee mismatch: got %d, expected %d", fees.BaseFee, expectedBaseFee)
	}

	if fees.ReserveBase != expectedReserveBase {
		t.Errorf("Reserve base mismatch: got %d, expected %d", fees.ReserveBase, expectedReserveBase)
	}

	if fees.ReserveIncrement != expectedReserveIncrement {
		t.Errorf("Reserve increment mismatch: got %d, expected %d",
			fees.ReserveIncrement, expectedReserveIncrement)
	}
}

// TestFeeSettingsRoundTrip verifies that FeeSettings round-trip correctly
// through genesis creation, binary codec, SHAMap, and state.ParseFeeSettings.
// This test is critical for understanding the AccountDelete conformance fixture
// behavior where CalculateBaseFee reads fees from the ledger SLE.
func TestFeeSettingsRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Fees.ReserveBase = drops.DropsPerXRP * 200
	cfg.Fees.ReserveIncrement = drops.DropsPerXRP * 50 // 50 XRP = 50_000_000 drops

	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis failed: %v", err)
	}

	// Read the FeeSettings SLE from the state map
	k := keylet.Fees()
	item, found, err := genesis.StateMap.Get(k.Key)
	if err != nil {
		t.Fatalf("Failed to get FeeSettings from state map: %v", err)
	}
	if !found {
		t.Fatal("FeeSettings not found in genesis state map")
	}

	data := item.Data()
	t.Logf("FeeSettings SLE bytes (%d): %x", len(data), data)

	// Parse the FeeSettings
	feeSettings, err := state.ParseFeeSettings(data)
	if err != nil {
		t.Fatalf("Failed to parse FeeSettings: %v", err)
	}

	t.Logf("Parsed FeeSettings:")
	t.Logf("  BaseFeeDrops: %d", feeSettings.BaseFeeDrops)
	t.Logf("  ReserveBaseDrops: %d", feeSettings.ReserveBaseDrops)
	t.Logf("  ReserveIncrementDrops: %d", feeSettings.ReserveIncrementDrops)
	t.Logf("  BaseFee (legacy): %d", feeSettings.BaseFee)
	t.Logf("  ReserveBase (legacy): %d", feeSettings.ReserveBase)
	t.Logf("  ReserveIncrement (legacy): %d", feeSettings.ReserveIncrement)

	gotBaseFee := feeSettings.GetBaseFee()
	gotReserveBase := feeSettings.GetReserveBase()
	gotReserveIncrement := feeSettings.GetReserveIncrement()

	t.Logf("  GetBaseFee(): %d", gotBaseFee)
	t.Logf("  GetReserveBase(): %d", gotReserveBase)
	t.Logf("  GetReserveIncrement(): %d", gotReserveIncrement)

	if gotBaseFee != 10 {
		t.Errorf("GetBaseFee() = %d, want 10", gotBaseFee)
	}
	if gotReserveBase != 200_000_000 {
		t.Errorf("GetReserveBase() = %d, want 200000000", gotReserveBase)
	}
	if gotReserveIncrement != 50_000_000 {
		t.Errorf("GetReserveIncrement() = %d, want 50000000", gotReserveIncrement)
	}
}

func TestCalculateLedgerHash(t *testing.T) {
	cfg := DefaultConfig()
	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis failed: %v", err)
	}

	// Recalculate hash
	recalculatedHash := CalculateLedgerHash(genesis.Header)

	if recalculatedHash != genesis.Header.Hash {
		t.Errorf("Recalculated hash mismatch: got %x, expected %x",
			recalculatedHash, genesis.Header.Hash)
	}
}
