package adapter

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// GetLedgerBySequence returns a ledger by its sequence number
func (a *LedgerServiceAdapter) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	l, err := a.svc.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}
	if l.IsOpen() {
		l, err = l.Snapshot()
		if err != nil {
			return nil, err
		}
	}
	return &ledgerReaderAdapter{l: l}, nil
}

// GetLedgerByHash returns a ledger by its hash
func (a *LedgerServiceAdapter) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	l, err := a.svc.GetLedgerByHash(hash)
	if err != nil {
		return nil, err
	}
	return &ledgerReaderAdapter{l: l}, nil
}

func (a *LedgerServiceAdapter) GetLedgerByHashContext(ctx context.Context, hash [32]byte) (types.LedgerReader, error) {
	l, err := a.svc.GetLedgerByHashContext(ctx, hash)
	if err != nil {
		return nil, err
	}
	return &ledgerReaderAdapter{l: l}, nil
}

// GetGenesisAccount returns the genesis account address
func (a *LedgerServiceAdapter) GetGenesisAccount() (string, error) {
	return a.svc.GetGenesisAccount()
}

// ledgerReaderAdapter adapts ledger.Ledger to types.LedgerReader interface
type ledgerReaderAdapter struct {
	l *ledger.Ledger
}

func (a *ledgerReaderAdapter) Sequence() uint32 {
	return a.l.Sequence()
}

func (a *ledgerReaderAdapter) Hash() [32]byte {
	return a.l.Hash()
}

func (a *ledgerReaderAdapter) ParentHash() [32]byte {
	return a.l.ParentHash()
}

func (a *ledgerReaderAdapter) IsClosed() bool {
	return a.l.IsClosed()
}

func (a *ledgerReaderAdapter) IsValidated() bool {
	return a.l.IsValidated()
}

func (a *ledgerReaderAdapter) TotalDrops() uint64 {
	return a.l.TotalDrops()
}

func (a *ledgerReaderAdapter) CloseTime() int64 {
	return protocol.RippleSeconds(a.l.CloseTime())
}

func (a *ledgerReaderAdapter) CloseTimeResolution() uint32 {
	return uint32(a.l.Header().CloseTimeResolution)
}

func (a *ledgerReaderAdapter) CloseFlags() uint8 {
	return a.l.Header().CloseFlags
}

func (a *ledgerReaderAdapter) ParentCloseTime() int64 {
	return protocol.RippleSeconds(a.l.ParentCloseTime())
}

func (a *ledgerReaderAdapter) TxMapHash() [32]byte {
	return a.l.Header().TxHash
}

func (a *ledgerReaderAdapter) StateMapHash() [32]byte {
	return a.l.Header().AccountHash
}

func (a *ledgerReaderAdapter) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	return a.l.ForEachTransaction(fn)
}

func (a *ledgerReaderAdapter) ForEachTransactionContext(ctx context.Context, fn func(txHash [32]byte, txData []byte) bool) error {
	return a.l.ForEachTransactionContext(ctx, fn)
}

func (a *ledgerReaderAdapter) GetLedgerTransaction(txHash [32]byte) ([]byte, bool, error) {
	return a.l.GetTransaction(txHash)
}

func (a *ledgerReaderAdapter) GetLedgerTransactionContext(ctx context.Context, txHash [32]byte) ([]byte, bool, error) {
	return a.l.GetTransactionContext(ctx, txHash)
}

func (a *ledgerReaderAdapter) ForEachLedgerStateContext(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	return a.l.ForEachContext(ctx, fn)
}

func (a *ledgerReaderAdapter) LedgerAmendmentRules() *amendment.Rules {
	return a.l.Rules()
}

func (a *ledgerReaderAdapter) LedgerAmendmentRulesWithError() (*amendment.Rules, error) {
	rules := a.l.Rules()
	if rules == nil {
		return nil, fmt.Errorf("ledger %d amendment rules unavailable", a.l.Sequence())
	}
	return rules, nil
}

// GetClosedLedgerView returns a read-only view of the last closed ledger
// for use by pathfinding and other operations that need direct state access.
func (a *LedgerServiceAdapter) GetClosedLedgerView() (types.LedgerStateView, error) {
	l := a.svc.GetClosedLedger()
	if l == nil {
		return nil, fmt.Errorf("no closed ledger available")
	}
	return l, nil
}

// GetLedgerViewBySeq returns a state view of the ledger with the given
// sequence, plus its metadata reader.
func (a *LedgerServiceAdapter) GetLedgerViewBySeq(seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	l, err := a.svc.GetLedgerBySequence(seq)
	if err != nil {
		return nil, nil, err
	}
	return l, &ledgerReaderAdapter{l: l}, nil
}

// GetLedgerViewByHash returns a state view of the ledger with the given
// hash, plus its metadata reader.
func (a *LedgerServiceAdapter) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	l, err := a.svc.GetLedgerByHash(hash)
	if err != nil {
		return nil, nil, err
	}
	return l, &ledgerReaderAdapter{l: l}, nil
}
