package statecompare

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

const (
	packMagic   = "XSCP"
	packVersion = 1
	kindState   = 1
	kindLedger  = 2

	packHeaderLen       = 6
	packEnvelopeLen     = packHeaderLen + 8 + 4
	indexLen            = 32
	hashLen             = 32
	minStateRecordLen   = indexLen + 4
	minLedgerRecordLen  = 8 + 4 + header.SizeBase + 4
	minTransactionEntry = hashLen + 4 + minTransactionBytes + 4 + 1

	maxStatePackBytes   int64  = 4 << 30
	maxLedgerPackBytes  int64  = 512 << 20
	maxStateEntryBytes  uint32 = 16 << 20
	minTransactionBytes        = 32
	maxTransactionBytes        = 1 << 20
	maxMetadataBytes           = 918744
)

var errPack = errors.New("statecompare: malformed pack")

type statePackExpectation struct {
	seq   uint32
	count uint32
	size  int64
}

type txRecord struct {
	txHash   [32]byte
	txBlob   []byte
	metaBlob []byte
}

type ledgerBlob struct {
	seq        uint32
	headerBlob []byte
	txs        []txRecord
}

type ledgerPack struct {
	batchStart uint32
	data       []byte
	records    []recordBoundary
}

type recordBoundary struct {
	start int
	end   int
}

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

func take(blob []byte, off, n int, field string) ([]byte, int, error) {
	if off < 0 || n < 0 || off > len(blob) || n > len(blob)-off {
		return nil, 0, fmt.Errorf("%w: truncated %s", errPack, field)
	}
	return blob[off : off+n], off + n, nil
}

func readUint32(blob []byte, off int, field string) (uint32, int, error) {
	b, next, err := take(blob, off, 4, field)
	if err != nil {
		return 0, 0, err
	}
	return binary.BigEndian.Uint32(b), next, nil
}

func readUint64(blob []byte, off int, field string) (uint64, int, error) {
	b, next, err := take(blob, off, 8, field)
	if err != nil {
		return 0, 0, err
	}
	return binary.BigEndian.Uint64(b), next, nil
}

func readBytes(blob []byte, off int, limit uint32, field string) ([]byte, int, error) {
	n, next, err := readUint32(blob, off, field+" length")
	if err != nil {
		return nil, 0, err
	}
	if n > limit {
		return nil, 0, fmt.Errorf("%w: %s length %d exceeds limit %d", errPack, field, n, limit)
	}
	b, next, err := take(blob, next, int(n), field)
	if err != nil {
		return nil, 0, err
	}
	return b, next, nil
}

