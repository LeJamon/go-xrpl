package ledger

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (l *Ledger) Exists(k keylet.Keylet) (bool, error) {
	return l.ExistsContext(context.Background(), k)
}

func (l *Ledger) ExistsContext(ctx context.Context, k keylet.Keylet) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.execution.state.HasContext(ctx, k.Key)
}

func (l *Ledger) Insert(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}
	if !l.writable {
		return ErrLedgerImmutable
	}

	exists, err := l.execution.state.Has(k.Key)
	if err != nil {
		return err
	}
	if exists {
		return ErrEntryExists
	}

	return l.execution.state.Put(k.Key, data)
}

func (l *Ledger) Update(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}
	if !l.writable {
		return ErrLedgerImmutable
	}

	exists, err := l.execution.state.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.execution.state.Put(k.Key, data)
}

func (l *Ledger) Erase(k keylet.Keylet) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}
	if !l.writable {
		return ErrLedgerImmutable
	}

	exists, err := l.execution.state.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.execution.state.Delete(k.Key)
}

func (l *Ledger) AdjustDropsDestroyed(drops drops.XRPAmount) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen || !l.writable {
		return ErrLedgerImmutable
	}
	l.dropsDestroyed = l.dropsDestroyed.Add(drops)
	return nil
}

func (l *Ledger) ApplyAtomically(apply func(Writer) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen || !l.writable {
		return ErrLedgerImmutable
	}

	staged, err := l.executionSnapshot(context.Background(), true)
	if err != nil {
		return err
	}
	defer staged.Discard()
	if err := apply(staged); err != nil {
		return err
	}
	l.execution.close()
	l.execution, staged.execution = staged.execution, nil
	l.dropsDestroyed = staged.dropsDestroyed
	staged.execution = emptyExecution(staged.header, l.execution.state.store)
	staged.dropsDestroyed = 0
	staged.writable = false
	return nil
}

// ConsumeState transfers an exclusively owned mutable snapshot derived from
// this ledger without cloning its SHAMaps. The target must not change between
// snapshot creation and transfer. After a successful transfer, src is empty and
// immutable and must not be used concurrently with the transfer.
func (l *Ledger) ConsumeState(src *Ledger) error {
	if src == nil {
		return errors.New("ledger: ConsumeState from nil source")
	}
	if src == l {
		return errors.New("ledger: ConsumeState source is target")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateOpen || !l.writable {
		return ErrLedgerImmutable
	}

	src.mu.Lock()
	defer src.mu.Unlock()
	if src.state != StateOpen || !src.writable {
		return ErrInvalidState
	}

	if l.execution != nil {
		l.execution.close()
	}
	srcStore := src.execution.state.store
	l.execution, src.execution = src.execution, nil
	l.dropsDestroyed = src.dropsDestroyed
	src.execution = emptyExecution(src.header, srcStore)
	src.dropsDestroyed = 0
	src.writable = false
	return nil
}

func (l *Ledger) invalidateMutableState() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.invalidateMutableStateLocked()
}

func (l *Ledger) invalidateMutableStateLocked() {
	var store *ExecutionStore
	if l.execution != nil {
		store = l.execution.state.store
		l.execution.close()
	}
	if store != nil {
		l.execution = emptyExecution(l.header, store)
	}
	l.dropsDestroyed = 0
	l.writable = false
}
