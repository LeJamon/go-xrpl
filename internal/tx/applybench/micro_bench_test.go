package applybench_test

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

var txKinds = []applyKind{
	{"payment-xrp", buildPaymentXRP},
	{"payment-iou", buildPaymentIOU},
	{"offer-create", buildOfferCreateCross},
}

// BenchmarkFlatten measures one reflective Flatten of a parsed transaction.
func BenchmarkFlatten(b *testing.B) {
	for _, kind := range txKinds {
		transaction := parseFixture(b, kind.build(b).blob)
		b.Run(kind.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := transaction.Flatten(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseFromBinary measures the full binary transaction parse.
func BenchmarkParseFromBinary(b *testing.B) {
	for _, kind := range txKinds {
		blob := kind.build(b).blob
		b.Run(kind.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				parseFixture(b, blob)
			}
		})
	}
}

// BenchmarkParseJSONRoundTrip isolates the decoded-map to typed-struct step
// of the binary parse, which goes through encoding/json.
func BenchmarkParseJSONRoundTrip(b *testing.B) {
	for _, kind := range txKinds {
		blob := kind.build(b).blob
		fields, err := binarycodec.DecodeBytes(blob)
		if err != nil {
			b.Fatal(err)
		}
		txType, _ := tx.TypeFromName(fields["TransactionType"].(string))
		b.Run(kind.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				data, err := json.Marshal(fields)
				if err != nil {
					b.Fatal(err)
				}
				target, _ := tx.NewFromType(txType)
				if err := json.Unmarshal(data, target); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

var entryNames = []string{"AccountRoot", "Offer", "RippleState", "DirectoryNode"}

type encoder interface {
	entry.Entry
	Encode() ([]byte, error)
}

// BenchmarkEntryEncode and BenchmarkEntryDecode measure one typed ledger-entry
// round trip for entries taken from the offer-crossing parent ledger.
func BenchmarkEntryEncode(b *testing.B) {
	entries := fixtureEntries(b)
	for _, name := range entryNames {
		sle := entries[name]
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := sle.Encode(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEntryDecode(b *testing.B) {
	entries := fixtureEntries(b)
	for _, name := range entryNames {
		sle := entries[name]
		data, err := sle.Encode()
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := entry.New(sle.Type()).Decode(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func parseFixture(tb testing.TB, blob []byte) tx.Transaction {
	tb.Helper()
	transaction, err := tx.ParseFromBinary(blob)
	if err != nil {
		tb.Fatal(err)
	}
	return transaction
}

func fixtureEntries(tb testing.TB) map[string]encoder {
	tb.Helper()
	want := map[entry.Type]string{
		entry.TypeAccountRoot:   "AccountRoot",
		entry.TypeOffer:         "Offer",
		entry.TypeRippleState:   "RippleState",
		entry.TypeDirectoryNode: "DirectoryNode",
	}
	found := make(map[string]encoder, len(want))
	var decodeErr error
	err := buildOfferCreateCross(tb).parent.ForEach(func(_ [32]byte, data []byte) bool {
		if len(data) < 3 || data[0] != 0x11 {
			return true
		}
		typ := entry.Type(binary.BigEndian.Uint16(data[1:3]))
		name, ok := want[typ]
		if !ok || found[name] != nil {
			return true
		}
		sle := entry.New(typ).(encoder)
		if decodeErr = sle.Decode(data); decodeErr != nil {
			return false
		}
		found[name] = sle
		return len(found) < len(want)
	})
	if err != nil || decodeErr != nil || len(found) != len(want) {
		tb.Fatalf("collect entries: err=%v decode=%v found=%d", err, decodeErr, len(found))
	}
	return found
}
