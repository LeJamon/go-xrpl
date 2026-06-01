package header

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

// xrplTime builds a time.Time that is an exact whole-second XRPL-epoch value, so
// AddRaw -> DeserializeHeader round-trips losslessly. Serialization truncates to
// 1-second granularity relative to the Ripple epoch.
func xrplTime(epochSecs int64) time.Time {
	return time.Unix(protocol.RippleEpochUnix+epochSecs, 0).UTC()
}

func fill(b *[32]byte, v byte) {
	for i := range b {
		b[i] = v
	}
}

func TestSizeConstants(t *testing.T) {
	// These encode the rippled wire contract (Ledger.cpp addRaw). A regression
	// here means an instant fork against the network.
	if SizeBase != 118 {
		t.Errorf("SizeBase = %d, want 118", SizeBase)
	}
	if SizeWithHash != 150 {
		t.Errorf("SizeWithHash = %d, want 150", SizeWithHash)
	}
}

// TestAddRawByteExact builds the expected wire bytes by hand at known offsets and
// asserts AddRaw matches them byte-for-byte. This guards the exact rippled layout:
// seq(u32) drops(u64) parentHash(32) txHash(32) accountHash(32)
// parentCloseTime(u32) closeTime(u32) closeTimeResolution(u8) closeFlags(u8).
func TestAddRawByteExact(t *testing.T) {
	var parentHash, txHash, accountHash [32]byte
	fill(&parentHash, 0xAA)
	fill(&txHash, 0xBB)
	fill(&accountHash, 0xCC)

	const parentCloseEpoch = 0x00111111
	const closeEpoch = 0x00222222

	h := LedgerHeader{
		LedgerIndex:         0x01020304,
		Drops:               0x1122334455667788,
		ParentHash:          parentHash,
		TxHash:              txHash,
		AccountHash:         accountHash,
		ParentCloseTime:     xrplTime(parentCloseEpoch),
		CloseTime:           xrplTime(closeEpoch),
		CloseTimeResolution: 10,
		CloseFlags:          0,
	}

	var want []byte
	want = binary.BigEndian.AppendUint32(want, 0x01020304)
	want = binary.BigEndian.AppendUint64(want, 0x1122334455667788)
	want = append(want, parentHash[:]...)
	want = append(want, txHash[:]...)
	want = append(want, accountHash[:]...)
	want = binary.BigEndian.AppendUint32(want, parentCloseEpoch)
	want = binary.BigEndian.AppendUint32(want, closeEpoch)
	want = append(want, 10) // CloseTimeResolution (uint8)
	want = append(want, 0)  // CloseFlags (uint8)

	if len(want) != SizeBase {
		t.Fatalf("hand-built expected slice len = %d, want %d", len(want), SizeBase)
	}

	got := AddRaw(h, false)
	if len(got) != SizeBase {
		t.Fatalf("AddRaw len = %d, want %d", len(got), SizeBase)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("AddRaw bytes mismatch\n got = % X\nwant = % X", got, want)
	}

	// Verify specific offsets explicitly (defends ordering + big-endian).
	if !bytes.Equal(got[0:4], []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("seq bytes = % X, want 01 02 03 04", got[0:4])
	}
	if !bytes.Equal(got[4:12], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}) {
		t.Errorf("drops bytes = % X, want 11 22 33 44 55 66 77 88", got[4:12])
	}
	if !bytes.Equal(got[12:44], parentHash[:]) {
		t.Errorf("parentHash bytes = % X", got[12:44])
	}
	if !bytes.Equal(got[44:76], txHash[:]) {
		t.Errorf("txHash bytes = % X", got[44:76])
	}
	if !bytes.Equal(got[76:108], accountHash[:]) {
		t.Errorf("accountHash bytes = % X", got[76:108])
	}
	if got := binary.BigEndian.Uint32(got[108:112]); got != parentCloseEpoch {
		t.Errorf("parentCloseTime = %#x, want %#x", got, parentCloseEpoch)
	}
	if got := binary.BigEndian.Uint32(got[112:116]); got != closeEpoch {
		t.Errorf("closeTime = %#x, want %#x", got, closeEpoch)
	}
	if got[116] != 10 {
		t.Errorf("closeTimeResolution = %d, want 10", got[116])
	}
	if got[117] != 0 {
		t.Errorf("closeFlags = %d, want 0", got[117])
	}
}

