package adaptor

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

var errPreferredLedgerAdaptor = errors.New("preferred ledger recovery unavailable")

type preparedPreferredLedger struct {
	adaptor      *Adaptor
	inner        consensus.PreferredLedgerPreparation
	target       consensus.Ledger
	historyFloor uint32
}

func (p *preparedPreferredLedger) Wait(ctx context.Context) error {
	return p.inner.Wait(ctx)
}

func (p *preparedPreferredLedger) TryPublish() (bool, error) {
	return p.inner.TryPublish()
}

func (p *preparedPreferredLedger) Finish() {
	p.inner.Finish()
}

func (p *preparedPreferredLedger) Release() {
	p.inner.Release()
}

// NotifyPreferredLedgerSwitched preserves the history floor observed before
// service publication. Once the selected ledger is canonical, recomputing the
// floor from the service's closed ledger would mistake a forked handoff for a
// direct successor.
func (p *preparedPreferredLedger) NotifyPreferredLedgerSwitched(ledger consensus.Ledger) error {
	if ledger == nil {
		ledger = p.target
	}
	if err := p.adaptor.emitLedgerSwitchedWithHistoryFloor(ledger, false, p.historyFloor, true); err != nil {
		return err
	}
	p.adaptor.promoteAfterPreferredLedgerSwitch()
	return nil
}

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
	return &preparedPreferredLedger{
		adaptor: a,
		inner:   prepared,
		target:  parent,
		historyFloor: switchedLedgerHistoryFloor(
			wrapped.Unwrap(),
			a.ledgerService.GetClosedLedger(),
			a.ledgerService.GetValidatedLedger(),
		),
	}, nil
}
