package adaptor

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

var errPreferredLedgerAdaptor = errors.New("preferred ledger recovery unavailable")

// PreparePreferredLedger detaches the expensive successor build from the
// consensus goroutine. The returned service object is published only from the
// engine's short commit fence; relay and validated-ledger notifications are
// finished after both locks have been released.
func (a *Adaptor) PreparePreferredLedger(
	ctx context.Context,
	parent consensus.Ledger,
) (consensus.PreferredLedgerPreparation, error) {
	if a == nil || a.ledgerService == nil {
		return nil, errPreferredLedgerAdaptor
	}
	wrapped, ok := parent.(*LedgerWrapper)
	if !ok || wrapped == nil || wrapped.Unwrap() == nil {
		return nil, errors.New("preferred ledger recovery: unexpected ledger type")
	}
	prepared, err := a.ledgerService.PreparePreferredLedger(ctx, wrapped.Unwrap())
	if err != nil {
		return nil, err
	}
	return prepared, nil
}
