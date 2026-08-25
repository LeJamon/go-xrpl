package replaytool

import (
	"bytes"
	"context"
	"fmt"
	"strings"
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

	t.Run("hash computation", func(t *testing.T) {
		malformed := tx.NewBaseTx(tx.TypeClawback, inner.Account)
		malformed.SetRawBytes([]byte{0})
		badExpected := expected
		badExpected.Transaction = malformed
		err := fillExpectedBatchInner(&txApplyInfo{}, input, badExpected)
		if err == nil || !strings.Contains(err.Error(), "tx 3 computing batch inner hash from outer execution:") {
			t.Fatalf("hash computation error = %v", err)
		}
	})

	t.Run("hash mismatch", func(t *testing.T) {
		badInput := input
		badInput.Hash[0] ^= 0xff
		want := fmt.Sprintf(
			"tx 3 batch inner hash mismatch: outer execution produced %X, captured ledger has %X",
			hash,
			badInput.Hash,
		)
		if err := fillExpectedBatchInner(&txApplyInfo{}, badInput, expected); err == nil || err.Error() != want {
			t.Fatalf("hash mismatch error = %v, want %q", err, want)
		}
	})

	t.Run("nil metadata", func(t *testing.T) {
		badExpected := expected
		badExpected.Metadata = nil
		want := "tx 3 batch inner outer execution returned nil metadata"
		if err := fillExpectedBatchInner(&txApplyInfo{}, input, badExpected); err == nil || err.Error() != want {
			t.Fatalf("nil metadata error = %v, want %q", err, want)
		}
	})

	t.Run("index mismatch", func(t *testing.T) {
		badInput := input
		badInput.Index++
		want := "tx 4 batch inner TransactionIndex mismatch: outer execution produced 3, captured ledger has 4"
		if err := fillExpectedBatchInner(&txApplyInfo{}, badInput, expected); err == nil || err.Error() != want {
			t.Fatalf("index mismatch error = %v, want %q", err, want)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		badExpected := expected
		metadata := *expected.Metadata
		metadata.ParentBatchID = nil
		badExpected.Metadata = &metadata
		want := "tx 3 batch inner metadata from outer execution is missing ParentBatchID"
		if err := fillExpectedBatchInner(&txApplyInfo{}, input, badExpected); err == nil || err.Error() != want {
			t.Fatalf("missing parent error = %v, want %q", err, want)
		}
	})
}

func TestExecuteBlockRejectsCanonicalTransactionDivergence(t *testing.T) {
	seed, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	makeInput := func(t *testing.T, sequence uint32, index int) blockTransaction {
		t.Helper()
		transaction := account.NewAccountSet(seed.GenesisAddress)
		transaction.Fee = "10"
		transaction.SetSequence(sequence)
		blob, err := tx.SerializeTransaction(transaction)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := tx.ComputeTransactionHash(transaction)
		if err != nil {
			t.Fatal(err)
		}
		return blockTransaction{Index: index, Hash: hash, Blob: blob, Transaction: transaction}
	}
	execute := func(transaction blockTransaction) error {
		stateMap, err := seed.StateMap.SnapshotMutable()
		if err != nil {
			t.Fatal(err)
		}
		_, err = executeBlock(context.Background(), blockExecution{
			StateMap:            stateMap,
			LedgerIndex:         2,
			ParentCloseTime:     time.Unix(10, 0),
			CloseTime:           time.Unix(20, 0),
			CloseTimeResolution: 10,
			TotalCoins:          seed.Header.Drops,
			Fees:                drops.DefaultFees(),
			Rules:               amendment.AllSupportedRules(),
			Transactions:        []blockTransaction{transaction},
		})
		return err
	}

	t.Run("not applied", func(t *testing.T) {
		err := execute(makeInput(t, 2, 0))
		if err == nil || err.Error() != "tx 0 was not applied: terPRE_SEQ" {
			t.Fatalf("non-applied error = %v", err)
		}
	})

	t.Run("metadata index", func(t *testing.T) {
		err := execute(makeInput(t, 1, 1))
		want := "tx 1 TransactionIndex mismatch: execution produced 0, captured ledger has 1"
		if err == nil || err.Error() != want {
			t.Fatalf("metadata index error = %v, want %q", err, want)
		}
	})
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
