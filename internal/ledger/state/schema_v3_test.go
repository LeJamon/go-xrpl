package state

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// The rippled-3.0.0 schema additions (issue #278) must survive the hand-written
// parse/serialize helpers in this package — the read path used by RPC and
// internal ledger logic. These tests round-trip each new field through the
// state-layer codepath (binarycodec blob → ParseX, and SerializeX → ParseX).

const (
	v3AcctA = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	v3AcctB = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
)

func mustEncodeBlob(t *testing.T, obj map[string]any) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(obj)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return data
}

func TestParseEscrow_Sequence(t *testing.T) {
	data := mustEncodeBlob(t, map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         v3AcctA,
		"Sequence":        uint32(99),
		"Destination":     v3AcctB,
		"Amount":          "1000000",
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	})

	escrow, err := ParseEscrow(data)
	if err != nil {
		t.Fatalf("ParseEscrow: %v", err)
	}
	if !escrow.HasSequence || escrow.Sequence != 99 {
		t.Fatalf("Sequence not parsed: HasSequence=%v Sequence=%d", escrow.HasSequence, escrow.Sequence)
	}
}

func TestPayChannel_Sequence_RoundTrip(t *testing.T) {
	var owner, dest [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
		dest[i] = byte(i + 100)
	}
	in := &PayChannelData{
		Account:       owner,
		DestinationID: dest,
		Amount:        1000000,
		Balance:       0,
		SettleDelay:   60,
		Sequence:      7,
		HasSequence:   true,
	}

	data, err := SerializePayChannelFromData(in)
	if err != nil {
		t.Fatalf("SerializePayChannelFromData: %v", err)
	}
	out, err := ParsePayChannel(data)
	if err != nil {
		t.Fatalf("ParsePayChannel: %v", err)
	}
	if !out.HasSequence || out.Sequence != 7 {
		t.Fatalf("Sequence not round-tripped: HasSequence=%v Sequence=%d", out.HasSequence, out.Sequence)
	}
}

func TestAccountRoot_PseudoAccountDesignators_RoundTrip(t *testing.T) {
	var vaultID, loanBrokerID [32]byte
	for i := range vaultID {
		vaultID[i] = byte(i + 1)
		loanBrokerID[i] = byte(i + 50)
	}
	in := &AccountRoot{
		Account:      v3AcctA,
		Balance:      1000000,
		Sequence:     1,
		VaultID:      vaultID,
		LoanBrokerID: loanBrokerID,
	}
	if !in.IsPseudoAccount() {
		t.Fatal("IsPseudoAccount should be true when VaultID/LoanBrokerID are set")
	}

	data, err := SerializeAccountRoot(in)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	out, err := ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	if out.VaultID != vaultID {
		t.Fatalf("VaultID not round-tripped: got %x want %x", out.VaultID, vaultID)
	}
	if out.LoanBrokerID != loanBrokerID {
		t.Fatalf("LoanBrokerID not round-tripped: got %x want %x", out.LoanBrokerID, loanBrokerID)
	}
	if !out.HasVaultID() || !out.HasLoanBrokerID() || !out.IsPseudoAccount() {
		t.Fatal("pseudo-account designators not detected after parse")
	}
}

func TestParseSignerList_Owner(t *testing.T) {
	data := mustEncodeBlob(t, map[string]any{
		"LedgerEntryType": "SignerList",
		"Flags":           uint32(0),
		"Owner":           v3AcctA,
		"OwnerNode":       "0",
		"SignerQuorum":    uint32(1),
		"SignerEntries": []any{
			map[string]any{
				"SignerEntry": map[string]any{
					"Account":      v3AcctB,
					"SignerWeight": uint32(1),
				},
			},
		},
		"SignerListID": uint32(0),
	})

	sl, err := ParseSignerList(data)
	if err != nil {
		t.Fatalf("ParseSignerList: %v", err)
	}
	if sl.Owner != v3AcctA {
		t.Fatalf("Owner not parsed: got %q want %q", sl.Owner, v3AcctA)
	}
}

func TestParseOracle_OracleDocumentID(t *testing.T) {
	data := mustEncodeBlob(t, map[string]any{
		"LedgerEntryType":  "Oracle",
		"Owner":            v3AcctA,
		"OracleDocumentID": uint32(5),
		"Provider":         "DEADBEEF",
		"AssetClass":       "0123",
		"LastUpdateTime":   uint32(123456789),
		"OwnerNode":        "0",
		"Flags":            uint32(0),
	})

	oracle, err := ParseOracle(data)
	if err != nil {
		t.Fatalf("ParseOracle: %v", err)
	}
	if !oracle.HasOracleDocumentID || oracle.OracleDocumentID != 5 {
		t.Fatalf("OracleDocumentID not parsed: Has=%v ID=%d", oracle.HasOracleDocumentID, oracle.OracleDocumentID)
	}
}

func TestMPTokenIssuance_MutableFlags_RoundTrip(t *testing.T) {
	var issuer [20]byte
	for i := range issuer {
		issuer[i] = byte(i + 1)
	}
	in := &MPTokenIssuanceData{
		Issuer:          issuer,
		Sequence:        1,
		MutableFlags:    7,
		HasMutableFlags: true,
	}

	data, err := SerializeMPTokenIssuance(in)
	if err != nil {
		t.Fatalf("SerializeMPTokenIssuance: %v", err)
	}
	out, err := ParseMPTokenIssuance(data)
	if err != nil {
		t.Fatalf("ParseMPTokenIssuance: %v", err)
	}
	if !out.HasMutableFlags || out.MutableFlags != 7 {
		t.Fatalf("MutableFlags not round-tripped: Has=%v Flags=%d", out.HasMutableFlags, out.MutableFlags)
	}
}
