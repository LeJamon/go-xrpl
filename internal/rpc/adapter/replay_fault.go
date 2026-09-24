package adapter

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
)

// ReplayFaultStatus exposes the ledger service's point-in-time validator-duty
// gate to RPC without adding replay-fault methods to the broad LedgerService
// interface. The handler reaches this method through an optional assertion on
// the concrete adapter.
func (a *LedgerServiceAdapter) ReplayFaultStatus() replayfault.Status {
	if a == nil || a.svc == nil {
		return replayfault.Status{
			Blocked:          true,
			PersistenceError: errors.New("ledger service is unavailable"),
		}
	}
	return a.svc.ReplayFaultStatus()
}

// ReplayBlocked reports whether local validator duties are currently gated by
// an unresolved replay fault.
func (a *LedgerServiceAdapter) ReplayBlocked() bool {
	if a == nil || a.svc == nil {
		return true
	}
	return a.svc.ReplayBlocked()
}

// RevalidateReplayFault runs the service-owned explicit transition verifier.
// The service clears the durable gate only after that verifier succeeds.
func (a *LedgerServiceAdapter) RevalidateReplayFault(ctx context.Context, id string) error {
	if a == nil || a.svc == nil {
		return errors.New("ledger service is unavailable")
	}
	return a.svc.RevalidateReplayFault(ctx, id)
}