func TestAddRawWithHash(t *testing.T) {
	var hash [32]byte
	fill(&hash, 0xDD)

	h := LedgerHeader{
		LedgerIndex:         42,
		Drops:               1000,
		ParentCloseTime:     xrplTime(100),
		CloseTime:           xrplTime(200),
		CloseTimeResolution: 30,
		CloseFlags:          0,
		Hash:                hash,
	}

	base := AddRaw(h, false)
	if len(base) != SizeBase {
		t.Fatalf("AddRaw(includeHash=false) len = %d, want %d", len(base), SizeBase)
	}

	withHash := AddRaw(h, true)
	if len(withHash) != SizeWithHash {
		t.Fatalf("AddRaw(includeHash=true) len = %d, want %d", len(withHash), SizeWithHash)
	}

	// The first SizeBase bytes must be identical to the no-hash form.
	if !bytes.Equal(withHash[:SizeBase], base) {
		t.Errorf("withHash prefix != base form")
	}

	// The trailing 32 bytes must equal the Hash field.
	if !bytes.Equal(withHash[SizeBase:], hash[:]) {
		t.Errorf("trailing hash bytes = % X, want % X", withHash[SizeBase:], hash[:])
	}
}

func TestRoundTrip(t *testing.T) {
	var parentHash, txHash, accountHash, hash [32]byte
	fill(&parentHash, 0x01)
	fill(&txHash, 0x02)
	fill(&accountHash, 0x03)
	fill(&hash, 0x04)

	orig := LedgerHeader{
		LedgerIndex:         123456,
		Drops:               9876543210,
		ParentHash:          parentHash,
		TxHash:              txHash,
		AccountHash:         accountHash,
		ParentCloseTime:     xrplTime(555),
		CloseTime:           xrplTime(666),
		CloseTimeResolution: 10,
		CloseFlags:          0,
		Hash:                hash,
	}

	t.Run("no hash", func(t *testing.T) {
		data := AddRaw(orig, false)
		got, err := DeserializeHeader(data, false)
		if err != nil {
			t.Fatalf("DeserializeHeader error: %v", err)
		}
		assertHeaderFieldsEqual(t, got, orig, false)
	})

	t.Run("with hash", func(t *testing.T) {
		data := AddRaw(orig, true)
		got, err := DeserializeHeader(data, true)
		if err != nil {
			t.Fatalf("DeserializeHeader error: %v", err)
		}
		assertHeaderFieldsEqual(t, got, orig, true)
	})
}

// assertHeaderFieldsEqual checks the fields that survive serialization. Validated
// and Accepted are not serialized; Hash is only present when checkHash is true.
func assertHeaderFieldsEqual(t *testing.T, got *LedgerHeader, want LedgerHeader, checkHash bool) {
	t.Helper()
	if got.LedgerIndex != want.LedgerIndex {
		t.Errorf("LedgerIndex = %d, want %d", got.LedgerIndex, want.LedgerIndex)
	}
	if got.Drops != want.Drops {
		t.Errorf("Drops = %d, want %d", got.Drops, want.Drops)
	}
	if got.ParentHash != want.ParentHash {
		t.Errorf("ParentHash = % X, want % X", got.ParentHash, want.ParentHash)
	}
	if got.TxHash != want.TxHash {
		t.Errorf("TxHash = % X, want % X", got.TxHash, want.TxHash)
	}
	if got.AccountHash != want.AccountHash {
		t.Errorf("AccountHash = % X, want % X", got.AccountHash, want.AccountHash)
	}
	if !got.ParentCloseTime.Equal(want.ParentCloseTime) {
		t.Errorf("ParentCloseTime = %v, want %v", got.ParentCloseTime, want.ParentCloseTime)
	}
	if !got.CloseTime.Equal(want.CloseTime) {
		t.Errorf("CloseTime = %v, want %v", got.CloseTime, want.CloseTime)
	}
	if got.CloseTimeResolution != want.CloseTimeResolution {
		t.Errorf("CloseTimeResolution = %d, want %d", got.CloseTimeResolution, want.CloseTimeResolution)
	}
	if got.CloseFlags != want.CloseFlags {
		t.Errorf("CloseFlags = %d, want %d", got.CloseFlags, want.CloseFlags)
	}
	if checkHash && got.Hash != want.Hash {
		t.Errorf("Hash = % X, want % X", got.Hash, want.Hash)
	}
}

