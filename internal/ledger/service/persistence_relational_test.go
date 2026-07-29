package service

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type recordingRepositoryManager struct {
	relationaldb.RepositoryManager
	persistCalls int
}

func (m *recordingRepositoryManager) PersistValidatedLedger(context.Context, relationaldb.ValidatedLedger) error {
	m.persistCalls++
	return nil
}

func TestPersistToRelationalDBRejectsMalformedTransactionData(t *testing.T) {
	validMeta := &tx.Metadata{
		TransactionIndex:  0,
		TransactionResult: ter.TesSUCCESS,
	}

	t.Run("missing required transaction field", func(t *testing.T) {
		raw := encodeRelationalTestMap(t, map[string]any{
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount":          "1",
			"Fee":             "10",
			"Sequence":        uint32(1),
			"SigningPubKey":   "",
			"TransactionType": "Payment",
		})
		assertRelationalPersistRejected(t, raw, validMeta, [32]byte{1})
	})

	t.Run("transaction shorter than protocol minimum", func(t *testing.T) {
		assertRelationalPersistRejected(t, make([]byte, 31), validMeta, [32]byte{2})
	})

	t.Run("transaction hash does not match map key", func(t *testing.T) {
		raw, hash := validRelationalTestTransaction(t)
		hash[0] ^= 0xff
		assertRelationalPersistRejected(t, raw, validMeta, hash)
	})

	t.Run("metadata missing transaction result", func(t *testing.T) {
		raw, hash := validRelationalTestTransaction(t)
		meta := encodeRelationalTestMap(t, map[string]any{
			"AffectedNodes":    []any{},
			"TransactionIndex": uint32(0),
		})
		combined := combineRelationalTestBlobs(t, raw, meta)
		assertRelationalPersistRejectedBlob(t, combined, hash)
	})
}

func assertRelationalPersistRejected(
	t *testing.T,
	raw []byte,
	meta *tx.Metadata,
	hash [32]byte,
) {
	t.Helper()
	combined, err := tx.CreateTxWithMetaBlob(raw, meta)
	if err != nil {
		t.Fatalf("CreateTxWithMetaBlob: %v", err)
	}
	assertRelationalPersistRejectedBlob(t, combined, hash)
}

func assertRelationalPersistRejectedBlob(t *testing.T, combined []byte, hash [32]byte) {
	t.Helper()
	l := relationalTestLedger(t, hash, combined)
	manager := &recordingRepositoryManager{}
	service := &Service{relationalDB: manager}

	if err := service.persistToRelationalDB(context.Background(), l); err == nil {
		t.Fatal("persistToRelationalDB returned nil")
	}
	if manager.persistCalls != 0 {
		t.Fatalf("PersistValidatedLedger calls = %d, want 0", manager.persistCalls)
	}
}

func relationalTestLedger(t *testing.T, hash [32]byte, combined []byte) *ledger.Ledger {
	t.Helper()
	g, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := ledger.FromGenesis(g.Header, g.StateMap, g.TxMap, drops.DefaultFees())
	l, err := ledger.NewOpen(parent, g.Header.CloseTime.Add(time.Second))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	if err := l.AddTransactionWithMeta(hash, combined); err != nil {
		t.Fatalf("AddTransactionWithMeta: %v", err)
	}
	if err := l.Close(g.Header.CloseTime.Add(time.Second), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return l
}

func validRelationalTestTransaction(t *testing.T) ([]byte, [32]byte) {
	t.Helper()
	sequence := uint32(1)
	transaction := payment.NewPayment(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH",
		tx.NewXRPAmount(1),
	)
	transaction.Fee = "10"
	transaction.Sequence = &sequence

	raw, err := tx.SerializeTransaction(transaction)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(transaction)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	return raw, hash
}

func encodeRelationalTestMap(t *testing.T, value map[string]any) []byte {
	t.Helper()
	encoded, err := binarycodec.Encode(value)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return raw
}

func combineRelationalTestBlobs(t *testing.T, raw, meta []byte) []byte {
	t.Helper()
	txWithPrefix, err := tx.EncodeWithVL(raw)
	if err != nil {
		t.Fatalf("encode transaction VL: %v", err)
	}
	metaWithPrefix, err := tx.EncodeWithVL(meta)
	if err != nil {
		t.Fatalf("encode metadata VL: %v", err)
	}
	return append(txWithPrefix, metaWithPrefix...)
}
