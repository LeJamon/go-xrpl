package genesis

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
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
	// No amendments → legacy fee format (XRPFees not present)
	cfg := Config{
		Fees:       StandardFees(),
		Amendments: nil,
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

func TestCreateGenesisLedgerModernFees(t *testing.T) {
	// Include XRPFees amendment → modern fee format
	cfg := Config{
		Fees:       StandardFees(),
		Amendments: [][32]byte{amendment.FeatureXRPFees},
	}

	genesis, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create genesis with modern fees failed: %v", err)
	}

	if genesis.Header.Drops != InitialXRP {
		t.Errorf("Genesis XRP mismatch: got %d, expected %d",
			genesis.Header.Drops, InitialXRP)
	}

	// Hash should differ from legacy format genesis
	legacyCfg := Config{
		Fees:       StandardFees(),
		Amendments: nil,
	}
	legacyGenesis, err := Create(legacyCfg)
	if err != nil {
		t.Fatalf("Create genesis with legacy fees failed: %v", err)
	}

	if genesis.Header.Hash == legacyGenesis.Header.Hash {
		t.Error("Modern fees genesis should produce different hash than legacy fees genesis")
	}

	t.Logf("Genesis with modern fees created successfully")
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

func TestHasXRPFeesAmendment(t *testing.T) {
	// No amendments → false
	if hasXRPFeesAmendment(nil) {
		t.Error("Expected false for nil amendments")
	}

	// Empty amendments → false
	if hasXRPFeesAmendment([][32]byte{}) {
		t.Error("Expected false for empty amendments")
	}

	// Unrelated amendment → false
	fakeAmendment := [32]byte{1, 2, 3}
	if hasXRPFeesAmendment([][32]byte{fakeAmendment}) {
		t.Error("Expected false for unrelated amendment")
	}

	// XRPFees present → true
	if !hasXRPFeesAmendment([][32]byte{amendment.FeatureXRPFees}) {
		t.Error("Expected true when XRPFees amendment is present")
	}

	// XRPFees among others → true
	amendments := [][32]byte{fakeAmendment, amendment.FeatureXRPFees, {9, 8, 7}}
	if !hasXRPFeesAmendment(amendments) {
		t.Error("Expected true when XRPFees is among multiple amendments")
	}
}

// TestGenesisHashConformance verifies that the genesis hash matches what rippled
// would produce for the same configuration. This catches serialization regressions.
//
// DefaultConfig() now includes all VoteDefaultYes amendments, matching rippled's
// genesis behavior of enabling non-vetoed amendments via getDesired().
// Reference: rippled Application.cpp:1707-1712, Ledger.cpp:168-229.
func TestGenesisHashConformance(t *testing.T) {
	t.Run("StandardDefaults_WithAmendments", func(t *testing.T) {
		// Standard genesis: default amendments, standard fees (10 drops, 10 XRP reserve, 2 XRP increment)
		cfg := DefaultConfig()
		gen, err := Create(cfg)
		if err != nil {
			t.Fatalf("genesis creation failed: %v", err)
		}

		// Verified hashes for standard genesis with default amendments and legacy fees
		expectedAccountHash := "3791bf543e5b77a17bc454f7a0720e4615760f457135f399de67c54d7929546d"
		expectedLedgerHash := "e158c218a9af027957a54ecd7d25f4ad35c90b2aaf8de4956723a17d80f5b3f4"

		gotAccountHash := hex.EncodeToString(gen.Header.AccountHash[:])
		gotLedgerHash := hex.EncodeToString(gen.Header.Hash[:])

		if gotAccountHash != expectedAccountHash {
			t.Errorf("AccountHash mismatch:\n  got:  %s\n  want: %s", gotAccountHash, expectedAccountHash)
		}
		if gotLedgerHash != expectedLedgerHash {
			t.Errorf("LedgerHash mismatch:\n  got:  %s\n  want: %s", gotLedgerHash, expectedLedgerHash)
		}
	})

	t.Run("NoAmendments", func(t *testing.T) {
		// Explicit no-amendments config for regression testing
		cfg := DefaultConfig()
		cfg.Amendments = nil

		gen, err := Create(cfg)
		if err != nil {
			t.Fatalf("genesis creation failed: %v", err)
		}

		// Verified hashes for standard genesis with NO amendments (legacy fees)
		expectedAccountHash := "ec2f822edfbc6f2f4de5aa7c8aff128f27db2c194315fd727445a4967dafd018"
		expectedLedgerHash := "b06f8e90df67b6a383e692a12963425b0e5fa6fbf0704370c137fce71d88a2d8"

		gotAccountHash := hex.EncodeToString(gen.Header.AccountHash[:])
		gotLedgerHash := hex.EncodeToString(gen.Header.Hash[:])

		if gotAccountHash != expectedAccountHash {
			t.Errorf("AccountHash mismatch:\n  got:  %s\n  want: %s", gotAccountHash, expectedAccountHash)
		}
		if gotLedgerHash != expectedLedgerHash {
			t.Errorf("LedgerHash mismatch:\n  got:  %s\n  want: %s", gotLedgerHash, expectedLedgerHash)
		}
	})
}
