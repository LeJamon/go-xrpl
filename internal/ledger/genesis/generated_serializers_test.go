package genesis

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
)

func TestGenesisGeneratedAccountRootBytes(t *testing.T) {
	accountID, address, err := GenerateGenesisAccountID()
	if err != nil {
		t.Fatalf("generate account: %v", err)
	}
	account := &accountRoot{
		Flags:      0x00100000,
		Account:    accountID,
		Sequence:   7,
		Balance:    123456789,
		OwnerCount: 0,
	}

	got, err := serializeAccountRoot(account)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	want, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Flags":             account.Flags,
		"Account":           address,
		"Balance":           fmt.Sprintf("%d", account.Balance),
		"Sequence":          account.Sequence,
		"OwnerCount":        account.OwnerCount,
		"PreviousTxnID":     genesisZeroHash256,
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatalf("encode reference: %v", err)
	}
	assertGenesisRawBytes(t, got, want, "1100612200100000240000000725000000002d000000005500000000000000000000000000000000000000000000000000000000000000006240000000075bcd158114b5f762798a53d543a014caf8b297cff8f2f937e8")

	fields, err := binarycodec.DecodeBytes(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if seq, ok := fields["PreviousTxnLgrSeq"]; !ok || seq != uint32(0) {
		t.Fatalf("PreviousTxnLgrSeq = %#v, want present uint32(0)", seq)
	}
}

func TestGenesisGeneratedFeeSettingsBytes(t *testing.T) {
	tests := []struct {
		name    string
		fees    *feeSettings
		want    map[string]any
		wantHex string
	}{
		{
			name: "legacy",
			fees: newLegacyFeeSettings(
				10,
				10,
				uint32(10*drops.DropsPerXRP),
				uint32(2*drops.DropsPerXRP),
			),
			want: map[string]any{
				"LedgerEntryType":   "FeeSettings",
				"Flags":             uint32(0),
				"BaseFee":           "a",
				"ReferenceFeeUnits": uint32(10),
				"ReserveBase":       uint32(10 * drops.DropsPerXRP),
				"ReserveIncrement":  uint32(2 * drops.DropsPerXRP),
			},
			wantHex: "1100732200000000201e0000000a201f009896802020001e848035000000000000000a",
		},
		{
			name: "modern",
			fees: newFeeSettings(
				drops.NewXRPAmount(10),
				10*drops.DropsPerXRP,
				2*drops.DropsPerXRP,
			),
			want: map[string]any{
				"LedgerEntryType":       "FeeSettings",
				"Flags":                 uint32(0),
				"BaseFeeDrops":          "10",
				"ReserveBaseDrops":      "10000000",
				"ReserveIncrementDrops": "2000000",
			},
			wantHex: "11007322000000006016400000000000000a60174000000000989680601840000000001e8480",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := serializeFeeSettings(tt.fees)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			want, err := binarycodec.EncodeBytes(tt.want)
			if err != nil {
				t.Fatalf("encode reference: %v", err)
			}
			assertGenesisRawBytes(t, got, want, tt.wantHex)
		})
	}
}

func TestGenesisGeneratedFeeSettingsPresentZeroAndErrors(t *testing.T) {
	data, err := serializeFeeSettings(newLegacyFeeSettings(0, 0, 0, 0))
	if err != nil {
		t.Fatalf("serialize present-zero fees: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode present-zero fees: %v", err)
	}
	for _, name := range []string{"ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"} {
		value, ok := fields[name]
		if !ok || value != uint32(0) {
			t.Errorf("%s = %#v, want present uint32(0)", name, value)
		}
	}
	if value, ok := fields["BaseFee"]; !ok || value != "0" {
		t.Errorf("BaseFee = %#v, want present zero UInt64", value)
	}
	if _, err := serializeAccountRoot(nil); err == nil {
		t.Error("nil AccountRoot must fail")
	}
	if _, err := serializeFeeSettings(nil); err == nil {
		t.Error("nil FeeSettings must fail")
	}
}

func TestGenesisGeneratedAmendmentsBytesAndOmissions(t *testing.T) {
	var amendment [32]byte
	for i := range amendment {
		amendment[i] = 0xAB
	}
	got, err := serializeAmendments([][32]byte{amendment})
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	want, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "Amendments",
		"Flags":           uint32(0),
		"Amendments":      []string{fmt.Sprintf("%064X", amendment)},
	})
	if err != nil {
		t.Fatalf("encode reference: %v", err)
	}
	assertGenesisRawBytes(t, got, want, "1100662200000000031320abababababababababababababababababababababababababababababababab")

	empty, err := serializeAmendments(nil)
	if err != nil {
		t.Fatalf("serialize empty amendments: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(empty)
	if err != nil {
		t.Fatalf("decode empty amendments: %v", err)
	}
	values, ok := fields["Amendments"].([]string)
	if !ok || len(values) != 0 {
		t.Fatalf("Amendments = %#v, want present empty Vector256", fields["Amendments"])
	}
	for _, name := range []string{"Majorities", "PreviousTxnID", "PreviousTxnLgrSeq"} {
		if _, ok := fields[name]; ok {
			t.Errorf("genesis Amendments must omit %s", name)
		}
	}
}

func assertGenesisRawBytes(t *testing.T, got, want []byte, wantHex string) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("generated bytes differ from reference\n got: %X\nwant: %X", got, want)
	}
	if gotHex := hex.EncodeToString(got); gotHex != wantHex {
		t.Fatalf("raw bytes = %s, want %s", gotHex, wantHex)
	}
}

const genesisZeroHash256 = "0000000000000000000000000000000000000000000000000000000000000000"
