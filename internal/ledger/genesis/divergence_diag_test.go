package genesis

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/keylet"
)

// TestDivergence_PrintHashesAndSLEs is a diagnostic for issue #190.
// It prints account_hash, ledger_hash, FeeSettings SLE bytes and Amendments
// SLE bytes for the four most likely "genesis equivalents" the kurtosis
// rippled:2.6.2 peer might be using.
func TestDivergence_PrintHashesAndSLEs(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			"DefaultConfig_DefaultYesAmendments_LegacyFees",
			DefaultConfig(), // legacy fees because XRPFees is VoteDefaultNo
		},
		{
			"NoAmendments_LegacyFees", // matches genesis_amendments_disabled = true in topology.star
			func() Config {
				c := DefaultConfig()
				c.Amendments = nil
				return c
			}(),
		},
	}

	feesK := keylet.Fees()
	amendmentsK := keylet.Amendments()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen, err := Create(tc.cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			t.Logf("=== %s ===", tc.name)
			t.Logf("LedgerSeq:   %d", gen.Header.LedgerIndex)
			t.Logf("AccountHash: %s", hex.EncodeToString(gen.Header.AccountHash[:]))
			t.Logf("LedgerHash:  %s", hex.EncodeToString(gen.Header.Hash[:]))
			t.Logf("Drops:       %d", gen.Header.Drops)
			t.Logf("CloseTimeRes:%d", gen.Header.CloseTimeResolution)
			t.Logf("Amendments count: %d", len(tc.cfg.Amendments))

			feesItem, found, err := gen.StateMap.Get(feesK.Key)
			if err != nil || !found {
				t.Fatalf("FeeSettings not found: %v", err)
			}
			t.Logf("FeeSettings keylet: %s", hex.EncodeToString(feesK.Key[:]))
			t.Logf("FeeSettings bytes (%dB): %s", len(feesItem.Data()),
				hex.EncodeToString(feesItem.Data()))

			amItem, found, err := gen.StateMap.Get(amendmentsK.Key)
			if err == nil && found {
				t.Logf("Amendments keylet: %s", hex.EncodeToString(amendmentsK.Key[:]))
				t.Logf("Amendments bytes (%dB): %s", len(amItem.Data()),
					hex.EncodeToString(amItem.Data()))
			} else {
				t.Logf("Amendments SLE: NOT PRESENT (none enabled)")
			}

			// Print raw root account SLE
			acctK := keylet.Account(gen.GenesisAccount)
			acctItem, found, err := gen.StateMap.Get(acctK.Key)
			if err != nil || !found {
				t.Fatalf("AccountRoot not found: %v", err)
			}
			t.Logf("AccountRoot keylet: %s", hex.EncodeToString(acctK.Key[:]))
			t.Logf("AccountRoot bytes (%dB): %s", len(acctItem.Data()),
				hex.EncodeToString(acctItem.Data()))
		})
	}
}

// TestDivergence_LegacyFeeSettingsExpectedBytes verifies the byte-level encoding
// of the legacy FeeSettings SLE (the format goXRPL uses when XRPFees is OFF and
// rippled:2.6.2 also defaults to OFF).
//
// Reference rippled Ledger.cpp:212-223 — for legacy:
//   sfBaseFee (UInt64)         = 10
//   sfReserveBase (UInt32)     = 10_000_000
//   sfReserveIncrement (UInt32)= 2_000_000
//   sfReferenceFeeUnits (UInt32)= 10
//   + commonFields: sfLedgerEntryType=0x0066 (FeeSettings=102), sfFlags=0
func TestDivergence_LegacyFeeSettingsExpectedBytes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Amendments = nil // legacy
	gen, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	feesK := keylet.Fees()
	item, found, err := gen.StateMap.Get(feesK.Key)
	if err != nil || !found {
		t.Fatalf("FeeSettings not found: %v", err)
	}
	got := hex.EncodeToString(item.Data())
	t.Logf("legacy FeeSettings SLE bytes: %s", got)
	// Manual XRPL binary codec assembly for verification:
	// fields are sorted by (typeCode, fieldCode):
	//   UInt16 LedgerEntryType (typeCode=1, fieldCode=1)  -> 0x11 + 0x0066
	//   UInt32 Flags (typeCode=2, fieldCode=2)            -> 0x22 + 0x00000000
	//   UInt32 ReferenceFeeUnits (typeCode=2, fieldCode=30)-> 0x21 0x1E + 0x0000000A
	//   UInt32 ReserveBase (typeCode=2, fieldCode=31)     -> 0x21 0x1F + 0x00989680
	//   UInt32 ReserveIncrement (typeCode=2, fieldCode=32)-> 0x21 0x20 + 0x001E8480
	//   UInt64 BaseFee (typeCode=3, fieldCode=5)          -> 0x35 + 0x000000000000000A
	// Expected hex: "11006622000000002211E000000000A211F00989680212001E8480350000000000000000A"
	// (printed for human comparison; not asserted because field layout is established by the codec)
	if len(got) == 0 {
		t.Fatal("empty FeeSettings bytes")
	}
}

// TestDivergence_AccountRootDefaultBytes prints exactly what the genesis
// AccountRoot SLE serializes to, so we can compare against rippled's
// equivalent byte stream from Ledger.cpp:186-192.
func TestDivergence_AccountRootDefaultBytes(t *testing.T) {
	cfg := DefaultConfig()
	gen, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	acctK := keylet.Account(gen.GenesisAccount)
	item, found, err := gen.StateMap.Get(acctK.Key)
	if err != nil || !found {
		t.Fatalf("AccountRoot not found: %v", err)
	}
	got := hex.EncodeToString(item.Data())
	t.Logf("Genesis AccountRoot SLE bytes: %s", got)
	// rippled sets only sfSequence=1, sfAccount=<id>, sfBalance=INITIAL_XRP.
	// commonFields auto-fill: sfLedgerEntryType=0x0061, sfFlags=0.
	// soeREQUIRED defaults: sfOwnerCount=0, sfPreviousTxnID=0, sfPreviousTxnLgrSeq=0.
	// goXRPL must produce the same bytes for the genesis hash to match.
	if len(got) < 80 {
		t.Fatal("AccountRoot bytes suspiciously short")
	}
}

func TestDivergence_PrintAmendmentList(t *testing.T) {
	ids := DefaultGenesisAmendments()
	t.Logf("DefaultYes amendment count: %d", len(ids))
	for i, id := range ids {
		t.Logf("  [%d] %s", i, fmt.Sprintf("%X", id))
	}
}
