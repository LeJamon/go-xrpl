package genesis

import (
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
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