// TestZeroTimeHandling verifies zero-value close times serialize to 4 zero bytes
// and deserialize back to the zero time.
func TestZeroTimeHandling(t *testing.T) {
	h := LedgerHeader{
		LedgerIndex:         7,
		Drops:               1,
		CloseTimeResolution: 10,
		// ParentCloseTime and CloseTime left as the zero time.
	}

	data := AddRaw(h, false)

	// ParentCloseTime occupies bytes [108:112], CloseTime [112:116].
	if !bytes.Equal(data[108:112], []byte{0, 0, 0, 0}) {
		t.Errorf("zero ParentCloseTime serialized to % X, want 00 00 00 00", data[108:112])
	}
	if !bytes.Equal(data[112:116], []byte{0, 0, 0, 0}) {
		t.Errorf("zero CloseTime serialized to % X, want 00 00 00 00", data[112:116])
	}

	got, err := DeserializeHeader(data, false)
	if err != nil {
		t.Fatalf("DeserializeHeader error: %v", err)
	}
	if !got.ParentCloseTime.IsZero() {
		t.Errorf("ParentCloseTime = %v, want zero", got.ParentCloseTime)
	}
	if !got.CloseTime.IsZero() {
		t.Errorf("CloseTime = %v, want zero", got.CloseTime)
	}
}

func TestDeserializePrefixedHeader(t *testing.T) {
	var hash [32]byte
	fill(&hash, 0x55)

	orig := LedgerHeader{
		LedgerIndex:         99,
		Drops:               42,
		ParentCloseTime:     xrplTime(10),
		CloseTime:           xrplTime(20),
		CloseTimeResolution: 30,
		Hash:                hash,
	}

	t.Run("matches unprefixed decode", func(t *testing.T) {
		raw := AddRaw(orig, true)
		prefix := []byte{0xDE, 0xAD, 0xBE, 0xEF}
		prefixed := append(append([]byte{}, prefix...), raw...)

		fromPrefixed, err := DeserializePrefixedHeader(prefixed, true)
		if err != nil {
			t.Fatalf("DeserializePrefixedHeader error: %v", err)
		}
		fromPlain, err := DeserializeHeader(raw, true)
		if err != nil {
			t.Fatalf("DeserializeHeader error: %v", err)
		}
		assertHeaderFieldsEqual(t, fromPrefixed, *fromPlain, true)
	})

	t.Run("too short returns error", func(t *testing.T) {
		_, err := DeserializePrefixedHeader([]byte{0x01, 0x02, 0x03}, false)
		if err == nil {
			t.Fatalf("expected error for <4 byte prefixed data, got nil")
		}
	})
}

func TestDeserializeHeaderErrors(t *testing.T) {
	full := AddRaw(LedgerHeader{LedgerIndex: 1, CloseTimeResolution: 10}, true)

	t.Run("shorter than SizeBase", func(t *testing.T) {
		_, err := DeserializeHeader(full[:SizeBase-1], false)
		if err == nil {
			t.Fatalf("expected error for data shorter than SizeBase, got nil")
		}
	})

	t.Run("hasHash but only SizeBase bytes", func(t *testing.T) {
		_, err := DeserializeHeader(full[:SizeBase], true)
		if err == nil {
			t.Fatalf("expected error for hasHash=true with only SizeBase bytes, got nil")
		}
	})

	t.Run("exactly SizeBase decodes without hash", func(t *testing.T) {
		if _, err := DeserializeHeader(full[:SizeBase], false); err != nil {
			t.Fatalf("unexpected error decoding exactly SizeBase bytes: %v", err)
		}
	})
}

func TestGetCloseAgree(t *testing.T) {
	tests := []struct {
		name  string
		flags uint8
		want  bool
	}{
		{"consensus on close time", 0, true},
		{"no consensus time", LCFNoConsensusTime, false},
		{"other flags set but not LCFNoConsensusTime", 0x02, true},
		{"LCFNoConsensusTime among other flags", LCFNoConsensusTime | 0x02, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := LedgerHeader{CloseFlags: tt.flags}
			if got := h.GetCloseAgree(); got != tt.want {
				t.Errorf("GetCloseAgree() = %v, want %v (flags=%#x)", got, tt.want, tt.flags)
			}
		})
	}
}
