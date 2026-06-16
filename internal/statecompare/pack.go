package statecompare

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// XSCP is the deterministic, length-prefixed binary format the
// xrpl-state-compare lab writes into object storage. Two kinds of pack hold
// raw mainnet bytes only, so they survive any goXRPL rebuild:
//
//	STATE  — every SLE of a checkpoint ledger (state/ckpt-<seq>.pack).
//	LEDGER — header + per-tx (hash, tx_blob, meta_blob) for a batch of
//	         ledgers, ~1000 per object (ledger/<batch>.pack).
//
// Every integer is big-endian. The LEDGER pack records each ledger's absolute
// byte offset in the manifest so a reader can seek straight to one ledger
// without scanning the whole batch.
const (
	packMagic   = "XSCP"
	packVersion = 1
	kindState   = 1
	kindLedger  = 2

	packHeaderLen = 6 // 4-byte magic + version (u8) + kind (u8)
	indexLen      = 32
	hashLen       = 32
)

// errPack signals a malformed or truncated pack; callers wrap it with context.
var errPack = errors.New("statecompare: malformed pack")

// txRecord is one transaction's raw bytes within a LEDGER pack.
type txRecord struct {
	txHash   [32]byte
	txBlob   []byte
	metaBlob []byte
}

// ledgerBlob is one ledger's raw bytes as bundled into a LEDGER pack.
type ledgerBlob struct {
	seq        uint64
	headerBlob []byte
	txs        []txRecord
}

// checkHeader validates the magic/version/kind and returns the body offset.
func checkHeader(blob []byte, expectedKind byte) (int, error) {
	if len(blob) < packHeaderLen {
		return 0, fmt.Errorf("%w: blob too short for header", errPack)
	}
	if string(blob[:4]) != packMagic {
		return 0, fmt.Errorf("%w: bad magic %q", errPack, blob[:4])
	}
	if blob[4] != packVersion {
		return 0, fmt.Errorf("%w: unsupported version %d", errPack, blob[4])
	}
	if blob[5] != expectedKind {
		return 0, fmt.Errorf("%w: expected kind %d, got %d", errPack, expectedKind, blob[5])
	}
	return packHeaderLen, nil
}

// getBytes reads a u32-length-prefixed byte run, returning a sub-slice of blob
// (no copy) and the offset just past it.
func getBytes(blob []byte, off int) ([]byte, int, error) {
	if off+4 > len(blob) {
		return nil, 0, fmt.Errorf("%w: length prefix overruns buffer", errPack)
	}
	n := int(binary.BigEndian.Uint32(blob[off:]))
	off += 4
	end := off + n
	if end > len(blob) {
		return nil, 0, fmt.Errorf("%w: truncated blob (length prefix overruns buffer)", errPack)
	}
	return blob[off:end], end, nil
}

// unpackState decodes a STATE pack into its checkpoint seq and SLE entries.
func unpackState(blob []byte) (uint64, []StateEntry, error) {
	off, err := checkHeader(blob, kindState)
	if err != nil {
		return 0, nil, err
	}
	if off+12 > len(blob) {
		return 0, nil, fmt.Errorf("%w: truncated state header", errPack)
	}
	seq := binary.BigEndian.Uint64(blob[off:])
	off += 8
	count := binary.BigEndian.Uint32(blob[off:])
	off += 4

	entries := make([]StateEntry, 0, count)
	for range count {
		if off+indexLen > len(blob) {
			return 0, nil, fmt.Errorf("%w: truncated state index", errPack)
		}
		var e StateEntry
		copy(e.Index[:], blob[off:off+indexLen])
		off += indexLen
		if e.Data, off, err = getBytes(blob, off); err != nil {
			return 0, nil, err
		}
		entries = append(entries, e)
	}
	return seq, entries, nil
}

// readOneLedger decodes the ledger record starting at off and returns the
// offset just past it.
func readOneLedger(blob []byte, off int) (ledgerBlob, int, error) {
	if off+8 > len(blob) {
		return ledgerBlob{}, 0, fmt.Errorf("%w: truncated ledger seq", errPack)
	}
	lb := ledgerBlob{seq: binary.BigEndian.Uint64(blob[off:])}
	off += 8

	var err error
	if lb.headerBlob, off, err = getBytes(blob, off); err != nil {
		return ledgerBlob{}, 0, err
	}
	if off+4 > len(blob) {
		return ledgerBlob{}, 0, fmt.Errorf("%w: truncated tx count", errPack)
	}
	txCount := binary.BigEndian.Uint32(blob[off:])
	off += 4

	lb.txs = make([]txRecord, 0, txCount)
	for range txCount {
		if off+hashLen > len(blob) {
			return ledgerBlob{}, 0, fmt.Errorf("%w: truncated tx hash", errPack)
		}
		var tr txRecord
		copy(tr.txHash[:], blob[off:off+hashLen])
		off += hashLen
		if tr.txBlob, off, err = getBytes(blob, off); err != nil {
			return ledgerBlob{}, 0, err
		}
		if tr.metaBlob, off, err = getBytes(blob, off); err != nil {
			return ledgerBlob{}, 0, err
		}
		lb.txs = append(lb.txs, tr)
	}
	return lb, off, nil
}

// readLedgerAt decodes a single ledger from a LEDGER pack at a
// manifest-recorded absolute byte offset.
func readLedgerAt(blob []byte, offset int) (ledgerBlob, error) {
	if offset < packHeaderLen || offset >= len(blob) {
		return ledgerBlob{}, fmt.Errorf("%w: ledger offset %d out of range", errPack, offset)
	}
	lb, _, err := readOneLedger(blob, offset)
	return lb, err
}
