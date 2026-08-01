package ledger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
)

func TestOpenLedgerSnapshotPreservesPointInTimeStateAndIsReadOnly(t *testing.T) {
	source := newOpenChild(t)
	stateKey := mutAcct(0x71)
	stateData := mutData(0x11)
	if err := source.Insert(stateKey, stateData); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	var txID [32]byte
	txID[0] = 0x81
	txData := bytes.Repeat([]byte{0x21}, 16)
	if err := source.AddTransaction(txID, txData); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	if err := source.AdjustDropsDestroyed(drops.XRPAmount(25)); err != nil {
		t.Fatalf("seed destroyed drops: %v", err)
	}

	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.IsOpen() {
		t.Fatal("snapshot lost the source protocol lifecycle state")
	}
	if !snapshot.IsImmutable() {
		t.Fatal("open-ledger snapshot reports writable despite immutable maps")
	}
	if snapshot.dropsDestroyed != source.dropsDestroyed {
		t.Fatalf("snapshot destroyed drops = %d, want %d", snapshot.dropsDestroyed, source.dropsDestroyed)
	}
	if got, err := snapshot.Read(stateKey); err != nil || !bytes.Equal(got, stateData) {
		t.Fatalf("snapshot state = %x, %v; want %x", got, err, stateData)
	}
	if got, found, err := snapshot.GetTransaction(txID); err != nil || !found || !bytes.Equal(got, txData) {
		t.Fatalf("snapshot transaction = %x, %v, %v; want %x", got, found, err, txData)
	}

	if err := snapshot.Insert(mutAcct(0x72), mutData(0x31)); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("snapshot Insert error = %v, want ErrLedgerImmutable", err)
	}
	if err := snapshot.AddTransaction([32]byte{0x82}, bytes.Repeat([]byte{0x41}, 16)); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("snapshot AddTransaction error = %v, want ErrLedgerImmutable", err)
	}
	if err := snapshot.AdjustDropsDestroyed(1); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("snapshot AdjustDropsDestroyed error = %v, want ErrLedgerImmutable", err)
	}
	if err := snapshot.Close(snapshot.CloseTime(), 0); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("snapshot Close error = %v, want ErrLedgerImmutable", err)
	}
	if snapshot.dropsDestroyed != 25 {
		t.Fatalf("rejected mutation changed snapshot destroyed drops to %d", snapshot.dropsDestroyed)
	}

	if err := source.Update(stateKey, mutData(0x51)); err != nil {
		t.Fatalf("mutate source state: %v", err)
	}
	var laterTx [32]byte
	laterTx[0] = 0x83
	if err := source.AddTransaction(laterTx, bytes.Repeat([]byte{0x61}, 16)); err != nil {
		t.Fatalf("mutate source transactions: %v", err)
	}
	if err := source.AdjustDropsDestroyed(5); err != nil {
		t.Fatalf("mutate source destroyed drops: %v", err)
	}

	if got, err := snapshot.Read(stateKey); err != nil || !bytes.Equal(got, stateData) {
		t.Fatalf("source mutation changed snapshot state to %x, %v", got, err)
	}
	if exists, err := snapshot.TxExists(laterTx); err != nil || exists {
		t.Fatalf("source transaction leaked into snapshot: exists=%v err=%v", exists, err)
	}
	if snapshot.dropsDestroyed != 25 {
		t.Fatalf("source mutation changed snapshot destroyed drops to %d", snapshot.dropsDestroyed)
	}
}

func TestClosedLedgerRejectsDestroyedDropMutation(t *testing.T) {
	l := newOpenChild(t)
	if err := l.AdjustDropsDestroyed(7); err != nil {
		t.Fatalf("AdjustDropsDestroyed before close: %v", err)
	}
	if err := l.Close(l.CloseTime(), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := l.dropsDestroyed
	if err := l.AdjustDropsDestroyed(3); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("AdjustDropsDestroyed after close error = %v, want ErrLedgerImmutable", err)
	}
	if l.dropsDestroyed != before {
		t.Fatalf("closed-ledger mutation changed destroyed drops from %d to %d", before, l.dropsDestroyed)
	}
}

func TestClosedLedgerRejectsMutableSnapshots(t *testing.T) {
	l := newOpenChild(t)
	if err := l.Close(l.CloseTime(), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.MutableSnapshot(); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("MutableSnapshot error = %v, want ErrLedgerImmutable", err)
	}
	if _, err := l.MutableSnapshotUnflushed(); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("MutableSnapshotUnflushed error = %v, want ErrLedgerImmutable", err)
	}
}
