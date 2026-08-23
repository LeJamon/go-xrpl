package state

import (
	"strconv"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func decodeSLE(t *testing.T, data []byte) map[string]any {
	t.Helper()
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return fields
}

func soeToUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case int:
		return uint64(n), true
	case int64:
		return uint64(n), true
	case float64:
		return uint64(n), true
	case string:
		// UInt64 fields (OwnerNode, DestinationNode) decode as hex strings.
		u, err := strconv.ParseUint(n, 16, 64)
		if err != nil {
			return 0, false
		}
		return u, true
	default:
		return 0, false
	}
}

// TestSerializeFeeSettings_FlagsPresent asserts sfFlags (soeREQUIRED common
// field) is serialized at its default 0 for both the modern and legacy field
// layouts. rippled emits Flags=0 on every FeeSettings (SLE template); the
// genesis serializer already does so and the runtime one must match.
func TestSerializeFeeSettings_FlagsPresent(t *testing.T) {
	cases := map[string]*FeeSettings{
		"modern": {
			XRPFeesMode:           true,
			BaseFeeDrops:          10,
			ReserveBaseDrops:      200_000_000,
			ReserveIncrementDrops: 50_000_000,
		},
		"legacy": {
			XRPFeesMode:       false,
			BaseFee:           10,
			ReferenceFeeUnits: 10,
			ReserveBase:       10_000_000,
			ReserveIncrement:  2_000_000,
		},
	}
	for name, fs := range cases {
		data, err := SerializeFeeSettings(fs)
		if err != nil {
			t.Fatalf("%s: serialize: %v", name, err)
		}
		fields := decodeSLE(t, data)
		f, ok := fields["Flags"]
		if !ok {
			t.Fatalf("%s: Flags must be present (soeREQUIRED)", name)
		}
		if v, _ := soeToUint64(f); v != 0 {
			t.Errorf("%s: Flags = %v, want 0", name, f)
		}
	}
}

func TestParseFeeSettingsGeneratedDecoderShapes(t *testing.T) {
	cases := map[string]*FeeSettings{
		"modern present zeros": {
			XRPFeesMode:       true,
			PreviousTxnID:     [32]byte{1},
			PreviousTxnLgrSeq: 9,
		},
		"legacy": {
			BaseFee:           10,
			ReferenceFeeUnits: 10,
			ReserveBase:       10_000_000,
			ReserveIncrement:  2_000_000,
			PreviousTxnID:     [32]byte{1},
			PreviousTxnLgrSeq: 9,
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := SerializeFeeSettings(want)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			got, err := ParseFeeSettings(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.XRPFeesMode != want.XRPFeesMode ||
				got.BaseFeeDrops != want.BaseFeeDrops ||
				got.ReserveBaseDrops != want.ReserveBaseDrops ||
				got.ReserveIncrementDrops != want.ReserveIncrementDrops ||
				got.BaseFee != want.BaseFee ||
				got.ReferenceFeeUnits != want.ReferenceFeeUnits ||
				got.ReserveBase != want.ReserveBase ||
				got.ReserveIncrement != want.ReserveIncrement ||
				got.PreviousTxnID != want.PreviousTxnID ||
				got.PreviousTxnLgrSeq != want.PreviousTxnLgrSeq {
				t.Fatalf("parsed FeeSettings differs:\nwant %+v\n got %+v", want, got)
			}
			roundTrip, err := SerializeFeeSettings(got)
			if err != nil {
				t.Fatalf("reserialize: %v", err)
			}
			if string(roundTrip) != string(data) {
				t.Fatalf("round-trip bytes differ:\nwant %X\n got %X", data, roundTrip)
			}
		})
	}
}

// TestSerializeSignerList_FlagsAlwaysPresent asserts sfFlags is serialized even
// when 0 (the MultiSignReserve-disabled path). rippled's writeSignersToSLE only
// *overwrites* the template default; the field is present at 0 regardless.
func TestSerializeSignerList_FlagsAlwaysPresent(t *testing.T) {
	addrA, _ := EncodeAccountID([20]byte{0x01})
	addrB, _ := EncodeAccountID([20]byte{0x02})
	entries := []SignerEntry{
		{Account: addrA, SignerWeight: 1},
		{Account: addrB, SignerWeight: 1},
	}

	data0, err := SerializeSignerList(2, entries, 0, false, 0, nil)
	if err != nil {
		t.Fatalf("serialize flags=0: %v", err)
	}
	f0 := decodeSLE(t, data0)
	v, ok := f0["Flags"]
	if !ok {
		t.Fatal("Flags must be present even when 0 (soeREQUIRED)")
	}
	if u, _ := soeToUint64(v); u != 0 {
		t.Errorf("Flags = %v, want 0", v)
	}

	data1, err := SerializeSignerList(2, entries, LsfOneOwnerCount, false, 0, nil)
	if err != nil {
		t.Fatalf("serialize flags=LsfOneOwnerCount: %v", err)
	}
	f1 := decodeSLE(t, data1)
	v1, ok := f1["Flags"]
	if !ok {
		t.Fatal("Flags must be present when non-zero")
	}
	if u, _ := soeToUint64(v1); u != uint64(LsfOneOwnerCount) {
		t.Errorf("Flags = %v, want %d", v1, LsfOneOwnerCount)
	}
}

