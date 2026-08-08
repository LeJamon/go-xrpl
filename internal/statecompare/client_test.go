package statecompare

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/url"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	txcodec "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type fakeManifest struct {
	snap           *LedgerSnapshot
	checkpointData checkpointManifest
	err            error
}

func (f *fakeManifest) snapshot(context.Context, uint32) (*LedgerSnapshot, error) {
	return f.snap, f.err
}
func (f *fakeManifest) checkpoint(context.Context, uint32) (checkpointManifest, error) {
	return f.checkpointData, f.err
}
func (f *fakeManifest) validateRange(context.Context, uint32, uint32) (bool, uint32, error) {
	return true, 0, f.err
}
func (f *fakeManifest) Close() error { return nil }

type memoryBlobStore struct {
	objects map[string][]byte
	opens   int
}

func (s *memoryBlobStore) open(ctx context.Context, key string) (*blobObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.opens++
	data, ok := s.objects[key]
	if !ok {
		return nil, errNotFound
	}
	return &blobObject{ReadCloser: io.NopCloser(bytes.NewReader(data)), size: int64(len(data))}, nil
}
func (s *memoryBlobStore) Close() error { return nil }

func encodeMeta(t *testing.T, index uint32) []byte {
	t.Helper()
	data, err := binarycodec.EncodeBytes(map[string]any{"TransactionIndex": index})
	if err != nil {
		t.Fatalf("encode metadata: %v", err)
	}
	return data
}

func makeTransactions(t *testing.T, indices ...uint32) []txRecord {
	t.Helper()
	records := make([]txRecord, len(indices))
	for i, index := range indices {
		blob := bytes.Repeat([]byte{byte(i + 1)}, minTransactionBytes)
		records[i] = txRecord{
			txHash:   sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), blob),
			txBlob:   blob,
			metaBlob: encodeMeta(t, index),
		}
	}
	return records
}

func transactionRoot(t *testing.T, records []txRecord) [32]byte {
	t.Helper()
	tree := shamap.New(shamap.TypeTransaction)
	for _, record := range records {
		txVL, err := txcodec.EncodeWithVL(record.txBlob)
		if err != nil {
			t.Fatal(err)
		}
		metaVL, err := txcodec.EncodeWithVL(record.metaBlob)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.PutWithNodeType(record.txHash, append(txVL, metaVL...), shamap.NodeTypeTransactionWithMeta); err != nil {
			t.Fatal(err)
		}
	}
	root, err := tree.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func ledgerFixture(t *testing.T) (*LedgerSnapshot, []byte) {
	t.Helper()
	const seq = uint32(25)
	records := makeTransactions(t, 1, 0)
	txRoot := transactionRoot(t, records)
	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         seq,
		ParentHash:          [32]byte{1},
		TxHash:              txRoot,
		AccountHash:         [32]byte{2},
		Drops:               99,
		ParentCloseTime:     protocol.FromRippleTime(100),
		CloseTime:           protocol.FromRippleTime(110),
		CloseTimeResolution: 10,
		CloseFlags:          1,
	}
	snapshot := &LedgerSnapshot{
		LedgerIndex:         seq,
		LedgerHash:          header.CalculateHash(ledgerHeader),
		ParentHash:          ledgerHeader.ParentHash,
		AccountHash:         ledgerHeader.AccountHash,
		TransactionHash:     txRoot,
		TotalCoins:          ledgerHeader.Drops,
		CloseTime:           110,
		CloseTimeResolution: 10,
		CloseFlags:          1,
		TransactionCount:    uint32(len(records)),
		blobKey:             "ledger/25.pack",
		blobOffset:          packEnvelopeLen,
		hasBlob:             true,
	}
	return snapshot, encodeLedgerPack(seq, []ledgerBlob{{seq: seq, headerBlob: header.AddRaw(ledgerHeader, false), txs: records}})
}

func encodeLedgerPack(start uint32, ledgers []ledgerBlob) []byte {
	var out bytes.Buffer
	out.WriteString(packMagic)
	out.WriteByte(packVersion)
	out.WriteByte(kindLedger)
	_ = binary.Write(&out, binary.BigEndian, uint64(start))
	_ = binary.Write(&out, binary.BigEndian, uint32(len(ledgers)))
	for _, ledger := range ledgers {
		_ = binary.Write(&out, binary.BigEndian, uint64(ledger.seq))
		writeSized(&out, ledger.headerBlob)
		_ = binary.Write(&out, binary.BigEndian, uint32(len(ledger.txs)))
		for _, record := range ledger.txs {
			out.Write(record.txHash[:])
			writeSized(&out, record.txBlob)
			writeSized(&out, record.metaBlob)
		}
	}
	return out.Bytes()
}

func writeSized(out *bytes.Buffer, data []byte) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(data)))
	out.Write(data)
}

