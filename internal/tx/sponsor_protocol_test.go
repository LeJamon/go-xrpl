package tx

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestSponsorTransactionFormats(t *testing.T) {
	common := FormatCommonFields()
	wantCommonTail := []FormatField{
		{Name: "Sponsor", Style: 1},
		{Name: "SponsorFlags", Style: 1},
		{Name: "SponsorSignature", Style: 1},
	}
	if got := common[len(common)-len(wantCommonTail):]; !reflect.DeepEqual(got, wantCommonTail) {
		t.Fatalf("Sponsor common fields = %#v, want %#v", got, wantCommonTail)
	}

	formats := FormatTemplates()
	tests := []struct {
		name string
		want []FormatField
	}{
		{
			name: "SponsorshipTransfer",
			want: []FormatField{
				{Name: "ObjectID", Style: 1},
				{Name: "Sponsee", Style: 1},
			},
		},
		{
			name: "SponsorshipSet",
			want: []FormatField{
				{Name: "CounterpartySponsor", Style: 1},
				{Name: "Sponsee", Style: 1},
				{Name: "FeeAmountDelta", Style: 1},
				{Name: "MaxFee", Style: 1},
				{Name: "RemainingOwnerCountDelta", Style: 1},
			},
		},
	}
	for _, test := range tests {
		if got := formats[test.name]; !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s format = %#v, want %#v", test.name, got, test.want)
		}
	}
}

func TestSponsorCommonModelAndFlags(t *testing.T) {
	flags := SpfSponsorFee | SpfSponsorReserve
	common := &Common{
		Account:         testAccount,
		TransactionType: "Payment",
		Sponsor:         testDestination,
		SponsorFlags:    &flags,
		SponsorSignature: &SponsorSignature{
			SigningPubKey: "ED0000000000000000000000000000000000000000000000000000000000000001",
			TxnSignature:  "DEADBEEF",
		},
	}
	fields := common.ToMap()
	if fields["Sponsor"] != testDestination || fields["SponsorFlags"] != flags {
		t.Fatalf("Sponsor common fields = (%#v, %#v)", fields["Sponsor"], fields["SponsorFlags"])
	}
	if _, ok := fields["SponsorSignature"].(map[string]any); !ok {
		t.Fatalf("SponsorSignature flattened as %#v", fields["SponsorSignature"])
	}
	if SpfSponsorFee != 1 || SpfSponsorReserve != 2 || SpfSponsorFlagMask != ^uint32(3) {
		t.Fatalf("Sponsor flag constants = (0x%X, 0x%X, 0x%X)", SpfSponsorFee, SpfSponsorReserve, SpfSponsorFlagMask)
	}
}

func TestSponsorTransactionCodecRoundTrip(t *testing.T) {
	tests := []struct {
		fields map[string]any
		golden string
	}{
		{
			fields: map[string]any{
				"TransactionType": "SponsorshipTransfer",
				"Account":         testAccount,
				"Sequence":        uint32(1),
				"Fee":             "10",
				"SigningPubKey":   "",
				"ObjectID":        "1111111111111111111111111111111111111111111111111111111111111111",
				"Sponsee":         testDestination,
			},
			golden: "12005A24000000015029111111111111111111111111111111111111111111111111111111111111111168400000000000000A73008114B5F762798A53D543A014CAF8B297CFF8F2F937E8801F14F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B90F",
		},
		{
			fields: map[string]any{
				"TransactionType":          "SponsorshipSet",
				"Account":                  testAccount,
				"Sequence":                 uint32(1),
				"Fee":                      "10",
				"SigningPubKey":            "",
				"CounterpartySponsor":      testDestination,
				"Sponsee":                  testAccount,
				"FeeAmountDelta":           "1000",
				"MaxFee":                   "100",
				"RemainingOwnerCountDelta": int(2),
			},
			golden: "12005B240000000168400000000000000A60214000000000000064602240000000000003E873008114B5F762798A53D543A014CAF8B297CFF8F2F937E8801E14F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B90F801F14B5F762798A53D543A014CAF8B297CFF8F2F937E8A200000002",
		},
	}
	for _, test := range tests {
		name := test.fields["TransactionType"].(string)
		t.Run(name, func(t *testing.T) {
			encoded, err := binarycodec.Encode(test.fields)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if encoded != test.golden {
				t.Fatalf("canonical bytes = %s, want %s", encoded, test.golden)
			}
			decoded, err := binarycodec.Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			for key, want := range test.fields {
				if got := decoded[key]; !reflect.DeepEqual(got, want) {
					t.Errorf("%s round-trip = %#v, want %#v", key, got, want)
				}
			}

			raw, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatalf("decode encoded transaction: %v", err)
			}
			parsed, err := ParseFromBinary(raw)
			if err != nil {
				t.Fatalf("ParseFromBinary: %v", err)
			}
			wantType, _ := TypeFromName(name)
			if parsed.TxType() != wantType {
				t.Fatalf("parsed type = %v, want %v", parsed.TxType(), wantType)
			}
			if !bytes.Equal(parsed.GetRawBytes(), raw) {
				t.Fatal("ParseFromBinary did not preserve canonical bytes")
			}
		})
	}
}

