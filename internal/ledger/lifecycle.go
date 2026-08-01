package ledger

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/negativeunl"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
)

// Close closes the ledger, making it immutable
func (l *Ledger) Close(closeTime time.Time, closeFlags uint8) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}
	if !l.writable {
		return ErrLedgerImmutable
	}

	// Guard total-drops against unsigned underflow before mutating state:
	// header.Drops is uint64, so over-subtraction would wrap to a huge value,
	// be hashed into the header, and fork the chain. Invariants bound destroyed
	// XRP below supply, so this never fires under correct operation.
	if l.dropsDestroyed < 0 || uint64(l.dropsDestroyed) > l.header.Drops {
		return fmt.Errorf("ledger: drops underflow closing ledger %d: destroyed %d exceeds total %d",
			l.header.LedgerIndex, int64(l.dropsDestroyed), l.header.Drops)
	}

	stateMap, err := l.stateMap.MutableFork()
	if err != nil {
		return fmt.Errorf("failed to fork state map: %w", err)
	}
	txMap, err := l.txMap.MutableFork()
	if err != nil {
		return fmt.Errorf("failed to fork transaction map: %w", err)
	}
	if err := skiplist.UpdateOnMap(stateMap, l.header.LedgerIndex, l.header.ParentHash); err != nil {
		return fmt.Errorf("failed to update skip list: %w", err)
	}
	rules, err := loadAmendmentsFromSHAMap(stateMap)
	if err != nil {
		return fmt.Errorf("failed to load amendment rules: %w", err)
	}
	if err := stateMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make state map immutable: %w", err)
	}
	if err := txMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make tx map immutable: %w", err)
	}
	accountHash, err := stateMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get state map hash: %w", err)
	}
	txHash, err := txMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get tx map hash: %w", err)
	}

	hdr := l.header
	hdr.Drops -= uint64(l.dropsDestroyed)
	hdr.AccountHash = accountHash
	hdr.TxHash = txHash
	hdr.CloseTime = closeTime
	hdr.CloseFlags = closeFlags
	hdr.Accepted = true
	hdr.Hash = header.CalculateHash(hdr)

	l.stateMap = stateMap
	l.txMap = txMap
	l.header = hdr
	l.rules = rules
	l.state = StateClosed
	l.writable = false

	return nil
}

// UpdateNegativeUNL applies pending ValidatorToDisable / ValidatorToReEnable
// transitions on the NegativeUNL SLE during flag-ledger processing,
// before any transactions are applied. No-op on any other ledger or when neither
// transition field is set. Caller must NOT hold l.mu — it acquires it internally.
func (l *Ledger) UpdateNegativeUNL() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}
	if !l.writable {
		return ErrLedgerImmutable
	}

	return negativeunl.Apply(l.stateMap, l.header.LedgerIndex)
}

func (l *Ledger) SetValidated() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateClosed {
		return ErrLedgerNotClosed
	}

	l.header.Validated = true
	l.state = StateValidated

	return nil
}