func unpackStateStream(
	ctx context.Context,
	r io.Reader,
	expected statePackExpectation,
	fn func(index [32]byte, data []byte) error,
) error {
	if fn == nil {
		return errors.New("statecompare: state callback is nil")
	}
	if expected.size < packEnvelopeLen || expected.size > maxStatePackBytes {
		return fmt.Errorf("%w: state pack size %d outside [%d,%d]", errPack, expected.size, packEnvelopeLen, maxStatePackBytes)
	}
	if int64(expected.count) > (expected.size-packEnvelopeLen)/minStateRecordLen {
		return fmt.Errorf("%w: state count %d cannot fit in %d bytes", errPack, expected.count, expected.size)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	br := bufio.NewReaderSize(r, 1<<20)
	var envelope [packEnvelopeLen]byte
	if _, err := io.ReadFull(br, envelope[:]); err != nil {
		return packReadError(ctx, "truncated state envelope", err)
	}
	if _, err := checkHeader(envelope[:], kindState); err != nil {
		return err
	}
	packSeq := binary.BigEndian.Uint64(envelope[packHeaderLen : packHeaderLen+8])
	if packSeq != uint64(expected.seq) {
		return fmt.Errorf("%w: state checkpoint %d, want %d", errPack, packSeq, expected.seq)
	}
	packCount := binary.BigEndian.Uint32(envelope[packHeaderLen+8:])
	if packCount != expected.count {
		return fmt.Errorf("%w: state count %d, manifest says %d", errPack, packCount, expected.count)
	}

	remaining := expected.size - packEnvelopeLen
	var index [indexLen]byte
	var previous [indexLen]byte
	havePrevious := false
	var lenBuf [4]byte
	for i := uint32(0); i < packCount; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if remaining < minStateRecordLen {
			return fmt.Errorf("%w: state entry %d cannot fit in remaining %d bytes", errPack, i, remaining)
		}
		if _, err := io.ReadFull(br, index[:]); err != nil {
			return packReadError(ctx, fmt.Sprintf("truncated state index %d", i), err)
		}
		if havePrevious && bytes.Compare(index[:], previous[:]) <= 0 {
			return fmt.Errorf("%w: state indexes are not strictly increasing", errPack)
		}
		previous = index
		havePrevious = true
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return packReadError(ctx, fmt.Sprintf("truncated state data length %d", i), err)
		}
		remaining -= minStateRecordLen
		dataLen := binary.BigEndian.Uint32(lenBuf[:])
		if dataLen > maxStateEntryBytes {
			return fmt.Errorf("%w: state entry %d length %d exceeds limit %d", errPack, i, dataLen, maxStateEntryBytes)
		}
		if int64(dataLen) > remaining {
			return fmt.Errorf("%w: state entry %d length %d exceeds remaining %d bytes", errPack, i, dataLen, remaining)
		}
		data := make([]byte, int(dataLen))
		if _, err := io.ReadFull(br, data); err != nil {
			return packReadError(ctx, fmt.Sprintf("truncated state data %d", i), err)
		}
		remaining -= int64(dataLen)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(index, data); err != nil {
			return err
		}
	}
	if remaining != 0 {
		return fmt.Errorf("%w: %d trailing state pack bytes", errPack, remaining)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: state pack exceeds manifest size", errPack)
		}
		return packReadError(ctx, "checking state pack EOF", err)
	}
	return nil
}

func packReadError(ctx context.Context, message string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %s: %v", errPack, message, err)
}

func indexLedgerPack(blob []byte) (*ledgerPack, error) {
	return indexLedgerPackContext(context.Background(), blob)
}

func indexLedgerPackContext(ctx context.Context, blob []byte) (*ledgerPack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(blob)) > maxLedgerPackBytes {
		return nil, fmt.Errorf("%w: ledger pack size %d exceeds limit %d", errPack, len(blob), maxLedgerPackBytes)
	}
	off, err := checkHeader(blob, kindLedger)
	if err != nil {
		return nil, err
	}
	batchStart, off, err := readUint64(blob, off, "ledger batch start")
	if err != nil {
		return nil, err
	}
	if batchStart > math.MaxUint32 {
		return nil, fmt.Errorf("%w: ledger batch start %d exceeds uint32", errPack, batchStart)
	}
	count, off, err := readUint32(blob, off, "ledger count")
	if err != nil {
		return nil, err
	}
	if uint64(count) > uint64((len(blob)-off)/minLedgerRecordLen) {
		return nil, fmt.Errorf("%w: ledger count %d cannot fit in %d bytes", errPack, count, len(blob)-off)
	}

	pack := &ledgerPack{
		batchStart: uint32(batchStart),
		data:       blob,
		records:    make([]recordBoundary, 0, int(count)),
	}
	for i := uint32(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := off
		seq, next, err := readUint64(blob, off, fmt.Sprintf("ledger %d sequence", i))
		if err != nil {
			return nil, err
		}
		expectedSeq := batchStart + uint64(i)
		if expectedSeq > math.MaxUint32 || seq != expectedSeq {
			return nil, fmt.Errorf("%w: ledger record %d has sequence %d, want %d", errPack, i, seq, expectedSeq)
		}
		off, err = skipLedgerRecord(ctx, blob, next, i)
		if err != nil {
			return nil, err
		}
		pack.records = append(pack.records, recordBoundary{start: start, end: off})
	}
	if off != len(blob) {
		return nil, fmt.Errorf("%w: %d trailing ledger pack bytes", errPack, len(blob)-off)
	}
	return pack, nil
}

