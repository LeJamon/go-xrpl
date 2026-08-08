package replaytool

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

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
