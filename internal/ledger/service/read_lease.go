package service

import (
	"context"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap"
)

// LedgerReadLease keeps an exact ledger version available until Close, including
// while queued work outlives its entry in service history.
type LedgerReadLease struct {
	Ledger    *ledger.Ledger
	Validated bool
	release   func()
}

func (l *LedgerReadLease) Close() error {
	if l != nil && l.release != nil {
		l.release()
	}
	return nil
}

// AcquireLedgerRead accepts the same selectors as ledger RPCs. Callers must close
// the lease after their final read, including canceled or replaced queued work.
func (s *Service) AcquireLedgerRead(ctx context.Context, selector string) (*LedgerReadLease, error) {
	releaseWork, err := s.acquireReplayPreparationWork(ctx)
	if err != nil {
		return nil, err
	}
	releases := []func(){releaseWork}
	release := sync.OnceFunc(func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	})
	complete := false
	defer func() {
		if !complete {
			release()
		}
	}()
	// Capture the descriptor and its root ownership before a generation can be
	// retired. The durable pin ends here; the request retains only exact roots.
	releaseGeneration, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseGeneration()
	l, validated, err := s.resolveLedgerForQuery(ctx, selector)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, svcerr.ErrLedgerNotFound
	}
	if s.config.Execution != nil {
		state, err := l.StateMapHash()
		if err != nil {
			return nil, err
		}
		txs, err := l.TxMapHash()
		if err != nil {
			return nil, err
		}
		for _, root := range []rootlease.Root{{Hash: state, Kind: shamap.TypeState}, {Hash: txs, Kind: shamap.TypeTransaction}} {
			done, err := s.config.Execution.AcquireRoot(root)
			if err != nil {
				return nil, err
			}
			releases = append(releases, done)
		}
	}
	complete = true
	return &LedgerReadLease{Ledger: l, Validated: validated, release: release}, nil
}