func TestSponsorMultisignatureWithoutSigningPubKeyRoundTrip(t *testing.T) {
	fields := baseCommon("Payment")
	fields["Destination"] = testDestination
	fields["Amount"] = "1"
	fields["Sponsor"] = testDestination
	fields["SponsorFlags"] = SpfSponsorFee
	fields["SponsorSignature"] = map[string]any{
		"Signers": []map[string]any{
			{
				"Signer": map[string]any{
					"Account":       testAccount,
					"SigningPubKey": "ED0000000000000000000000000000000000000000000000000000000000000002",
					"TxnSignature":  "AA",
				},
			},
			{
				"Signer": map[string]any{
					"Account":       testDestination,
					"SigningPubKey": "ED0000000000000000000000000000000000000000000000000000000000000003",
					"TxnSignature":  "BB",
				},
			},
		},
	}

	raw, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("encode transaction: %v", err)
	}
	parsed, err := ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("parse transaction: %v", err)
	}
	if !bytes.Equal(parsed.GetRawBytes(), raw) {
		t.Fatal("ParseFromBinary did not preserve canonical bytes")
	}
	flat, err := parsed.Flatten()
	if err != nil {
		t.Fatalf("flatten transaction: %v", err)
	}
	sponsor, ok := flat["SponsorSignature"].(map[string]any)
	if !ok {
		t.Fatalf("flattened SponsorSignature = %#v", flat["SponsorSignature"])
	}
	if _, present := sponsor["SigningPubKey"]; present {
		t.Fatal("flattened SponsorSignature injected SigningPubKey")
	}
	wantID := sha512half.Sum(append([]byte{0x54, 0x58, 0x4E, 0x00}, raw...))
	gotID, err := ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("compute transaction hash: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("transaction hash = %X, want %X", gotID, wantID)
	}
}

func TestNestedSignatureSigningPubKeyPresence(t *testing.T) {
	type presenceAwareSignature interface {
		ToMap() map[string]any
		MarkFieldPresent(string)
	}
	tests := []struct {
		name string
		new  func() presenceAwareSignature
	}{
		{
			name: "counterparty",
			new:  func() presenceAwareSignature { return &CounterpartySignature{} },
		},
		{
			name: "sponsor",
			new:  func() presenceAwareSignature { return &SponsorSignature{} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signature := test.new()
			if _, present := signature.ToMap()["SigningPubKey"]; present {
				t.Fatal("empty unmarked SigningPubKey was emitted")
			}
			signature.MarkFieldPresent("SigningPubKey")
			if value, present := signature.ToMap()["SigningPubKey"]; !present || value != "" {
				t.Fatalf("explicit empty SigningPubKey = (%#v, %v)", value, present)
			}
		})
	}
}

func TestSponsorSignedDeltaCodec(t *testing.T) {
	fields := baseCommon("SponsorshipSet")
	fields["CounterpartySponsor"] = testDestination
	fields["Sponsee"] = testAccount
	fields["FeeAmountDelta"] = "-1000"
	fields["RemainingOwnerCountDelta"] = int(-2)

	encoded, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := binarycodec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := decoded["FeeAmountDelta"]; got != "-1000" {
		t.Fatalf("FeeAmountDelta round-trip = %#v, want -1000", got)
	}
	if got := decoded["RemainingOwnerCountDelta"]; got != int(-2) {
		t.Fatalf("RemainingOwnerCountDelta round-trip = %#v, want -2", got)
	}
}

func TestSponsorTransactionTemplatesRejectForeignFields(t *testing.T) {
	for _, name := range []string{"SponsorshipTransfer", "SponsorshipSet"} {
		t.Run(name, func(t *testing.T) {
			fields := baseCommon(name)
			fields["Amount"] = "1"
			encoded, err := binarycodec.Encode(fields)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			raw, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatalf("decode transaction: %v", err)
			}
			_, err = ParseFromBinary(raw)
			if result, ok := ter.AsResultError(err); !ok || result.Code != ter.TemMALFORMED {
				t.Fatalf("ParseFromBinary error = %v, want temMALFORMED", err)
			}
			jsonData, err := json.Marshal(fields)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if _, err := ParseJSON(jsonData); err == nil {
				t.Fatal("ParseJSON accepted foreign SponsorSignature field")
			}
		})
	}
}

