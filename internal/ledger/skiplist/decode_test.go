package skiplist

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

func ledgerHashesFixture(tb testing.TB, count int) ([]byte, [][32]byte, []string) {
	tb.Helper()
	hashes := make([][32]byte, count)
	hashStrings := make([]string, count)
	for i := range hashes {
		binary.BigEndian.PutUint32(hashes[i][:4], uint32(i+1))
		for j := 4; j < len(hashes[i]); j++ {
			hashes[i][j] = byte(i + j)
		}
		hashStrings[i] = fmt.Sprintf("%064X", hashes[i])
	}
	entry := &ledgerfields.LedgerHashes{}
	entry.SetFlags(0)
	entry.SetLastLedgerSequence(uint32(count))
	entry.SetHashes(hashStrings)
	data, err := entry.Encode()
	if err != nil {
		tb.Fatalf("Encode LedgerHashes: %v", err)
	}
	return data, hashes, hashStrings
}

func TestDecodeLedgerHashesMatchesPublicDecode(t *testing.T) {
	data, wantHashes, wantStrings := ledgerHashesFixture(t, 256)

	var public ledgerfields.LedgerHashes
	if err := public.Decode(data); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(public.Hashes) != len(wantStrings) {
		t.Fatalf("public Hashes length = %d, want %d", len(public.Hashes), len(wantStrings))
	}
	for i := range wantStrings {
		if public.Hashes[i] != wantStrings[i] {
			t.Fatalf("public Hashes[%d] = %s, want %s", i, public.Hashes[i], wantStrings[i])
		}
		if public.Hashes[i] != strings.ToUpper(public.Hashes[i]) {
			t.Fatalf("public Hashes[%d] is not uppercase: %s", i, public.Hashes[i])
		}
	}
	roundTrip, err := public.Encode()
	if err != nil {
		t.Fatalf("Encode decoded public entry: %v", err)
	}
	if !bytes.Equal(roundTrip, data) {
		t.Fatal("public Decode/Encode changed LedgerHashes bytes")
	}

	fields, gotHashes, err := decodeLedgerHashes(data)
	if err != nil {
		t.Fatalf("decodeLedgerHashes: %v", err)
	}
	if fields.hasFirst || !fields.hasLast {
		t.Fatalf("presence: first=%v last=%v", fields.hasFirst, fields.hasLast)
	}
	if len(gotHashes) != len(wantHashes) {
		t.Fatalf("typed Hashes length = %d, want %d", len(gotHashes), len(wantHashes))
	}
	for i := range wantHashes {
		if gotHashes[i] != wantHashes[i] {
			t.Fatalf("typed Hashes[%d] = %X, want %X", i, gotHashes[i], wantHashes[i])
		}
	}
}

func TestDecodeLedgerHashesOwnsHashes(t *testing.T) {
	data, wantHashes, _ := ledgerHashesFixture(t, 1)
	marker := bytes.Index(data, []byte{0x02, 0x13, 0x20})
	if marker < 0 {
		t.Fatal("Hashes field marker not found")
	}
	_, hashes, err := decodeLedgerHashes(data)
	if err != nil {
		t.Fatalf("decodeLedgerHashes: %v", err)
	}
	data[marker+3] ^= 0xff
	if hashes[0] != wantHashes[0] {
		t.Fatal("typed hashes alias the encoded Vector256 payload")
	}
}

func TestDecodeLedgerHashesMatchesPublicErrors(t *testing.T) {
	valid, _, _ := ledgerHashesFixture(t, 1)
	marker := bytes.Index(valid, []byte{0x02, 0x13, 0x20})
	if marker < 0 {
		t.Fatal("Hashes field marker not found")
	}

	encode := func(fields map[string]any) []byte {
		t.Helper()
		data, err := binarycodec.EncodeBytes(fields)
		if err != nil {
			t.Fatalf("EncodeBytes: %v", err)
		}
		return data
	}
	cloneWithLength := func(length byte) []byte {
		data := append([]byte(nil), valid...)
		data[marker+2] = length
		return data
	}
	duplicate := append(append([]byte(nil), valid...), valid[marker:]...)
	wrongType := append([]byte(nil), valid...)
	wrongType[marker] = 0x52
	wrongLedgerEntryType := append([]byte(nil), valid...)
	wrongLedgerEntryType[2] = 0
	truncatedUint32 := append([]byte(nil), valid[:6]...)
	duplicateFlags := append(append([]byte(nil), valid...), valid[3:8]...)
	lastSequenceMarker := bytes.Index(valid, []byte{0x20, 0x1b})
	if lastSequenceMarker < 0 {
		t.Fatal("LastLedgerSequence field marker not found")
	}
	duplicateLastSequence := append(append([]byte(nil), valid...), valid[lastSequenceMarker:lastSequenceMarker+6]...)
	malformedSponsor := append([]byte(nil), valid[:marker]...)
	malformedSponsor = append(malformedSponsor, 0x80, 0x1b, 19)
	malformedSponsor = append(malformedSponsor, make([]byte, 19)...)
	malformedSponsor = append(malformedSponsor, valid[marker:]...)
	truncatedSponsor := append([]byte(nil), valid[:marker]...)
	truncatedSponsor = append(truncatedSponsor, 0x80, 0x1b, 20)
	truncatedSponsor = append(truncatedSponsor, make([]byte, 10)...)

	tests := []struct {
		name string
		data []byte
	}{
		{name: "missing flags", data: encode(map[string]any{
			"LedgerEntryType":    "LedgerHashes",
			"LastLedgerSequence": uint32(1),
			"Hashes":             []string{strings.Repeat("01", 32)},
		})},
		{name: "missing hashes", data: encode(map[string]any{
			"LedgerEntryType":    "LedgerHashes",
			"Flags":              uint32(0),
			"LastLedgerSequence": uint32(1),
		})},
		{name: "missing ledger entry type", data: encode(map[string]any{
			"Flags":              uint32(0),
			"LastLedgerSequence": uint32(1),
			"Hashes":             []string{strings.Repeat("01", 32)},
		})},
		{name: "wrong ledger entry type", data: wrongLedgerEntryType},
		{name: "truncated extended type header", data: []byte{0x01}},
		{name: "truncated extended field header", data: []byte{0x10}},
		{name: "truncated UInt16", data: []byte{0x11, 0x00}},
		{name: "truncated UInt32", data: truncatedUint32},
		{name: "malformed Sponsor", data: malformedSponsor},
		{name: "truncated Sponsor", data: truncatedSponsor},
		{name: "31 byte vector", data: cloneWithLength(31)},
		{name: "33 byte vector", data: cloneWithLength(33)},
		{name: "truncated vector", data: valid[:len(valid)-1]},
		{name: "invalid VL prefix", data: cloneWithLength(255)},
		{name: "duplicate hashes", data: duplicate},
		{name: "duplicate flags", data: duplicateFlags},
		{name: "duplicate last ledger sequence", data: duplicateLastSequence},
		{name: "wrong field type", data: wrongType},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var public ledgerfields.LedgerHashes
			publicErr := public.Decode(test.data)
			_, _, typedErr := decodeLedgerHashes(test.data)
			if publicErr == nil || typedErr == nil {
				t.Fatalf("errors: public=%v typed=%v", publicErr, typedErr)
			}
			if publicErr.Error() != typedErr.Error() {
				t.Fatalf("typed error = %q, public error = %q", typedErr, publicErr)
			}
		})
	}
}

