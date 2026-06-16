package statecompare

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

// packState and packLedgerBatch mirror the lab's core/pack.py encoders. The Go
// client only ever decodes packs in production, so the encoders live here as
// fixture builders that exercise the decoder over a faithful round trip.
func packState(seq uint64, entries []StateEntry) []byte {
	out := []byte(packMagic)
	out = append(out, packVersion, kindState)
	out = binary.BigEndian.AppendUint64(out, seq)
	out = binary.BigEndian.AppendUint32(out, uint32(len(entries)))
	for _, e := range entries {
		out = append(out, e.Index[:]...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(e.Data)))
		out = append(out, e.Data...)
	}
	return out
}

func packLedgerBatch(batchStart uint64, ledgers []ledgerBlob) ([]byte, map[uint64]int) {
	out := []byte(packMagic)
	out = append(out, packVersion, kindLedger)
	out = binary.BigEndian.AppendUint64(out, batchStart)
	out = binary.BigEndian.AppendUint32(out, uint32(len(ledgers)))
	offsets := make(map[uint64]int, len(ledgers))
	for _, lg := range ledgers {
		offsets[lg.seq] = len(out)
		out = binary.BigEndian.AppendUint64(out, lg.seq)
		out = binary.BigEndian.AppendUint32(out, uint32(len(lg.headerBlob)))
		out = append(out, lg.headerBlob...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(lg.txs)))
		for _, tx := range lg.txs {
			out = append(out, tx.txHash[:]...)
			out = binary.BigEndian.AppendUint32(out, uint32(len(tx.txBlob)))
			out = append(out, tx.txBlob...)
			out = binary.BigEndian.AppendUint32(out, uint32(len(tx.metaBlob)))
			out = append(out, tx.metaBlob...)
		}
	}
	return out, offsets
}

func idx(b byte) [32]byte {
	var a [32]byte
	for i := range a {
		a[i] = b
	}
	return a
}

func TestStatePackRoundtrip(t *testing.T) {
	entries := []StateEntry{
		{Index: idx(0x01), Data: []byte("hello")},
		{Index: idx(0x02), Data: []byte{}},
		{Index: idx(0xff), Data: []byte{0x00, 0x01, 0x02}},
	}
	blob := packState(99250000, entries)

	seq, got, err := unpackState(blob)
	if err != nil {
		t.Fatalf("unpackState: %v", err)
	}
	if seq != 99250000 {
		t.Errorf("seq = %d, want 99250000", seq)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i, e := range entries {
		if got[i].Index != e.Index {
			t.Errorf("entry %d index = %x, want %x", i, got[i].Index, e.Index)
		}
		if !bytes.Equal(got[i].Data, e.Data) {
			t.Errorf("entry %d data = %x, want %x", i, got[i].Data, e.Data)
		}
	}
}

// TestStatePackGoldenBytes pins the on-wire encoding so it cannot silently
// drift from the lab's pack.py format (which the bytes were derived from).
func TestStatePackGoldenBytes(t *testing.T) {
	blob := packState(1, []StateEntry{{Index: idx(0x01), Data: []byte("x")}})
	const want = "58534350" + "01" + "01" + // magic, version, kind=state
		"0000000000000001" + // checkpoint seq = 1
		"00000001" + // entry count = 1
		"0101010101010101010101010101010101010101010101010101010101010101" + // index
		"00000001" + "78" // data len = 1, "x"
	if got := hex.EncodeToString(blob); got != want {
		t.Errorf("state pack bytes:\n got %s\nwant %s", got, want)
	}
}

func TestLedgerBatchRoundtripAndOffsets(t *testing.T) {
	ledgers := []ledgerBlob{
		{seq: 1000, headerBlob: []byte("H0"), txs: []txRecord{
			{txHash: idx(0xaa), txBlob: []byte("tx0"), metaBlob: []byte("meta0")},
			{txHash: idx(0xbb), txBlob: []byte("tx1"), metaBlob: []byte("meta1")},
		}},
		{seq: 1001, headerBlob: []byte("H1")},
		{seq: 1002, headerBlob: []byte("H2"), txs: []txRecord{
			{txHash: idx(0xcc), txBlob: []byte("tx2"), metaBlob: []byte("meta2")},
		}},
	}
	blob, offsets := packLedgerBatch(1000, ledgers)

	// Seek straight to one ledger at its manifest-recorded offset.
	one, err := readLedgerAt(blob, offsets[1002])
	if err != nil {
		t.Fatalf("readLedgerAt(1002): %v", err)
	}
	if one.seq != 1002 {
		t.Errorf("seq = %d, want 1002", one.seq)
	}
	if len(one.txs) != 1 || !bytes.Equal(one.txs[0].metaBlob, []byte("meta2")) {
		t.Errorf("ledger 1002 txs = %+v, want one tx with meta2", one.txs)
	}

	mid, err := readLedgerAt(blob, offsets[1000])
	if err != nil {
		t.Fatalf("readLedgerAt(1000): %v", err)
	}
	if len(mid.txs) != 2 || !bytes.Equal(mid.txs[1].txBlob, []byte("tx1")) {
		t.Errorf("ledger 1000 second tx = %+v, want tx1", mid.txs)
	}

	empty, err := readLedgerAt(blob, offsets[1001])
	if err != nil {
		t.Fatalf("readLedgerAt(1001): %v", err)
	}
	if len(empty.txs) != 0 {
		t.Errorf("ledger 1001 txs = %d, want 0", len(empty.txs))
	}
}

func TestUnpackStateRejectsBadMagicAndKind(t *testing.T) {
	if _, _, err := unpackState([]byte("NOTAPACK" + "\x00\x00\x00\x00")); !errors.Is(err, errPack) {
		t.Errorf("bad magic: err = %v, want errPack", err)
	}
	// A LEDGER pack must not decode as a STATE pack.
	lblob, _ := packLedgerBatch(1, nil)
	if _, _, err := unpackState(lblob); !errors.Is(err, errPack) {
		t.Errorf("kind mismatch: err = %v, want errPack", err)
	}
}

func TestUnpackStateRejectsTruncated(t *testing.T) {
	blob := packState(1, []StateEntry{{Index: idx(0x01), Data: []byte("abcdef")}})
	if _, _, err := unpackState(blob[:len(blob)-3]); !errors.Is(err, errPack) {
		t.Errorf("truncated: err = %v, want errPack", err)
	}
}

func TestReadLedgerAtOutOfRange(t *testing.T) {
	blob, _ := packLedgerBatch(1, []ledgerBlob{{seq: 1, headerBlob: []byte("H")}})
	if _, err := readLedgerAt(blob, len(blob)+1); !errors.Is(err, errPack) {
		t.Errorf("offset past end: err = %v, want errPack", err)
	}
	if _, err := readLedgerAt(blob, 0); !errors.Is(err, errPack) {
		t.Errorf("offset inside header: err = %v, want errPack", err)
	}
}