func TestSponsorSignatureTemplateRejectsForeignFields(t *testing.T) {
	tests := []struct {
		name      string
		signature map[string]any
	}{
		{
			name: "direct field",
			signature: map[string]any{
				"SigningPubKey": "",
				"Sequence":      uint32(1),
			},
		},
		{
			name: "nested Signer field",
			signature: map[string]any{
				"Signers": []any{
					map[string]any{
						"Signer": map[string]any{
							"Sequence": uint32(1),
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := baseCommon("Payment")
			fields["Destination"] = testDestination
			fields["Amount"] = "1"
			fields["SponsorSignature"] = test.signature
			encoded, err := binarycodec.Encode(fields)
			if err == nil {
				raw, err := hex.DecodeString(encoded)
				if err != nil {
					t.Fatalf("decode transaction: %v", err)
				}
				_, err = ParseFromBinary(raw)
				if result, ok := ter.AsResultError(err); !ok || result.Code != ter.TemMALFORMED {
					t.Fatalf("ParseFromBinary error = %v, want temMALFORMED", err)
				}
			}
			jsonData, err := json.Marshal(fields)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if _, err := ParseJSON(jsonData); err == nil {
				t.Fatal("ParseJSON accepted foreign transaction field")
			}
		})
	}
}

func TestSponsorCommonFieldsJSONRoundTrip(t *testing.T) {
	flags := SpfSponsorFee | SpfSponsorReserve
	sequence := uint32(1)
	tx := NewBaseTx(TypeSponsorshipSet, testAccount)
	tx.Fee = "10"
	tx.Sequence = &sequence
	tx.SigningPubKey = "ED0000000000000000000000000000000000000000000000000000000000000001"
	tx.Sponsor = testDestination
	tx.SponsorFlags = &flags
	tx.SponsorSignature = &SponsorSignature{
		SigningPubKey: "ED0000000000000000000000000000000000000000000000000000000000000001",
		TxnSignature:  "DEADBEEF",
	}

	encoded, err := ToJSON(tx)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	parsed, err := ParseJSON(encoded)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	got := parsed.GetCommon()
	if got.Sponsor != tx.Sponsor || got.SponsorFlags == nil || *got.SponsorFlags != flags {
		t.Fatalf("Sponsor fields = (%q, %#v), want (%q, %d)", got.Sponsor, got.SponsorFlags, tx.Sponsor, flags)
	}
	if got.SponsorSignature == nil ||
		got.SponsorSignature.SigningPubKey != tx.SponsorSignature.SigningPubKey ||
		got.SponsorSignature.TxnSignature != tx.SponsorSignature.TxnSignature {
		t.Fatalf("SponsorSignature = %#v, want values from %#v", got.SponsorSignature, tx.SponsorSignature)
	}
	if !got.SponsorSignature.HasField("SigningPubKey") || !got.SponsorSignature.HasField("TxnSignature") {
		t.Fatal("parsed SponsorSignature did not retain field presence")
	}
}

func TestSponsorSignatureExcludedFromSigningData(t *testing.T) {
	fields := baseCommon("Payment")
	fields["Destination"] = testDestination
	fields["Amount"] = "1000"
	fields["Sponsor"] = testDestination
	fields["SponsorFlags"] = SpfSponsorFee

	without, err := binarycodec.EncodeForSigning(fields)
	if err != nil {
		t.Fatalf("EncodeForSigning without SponsorSignature: %v", err)
	}
	fields["SponsorSignature"] = map[string]any{
		"SigningPubKey": "ED0000000000000000000000000000000000000000000000000000000000000001",
		"TxnSignature":  "DEADBEEF",
	}
	with, err := binarycodec.EncodeForSigning(fields)
	if err != nil {
		t.Fatalf("EncodeForSigning with SponsorSignature: %v", err)
	}
	if with != without {
		t.Fatalf("SponsorSignature changed ordinary signing payload:\nwithout=%s\nwith=%s", without, with)
	}

	wire, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := binarycodec.Decode(wire)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	signature, ok := decoded["SponsorSignature"].(map[string]any)
	if !ok {
		t.Fatalf("decoded SponsorSignature = %#v", decoded["SponsorSignature"])
	}
	if signature["TxnSignature"] != "DEADBEEF" {
		t.Fatalf("SponsorSignature.TxnSignature = %#v, want DEADBEEF", signature["TxnSignature"])
	}
}