func TestTransactionsValidatesAndOrders(t *testing.T) {
	snapshot, pack := ledgerFixture(t)
	blobs := &memoryBlobStore{objects: map[string][]byte{snapshot.blobKey: pack}}
	client := newClient(&fakeManifest{}, blobs)

	txs, err := client.Transactions(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 2 || txs[0].TxIndex != 0 || txs[1].TxIndex != 1 {
		t.Fatalf("unexpected transaction order: %+v", txs)
	}
	first := txs[0].TxBlob[0]
	txs[0].TxBlob[0] ^= 0xff
	again, err := client.Transactions(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("Transactions cached: %v", err)
	}
	if again[0].TxBlob[0] != first || blobs.opens != 1 {
		t.Fatalf("cache ownership/open count violated: byte=%d opens=%d", again[0].TxBlob[0], blobs.opens)
	}
}

func TestStreamStateEntriesValidatesIdentityBeforeCallback(t *testing.T) {
	const seq = uint32(10)
	entries := []StateEntry{{Index: [32]byte{1}, Data: []byte{2}}}
	data := encodeStatePack(seq+1, entries)
	snapshot := &LedgerSnapshot{LedgerIndex: seq, AccountHash: [32]byte{3}}
	manifest := &fakeManifest{checkpointData: checkpointManifest{
		seq: seq, blobKey: "state/ckpt-10.pack", accountHash: snapshot.AccountHash,
		objectCount: 1, sizeBytes: int64(len(data)),
	}}
	blobs := &memoryBlobStore{objects: map[string][]byte{"state/ckpt-10.pack": data}}
	client := newClient(manifest, blobs)
	called := 0
	if err := client.StreamStateEntries(context.Background(), snapshot, func(StateEntry) error { called++; return nil }); !errors.Is(err, errPack) {
		t.Fatalf("StreamStateEntries error = %v", err)
	}
	if called != 0 {
		t.Fatalf("callback ran %d times before identity validation", called)
	}

	manifest.checkpointData.accountHash[0] ^= 1
	if err := client.StreamStateEntries(context.Background(), snapshot, func(StateEntry) error { called++; return nil }); err == nil {
		t.Fatal("account hash mismatch accepted")
	}
	if called != 0 || blobs.opens != 1 {
		t.Fatalf("account mismatch reached blob/callback: opens=%d callbacks=%d", blobs.opens, called)
	}
}

func TestStreamStateEntriesPropagatesCancellation(t *testing.T) {
	const seq = uint32(10)
	data := encodeStatePack(seq, []StateEntry{{Index: [32]byte{1}, Data: []byte{2}}})
	snapshot := &LedgerSnapshot{LedgerIndex: seq, AccountHash: [32]byte{3}}
	manifest := &fakeManifest{checkpointData: checkpointManifest{
		seq: seq, blobKey: "state/ckpt-10.pack", accountHash: snapshot.AccountHash,
		objectCount: 1, sizeBytes: int64(len(data)),
	}}
	client := newClient(manifest, &memoryBlobStore{objects: map[string][]byte{"state/ckpt-10.pack": data}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.StreamStateEntries(ctx, snapshot, func(StateEntry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamStateEntries error = %v", err)
	}
}

func TestTransactionsRejectsNonBoundaryOffset(t *testing.T) {
	snapshot, pack := ledgerFixture(t)
	snapshot.blobOffset++
	client := newClient(&fakeManifest{}, &memoryBlobStore{objects: map[string][]byte{snapshot.blobKey: pack}})
	if _, err := client.Transactions(context.Background(), snapshot); !errors.Is(err, errPack) {
		t.Fatalf("Transactions error = %v, want malformed pack", err)
	}
}

func TestValidateTransactionsRejectsCorruption(t *testing.T) {
	snapshot, _ := ledgerFixture(t)
	valid := makeTransactions(t, 0, 1)
	snapshot.TransactionHash = transactionRoot(t, valid)

	tests := []struct {
		name   string
		mutate func([]txRecord, *LedgerSnapshot)
	}{
		{"hash", func(records []txRecord, _ *LedgerSnapshot) { records[0].txHash[0] ^= 1 }},
		{"duplicate index", func(records []txRecord, _ *LedgerSnapshot) { records[1].metaBlob = encodeMeta(t, 0) }},
		{"count", func(_ []txRecord, snap *LedgerSnapshot) { snap.TransactionCount++ }},
		{"root", func(_ []txRecord, snap *LedgerSnapshot) { snap.TransactionHash[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := make([]txRecord, len(valid))
			for i := range valid {
				records[i] = txRecord{txHash: valid[i].txHash, txBlob: bytes.Clone(valid[i].txBlob), metaBlob: bytes.Clone(valid[i].metaBlob)}
			}
			snap := *snapshot
			test.mutate(records, &snap)
			if _, err := validateTransactions(records, &snap); err == nil {
				t.Fatal("validateTransactions unexpectedly succeeded")
			}
		})
	}
}

func TestValidateLedgerHeaderRejectsMismatch(t *testing.T) {
	snapshot, packData := ledgerFixture(t)
	pack, err := indexLedgerPack(packData)
	if err != nil {
		t.Fatal(err)
	}
	record, err := pack.readLedgerAt(packEnvelopeLen, snapshot.TransactionCount)
	if err != nil {
		t.Fatal(err)
	}
	bad := *snapshot
	bad.TotalCoins++
	if err := validateLedgerHeader(record.headerBlob, &bad); err == nil {
		t.Fatal("validateLedgerHeader unexpectedly succeeded")
	}
}

func TestManifestDSNEscapesCredentials(t *testing.T) {
	t.Setenv("POSTGRES_USER", "user:name")
	t.Setenv("POSTGRES_PASSWORD", "p@ss/word")
	dsn, err := manifestDSNFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "user:name" || password != "p@ss/word" {
		t.Fatalf("credentials did not round trip through DSN: %q", dsn)
	}
	t.Setenv("POSTGRES_PORT", "not-a-port")
	if _, err := manifestDSNFromEnv(); err == nil {
		t.Fatal("invalid POSTGRES_PORT accepted")
	}
}

type fakeRows struct {
	values []int64
	index  int
	err    error
	closed bool
}

func (r *fakeRows) Next() bool { return r.index < len(r.values) }
func (r *fakeRows) Scan(dest ...any) error {
	*(dest[0].(*int64)) = r.values[r.index]
	r.index++
	return nil
}
func (r *fakeRows) Err() error   { return r.err }
func (r *fakeRows) Close() error { r.closed = true; return nil }

type scannerFunc func(...any) error

func (f scannerFunc) Scan(dest ...any) error { return f(dest...) }

type fakeManifestDB struct {
	row  rowScanner
	rows *fakeRows
}

func (f fakeManifestDB) queryRow(context.Context, string, ...any) rowScanner { return f.row }
func (f fakeManifestDB) query(context.Context, string, ...any) (rowIterator, error) {
	return f.rows, nil
}
func (f fakeManifestDB) close() error { return nil }

func TestValidateRangeMaxUint32(t *testing.T) {
	rows := &fakeRows{values: []int64{math.MaxUint32}}
	manifest := &sqlManifest{db: fakeManifestDB{rows: rows}}
	valid, missing, err := manifest.validateRange(context.Background(), math.MaxUint32, math.MaxUint32)
	if err != nil || !valid || missing != 0 || !rows.closed {
		t.Fatalf("validateRange = (%t,%d,%v), closed=%t", valid, missing, err, rows.closed)
	}
}

func TestSQLManifestSnapshotLoadsTrustMetadata(t *testing.T) {
	hash := bytes.Repeat([]byte{7}, 32)
	row := scannerFunc(func(dest ...any) error {
		*(dest[0].(*int64)) = 25
		for _, i := range []int{1, 2, 3, 4} {
			*(dest[i].(*[]byte)) = bytes.Clone(hash)
		}
		*(dest[5].(*uint64)) = 100
		*(dest[6].(*int64)) = 200
		*(dest[7].(*int64)) = 10
		*(dest[8].(*int64)) = 1
		*(dest[9].(*int64)) = 2
		*(dest[10].(*sql.NullString)) = sql.NullString{String: "ledger/0.pack", Valid: true}
		*(dest[11].(*sql.NullInt64)) = sql.NullInt64{Int64: packEnvelopeLen, Valid: true}
		return nil
	})
	manifest := &sqlManifest{db: fakeManifestDB{row: row}}
	snapshot, err := manifest.snapshot(context.Background(), 25)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.TransactionCount != 2 || snapshot.blobKey != "ledger/0.pack" || snapshot.blobOffset != packEnvelopeLen || !snapshot.hasBlob {
		t.Fatalf("snapshot trust metadata not loaded: %+v", snapshot)
	}
}

func TestSQLManifestRejectsImpossibleCheckpoint(t *testing.T) {
	row := scannerFunc(func(dest ...any) error {
		*(dest[0].(*int64)) = 10
		*(dest[1].(*string)) = "state/ckpt-10.pack"
		*(dest[2].(*[]byte)) = make([]byte, 32)
		*(dest[3].(*int64)) = 2
		*(dest[4].(*int64)) = packEnvelopeLen + minStateRecordLen
		return nil
	})
	manifest := &sqlManifest{db: fakeManifestDB{row: row}}
	if _, err := manifest.checkpoint(context.Background(), 10); err == nil {
		t.Fatal("checkpoint with impossible object count accepted")
	}
}
