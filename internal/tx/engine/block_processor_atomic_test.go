package engine

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestBlockProcessor_MetadataSerializationFailureIsAtomic(t *testing.T) {
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	parent := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}

	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("decode account: %v", err)
	}
	accountKey := keylet.Account(accountID)
	before, err := view.Read(accountKey)
	if err != nil {
		t.Fatalf("read account before apply: %v", err)
	}

	engine := recoveryEngine(view, txcore.TapNONE)
	bp := NewBlockProcessor(engine)
	serializeErr := errors.New("forced metadata serialization failure")
	bp.createTxWithMetaBlob = func([]byte, *txcore.Metadata) ([]byte, error) {
		return nil, serializeErr
	}

	txn := successfulRecoveryTx{recoveryTx(10, 1)}
	failed, err := bp.ApplyTransaction(txn, []byte{0x12, 0x03})
	if !errors.Is(err, serializeErr) {
		t.Fatalf("apply error = %v, want %v", err, serializeErr)
	}
	after, err := view.Read(accountKey)
	if err != nil {
		t.Fatalf("read account after failed apply: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("metadata serialization failure committed account state")
	}
	if view.TxCount() != 0 {
		t.Fatalf("ledger transaction count = %d, want 0", view.TxCount())
	}
	if engine.TxCount() != 0 {
		t.Fatalf("engine transaction count = %d, want 0", engine.TxCount())
	}
	if failed.Index != 0 {
		t.Fatalf("failed transaction index = %d, want 0", failed.Index)
	}
	if view.TxExists(failed.Hash) {
		t.Fatal("metadata serialization failure committed a transaction leaf")
	}

	bp.createTxWithMetaBlob = txcore.CreateTxWithMetaBlob
	succeeded, err := bp.ApplyTransaction(txn, []byte{0x12, 0x03})
	if err != nil {
		t.Fatalf("apply after serialization failure: %v", err)
	}
	if !succeeded.ApplyResult.Applied {
		t.Fatalf("subsequent transaction result = %s, want applied", succeeded.ApplyResult.Result)
	}
	if succeeded.Index != 0 || succeeded.ApplyResult.Metadata.TransactionIndex != 0 {
		t.Fatalf("subsequent indexes = block %d metadata %d, want 0/0",
			succeeded.Index, succeeded.ApplyResult.Metadata.TransactionIndex)
	}
	if view.TxCount() != 1 || engine.TxCount() != 1 {
		t.Fatalf("transaction counts = ledger %d engine %d, want 1/1", view.TxCount(), engine.TxCount())
	}
	if !view.TxExists(succeeded.Hash) {
		t.Fatal("successful transaction state was published without its transaction leaf")
	}
}

func TestBlockProcessor_StagingDoesNotFlushBackedSHAMaps(t *testing.T) {
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	parent := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}
	family := shamap.NewMemoryNodeStoreFamily()
	family.SetMinimumLedgerSeq(view.Sequence() + 1)
	view.SetStateMapFamily(family)
	if _, err := view.MutableSnapshot(); !errors.Is(err, shamap.ErrStoreBelowMinimum) {
		t.Fatalf("flushing snapshot error = %v, want %v", err, shamap.ErrStoreBelowMinimum)
	}

	bp := NewBlockProcessor(recoveryEngine(view, txcore.TapNONE))
	result, err := bp.ApplyTransaction(successfulRecoveryTx{recoveryTx(10, 1)}, []byte{0x12, 0x03})
	if err != nil {
		t.Fatalf("apply with backed SHAMap: %v", err)
	}
	if !result.ApplyResult.Applied {
		t.Fatalf("transaction result = %s, want applied", result.ApplyResult.Result)
	}
	if view.TxCount() != 1 || !view.TxExists(result.Hash) {
		t.Fatal("applied transaction was not committed atomically")
	}
}

type batchLeafRecoveryTx struct {
	successfulRecoveryTx
	inners []txcore.Transaction
}

func (b *batchLeafRecoveryTx) ApplyInnerTransactions(ctx *txcore.ApplyContext) (ter.Result, []txcore.AppliedInnerTransaction) {
	records := make([]txcore.AppliedInnerTransaction, len(b.inners))
	for i, inner := range b.inners {
		parent := ctx.TxHash
		records[i] = txcore.AppliedInnerTransaction{
			Transaction: inner,
			Metadata: &txcore.Metadata{
				AffectedNodes:     []txcore.AffectedNode{},
				TransactionIndex:  ctx.TransactionIndex + 1 + uint32(i),
				TransactionResult: ter.TesSUCCESS,
				ParentBatchID:     &parent,
			},
		}
	}
	return ter.TesSUCCESS, records
}