func skipLedgerRecord(ctx context.Context, blob []byte, off int, record uint32) (int, error) {
	headerBlob, off, err := readBytes(blob, off, header.SizeBase, fmt.Sprintf("ledger %d header", record))
	if err != nil {
		return 0, err
	}
	if len(headerBlob) != header.SizeBase {
		return 0, fmt.Errorf("%w: ledger %d header length %d, want %d", errPack, record, len(headerBlob), header.SizeBase)
	}
	txCount, off, err := readUint32(blob, off, fmt.Sprintf("ledger %d transaction count", record))
	if err != nil {
		return 0, err
	}
	if uint64(txCount) > uint64((len(blob)-off)/minTransactionEntry) {
		return 0, fmt.Errorf("%w: ledger %d transaction count %d cannot fit in %d bytes", errPack, record, txCount, len(blob)-off)
	}
	for i := uint32(0); i < txCount; i++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, next, err := take(blob, off, hashLen, fmt.Sprintf("ledger %d transaction %d hash", record, i)); err != nil {
			return 0, err
		} else {
			off = next
		}
		txBlob, next, err := readBytes(blob, off, maxTransactionBytes, fmt.Sprintf("ledger %d transaction %d", record, i))
		if err != nil {
			return 0, err
		}
		if len(txBlob) < minTransactionBytes {
			return 0, fmt.Errorf("%w: ledger %d transaction %d length %d below minimum %d", errPack, record, i, len(txBlob), minTransactionBytes)
		}
		off = next
		metaBlob, next, err := readBytes(blob, off, maxMetadataBytes, fmt.Sprintf("ledger %d transaction %d metadata", record, i))
		if err != nil {
			return 0, err
		}
		if len(metaBlob) == 0 {
			return 0, fmt.Errorf("%w: ledger %d transaction %d has empty metadata", errPack, record, i)
		}
		off = next
	}
	return off, nil
}

func (p *ledgerPack) readLedgerAt(offset int, expectedTxCount uint32) (ledgerBlob, error) {
	return p.readLedgerAtContext(context.Background(), offset, expectedTxCount)
}

func (p *ledgerPack) readLedgerAtContext(ctx context.Context, offset int, expectedTxCount uint32) (ledgerBlob, error) {
	if err := ctx.Err(); err != nil {
		return ledgerBlob{}, err
	}
	i := sort.Search(len(p.records), func(i int) bool { return p.records[i].start >= offset })
	if i == len(p.records) || p.records[i].start != offset {
		return ledgerBlob{}, fmt.Errorf("%w: ledger offset %d is not a record boundary", errPack, offset)
	}
	end := p.records[i].end
	seq, off, err := readUint64(p.data, offset, "ledger sequence")
	if err != nil {
		return ledgerBlob{}, err
	}
	headerBlob, off, err := readBytes(p.data, off, header.SizeBase, "ledger header")
	if err != nil {
		return ledgerBlob{}, err
	}
	txCount, off, err := readUint32(p.data, off, "transaction count")
	if err != nil {
		return ledgerBlob{}, err
	}
	if txCount != expectedTxCount {
		return ledgerBlob{}, fmt.Errorf("%w: ledger transaction count %d, manifest says %d", errPack, txCount, expectedTxCount)
	}
	lb := ledgerBlob{seq: uint32(seq), headerBlob: headerBlob, txs: make([]txRecord, 0, int(txCount))}
	for i := uint32(0); i < txCount; i++ {
		if err := ctx.Err(); err != nil {
			return ledgerBlob{}, err
		}
		hashBytes, next, err := take(p.data, off, hashLen, fmt.Sprintf("transaction %d hash", i))
		if err != nil {
			return ledgerBlob{}, err
		}
		off = next
		var record txRecord
		copy(record.txHash[:], hashBytes)
		if record.txBlob, off, err = readBytes(p.data, off, maxTransactionBytes, fmt.Sprintf("transaction %d", i)); err != nil {
			return ledgerBlob{}, err
		}
		if record.metaBlob, off, err = readBytes(p.data, off, maxMetadataBytes, fmt.Sprintf("transaction %d metadata", i)); err != nil {
			return ledgerBlob{}, err
		}
		lb.txs = append(lb.txs, record)
	}
	if off != end {
		return ledgerBlob{}, fmt.Errorf("%w: ledger record ends at %d, indexed end is %d", errPack, off, end)
	}
	return lb, nil
}
