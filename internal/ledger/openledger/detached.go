package openledger

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger"
)

// TransactionStatsContext measures a captured ledger rather than the view's
// latest current pointer. Detached callers use it to reserve memory for the
// exact snapshot they may publish.
func TransactionStatsContext(ctx context.Context, view *ledger.Ledger) (TransactionSnapshotStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return collectTransactionStatsContext(ctx, view)
}

// ForkCurrent creates an independent open-ledger wrapper from parent. The
// source ledger is immutable; SubmitDetailed snapshots it before applying.
func (o *OpenLedger) ForkCurrent(parent *ledger.Ledger) (*OpenLedger, error) {
	if o == nil {
		return nil, errors.New("openledger: cannot fork a nil view")
	}
	if parent == nil {
		return nil, errors.New("openledger: cannot fork a nil current ledger")
	}
	return &OpenLedger{
		cfg:     o.cfg,
		logger:  o.logger,
		current: parent,
	}, nil
}

// PublishCurrent replaces the published current view only when it still points
// at expected. Callers fence the surrounding service state separately.
func (o *OpenLedger) PublishCurrent(expected, replacement *ledger.Ledger) bool {
	if o == nil || expected == nil || replacement == nil {
		return false
	}
	o.currentMu.Lock()
	defer o.currentMu.Unlock()
	if o.current != expected {
		return false
	}
	o.current = replacement
	o.cachedTxs = nil
	return true
}
