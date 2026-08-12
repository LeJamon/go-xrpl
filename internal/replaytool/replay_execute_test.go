package replaytool

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestFillExpectedBatchInnerValidatesCanonicalLeaf(t *testing.T) {
	inner := tx.NewBaseTx(tx.TypeAccountSet, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	inner.Fee = "0"
	inner.SigningPubKey = ""
	inner.SetFlags(tx.TfInnerBatchTxn)
	inner.SetRawBytes(bytes.Repeat([]byte{0x42}, 40))
	hash, err := tx.ComputeTransactionHash(inner)
	if err != nil {
		t.Fatal(err)
	}
	parent := [32]byte{1}
	expected := tx.AppliedInnerTransaction{
		Transaction: inner,
		Metadata: &tx.Metadata{
			TransactionIndex:  3,
			TransactionResult: ter.TesSUCCESS,
			ParentBatchID:     &parent,
		},
	}
	input := blockTransaction{Index: 3, Hash: hash, Transaction: inner}
	var info txApplyInfo
	if err := fillExpectedBatchInner(&info, input, expected); err != nil {
		t.Fatalf("valid inner rejected: %v", err)
	}
	if !info.Applied || info.Result != ter.TesSUCCESS.String() || len(info.MetaBlob) == 0 {
		t.Fatalf("inner result not synthesized from outer execution: %+v", info)
	}

	badHash := input
	badHash.Hash[0] ^= 0xff
	if err := fillExpectedBatchInner(&txApplyInfo{}, badHash, expected); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	badIndex := input
	badIndex.Index++
	if err := fillExpectedBatchInner(&txApplyInfo{}, badIndex, expected); err == nil {
		t.Fatal("index mismatch accepted")
	}
	expected.Metadata.ParentBatchID = nil
	if err := fillExpectedBatchInner(&txApplyInfo{}, input, expected); err == nil {
		t.Fatal("missing ParentBatchID accepted")
	}
}

func TestExecuteBlockSkipsRecordedBatchInnerLeaves(t *testing.T) {
	seed, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	outer := batchtx.NewBatch(seed.GenesisAddress)
	outer.Fee = "40"
	outer.SetSequence(1)
	outer.SetFlags(batchtx.BatchFlagAllOrNothing)
	inners := make([]tx.Transaction, 2)
	for i := range inners {
		inner := account.NewAccountSet(seed.GenesisAddress)
		inner.Fee = "0"
		inner.SigningPubKey = ""
		inner.SetSequence(uint32(i + 2))
		inner.SetFlags(tx.TfInnerBatchTxn)
		outer.AddInnerTransaction(inner)
		inners[i] = inner
	}

	transactions := make([]blockTransaction, 0, 3)
	for index, transaction := range append([]tx.Transaction{outer}, inners...) {
		blob, serializeErr := tx.SerializeTransaction(transaction)
		if serializeErr != nil {
			t.Fatal(serializeErr)
		}
		hash, hashErr := tx.ComputeTransactionHash(transaction)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		transactions = append(transactions, blockTransaction{
			Index:       index,
			Hash:        hash,
			Blob:        blob,
			Transaction: transaction,
		})
	}

	execute := func(replayTransactions []blockTransaction) (*executedBlock, error) {
		stateMap, snapshotErr := seed.StateMap.SnapshotMutable()
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		return executeBlock(context.Background(), blockExecution{
			StateMap:            stateMap,
			LedgerIndex:         2,
			ParentCloseTime:     time.Unix(10, 0),
			CloseTime:           time.Unix(20, 0),
			CloseTimeResolution: 10,
			TotalCoins:          seed.Header.Drops,
			Fees:                drops.DefaultFees(),
			Rules:               amendment.AllSupportedRules(),
			Transactions:        replayTransactions,
		})
	}
	if _, err := execute(transactions[:2]); err == nil {
		t.Fatal("replay accepted a missing trailing batch inner")
	}
	reordered := append([]blockTransaction(nil), transactions...)
	reordered[1], reordered[2] = reordered[2], reordered[1]
	if _, err := execute(reordered); err == nil {
		t.Fatal("replay accepted reordered batch inner leaves")
	}
	if _, err := execute(transactions[1:]); err == nil {
		t.Fatal("replay accepted a batch inner without its outer")
	}

	result, err := execute(transactions)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TxResults) != 3 {
		t.Fatalf("transaction results = %d, want outer + 2 inner", len(result.TxResults))
	}
	for i, txResult := range result.TxResults {
		if !txResult.Applied || txResult.Result != ter.TesSUCCESS.String() {
			t.Fatalf("transaction %d was reapplied or rejected: %+v", i, txResult)
		}
	}
	accountRoot, err := tx.ReadAccountRoot(result.Ledger, seed.GenesisAccount)
	if err != nil {
		t.Fatal(err)
	}
	if accountRoot == nil || accountRoot.Sequence != 4 {
		t.Fatalf("genesis sequence = %+v, want outer and each inner applied exactly once", accountRoot)
	}
}

