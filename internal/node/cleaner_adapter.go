package node

import (
	"context"
	"errors"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/shamap"
)

type ledgerCleanerService interface {
	AvailableLedgerRange() (uint32, uint32, bool)
	CleanerLedger(context.Context, uint32) (*ledger.Ledger, error)
	CanonicalLedgerHash(context.Context, uint32) ([32]byte, bool, error)
	RepairCleanerLedgerIndex(context.Context, uint32, [32]byte, [32]byte) (bool, error)
	RepairLedgerTransactions(context.Context, uint32) error
}

type ledgerCleanerSource struct {
	svc    ledgerCleanerService
	family shamap.Family

	mu        sync.RWMutex
	reacquire func(context.Context, uint32) error
}

func (s *ledgerCleanerSource) AvailableRange() (uint32, uint32, bool) {
	return s.svc.AvailableLedgerRange()
}

func (s *ledgerCleanerSource) Ledger(ctx context.Context, seq uint32) (cleaner.LedgerData, bool, error) {
	l, err := s.svc.CleanerLedger(ctx, seq)
	if err != nil || l == nil {
		return cleaner.LedgerData{}, false, err
	}
	sr, err := l.StateMapHash()
	if err != nil {
		return cleaner.LedgerData{}, false, err
	}
	tr, err := l.TxMapHash()
	if err != nil {
		return cleaner.LedgerData{}, false, err
	}
	return cleaner.LedgerData{
		Sequence:   l.Sequence(),
		Hash:       l.Hash(),
		ParentHash: l.ParentHash(),
		StateRoot:  sr,
		TxRoot:     tr,
	}, true, nil
}

func (s *ledgerCleanerSource) CanonicalHash(
	ctx context.Context,
	seq uint32,
) ([32]byte, bool, error) {
	return s.svc.CanonicalLedgerHash(ctx, seq)
}

func (s *ledgerCleanerSource) RepairLedgerIndex(
	ctx context.Context,
	info cleaner.LedgerData,
) (bool, error) {
	return s.svc.RepairCleanerLedgerIndex(
		ctx, info.Sequence, info.Hash, info.ParentHash,
	)
}

func (s *ledgerCleanerSource) Family() shamap.Family { return s.family }

func (s *ledgerCleanerSource) SetReacquire(fn func(context.Context, uint32) error) {
	s.mu.Lock()
	s.reacquire = fn
	s.mu.Unlock()
}

func (s *ledgerCleanerSource) Reacquire(ctx context.Context, seq uint32) error {
	s.mu.RLock()
	fn := s.reacquire
	s.mu.RUnlock()
	if fn == nil {
		return errors.New("ledger_cleaner: ledger acquisition unavailable")
	}
	return fn(ctx, seq)
}

func (s *ledgerCleanerSource) RepairTransactions(ctx context.Context, seq uint32) error {
	return s.svc.RepairLedgerTransactions(ctx, seq)
}

// toCleanerStatus translates the cleaner package's status into the RPC-types
// mirror struct (see ServiceContainer.LedgerCleanerConfigure for the layering
// boundary).
func toCleanerStatus(s cleaner.Status) types.LedgerCleanerStatus {
	return types.LedgerCleanerStatus{
		State:          s.State,
		MinLedger:      s.MinLedger,
		MaxLedger:      s.MaxLedger,
		CheckNodes:     s.CheckNodes,
		FixTxns:        s.FixTxns,
		Failures:       s.Failures,
		LedgersChecked: s.LedgersChecked,
		NodesChecked:   s.NodesChecked,
		MissingNodes:   s.MissingNodes,
		LastError:      s.LastError,
	}
}