func TestDecodeLedgerHashesRejectsNonCanonicalFieldHeaders(t *testing.T) {
	valid, _, _ := ledgerHashesFixture(t, 1)
	if valid[0] != 0x11 {
		t.Fatalf("LedgerEntryType header = %02X, want 11", valid[0])
	}

	tests := []struct {
		name    string
		header  []byte
		wantErr error
	}{
		{name: "extended type", header: []byte{0x01, 0x01}, wantErr: serdes.ErrInvalidTypecode},
		{name: "extended field", header: []byte{0x10, 0x01}, wantErr: serdes.ErrInvalidFieldcode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := append(append([]byte(nil), test.header...), valid[1:]...)

			var public ledgerfields.LedgerHashes
			if err := public.Decode(data); err != test.wantErr {
				t.Fatalf("public Decode error = %v, want %v", err, test.wantErr)
			}
			if _, _, err := decodeLedgerHashes(data); err != test.wantErr {
				t.Fatalf("typed decode error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLedgerHashesRewritePreservesOptionalFields(t *testing.T) {
	const sponsor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	data, wantHashes, hashStrings := ledgerHashesFixture(t, 1)
	var entry ledgerfields.LedgerHashes
	if err := entry.Decode(data); err != nil {
		t.Fatalf("Decode fixture: %v", err)
	}
	entry.SetFlags(0x10)
	entry.SetFirstLedgerSequence(1)
	entry.SetSponsor(sponsor)
	data, err := entry.Encode()
	if err != nil {
		t.Fatalf("Encode optional fields: %v", err)
	}
	stateMap := shamap.New(shamap.TypeState)
	key := keylet.LedgerHashes().Key
	if err := stateMap.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fields, hashes, lastSeq, err := ReadLedgerHashesSLE(stateMap, key)
	if err != nil {
		t.Fatalf("ReadLedgerHashesSLE: %v", err)
	}
	if err := Write(stateMap, key, fields, hashes, lastSeq); err != nil {
		t.Fatalf("Write: %v", err)
	}
	item, found, err := stateMap.Get(key)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !bytes.Equal(item.Data(), data) {
		t.Fatal("typed read/rewrite changed LedgerHashes bytes")
	}
	if hashes[0] != wantHashes[0] || fmt.Sprintf("%064X", hashes[0]) != hashStrings[0] {
		t.Fatal("typed read changed Hashes")
	}
}

var (
	benchmarkLedgerHashes       [][32]byte
	benchmarkLedgerHashesFields *LedgerHashesFields
	benchmarkPublicLedgerHashes ledgerfields.LedgerHashes
)

func BenchmarkLedgerHashesBinaryDecode(b *testing.B) {
	data, _, _ := ledgerHashesFixture(b, 256)
	b.Run("public-hex-round-trip", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var entry ledgerfields.LedgerHashes
			if err := entry.Decode(data); err != nil {
				b.Fatal(err)
			}
			hashes := make([][32]byte, len(entry.Hashes))
			for i, value := range entry.Hashes {
				decoded, err := hex.DecodeString(value)
				if err != nil {
					b.Fatal(err)
				}
				copy(hashes[i][:], decoded)
			}
			benchmarkLedgerHashes = hashes
			benchmarkPublicLedgerHashes = entry
		}
	})
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			fields, hashes, err := decodeLedgerHashes(data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkLedgerHashes = hashes
			benchmarkLedgerHashesFields = fields
		}
	})
}
