package entry

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

const (
	innerTemplateAccount = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	innerTemplateHash    = "0000000000000000000000000000000000000000000000000000000000000000"
)

func signerListBlob(t *testing.T, signer any) []byte {
	t.Helper()
	blob, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "SignerList",
		"Flags":             uint32(0),
		"OwnerNode":         "0",
		"SignerQuorum":      uint32(1),
		"SignerEntries":     []any{signer},
		"SignerListID":      uint32(0),
		"PreviousTxnID":     innerTemplateHash,
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatalf("encode SignerList: %v", err)
	}
	return blob
}

func TestSignerEntriesDecodeEnforcesInnerObjectTemplate(t *testing.T) {
	valid := map[string]any{"SignerEntry": map[string]any{
		"Account":      innerTemplateAccount,
		"SignerWeight": uint32(1),
	}}
	tests := []struct {
		name    string
		signer  any
		wantErr string
	}{
		{name: "valid", signer: valid},
		{
			name: "wrong wrapper",
			signer: map[string]any{"NFToken": map[string]any{
				"NFTokenID": innerTemplateHash,
			}},
			wantErr: "wrong wrapper",
		},
		{
			name: "missing required field",
			signer: map[string]any{"SignerEntry": map[string]any{
				"Account": innerTemplateAccount,
			}},
			wantErr: "required field SignerWeight is missing",
		},
		{
			name: "unknown field",
			signer: map[string]any{"SignerEntry": map[string]any{
				"Account":      innerTemplateAccount,
				"SignerWeight": uint32(1),
				"Sequence":     uint32(1),
			}},
			wantErr: "field Sequence is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var entry SignerList
			err := entry.Decode(signerListBlob(t, tt.signer))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Decode error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestSignerEntriesDecodeRejectsDuplicateInnerField(t *testing.T) {
	blob := signerListBlob(t, map[string]any{"SignerEntry": map[string]any{
		"Account":      innerTemplateAccount,
		"SignerWeight": uint32(1),
	}})
	duplicate, err := binarycodec.EncodeBytes(map[string]any{"SignerWeight": uint32(2)})
	if err != nil {
		t.Fatalf("encode duplicate field: %v", err)
	}
	end := bytes.Index(blob, []byte{0xE1, 0xF1})
	if end < 0 {
		t.Fatal("SignerEntry object terminator not found")
	}
	malformed := make([]byte, 0, len(blob)+len(duplicate))
	malformed = append(malformed, blob[:end]...)
	malformed = append(malformed, duplicate...)
	malformed = append(malformed, blob[end:]...)

	var entry SignerList
	if err := entry.Decode(malformed); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("Decode error = %v, want duplicate field", err)
	}
}

func TestInnerObjectTemplateRejectsWrongDecodedType(t *testing.T) {
	err := validateDecodedSTArray("SignerEntries", []any{map[string]any{
		"SignerEntry": map[string]any{
			"Account":      innerTemplateAccount,
			"SignerWeight": uint32(1),
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "want UInt16") {
		t.Fatalf("validation error = %v, want wrong-type failure", err)
	}
}

func TestSignerListEncodeEnforcesInnerObjectTemplate(t *testing.T) {
	var signerList SignerList
	signerList.SetOwnerNode("0")
	signerList.SetSignerQuorum(1)
	signerList.SetSignerEntries([]any{map[string]any{
		"SignerEntry": map[string]any{"Account": innerTemplateAccount},
	}})
	signerList.SetSignerListID(0)
	signerList.SetFlags(0)

	if _, err := signerList.Encode(); err == nil || !strings.Contains(err.Error(), "required field SignerWeight is missing") {
		t.Fatalf("Encode error = %v, want missing SignerWeight", err)
	}
}

func TestPriceDataRejectsExplicitDefault(t *testing.T) {
	err := validateSTArrayForEncode("PriceDataSeries", []any{map[string]any{
		"PriceData": map[string]any{
			"BaseAsset":  "0000000000000000000000005553440000000000",
			"QuoteAsset": "0000000000000000000000004555520000000000",
			"Scale":      0,
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "default field Scale is explicitly set") {
		t.Fatalf("validation error = %v, want explicit-default failure", err)
	}
}

func TestInnerObjectEncodeAcceptsCodecIntegerInputs(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		value     []any
	}{
		{
			name:      "SignerWeight",
			fieldName: "SignerEntries",
			value: []any{map[string]any{"SignerEntry": map[string]any{
				"Account":      innerTemplateAccount,
				"SignerWeight": uint32(1),
			}}},
		},
		{
			name:      "TradingFee",
			fieldName: "VoteSlots",
			value: []any{map[string]any{"VoteEntry": map[string]any{
				"Account":    innerTemplateAccount,
				"TradingFee": uint32(1),
				"VoteWeight": uint32(1),
			}}},
		},
		{
			name: "DiscountedFee",
			value: []any{map[string]any{"AuctionSlot": map[string]any{
				"Account":       innerTemplateAccount,
				"Expiration":    uint32(1),
				"DiscountedFee": uint32(1),
				"Price":         "1",
			}}},
		},
		{
			name:      "Scale",
			fieldName: "PriceDataSeries",
			value: []any{map[string]any{"PriceData": map[string]any{
				"BaseAsset":  "0000000000000000000000005553440000000000",
				"QuoteAsset": "0000000000000000000000004555520000000000",
				"Scale":      uint32(1),
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.fieldName == "" {
				err = validateInnerObjectForEncode("AuctionSlot", tt.value[0].(map[string]any)["AuctionSlot"].(map[string]any))
			} else {
				err = validateSTArrayForEncode(tt.fieldName, tt.value)
			}
			if err != nil {
				t.Fatalf("validation error = %v, want accepted codec integer", err)
			}
		})
	}
}

func TestAuctionSlotAuthAccountsNormalization(t *testing.T) {
	slot := map[string]any{
		"Account":    innerTemplateAccount,
		"Expiration": uint32(1),
		"Price":      "1",
		"AuthAccounts": []map[string]any{{
			"AuthAccount": map[string]any{"Account": innerTemplateAccount},
		}},
	}
	if err := validateInnerObjectForEncode("AuctionSlot", slot); err != nil {
		t.Fatalf("validate []map AuthAccounts: %v", err)
	}

	slot["AuthAccounts"] = []string{innerTemplateAccount}
	if err := validateInnerObjectForEncode("AuctionSlot", slot); err == nil || !strings.Contains(err.Error(), "want array") {
		t.Fatalf("validation error = %v, want AuthAccounts type failure", err)
	}
}

func TestSignerListEncodeOmitsDiscardableInnerField(t *testing.T) {
	var signerList SignerList
	signerList.SetOwnerNode("0")
	signerList.SetSignerQuorum(1)
	signerList.SetSignerEntries([]any{map[string]any{
		"SignerEntry": map[string]any{
			"Account":      innerTemplateAccount,
			"SignerWeight": uint32(1),
			"hash":         innerTemplateHash,
		},
	}})
	signerList.SetSignerListID(0)
	signerList.SetFlags(0)

	blob, err := signerList.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var decoded SignerList
	if err := decoded.Decode(blob); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	signerEntry := decoded.ToMap()["SignerEntries"].([]any)[0].(map[string]any)["SignerEntry"].(map[string]any)
	if _, ok := signerEntry["hash"]; ok {
		t.Fatal("discardable hash field was serialized")
	}

	signerList.SetSignerEntries([]any{map[string]any{
		"SignerEntry": map[string]any{
			"Account":      innerTemplateAccount,
			"SignerWeight": uint32(1),
			"Sequence":     uint32(1),
		},
	}})
	if _, err := signerList.Encode(); err == nil || !strings.Contains(err.Error(), "field Sequence is not allowed") {
		t.Fatalf("Encode error = %v, want non-discardable field rejection", err)
	}
}

func TestInnerObjectEncodeAcceptsUInt64FieldShapes(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		fieldName string
		object    map[string]any
	}{
		{
			name:      "XChainClaimID",
			template:  "XChainClaimAttestationCollectionElement",
			fieldName: "XChainClaimID",
			object: map[string]any{
				"AttestationSignerAccount": innerTemplateAccount,
				"PublicKey":                "",
				"Signature":                "",
				"Amount":                   "1",
				"Account":                  innerTemplateAccount,
				"AttestationRewardAccount": innerTemplateAccount,
				"WasLockingChainSend":      1,
			},
		},
		{
			name:      "XChainAccountCreateCount",
			template:  "XChainCreateAccountAttestationCollectionElement",
			fieldName: "XChainAccountCreateCount",
			object: map[string]any{
				"AttestationSignerAccount": innerTemplateAccount,
				"PublicKey":                "",
				"Signature":                "",
				"Amount":                   "1",
				"Account":                  innerTemplateAccount,
				"AttestationRewardAccount": innerTemplateAccount,
				"WasLockingChainSend":      1,
				"Destination":              innerTemplateAccount,
				"SignatureReward":          "1",
			},
		},
		{
			name:      "AssetPrice",
			template:  "PriceData",
			fieldName: "AssetPrice",
			object: map[string]any{
				"BaseAsset":  "0000000000000000000000005553440000000000",
				"QuoteAsset": "0000000000000000000000004555520000000000",
			},
		},
		{
			name:      "BookNode",
			template:  "Book",
			fieldName: "BookNode",
			object: map[string]any{
				"BookDirectory": innerTemplateHash,
			},
		},
	}

	for _, tt := range tests {
		for _, value := range []any{uint64(42), "2a"} {
			t.Run(fmt.Sprintf("%s/%T", tt.name, value), func(t *testing.T) {
				object := make(map[string]any, len(tt.object)+1)
				for name, fieldValue := range tt.object {
					object[name] = fieldValue
				}
				object[tt.fieldName] = value
				if err := validateInnerObjectForEncode(tt.template, object); err != nil {
					t.Fatalf("validation error = %v, want accepted UInt64 input", err)
				}
			})
		}
	}
}

func TestInnerObjectEncoderNumericBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		kind  innerValueKind
		value any
		valid bool
	}{
		{name: "UInt8 integral float", kind: innerUInt8, value: float64(255), valid: true},
		{name: "UInt8 overflow", kind: innerUInt8, value: float64(256)},
		{name: "UInt8 fractional", kind: innerUInt8, value: 1.5},
		{name: "UInt8 negative", kind: innerUInt8, value: float64(-1)},
		{name: "UInt16 integral float", kind: innerUInt16, value: float64(65535), valid: true},
		{name: "UInt16 overflow", kind: innerUInt16, value: float64(65536)},
		{name: "UInt32 integral float", kind: innerUInt32, value: float64(math.MaxUint32), valid: true},
		{name: "UInt32 overflow", kind: innerUInt32, value: float64(math.MaxUint32) + 1},
		{name: "UInt64 numeric JSON maximum", kind: innerUInt64, value: float64(math.MaxUint32), valid: true},
		{name: "UInt64 numeric JSON overflow", kind: innerUInt64, value: float64(math.MaxUint32) + 1},
		{name: "UInt64 maximum hex string", kind: innerUInt64, value: "ffffffffffffffff", valid: true},
		{name: "UInt64 overflowing hex string", kind: innerUInt64, value: "10000000000000000"},
		{name: "UInt64 malformed string", kind: innerUInt64, value: "xyz"},
		{name: "UInt64 maximum typed value", kind: innerUInt64, value: uint64(math.MaxUint64), valid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validInnerValue(tt.kind, tt.value, true); got != tt.valid {
				t.Fatalf("validInnerValue(%v, %T(%v)) = %t, want %t", tt.kind, tt.value, tt.value, got, tt.valid)
			}
		})
	}
}

func TestPermissionValueUsesCodecContract(t *testing.T) {
	if err := validateInnerObjectForEncode("Permission", map[string]any{
		"PermissionValue": float64(math.MaxUint32),
	}); err != nil {
		t.Fatalf("encoder rejected numeric PermissionValue: %v", err)
	}
	if err := validateInnerObjectForEncode("Permission", map[string]any{
		"PermissionValue": "Payment",
	}); err != nil {
		t.Fatalf("encoder rejected symbolic PermissionValue: %v", err)
	}
	if err := validateDecodedInnerObject("Permission", map[string]any{
		"PermissionValue": "Payment",
	}); err != nil {
		t.Fatalf("decoder rejected canonical PermissionValue: %v", err)
	}
	if err := validateDecodedInnerObject("Permission", map[string]any{
		"PermissionValue": uint32(1),
	}); err == nil || !strings.Contains(err.Error(), "want PermissionValue") {
		t.Fatalf("decoder error = %v, want strict PermissionValue failure", err)
	}
}

func TestUInt64DecoderContractRemainsCanonical(t *testing.T) {
	priceData := map[string]any{
		"BaseAsset":  "0000000000000000000000005553440000000000",
		"QuoteAsset": "0000000000000000000000004555520000000000",
		"AssetPrice": "2a",
	}
	if err := validateDecodedInnerObject("PriceData", priceData); err != nil {
		t.Fatalf("decoder rejected canonical UInt64 string: %v", err)
	}
	priceData["AssetPrice"] = uint64(42)
	if err := validateDecodedInnerObject("PriceData", priceData); err == nil || !strings.Contains(err.Error(), "want UInt64") {
		t.Fatalf("decoder error = %v, want strict UInt64 failure", err)
	}
}

func TestDiscardableInnerFieldsValidateDeclaredType(t *testing.T) {
	for _, fieldName := range []string{"hash", "index"} {
		t.Run(fieldName, func(t *testing.T) {
			object := map[string]any{
				"Account":      innerTemplateAccount,
				"SignerWeight": uint32(1),
				fieldName:      "not-a-hash",
			}
			err := validateInnerObjectForEncode("SignerEntry", object)
			if err == nil || !strings.Contains(err.Error(), "discardable field "+fieldName) {
				t.Fatalf("validation error = %v, want malformed %s rejection", err, fieldName)
			}
		})
	}
}
