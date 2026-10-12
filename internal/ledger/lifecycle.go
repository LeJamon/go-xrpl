package ledger

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/negativeunl"
)

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

	return l.closeExecution(closeTime, closeFlags)
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

	return negativeunl.Apply(l.stateTree(), l.header.LedgerIndex)
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
