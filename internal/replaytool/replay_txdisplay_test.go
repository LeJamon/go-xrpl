package replaytool

import (
	"encoding/hex"
	"testing"

	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
)

// A real serialized transaction blob (SigningPubKey + signature + Account
// present) used to exercise fillTxDisplay's three paths.
const sampleTxBlobHex = "1200192400000001202A0000000068400000000000000A73210330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD02074473045022100858210F05EA58ACCAD60DA2EB7D505EF56016E0D323698D4819199358809120D022007B68AF69934E1E1DD12CC2A9E5D0F9696D5BDFA8CAA50CA29A36B9AA26C2F0A8114B5F762798A53D543A014CAF8B297CFF8F2F937E8"

// TestFillTxDisplay locks the contract that matters for hash-safety: the
// display fields fillTxDisplay derives from the parsed transaction must equal
// what a full decode of the same blob produces, and the expensive DecodedTx map
// must only be materialized when a consumer asks for it (or on a parse failure,
// where a best-effort decode is the only labeling option).
func TestFillTxDisplay(t *testing.T) {
	blob, err := hex.DecodeString(sampleTxBlobHex)
	if err != nil {
		t.Fatalf("decoding sample blob: %v", err)
	}
	parsed, err := txengine.ParseAndPrepare(blob)
	if err != nil {
		t.Fatalf("parsing sample blob: %v", err)
	}
	common := parsed.Transaction.GetCommon()
	decoded := decodeEntryData(sampleTxBlobHex)
	if decoded == nil {
		t.Fatal("sample blob did not decode")
	}

	// The parse-sourced fields must match the decode-sourced fields exactly;
	// otherwise switching the hot path to GetCommon would change displayed/
	// recorded values.
	if common.TransactionType != decoded["TransactionType"] {
		t.Fatalf("parse vs decode TransactionType mismatch: %q != %v", common.TransactionType, decoded["TransactionType"])
	}
	if common.Account != decoded["Account"] {
		t.Fatalf("parse vs decode Account mismatch: %q != %v", common.Account, decoded["Account"])
	}

	t.Run("hot path skips the decode", func(t *testing.T) {
		var info TxApplyInfo
		fillTxDisplay(&info, blob, parsed.Transaction, false)
		if info.TxType != common.TransactionType {
			t.Errorf("TxType = %q, want %q", info.TxType, common.TransactionType)
		}
		if info.Account != common.Account {
			t.Errorf("Account = %q, want %q", info.Account, common.Account)
		}
		if info.DecodedTx != nil {
			t.Errorf("DecodedTx populated on hot path; should be lazy")
		}
		if len(info.RawBlob) == 0 {
			t.Errorf("RawBlob not retained on hot path; a late-failure dump could not recover DecodedTx")
		}
	})

	// A divergence dump can fire even on a run that skipped per-tx detail (no
	// --dump-dir / -v). materializeDecoded must backfill DecodedTx from the
	// retained blob so tx_results.json is never silently missing it.
	t.Run("dump materializes the decode on demand", func(t *testing.T) {
		var info TxApplyInfo
		fillTxDisplay(&info, blob, parsed.Transaction, false)
		if info.DecodedTx != nil {
			t.Fatal("precondition: DecodedTx should be nil before materialize")
		}
		results := []TxApplyInfo{info}
		materializeDecoded(results)
		if results[0].DecodedTx == nil {
			t.Fatal("materializeDecoded did not backfill DecodedTx")
		}
		if results[0].DecodedTx["TransactionType"] != decoded["TransactionType"] {
			t.Errorf("backfilled TransactionType = %v, want %v", results[0].DecodedTx["TransactionType"], decoded["TransactionType"])
		}
		if results[0].DecodedTx["Account"] != decoded["Account"] {
			t.Errorf("backfilled Account = %v, want %v", results[0].DecodedTx["Account"], decoded["Account"])
		}
	})

	t.Run("detail path materializes the decode", func(t *testing.T) {
		var info TxApplyInfo
		fillTxDisplay(&info, blob, parsed.Transaction, true)
		if info.DecodedTx == nil {
			t.Errorf("DecodedTx nil when detail requested")
		}
		if info.TxType != common.TransactionType {
			t.Errorf("TxType = %q, want %q", info.TxType, common.TransactionType)
		}
	})

	t.Run("parse failure still labels the tx", func(t *testing.T) {
		var info TxApplyInfo
		fillTxDisplay(&info, blob, nil, false)
		if info.DecodedTx == nil {
			t.Errorf("expected best-effort decode on parse failure")
		}
		if info.TxType != common.TransactionType {
			t.Errorf("TxType = %q, want %q", info.TxType, common.TransactionType)
		}
		if info.Account != common.Account {
			t.Errorf("Account = %q, want %q", info.Account, common.Account)
		}
	})
}