func TestExecuteBlockCarriesParentHash(t *testing.T) {
	seed, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	parentHash := [32]byte{1, 2, 3}
	result, err := executeBlock(context.Background(), blockExecution{
		StateMap:            seed.StateMap,
		LedgerIndex:         2,
		ParentHash:          parentHash,
		ParentCloseTime:     time.Unix(10, 0),
		CloseTime:           time.Unix(20, 0),
		CloseTimeResolution: 10,
		TotalCoins:          seed.Header.Drops,
		Fees:                drops.DefaultFees(),
		Rules:               amendment.EmptyRules(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ledger.ParentHash() != parentHash {
		t.Fatalf("parent hash = %x, want %x", result.Ledger.ParentHash(), parentHash)
	}
}

func TestExecuteBlockAppliesNegativeUNLTransitionBeforeTransactions(t *testing.T) {
	seed, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	validator := bytes.Repeat([]byte{0x42}, 33)
	validator[0] = 0xED
	negativeUNL, err := pseudo.SerializeNegativeUNLSLE(&pseudo.NegativeUNLSLE{
		ValidatorToDisable: validator,
	})
	if err != nil {
		t.Fatal(err)
	}
	stateMap, err := seed.StateMap.SnapshotMutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := stateMap.Put(keylet.NegativeUNL().Key, negativeUNL); err != nil {
		t.Fatal(err)
	}

	const flagLedger = uint32(256)
	result, err := executeBlock(context.Background(), blockExecution{
		StateMap:            stateMap,
		LedgerIndex:         flagLedger,
		ParentCloseTime:     time.Unix(10, 0),
		CloseTime:           time.Unix(20, 0),
		CloseTimeResolution: 10,
		TotalCoins:          seed.Header.Drops,
		Fees:                drops.DefaultFees(),
		Rules:               amendment.EmptyRules(),
	})
	if err != nil {
		t.Fatal(err)
	}
	item, found, err := result.StateMap.Get(keylet.NegativeUNL().Key)
	if err != nil || !found {
		t.Fatalf("read NegativeUNL: found=%v err=%v", found, err)
	}
	entry, err := pseudo.ParseNegativeUNLSLE(item.Data())
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.DisabledValidators) != 1 {
		t.Fatalf("disabled validators = %d, want 1", len(entry.DisabledValidators))
	}
	if !bytes.Equal(entry.DisabledValidators[0].PublicKey, validator) {
		t.Fatalf("disabled validator = %x, want %x", entry.DisabledValidators[0].PublicKey, validator)
	}
	if entry.DisabledValidators[0].FirstLedgerSequence != flagLedger {
		t.Fatalf("first ledger sequence = %d, want %d", entry.DisabledValidators[0].FirstLedgerSequence, flagLedger)
	}
	if len(entry.ValidatorToDisable) != 0 {
		t.Fatalf("pending validator was not cleared: %x", entry.ValidatorToDisable)
	}
}
