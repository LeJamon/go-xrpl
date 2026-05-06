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
//   + commonFields: sfLedgerEntryType=0x0073 (FeeSettings=115,
//     ledger_entries.macro:312), sfFlags=0.
//
// Field-id encoding rule (rippled Serializer.cpp): when fieldCode >= 16,
// the header is (typeCode<<4)|0 followed by a separate byte holding the
// fieldCode. So sfReferenceFeeUnits (type=2, field=30) is `20 1E`, not
// `21 1E` — `21` would mean (type=2, field=1) which is sfFlags.
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
	// Fields sorted by (typeCode, fieldCode):
	//   UInt16 LedgerEntryType  (t=1, f=1)  -> 11 + 0073
	//   UInt32 Flags            (t=2, f=2)  -> 22 + 00000000
	//   UInt32 ReferenceFeeUnits(t=2, f=30) -> 20 1E + 0000000A
	//   UInt32 ReserveBase      (t=2, f=31) -> 20 1F + 00989680
	//   UInt32 ReserveIncrement (t=2, f=32) -> 20 20 + 001E8480
	//   UInt64 BaseFee          (t=3, f=5)  -> 35 + 000000000000000A
	const want = "1100732200000000201e0000000a201f009896802020001e848035000000000000000a"
	if got != want {
		t.Fatalf("legacy FeeSettings SLE bytes diverged from rippled wire format\n got:  %s\n want: %s", got, want)
	}
}

// TestDivergence_AccountRootDefaultBytes asserts the exact byte stream
// produced for the genesis AccountRoot SLE, so any drift from rippled's
// Ledger.cpp:186-192 layout is caught here rather than only via the
// downstream account_hash mismatch.
//
// The genesis account ID is deterministic (DefaultConfig derives it
// from the well-known seed), so this assertion is stable.
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
	// Fields sorted by (typeCode, fieldCode):
	//   UInt16 LedgerEntryType  (t=1, f=1)   -> 11 + 0061 (AccountRoot=97)
	//   UInt32 Flags            (t=2, f=2)   -> 22 + 00000000
	//   UInt32 Sequence         (t=2, f=4)   -> 24 + 00000001
	//   UInt32 PreviousTxnLgrSeq(t=2, f=5)   -> 25 + 00000000
	//   UInt32 OwnerCount       (t=2, f=13)  -> 2D + 00000000
	//   Hash256 PreviousTxnID   (t=5, f=5)   -> 55 + <32 zero bytes>
	//   Amount  Balance         (t=6, f=2)   -> 62 + 416345785D8A0000 (INITIAL_XRP, native)
	//   Blob    AccountID       (t=8, f=1)   -> 81 14 + <20-byte genesis account>
	const want = "1100612200000000240000000125000000002d0000000055000000000000000000000000000000000000000000000000000000000000000062416345785d8a00008114b5f762798a53d543a014caf8b297cff8f2f937e8"
	if got != want {
		t.Fatalf("Genesis AccountRoot SLE bytes diverged from rippled wire format\n got:  %s\n want: %s", got, want)
	}
}

func TestDivergence_PrintAmendmentList(t *testing.T) {
	ids := DefaultGenesisAmendments()
	t.Logf("DefaultYes amendment count: %d", len(ids))
	for i, id := range ids {
		t.Logf("  [%d] %s", i, fmt.Sprintf("%X", id))
	}
}