// TestSerializeCheck_DestinationNodeAlwaysPresent asserts sfDestinationNode
// (soeREQUIRED on ltCHECK) is serialized even at its default 0, and round-trips.
func TestSerializeCheck_DestinationNodeAlwaysPresent(t *testing.T) {
	mk := func(hasNode bool, node uint64) *CheckData {
		c := &CheckData{
			Account:         [20]byte{0x01},
			DestinationID:   [20]byte{0x02},
			SendMax:         1_000_000,
			IsNativeSendMax: true,
			SendMaxAmount:   NewXRPAmountFromInt(1_000_000),
			Sequence:        5,
			OwnerNode:       0,
		}
		c.HasDestNode = hasNode
		c.DestinationNode = node
		return c
	}

	data0, err := SerializeCheckFromData(mk(false, 0))
	if err != nil {
		t.Fatalf("serialize !HasDestNode: %v", err)
	}
	f0 := decodeSLE(t, data0)
	v, ok := f0["DestinationNode"]
	if !ok {
		t.Fatal("DestinationNode must be present even at 0 (soeREQUIRED)")
	}
	if u, _ := soeToUint64(v); u != 0 {
		t.Errorf("DestinationNode = %v, want 0", v)
	}
	parsed, err := ParseCheck(data0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.HasDestNode || parsed.DestinationNode != 0 {
		t.Errorf("round-trip DestinationNode: HasDestNode=%v node=%d", parsed.HasDestNode, parsed.DestinationNode)
	}

	data1, err := SerializeCheckFromData(mk(true, 7))
	if err != nil {
		t.Fatalf("serialize HasDestNode: %v", err)
	}
	f1 := decodeSLE(t, data1)
	v1, _ := soeToUint64(f1["DestinationNode"])
	if v1 != 7 {
		t.Errorf("DestinationNode = %v, want 7", f1["DestinationNode"])
	}
}

func TestSerializeCheck_PreservesThreading(t *testing.T) {
	check := &CheckData{
		Account:           [20]byte{0x01},
		DestinationID:     [20]byte{0x02},
		SendMax:           1,
		SendMaxAmount:     NewXRPAmountFromInt(1),
		IsNativeSendMax:   true,
		Sequence:          1,
		PreviousTxnID:     [32]byte{0xAB},
		PreviousTxnLgrSeq: 7,
	}

	data, err := SerializeCheckFromData(check)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	parsed, err := ParseCheck(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	roundTrip, err := SerializeCheckFromData(parsed)
	if err != nil {
		t.Fatalf("re-serialize: %v", err)
	}
	if string(roundTrip) != string(data) {
		t.Fatal("Check threading fields changed across parse and serialize")
	}
}

func TestSerializeMPToken_LockedAmountPresentAtZero(t *testing.T) {
	zero := uint64(0)
	cases := map[string][]byte{}

	issuance, err := SerializeMPTokenIssuance(&MPTokenIssuanceData{
		Issuer:            [20]byte{0x01},
		Sequence:          1,
		OutstandingAmount: 0,
		LockedAmount:      &zero,
	})
	if err != nil {
		t.Fatalf("serialize issuance: %v", err)
	}
	cases["issuance"] = issuance

	token, err := SerializeMPToken(&MPTokenData{
		Account:      [20]byte{0x02},
		LockedAmount: &zero,
	})
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}
	cases["token"] = token

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			fields := decodeSLE(t, data)
			if value, ok := fields["LockedAmount"]; !ok || value != "0" {
				t.Fatalf("LockedAmount = %v, present = %v; want present zero", value, ok)
			}
		})
	}
}

func TestSerializeNFTokenPage_EmptyTokensPresent(t *testing.T) {
	data, err := SerializeNFTokenPage(&NFTokenPageData{})
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	fields := decodeSLE(t, data)
	tokens, ok := fields["NFTokens"]
	if !ok {
		t.Fatal("required NFTokens array was omitted")
	}
	if values, ok := tokens.([]any); !ok || len(values) != 0 {
		t.Fatalf("NFTokens = %#v, want empty array", tokens)
	}
}
