package statecompare

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func encodeStatePack(seq uint32, entries []StateEntry) []byte {
	var out bytes.Buffer
	out.WriteString(packMagic)
	out.WriteByte(packVersion)
	out.WriteByte(kindState)
	_ = binary.Write(&out, binary.BigEndian, uint64(seq))
	_ = binary.Write(&out, binary.BigEndian, uint32(len(entries)))
	for _, entry := range entries {
		out.Write(entry.Index[:])
		writeSized(&out, entry.Data)
	}
	return out.Bytes()
}

func TestUnpackStateStreamValidatesBeforeCallback(t *testing.T) {
	entries := []StateEntry{{Index: [32]byte{1}, Data: []byte{2, 3}}}
	data := encodeStatePack(10, entries)
	called := 0
	err := unpackStateStream(context.Background(), bytes.NewReader(data), statePackExpectation{
		seq: 11, count: 1, size: int64(len(data)),
	}, func([32]byte, []byte) error { called++; return nil })
	if !errors.Is(err, errPack) || called != 0 {
		t.Fatalf("wrong identity error=%v callbacks=%d", err, called)
	}

	err = unpackStateStream(context.Background(), bytes.NewReader(data), statePackExpectation{
		seq: 10, count: 1, size: int64(len(data)),
	}, func(index [32]byte, value []byte) error {
		called++
		if index != entries[0].Index || !bytes.Equal(value, entries[0].Data) {
			t.Fatalf("callback entry = %x/%x", index, value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unpackStateStream: %v", err)
	}
}

func TestUnpackStateStreamRejectsBoundsTrailingAndCancellation(t *testing.T) {
	data := encodeStatePack(10, []StateEntry{{Index: [32]byte{1}, Data: []byte{2}}})
	tests := []struct {
		name string
		data []byte
		exp  statePackExpectation
	}{
		{"truncated", data[:len(data)-1], statePackExpectation{seq: 10, count: 1, size: int64(len(data))}},
		{"trailing", append(bytes.Clone(data), 0), statePackExpectation{seq: 10, count: 1, size: int64(len(data))}},
		{"oversized count", data, statePackExpectation{seq: 10, count: math.MaxUint32, size: int64(len(data))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := unpackStateStream(context.Background(), bytes.NewReader(test.data), test.exp, func([32]byte, []byte) error { return nil }); err == nil {
				t.Fatal("malformed pack accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := unpackStateStream(ctx, bytes.NewReader(data), statePackExpectation{seq: 10, count: 1, size: int64(len(data))}, func([32]byte, []byte) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestUnpackStateStreamRejectsNonIncreasingIndexes(t *testing.T) {
	tests := []struct {
		name    string
		entries []StateEntry
	}{
		{
			name: "duplicate",
			entries: []StateEntry{
				{Index: [32]byte{1}, Data: []byte{1}},
				{Index: [32]byte{1}, Data: []byte{2}},
			},
		},
		{
			name: "descending",
			entries: []StateEntry{
				{Index: [32]byte{2}, Data: []byte{1}},
				{Index: [32]byte{1}, Data: []byte{2}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := encodeStatePack(10, test.entries)
			err := unpackStateStream(
				context.Background(),
				bytes.NewReader(data),
				statePackExpectation{seq: 10, count: uint32(len(test.entries)), size: int64(len(data))},
				func([32]byte, []byte) error { return nil },
			)
			if !errors.Is(err, errPack) {
				t.Fatalf("error = %v, want errPack", err)
			}
		})
	}
}

func TestUnpackStateStreamPropagatesCallbackError(t *testing.T) {
	data := encodeStatePack(10, []StateEntry{
		{Index: [32]byte{1}, Data: []byte{1}},
		{Index: [32]byte{2}, Data: []byte{2}},
	})
	sentinel := errors.New("callback failed")
	calls := 0
	err := unpackStateStream(
		context.Background(),
		bytes.NewReader(data),
		statePackExpectation{seq: 10, count: 2, size: int64(len(data))},
		func([32]byte, []byte) error {
			calls++
			return sentinel
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want callback error", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestIndexLedgerPackRejectsMalformedEnvelope(t *testing.T) {
	_, valid := ledgerFixture(t)
	for _, data := range [][]byte{
		valid[:len(valid)-1],
		append(bytes.Clone(valid), 0),
		func() []byte { b := bytes.Clone(valid); binary.BigEndian.PutUint32(b[14:18], math.MaxUint32); return b }(),
		func() []byte {
			b := bytes.Clone(valid)
			binary.BigEndian.PutUint64(b[6:14], uint64(math.MaxUint32)+1)
			return b
		}(),
	} {
		if _, err := indexLedgerPack(data); !errors.Is(err, errPack) {
			t.Fatalf("indexLedgerPack error = %v, want malformed pack", err)
		}
	}
}

func FuzzIndexLedgerPack(f *testing.F) {
	_, seed := ledgerFixtureForFuzz()
	f.Add(seed)
	f.Add([]byte(packMagic))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = indexLedgerPack(data)
	})
}

func ledgerFixtureForFuzz() (*LedgerSnapshot, []byte) {
	return nil, []byte{packMagic[0], packMagic[1], packMagic[2], packMagic[3], packVersion, kindLedger}
}

func FuzzUnpackStateStream(f *testing.F) {
	seed := encodeStatePack(1, nil)
	f.Add(seed, uint32(0), int64(len(seed)))
	f.Add([]byte(packMagic), uint32(math.MaxUint32), int64(packEnvelopeLen))
	f.Fuzz(func(t *testing.T, data []byte, count uint32, size int64) {
		_ = unpackStateStream(context.Background(), bytes.NewReader(data), statePackExpectation{seq: 1, count: count, size: size}, func([32]byte, []byte) error { return nil })
	})
}
