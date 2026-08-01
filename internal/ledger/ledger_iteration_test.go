package ledger

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestForEachUsesPointInTimeSnapshotAndAllowsMutation(t *testing.T) {
	l := newOpenChild(t)
	seed := mutAcct(0xd1)
	if err := l.Insert(seed, mutData(0x11)); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	insertedDuringCallback := mutAcct(0xd2)
	mutated := false
	visitedInserted := false
	if err := l.ForEach(func(key [32]byte, _ []byte) bool {
		if key == insertedDuringCallback.Key {
			visitedInserted = true
		}
		if !mutated {
			mutated = true
			if err := l.Insert(insertedDuringCallback, mutData(0x21)); err != nil {
				t.Errorf("Insert from callback: %v", err)
				return false
			}
		}
		return true
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if !mutated {
		t.Fatal("ForEach invoked no callback")
	}
	if visitedInserted {
		t.Fatal("ForEach visited state inserted after its snapshot was taken")
	}
	if exists, err := l.Exists(insertedDuringCallback); err != nil || !exists {
		t.Fatalf("callback insertion was not committed: exists=%v err=%v", exists, err)
	}
}

func TestForEachTransactionUsesPointInTimeSnapshotAndAllowsMutation(t *testing.T) {
	l := newOpenChild(t)
	var seed [32]byte
	seed[0] = 0xe1
	if err := l.AddTransaction(seed, bytes.Repeat([]byte{0x11}, 16)); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	var insertedDuringCallback [32]byte
	insertedDuringCallback[0] = 0xe2
	mutated := false
	visitedInserted := false
	if err := l.ForEachTransaction(func(txID [32]byte, _ []byte) bool {
		if txID == insertedDuringCallback {
			visitedInserted = true
		}
		if !mutated {
			mutated = true
			if err := l.AddTransaction(insertedDuringCallback, bytes.Repeat([]byte{0x21}, 16)); err != nil {
				t.Errorf("AddTransaction from callback: %v", err)
				return false
			}
		}
		return true
	}); err != nil {
		t.Fatalf("ForEachTransaction: %v", err)
	}
	if !mutated {
		t.Fatal("ForEachTransaction invoked no callback")
	}
	if visitedInserted {
		t.Fatal("ForEachTransaction visited a transaction inserted after its snapshot was taken")
	}
	if exists, err := l.TxExists(insertedDuringCallback); err != nil || !exists {
		t.Fatalf("callback transaction was not committed: exists=%v err=%v", exists, err)
	}
}

func TestForEachCallbackCanReenterWithQueuedWriter(t *testing.T) {
	l := newOpenChild(t)
	stateKey := mutAcct(0xf1)
	original := mutData(0x11)
	updated := mutData(0x21)
	if err := l.Insert(stateKey, original); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		var callbackErr error
		iterationErr := l.ForEach(func(key [32]byte, _ []byte) bool {
			if key != stateKey.Key {
				return true
			}

			l.mu.RLock()
			writerStarted := make(chan struct{})
			writerDone := make(chan error, 1)
			go func() {
				close(writerStarted)
				writerDone <- l.Update(stateKey, updated)
			}()
			<-writerStarted
			for l.mu.TryRLock() {
				l.mu.RUnlock()
				runtime.Gosched()
			}
			l.mu.RUnlock()

			got, err := l.Read(stateKey)
			if err != nil {
				callbackErr = fmt.Errorf("reentrant Read: %w", err)
				return false
			}
			if err := <-writerDone; err != nil {
				callbackErr = fmt.Errorf("queued Update: %w", err)
				return false
			}
			if !bytes.Equal(got, updated) {
				callbackErr = fmt.Errorf("reentrant Read = %x, want %x", got, updated)
			}
			return false
		})
		if iterationErr != nil {
			done <- iterationErr
			return
		}
		done <- callbackErr
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-timer.C:
		t.Fatal("ForEach callback deadlocked behind a queued writer")
	}
}
