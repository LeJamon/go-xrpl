package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (l *Ledger) Exists(k keylet.Keylet) (bool, error) {
	return l.ExistsContext(context.Background(), k)
}

func (l *Ledger) ExistsContext(ctx context.Context, k keylet.Keylet) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.HasContext(ctx, k.Key)
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

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if exists {
		return ErrEntryExists
	}

	return l.stateMap.Put(k.Key, data)
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

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Put(k.Key, data)
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

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Delete(k.Key)
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

	stateMap, err := l.stateMap.MutableFork()
	if err != nil {
		return fmt.Errorf("fork ledger state: %w", err)
	}
	staged := &Ledger{
		stateMap:       stateMap,
		txMap:          l.txMap,
		header:         l.header,
		fees:           l.fees,
		state:          l.state,
		writable:       true,
		dropsDestroyed: l.dropsDestroyed,
		rules:          l.rules,
	}
	if err := apply(staged); err != nil {
		return err
	}

	l.stateMap = staged.stateMap
	l.dropsDestroyed = staged.dropsDestroyed
	return nil
}

// AdoptState replaces this ledger's state map, tx map, and destroyed-drops tally
// with src's in one shot. Header and fees are unchanged.
func (l *Ledger) AdoptState(src *Ledger) error {
	if src == nil {
		return errors.New("ledger: AdoptState from nil source")
	}
	l.mu.RLock()
	targetWritable := l.state == StateOpen && l.writable
	l.mu.RUnlock()
	if !targetWritable {
		return ErrLedgerImmutable
	}

	src.mu.RLock()
	if src.state != StateOpen || !src.writable {
		src.mu.RUnlock()
		return ErrInvalidState
	}
	stateMap, err := src.stateMap.DetachedMutable()
	if err != nil {
		src.mu.RUnlock()
		return fmt.Errorf("detach adopted state map: %w", err)
	}
	txMap, err := src.txMap.DetachedMutable()
	if err != nil {
		src.mu.RUnlock()
		return fmt.Errorf("detach adopted transaction map: %w", err)
	}
	dropsDestroyed := src.dropsDestroyed
	src.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateOpen || !l.writable {
		return ErrLedgerImmutable
	}
	l.stateMap = stateMap
	l.txMap = txMap
	l.dropsDestroyed = dropsDestroyed
	return nil
}
