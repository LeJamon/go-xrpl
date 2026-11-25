package replaytool

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type replayFeeHistoryClient struct {
	snapshots      map[uint32]*statecompare.LedgerSnapshot
	transactions   map[uint32][]statecompare.Transaction
	snapshotErr    map[uint32]error
	transactionErr error
	stateEntries   map[uint32][]statecompare.StateEntry
}

func (c *replayFeeHistoryClient) Snapshot(_ context.Context, seq uint32) (*statecompare.LedgerSnapshot, error) {
	if err := c.snapshotErr[seq]; err != nil {
		return nil, err
	}
	return c.snapshots[seq], nil
}

func (c *replayFeeHistoryClient) Transactions(_ context.Context, snapshot *statecompare.LedgerSnapshot) ([]statecompare.Transaction, error) {
	return c.transactions[snapshot.LedgerIndex], c.transactionErr
}

func (c *replayFeeHistoryClient) StreamStateEntries(_ context.Context, snapshot *statecompare.LedgerSnapshot, fn func(statecompare.StateEntry) error) error {
	for _, entry := range c.stateEntries[snapshot.LedgerIndex] {
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

func TestResolveReplaySeedFeesInheritsVerifiedSetFeeHistory(t *testing.T) {
	record := makeSetFeeRecord(t, drops.Fees{Base: -15, Reserve: -7, Increment: 20}, ter.TesSUCCESS)
	stateMap := partialFeeState(t, record.TxHash, 5, true)
	current := &statecompare.LedgerSnapshot{
		LedgerIndex: 6,
		LedgerHash:  [32]byte{6},
		ParentHash:  [32]byte{5},
	}
	prior := &statecompare.LedgerSnapshot{LedgerIndex: 5, LedgerHash: [32]byte{5}}
	client := &replayFeeHistoryClient{
		snapshots:    map[uint32]*statecompare.LedgerSnapshot{5: prior},
		transactions: map[uint32][]statecompare.Transaction{5: {record}},
		snapshotErr:  make(map[uint32]error),
	}

	got, err := resolveReplaySeedFees(context.Background(), client, current, stateMap, drops.DefaultFees(), nil)
	if err != nil {
		t.Fatalf("resolveReplaySeedFees: %v", err)
	}
	// BaseFeeDrops is explicitly zero in the seed; it must not be replaced by
	// the historical -15 value. The omitted fields inherit signs and values.
	if want := (drops.Fees{Base: 0, Reserve: -7, Increment: 20}); got != want {
		t.Fatalf("resolved fees = %+v, want %+v", got, want)
	}
}

func TestResolveReplaySeedFeesRejectsUnavailableHistory(t *testing.T) {
	recordHash := [32]byte{0x42}
	stateMap := partialFeeState(t, recordHash, 5, true)
	current := &statecompare.LedgerSnapshot{LedgerIndex: 6, LedgerHash: [32]byte{6}, ParentHash: [32]byte{5}}
	prior := &statecompare.LedgerSnapshot{LedgerIndex: 5, LedgerHash: [32]byte{5}}
	client := &replayFeeHistoryClient{
		snapshots:    map[uint32]*statecompare.LedgerSnapshot{5: prior},
		transactions: map[uint32][]statecompare.Transaction{},
		snapshotErr:  make(map[uint32]error),
	}

	_, err := resolveReplaySeedFees(context.Background(), client, current, stateMap, drops.DefaultFees(), nil)
	if err == nil || !strings.Contains(err.Error(), "does not contain referenced SetFee") {
		t.Fatalf("resolveReplaySeedFees error = %v, want missing-history error", err)
	}
}

func TestResolveReplaySeedFeesUsesLastSetFeeInLedger(t *testing.T) {
	first := makeSetFeeRecord(t, drops.Fees{Base: 11, Reserve: 22, Increment: 33}, ter.TesSUCCESS)
	last := makeSetFeeRecord(t, drops.Fees{Base: -15, Reserve: -7, Increment: 20}, ter.TesSUCCESS)
	last.TxIndex = 1
	stateMap := partialFeeState(t, [32]byte{}, 0, true)
	current := &statecompare.LedgerSnapshot{LedgerIndex: 5}
	client := &replayFeeHistoryClient{
		snapshots:    map[uint32]*statecompare.LedgerSnapshot{},
		transactions: map[uint32][]statecompare.Transaction{5: {first, last}},
		snapshotErr:  make(map[uint32]error),
	}

	got, err := resolveReplaySeedFees(context.Background(), client, current, stateMap, drops.DefaultFees(), nil)
	if err != nil {
		t.Fatalf("resolveReplaySeedFees: %v", err)
	}
	if want := (drops.Fees{Base: 0, Reserve: -7, Increment: 20}); got != want {
		t.Fatalf("resolved fees = %+v, want %+v", got, want)
	}
}

func TestResolveReplaySeedFeesAnchorsCustomGenesisSchedule(t *testing.T) {
	stateMap := partialFeeState(t, [32]byte{}, 0, true)
	genesisState := fullFeeState(t, drops.Fees{Base: 12, Reserve: 34, Increment: 56}, true)
	genesisEntries := make([]statecompare.StateEntry, 0, genesisState.Size())
	if err := genesisState.ForEach(func(item *shamap.Item) bool {
		genesisEntries = append(genesisEntries, statecompare.StateEntry{Index: item.Key(), Data: item.Data()})
		return true
	}); err != nil {
		t.Fatalf("iterate genesis state: %v", err)
	}
	genesisRoot, err := genesisState.Hash()
	if err != nil {
		t.Fatalf("hash genesis state: %v", err)
	}
	client := &replayFeeHistoryClient{
		snapshots: map[uint32]*statecompare.LedgerSnapshot{
			3: {LedgerIndex: 3, LedgerHash: [32]byte{3}},
			2: {LedgerIndex: 2, LedgerHash: [32]byte{2}, ParentHash: [32]byte{1}},
			1: {LedgerIndex: 1, LedgerHash: [32]byte{1}, AccountHash: genesisRoot},
		},
		transactions: map[uint32][]statecompare.Transaction{3: {}, 2: {}, 1: {}},
		snapshotErr:  make(map[uint32]error),
		stateEntries: map[uint32][]statecompare.StateEntry{1: genesisEntries},
	}
	// The parent links above are intentionally arranged as 3 -> 2 -> 1.
	client.snapshots[3].ParentHash = client.snapshots[2].LedgerHash

	got, err := resolveReplaySeedFees(context.Background(), client, client.snapshots[3], stateMap, drops.DefaultFees(), nil)
	if err != nil {
		t.Fatalf("resolveReplaySeedFees: %v", err)
	}
	if want := (drops.Fees{Base: 0, Reserve: 34, Increment: 56}); got != want {
		t.Fatalf("resolved fees = %+v, want %+v", got, want)
	}

	got, err = resolveReplaySeedFees(context.Background(), client, client.snapshots[3], shamap.New(shamap.TypeState), drops.DefaultFees(), nil)
	if err != nil || got != (drops.Fees{Base: 12, Reserve: 34, Increment: 56}) {
		t.Fatalf("absent FeeSettings resolved to %+v, %v", got, err)
	}
}

func TestFeeHistoryEmptyLedgerArchiveErrors(t *testing.T) {
	invalid := errors.New("invalid ledger header")
	for _, test := range []struct {
		name             string
		hash             [32]byte
		count            uint32
		readErr, wantErr error
	}{
		{"missing empty pack", [32]byte{}, 0, statecompare.ErrNotFound, nil},
		{"missing nonempty pack", [32]byte{1}, 1, statecompare.ErrNotFound, statecompare.ErrNotFound},
		{"invalid empty pack", [32]byte{}, 0, invalid, invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &replayFeeHistoryClient{transactionErr: test.readErr}
			_, err := transactionsOrEmpty(context.Background(), client, &statecompare.LedgerSnapshot{TransactionHash: test.hash, TransactionCount: test.count})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("read transactions: got %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestResolveReplaySeedFeesRejectsMissingArchiveAncestry(t *testing.T) {
	stateMap := partialFeeState(t, [32]byte{}, 0, true)
	current := &statecompare.LedgerSnapshot{LedgerIndex: 3, LedgerHash: [32]byte{3}}
	client := &replayFeeHistoryClient{
		snapshots:    map[uint32]*statecompare.LedgerSnapshot{},
		transactions: map[uint32][]statecompare.Transaction{3: {}},
		snapshotErr:  map[uint32]error{2: errors.New("checkpoint unavailable")},
	}

	_, err := resolveReplaySeedFees(context.Background(), client, current, stateMap, drops.DefaultFees(), nil)
	if err == nil || !strings.Contains(err.Error(), "loading FeeSettings history ledger 2") {
		t.Fatalf("resolveReplaySeedFees error = %v, want ancestry error", err)
	}
}

func TestResolveReplaySeedFeesFullEntryFastPath(t *testing.T) {
	stateMap := fullFeeState(t, drops.Fees{Base: -15, Reserve: 0, Increment: 20}, true)
	current := &statecompare.LedgerSnapshot{LedgerIndex: 500}
	got, err := resolveReplaySeedFees(context.Background(), nil, current, stateMap, drops.DefaultFees(), nil)
	if err != nil {
		t.Fatalf("resolveReplaySeedFees: %v", err)
	}
	want := drops.Fees{Base: -15, Reserve: 0, Increment: 20}
	if got != want {
		t.Fatalf("resolved fees = %+v, want %+v", got, want)
	}
}

func TestResumeFromCheckpointV2InheritsVerifiedSetFeeHistory(t *testing.T) {
	const seq = uint32(6)
	record := makeSetFeeRecord(t, drops.Fees{Base: -15, Reserve: -7, Increment: 20}, ter.TesSUCCESS)
	stateMap := partialFeeState(t, record.TxHash, 5, true)
	root, err := stateMap.Hash()
	if err != nil {
		t.Fatalf("hash state: %v", err)
	}
	dir := t.TempDir()
	if err := writeCheckpoint(context.Background(), dir, seq, stateMap, drops.DefaultFees()); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}
	path := checkpointPath(dir, seq)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	legacy := make([]byte, 0, len(data)-int(checkpointHeaderSize-checkpointLegacyHeaderSize))
	legacy = append(legacy, data[:checkpointLegacyHeaderSize]...)
	legacy = append(legacy, data[checkpointHeaderSize:]...)
	binary.BigEndian.PutUint32(legacy[len(checkpointMagic):], checkpointLegacyVersion)
	sealCheckpoint(legacy)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write v2 checkpoint: %v", err)
	}

	client := &replayFeeHistoryClient{
		snapshots: map[uint32]*statecompare.LedgerSnapshot{
			seq: {LedgerIndex: seq, AccountHash: root},
			5:   {LedgerIndex: 5, LedgerHash: [32]byte{5}},
		},
		transactions: map[uint32][]statecompare.Transaction{5: {record}},
		snapshotErr:  make(map[uint32]error),
	}
	_, _, got, err := resumeFromCheckpoint(context.Background(), client, dir, seq)
	if err != nil {
		t.Fatalf("resumeFromCheckpoint(v2): %v", err)
	}
	want := drops.Fees{Base: 0, Reserve: -7, Increment: 20}
	if got != want {
		t.Fatalf("resumed fees = %+v, want %+v", got, want)
	}
}

func makeSetFeeRecord(t *testing.T, fees drops.Fees, result ter.Result) statecompare.Transaction {
	t.Helper()
	all.RegisterAll()
	setFee := pseudo.NewSetFee()
	setFee.BaseFeeDrops = fees.Base.String()
	setFee.ReserveBaseDrops = fees.Reserve.String()
	setFee.ReserveIncrementDrops = fees.Increment.String()
	blob, err := pseudo.EncodePseudoTx(setFee)
	if err != nil {
		t.Fatalf("encode SetFee: %v", err)
	}
	parsed, err := txcore.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("parse SetFee: %v", err)
	}
	hash, err := txcore.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("hash SetFee: %v", err)
	}
	meta, err := txcore.SerializeMetadata(&txcore.Metadata{TransactionResult: result, TransactionIndex: 0})
	if err != nil {
		t.Fatalf("encode SetFee metadata: %v", err)
	}
	return statecompare.Transaction{TxIndex: 0, TxHash: hash, TxBlob: blob, MetaBlob: meta}
}

func partialFeeState(t *testing.T, previous [32]byte, sequence uint32, xrpFees bool) *shamap.SHAMap {
	t.Helper()
	fields := map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFeeDrops":    "0",
	}
	if previous != ([32]byte{}) {
		fields["PreviousTxnID"] = protocol.Hash256Hex(previous)
		fields["PreviousTxnLgrSeq"] = sequence
	}
	data, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("encode partial FeeSettings: %v", err)
	}
	amendments := [][32]byte(nil)
	if xrpFees {
		amendments = append(amendments, amendment.FeatureXRPFees)
	}
	amendmentData, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: amendments})
	if err != nil {
		t.Fatalf("encode Amendments: %v", err)
	}
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(keylet.Fees().Key, data); err != nil {
		t.Fatalf("put FeeSettings: %v", err)
	}
	if err := stateMap.Put(keylet.Amendments().Key, amendmentData); err != nil {
		t.Fatalf("put Amendments: %v", err)
	}
	return stateMap
}

func fullFeeState(t *testing.T, fees drops.Fees, xrpFees bool) *shamap.SHAMap {
	t.Helper()
	data, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           xrpFees,
		BaseFeeDrops:          fees.Base,
		ReserveBaseDrops:      fees.Reserve,
		ReserveIncrementDrops: fees.Increment,
	})
	if err != nil {
		t.Fatalf("encode full FeeSettings: %v", err)
	}
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(keylet.Fees().Key, data); err != nil {
		t.Fatalf("put FeeSettings: %v", err)
	}
	amendmentData, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: [][32]byte{amendment.FeatureXRPFees}})
	if err != nil {
		t.Fatalf("encode Amendments: %v", err)
	}
	if err := stateMap.Put(keylet.Amendments().Key, amendmentData); err != nil {
		t.Fatalf("put Amendments: %v", err)
	}
	return stateMap
}