func TestBlockProcessor_ApplyTransactionCommitsOnlyBatchOuter(t *testing.T) {
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	parent := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	inner1 := successfulRecoveryTx{recoveryTx(0, 2)}
	inner2 := successfulRecoveryTx{recoveryTx(0, 3)}
	outer := &batchLeafRecoveryTx{
		successfulRecoveryTx: successfulRecoveryTx{recoveryTx(10, 1)},
		inners:               []txcore.Transaction{inner1, inner2},
	}
	engine := recoveryEngine(view, txcore.TapNONE)
	result, err := NewBlockProcessor(engine).ApplyTransaction(outer, []byte{0x12, 0x03})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ApplyResult.Applied || len(result.ApplyResult.AppliedInnerTransactions) != 0 {
		t.Fatalf("apply result = %+v", result.ApplyResult)
	}
	if view.TxCount() != 1 || engine.TxCount() != 1 {
		t.Fatalf("transaction counts = ledger %d engine %d, want 1/1", view.TxCount(), engine.TxCount())
	}
	if !view.TxExists(result.Hash) {
		t.Fatal("outer transaction leaf missing")
	}
	for _, inner := range outer.inners {
		hash, hashErr := txcore.ComputeTransactionHash(inner)
		if hashErr != nil {
			t.Fatalf("compute inner transaction hash: %v", hashErr)
		}
		if view.TxExists(hash) {
			t.Fatalf("open-ledger apply committed inner transaction leaf %x", hash)
		}
	}
}

func TestBlockProcessor_CommitsBatchInnerLeavesAtomically(t *testing.T) {
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	parent := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	inner1 := successfulRecoveryTx{recoveryTx(0, 2)}
	inner2 := successfulRecoveryTx{recoveryTx(0, 3)}
	outer := &batchLeafRecoveryTx{
		successfulRecoveryTx: successfulRecoveryTx{recoveryTx(10, 1)},
		inners:               []txcore.Transaction{inner1, inner2},
	}
	engine := recoveryEngine(view, txcore.TapNONE)
	result, err := NewBlockProcessor(engine).ApplyLedgerTransaction(outer, []byte{0x12, 0x03})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ApplyResult.Applied || len(result.ApplyResult.AppliedInnerTransactions) != 2 {
		t.Fatalf("apply result = %+v", result.ApplyResult)
	}
	if view.TxCount() != 3 || engine.TxCount() != 3 {
		t.Fatalf("transaction counts = ledger %d engine %d, want 3/3", view.TxCount(), engine.TxCount())
	}
	for _, inner := range result.ApplyResult.AppliedInnerTransactions {
		hash, hashErr := txcore.ComputeTransactionHash(inner.Transaction)
		if hashErr != nil || !view.TxExists(hash) {
			t.Fatalf("committed inner leaf missing: hash=%x err=%v", hash, hashErr)
		}
	}
}

func TestBlockProcessor_InnerMetadataSerializationFailureIsAtomic(t *testing.T) {
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	parent := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}

	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("decode account: %v", err)
	}
	accountKey := keylet.Account(accountID)
	before, err := view.Read(accountKey)
	if err != nil {
		t.Fatalf("read account before apply: %v", err)
	}

	inner1 := successfulRecoveryTx{recoveryTx(0, 2)}
	inner2 := successfulRecoveryTx{recoveryTx(0, 3)}
	outer := &batchLeafRecoveryTx{
		successfulRecoveryTx: successfulRecoveryTx{recoveryTx(10, 1)},
		inners:               []txcore.Transaction{inner1, inner2},
	}
	engine := recoveryEngine(view, txcore.TapNONE)
	bp := NewBlockProcessor(engine)
	serializeErr := errors.New("forced inner metadata serialization failure")
	serializeCalls := 0
	bp.createTxWithMetaBlob = func(txBlob []byte, metadata *txcore.Metadata) ([]byte, error) {
		serializeCalls++
		if serializeCalls == 2 {
			return nil, serializeErr
		}
		return txcore.CreateTxWithMetaBlob(txBlob, metadata)
	}

	failed, err := bp.ApplyLedgerTransaction(outer, []byte{0x12, 0x03})
	if !errors.Is(err, serializeErr) {
		t.Fatalf("apply error = %v, want %v", err, serializeErr)
	}
	if serializeCalls != 2 {
		t.Fatalf("metadata serialization calls = %d, want 2", serializeCalls)
	}
	if !failed.ApplyResult.Applied || len(failed.ApplyResult.AppliedInnerTransactions) != 2 {
		t.Fatalf("staged apply result = %+v", failed.ApplyResult)
	}

	after, err := view.Read(accountKey)
	if err != nil {
		t.Fatalf("read account after failed apply: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("inner metadata serialization failure committed account state")
	}
	if view.TxCount() != 0 || engine.TxCount() != 0 {
		t.Fatalf("transaction counts = ledger %d engine %d, want 0/0", view.TxCount(), engine.TxCount())
	}
	if failed.Index != 0 {
		t.Fatalf("failed transaction index = %d, want 0", failed.Index)
	}
	if view.TxExists(failed.Hash) {
		t.Fatal("inner metadata serialization failure committed the outer transaction leaf")
	}
	for _, inner := range failed.ApplyResult.AppliedInnerTransactions {
		hash, hashErr := txcore.ComputeTransactionHash(inner.Transaction)
		if hashErr != nil {
			t.Fatalf("compute inner transaction hash: %v", hashErr)
		}
		if view.TxExists(hash) {
			t.Fatalf("inner metadata serialization failure committed inner transaction leaf %x", hash)
		}
	}
}
