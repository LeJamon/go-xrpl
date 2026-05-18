package ledgerfields

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types"
)

// TestReadAmountAny_MatchesCodec exercises the inline IOU/MPT decoders
// against the codec's reference path for cases the metadata-parity test
// doesn't exercise: zero, negative, non-standard currency, MPT.
func TestReadAmountAny_MatchesCodec(t *testing.T) {
	cases := []struct {
		name string
		// Serialize input via codec, then decode through both paths.
		jsonValue any
	}{
		{
			name:      "IOU_positive_standard",
			jsonValue: map[string]any{"value": "100", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "IOU_fractional",
			jsonValue: map[string]any{"value": "0.0012345", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "IOU_negative",
			jsonValue: map[string]any{"value": "-42.5", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "IOU_large_integer",
			jsonValue: map[string]any{"value": "9999999999999999", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "IOU_zero",
			jsonValue: map[string]any{"value": "0", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "IOU_non_standard_currency",
			jsonValue: map[string]any{"value": "1", "currency": "0158415500000000C1F76FF6ECB0BAC600000000", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		},
		{
			name:      "MPT_positive",
			jsonValue: map[string]any{"value": "1234567890", "mpt_issuance_id": "00000000ABCDEF0123456789FEDCBA9876543210FEDCBA98"},
		},
		{
			name:      "MPT_negative",
			jsonValue: map[string]any{"value": "-1234567890", "mpt_issuance_id": "00000000ABCDEF0123456789FEDCBA9876543210FEDCBA98"},
		},
		{
			name:      "XRP_drops",
			jsonValue: "1000000",
		},
	}

	amt := &types.Amount{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := amt.FromJSON(tc.jsonValue)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			// Reference: codec round-trip.
			refParser := serdes.NewBinaryParser(encoded, definitions.Get())
			refDecoded, err := amt.ToJSON(refParser)
			if err != nil {
				t.Fatalf("codec ToJSON: %v", err)
			}

			// Inline: streamReader path.
			sr := newStreamReader(encoded)
			got, err := sr.readAmountAny()
			if err != nil {
				t.Fatalf("readAmountAny: %v", err)
			}
			if sr.pos != len(encoded) {
				t.Errorf("reader did not consume entire blob: pos=%d len=%d", sr.pos, len(encoded))
			}

			if !reflect.DeepEqual(refDecoded, got) {
				t.Errorf("decoder mismatch\n  codec=%#v\n  inline=%#v\n  blob=%s", refDecoded, got, hex.EncodeToString(encoded))
			}
		})
	}
}
