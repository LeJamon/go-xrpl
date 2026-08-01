package ledger

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
)

func TestAdoptStateDetachesSourceMaps(t *testing.T) {
	target := newOpenChild(t)
	source, err := target.MutableSnapshot()
	if err != nil {
		t.Fatalf("MutableSnapshot: %v", err)
	}

	stateKey := mutAcct(0x91)
	adoptedState := mutData(0x11)
	if err := source.Insert(stateKey, adoptedState); err != nil {
		t.Fatalf("seed source state: %v", err)
	}
	var txID [32]byte
	txID[0] = 0xa1
	adoptedTx := bytes.Repeat([]byte{0x21}, 16)
	if err := source.AddTransactionWithMeta(txID, adoptedTx); err != nil {
		t.Fatalf("seed source transaction: %v", err)
	}
	if err := source.AdjustDropsDestroyed(drops.XRPAmount(11)); err != nil {
		t.Fatalf("seed source destroyed drops: %v", err)
	}

	if err := target.AdoptState(source); err != nil {
		t.Fatalf("AdoptState: %v", err)
	}
	targetStateRoot, err := target.StateMapHash()
	if err != nil {
		t.Fatalf("target state root after adoption: %v", err)
	}
	targetTxRoot, err := target.TxMapHash()
	if err != nil {
		t.Fatalf("target transaction root after adoption: %v", err)
	}
	if target.dropsDestroyed != 11 {
		t.Fatalf("target destroyed drops = %d, want 11", target.dropsDestroyed)
	}

	if err := source.Update(stateKey, mutData(0x31)); err != nil {
		t.Fatalf("mutate source state after adoption: %v", err)
	}
	if err := source.AddTransaction([32]byte{0xa2}, bytes.Repeat([]byte{0x41}, 16)); err != nil {
		t.Fatalf("mutate source transactions after adoption: %v", err)
	}
	if err := source.AdjustDropsDestroyed(5); err != nil {
		t.Fatalf("mutate source destroyed drops after adoption: %v", err)
	}
	if err := source.Close(source.CloseTime(), 0); err != nil {
		t.Fatalf("close source after adoption: %v", err)
	}

	if got, err := target.StateMapHash(); err != nil || got != targetStateRoot {
		t.Fatalf("source mutation/close changed target state root to %x, %v; want %x", got, err, targetStateRoot)
	}
	if got, err := target.TxMapHash(); err != nil || got != targetTxRoot {
		t.Fatalf("source mutation/close changed target transaction root to %x, %v; want %x", got, err, targetTxRoot)
	}
	if got, err := target.Read(stateKey); err != nil || !bytes.Equal(got, adoptedState) {
		t.Fatalf("target adopted state = %x, %v; want %x", got, err, adoptedState)
	}
	if got, found, err := target.GetTransaction(txID); err != nil || !found || !bytes.Equal(got, adoptedTx) {
		t.Fatalf("target adopted transaction = %x, %v, %v; want %x", got, found, err, adoptedTx)
	}
	if target.dropsDestroyed != 11 {
		t.Fatalf("source mutation changed target destroyed drops to %d", target.dropsDestroyed)
	}
	if target.IsImmutable() {
		t.Fatal("source close made target immutable")
	}
	if err := target.Insert(mutAcct(0x92), mutData(0x51)); err != nil {
		t.Fatalf("target is not writable after source close: %v", err)
	}
}
