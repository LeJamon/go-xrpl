package amm

import (
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func buildTestAMM(t *testing.T, tradingFee uint16) *AMMData {
	t.Helper()
	var acct [20]byte
	acct[0] = 0x01
	var voter [20]byte
	voter[0] = 0x02
	acctAddr, err := state.EncodeAccountID(acct)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	lpt := state.NewIssuedAmountFromValue(1_000_000_000_000_000, -3,
		GenerateAMMLPTCurrency("XRP", "USD"), acctAddr)
	return &AMMData{
		Account:        acct,
		Asset:          tx.Asset{Currency: "XRP"},
		Asset2:         tx.Asset{Currency: "USD", Issuer: acctAddr},
		TradingFee:     tradingFee,
		LPTokenBalance: lpt,
		OwnerNode:      0,
		VoteSlots: []VoteSlotData{
			{Account: voter, TradingFee: tradingFee, VoteWeight: 100000},
		},
	}
}

func decodeFieldsBytes(t *testing.T, data []byte) map[string]any {
	t.Helper()
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return fields
}

// TestSerializeAMM_FlagsAlwaysPresent asserts sfFlags (soeREQUIRED common
// field) is serialized even at its default 0, matching rippled which emits
// Flags=0 from the SLE template on every AMM.
func TestSerializeAMM_FlagsAlwaysPresent(t *testing.T) {
	for _, fee := range []uint16{0, 500} {
		data, err := serializeAMMData(buildTestAMM(t, fee))
		if err != nil {
			t.Fatalf("serialize (fee=%d): %v", fee, err)
		}
		fields := decodeFieldsBytes(t, data)
		f, ok := fields["Flags"]
		if !ok {
			t.Fatalf("fee=%d: Flags must be present (soeREQUIRED)", fee)
		}
		if toUint64(f) != 0 {
			t.Errorf("fee=%d: Flags = %v, want 0", fee, f)
		}
	}
}

// TestSerializeAMM_TradingFeeDefaultOmitted asserts sfTradingFee (soeDEFAULT)
// is omitted when 0 and present when non-zero, both at the top level and in the
// inner VoteEntry — matching rippled AMMUtils.cpp initializeFeeAuctionVote.
func TestSerializeAMM_TradingFeeDefaultOmitted(t *testing.T) {
	data0, err := serializeAMMData(buildTestAMM(t, 0))
	if err != nil {
		t.Fatalf("serialize fee=0: %v", err)
	}
	f0 := decodeFieldsBytes(t, data0)
	if _, ok := f0["TradingFee"]; ok {
		t.Error("TradingFee must be omitted at default 0 (top-level)")
	}
	if tf := voteEntryHasTradingFee(t, f0); tf {
		t.Error("VoteEntry.TradingFee must be omitted at default 0")
	}

	data1, err := serializeAMMData(buildTestAMM(t, 500))
	if err != nil {
		t.Fatalf("serialize fee=500: %v", err)
	}
	f1 := decodeFieldsBytes(t, data1)
	tf, ok := f1["TradingFee"]
	if !ok {
		t.Fatal("TradingFee must be present when non-zero (top-level)")
	}
	if toUint64(tf) != 500 {
		t.Errorf("top-level TradingFee = %v, want 500", tf)
	}
	if !voteEntryHasTradingFee(t, f1) {
		t.Error("VoteEntry.TradingFee must be present when non-zero")
	}
}

// TestSerializeAMM_RoundTrip asserts serialize→parse preserves TradingFee and
// that re-serialization keeps the soe field-presence invariant (TradingFee
// present iff non-zero, Flags always present) stable across the round trip.
// Byte-equality is intentionally not asserted: STAmount re-normalizes the
// LPTokenBalance representation, which is orthogonal to the soe rules here.
func TestSerializeAMM_RoundTrip(t *testing.T) {
	for _, fee := range []uint16{0, 500} {
		data, err := serializeAMMData(buildTestAMM(t, fee))
		if err != nil {
			t.Fatalf("serialize (fee=%d): %v", fee, err)
		}
		parsed, err := ParseAMMData(data)
		if err != nil {
			t.Fatalf("parse (fee=%d): %v", fee, err)
		}
		if parsed.TradingFee != fee {
			t.Errorf("fee=%d: round-trip TradingFee = %d", fee, parsed.TradingFee)
		}
		data2, err := serializeAMMData(parsed)
		if err != nil {
			t.Fatalf("re-serialize (fee=%d): %v", fee, err)
		}
		fields := decodeFieldsBytes(t, data2)
		if _, ok := fields["Flags"]; !ok {
			t.Errorf("fee=%d: Flags must remain present after round trip", fee)
		}
		_, hasTF := fields["TradingFee"]
		if hasTF != (fee != 0) {
			t.Errorf("fee=%d: TradingFee presence=%v after round trip, want %v", fee, hasTF, fee != 0)
		}
		if voteEntryHasTradingFee(t, fields) != (fee != 0) {
			t.Errorf("fee=%d: VoteEntry.TradingFee presence wrong after round trip", fee)
		}
	}
}

// TestSerializeAMM_PreviousTxnRoundTrip asserts the soeOPTIONAL threading
// pointers are omitted on an un-threaded AMM and round-trip faithfully once set,
// so a no-op modification re-serializes with PreviousTxnID intact and the apply
// layer's unchanged-entry guard prunes it instead of writing a ghost ModifiedNode.
func TestSerializeAMM_PreviousTxnRoundTrip(t *testing.T) {
	// Un-threaded: both soeOPTIONAL pointers absent.
	data, err := serializeAMMData(buildTestAMM(t, 500))
	if err != nil {
		t.Fatalf("serialize (un-threaded): %v", err)
	}
	f := decodeFieldsBytes(t, data)
	if _, ok := f["PreviousTxnID"]; ok {
		t.Error("PreviousTxnID must be absent on an un-threaded AMM (soeOPTIONAL)")
	}
	if _, ok := f["PreviousTxnLgrSeq"]; ok {
		t.Error("PreviousTxnLgrSeq must be absent on an un-threaded AMM (soeOPTIONAL)")
	}

	// Threaded: both present, correct, and stable across serialize → parse → serialize.
	const prevTxnID = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	const prevLgrSeq = uint32(98948137)
	amm := buildTestAMM(t, 500)
	amm.PreviousTxnID = prevTxnID
	amm.PreviousTxnLgrSeq = prevLgrSeq

	data2, err := serializeAMMData(amm)
	if err != nil {
		t.Fatalf("serialize (threaded): %v", err)
	}
	f2 := decodeFieldsBytes(t, data2)
	if got, ok := f2["PreviousTxnID"].(string); !ok || !strings.EqualFold(got, prevTxnID) {
		t.Errorf("serialized PreviousTxnID = %v, want %s", f2["PreviousTxnID"], prevTxnID)
	}
	if got := toUint64(f2["PreviousTxnLgrSeq"]); got != uint64(prevLgrSeq) {
		t.Errorf("serialized PreviousTxnLgrSeq = %d, want %d", got, prevLgrSeq)
	}

	parsed, err := ParseAMMData(data2)
	if err != nil {
		t.Fatalf("parse (threaded): %v", err)
	}
	if !strings.EqualFold(parsed.PreviousTxnID, prevTxnID) {
		t.Errorf("round-trip PreviousTxnID = %s, want %s", parsed.PreviousTxnID, prevTxnID)
	}
	if parsed.PreviousTxnLgrSeq != prevLgrSeq {
		t.Errorf("round-trip PreviousTxnLgrSeq = %d, want %d", parsed.PreviousTxnLgrSeq, prevLgrSeq)
	}

	data3, err := serializeAMMData(parsed)
	if err != nil {
		t.Fatalf("re-serialize (threaded): %v", err)
	}
	f3 := decodeFieldsBytes(t, data3)
	if _, ok := f3["PreviousTxnID"]; !ok {
		t.Error("PreviousTxnID must remain present after round trip")
	}
	if _, ok := f3["PreviousTxnLgrSeq"]; !ok {
		t.Error("PreviousTxnLgrSeq must remain present after round trip")
	}
}

func voteEntryHasTradingFee(t *testing.T, fields map[string]any) bool {
	t.Helper()
	slots, ok := fields["VoteSlots"].([]any)
	if !ok || len(slots) == 0 {
		t.Fatalf("VoteSlots missing or empty: %T", fields["VoteSlots"])
	}
	first, ok := slots[0].(map[string]any)
	if !ok {
		t.Fatalf("VoteSlots[0] not a map: %T", slots[0])
	}
	ve, ok := first["VoteEntry"].(map[string]any)
	if !ok {
		t.Fatalf("VoteEntry missing: %T", first["VoteEntry"])
	}
	_, has := ve["TradingFee"]
	return has
}

func toUint64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case int:
		return uint64(n)
	case int64:
		return uint64(n)
	case float64:
		return uint64(n)
	default:
		return 0
	}
}
