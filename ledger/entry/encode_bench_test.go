package entry

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

var benchmarkEntries = []struct {
	name   string
	fields map[string]any
}{
	{"AccountRoot", map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           fxAccount,
		"Balance":           "99999999999999988",
		"Flags":             uint32(0),
		"OwnerCount":        uint32(3),
		"Sequence":          uint32(4172),
		"PreviousTxnID":     strings.Repeat("A1", 32),
		"PreviousTxnLgrSeq": uint32(92263421),
	}},
	{"Offer", map[string]any{
		"LedgerEntryType":   "Offer",
		"Account":           fxAccount,
		"Sequence":          uint32(4171),
		"TakerPays":         map[string]any{"value": "1234.5678", "currency": "USD", "issuer": fxIssuer},
		"TakerGets":         "250000000",
		"BookDirectory":     strings.Repeat("B2", 24) + "4F0FBE1F2F3E4B4C",
		"BookNode":          "0",
		"OwnerNode":         "2",
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("C3", 32),
		"PreviousTxnLgrSeq": uint32(92263421),
	}},
	{"RippleState", map[string]any{
		"LedgerEntryType":   "RippleState",
		"Balance":           map[string]any{"value": "-42.17", "currency": "USD", "issuer": "rrrrrrrrrrrrrrrrrrrrBZbvji"},
		"LowLimit":          map[string]any{"value": "0", "currency": "USD", "issuer": fxAccount},
		"HighLimit":         map[string]any{"value": "1000000000", "currency": "USD", "issuer": fxIssuer},
		"LowNode":           "0",
		"HighNode":          "3",
		"Flags":             uint32(0x00220000),
		"PreviousTxnID":     strings.Repeat("D4", 32),
		"PreviousTxnLgrSeq": uint32(92263421),
	}},
	{"DirectoryNode", map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Owner":           fxAccount,
		"RootIndex":       strings.Repeat("E5", 32),
		"Flags":           uint32(0),
		"Indexes": []any{
			strings.Repeat("01", 32), strings.Repeat("02", 32), strings.Repeat("03", 32),
			strings.Repeat("04", 32), strings.Repeat("05", 32),
		},
		"PreviousTxnID":     strings.Repeat("F6", 32),
		"PreviousTxnLgrSeq": uint32(92263421),
	}},
}

func BenchmarkEncode(b *testing.B) {
	for _, bench := range benchmarkEntries {
		data, err := binarycodec.EncodeBytes(bench.fields)
		if err != nil {
			b.Fatal(err)
		}
		e := NewByName(bench.name)
		if err := e.Decode(data); err != nil {
			b.Fatal(err)
		}
		encoders := []namedEncoder{
			{name: "direct", encode: e.(encodeIface).Encode},
			{name: "viaMap", encode: e.(viaMapEncoder).encodeViaMap},
		}
		for _, encoder := range encoders {
			b.Run(bench.name+"/"+encoder.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := encoder.encode(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
