package adapter

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
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
	return a.acquireLedgerRead(ctx, hex.EncodeToString(hash[:]))
}

// GetLedgerBySequenceContext acquires a root lease for the exact ledger
// version. Handlers must close the returned reader after their final read.
func (a *LedgerServiceAdapter) GetLedgerBySequenceContext(ctx context.Context, seq uint32) (types.LedgerReader, error) {
	return a.acquireLedgerRead(ctx, strconv.FormatUint(uint64(seq), 10))
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

// ledgerReadHandle combines the state view and metadata reader for one
// request. The service lease is released exactly once, even when callers pass
// the same handle through both interfaces or close it from cancellation and
// completion paths concurrently.
type ledgerReadHandle struct {
	*ledger.Ledger
	lease     *service.LedgerReadLease
	closeOnce sync.Once
	closeErr  error
}

func (h *ledgerReadHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.lease != nil {
			h.closeErr = h.lease.Close()
		}
	})
	return h.closeErr
}

func (h *ledgerReadHandle) Sequence() uint32 { return h.Ledger.Sequence() }

func (h *ledgerReadHandle) Hash() [32]byte { return h.Ledger.Hash() }

func (h *ledgerReadHandle) ParentHash() [32]byte { return h.Ledger.ParentHash() }

func (h *ledgerReadHandle) IsClosed() bool { return h.Ledger.IsClosed() }

func (h *ledgerReadHandle) IsValidated() bool { return h.Ledger.IsValidated() }

func (h *ledgerReadHandle) TotalDrops() uint64 { return h.Ledger.TotalDrops() }

func (h *ledgerReadHandle) CloseTime() int64 { return protocol.RippleSeconds(h.Ledger.CloseTime()) }

func (h *ledgerReadHandle) CloseTimeResolution() uint32 {
	return uint32(h.Ledger.Header().CloseTimeResolution)
}

func (h *ledgerReadHandle) CloseFlags() uint8 { return h.Ledger.Header().CloseFlags }

func (h *ledgerReadHandle) ParentCloseTime() int64 {
	return protocol.RippleSeconds(h.Ledger.ParentCloseTime())
}

func (h *ledgerReadHandle) TxMapHash() [32]byte { return h.Ledger.Header().TxHash }

func (h *ledgerReadHandle) StateMapHash() [32]byte { return h.Ledger.Header().AccountHash }

func (h *ledgerReadHandle) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	return h.Ledger.ForEachTransaction(fn)
}

func (h *ledgerReadHandle) ForEachTransactionContext(ctx context.Context, fn func(txHash [32]byte, txData []byte) bool) error {
	return h.Ledger.ForEachTransactionContext(ctx, fn)
}

func (h *ledgerReadHandle) GetLedgerTransaction(txHash [32]byte) ([]byte, bool, error) {
	return h.Ledger.GetTransaction(txHash)
}

func (h *ledgerReadHandle) GetLedgerTransactionContext(ctx context.Context, txHash [32]byte) ([]byte, bool, error) {
	return h.Ledger.GetTransactionContext(ctx, txHash)
}

func (h *ledgerReadHandle) ForEachLedgerStateContext(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	return h.Ledger.ForEachContext(ctx, fn)
}

func (h *ledgerReadHandle) LedgerAmendmentRules() *amendment.Rules { return h.Ledger.Rules() }

func (h *ledgerReadHandle) LedgerAmendmentRulesWithError() (*amendment.Rules, error) {
	rules := h.Ledger.Rules()
	if rules == nil {
		return nil, fmt.Errorf("ledger %d amendment rules unavailable", h.Ledger.Sequence())
	}
	return rules, nil
}

func (a *LedgerServiceAdapter) acquireLedgerRead(ctx context.Context, selector string) (types.LedgerReader, error) {
	lease, err := a.svc.AcquireLedgerRead(ctx, selector)
	if err != nil {
		return nil, err
	}
	if lease == nil || lease.Ledger == nil {
		if lease != nil {
			_ = lease.Close()
		}
		return nil, fmt.Errorf("ledger read lease has no ledger")
	}
	return &ledgerReadHandle{Ledger: lease.Ledger, lease: lease}, nil
}

func (a *LedgerServiceAdapter) acquireLedgerView(ctx context.Context, selector string) (*ledgerReadHandle, error) {
	reader, err := a.acquireLedgerRead(ctx, selector)
	if err != nil {
		return nil, err
	}
	handle, ok := reader.(*ledgerReadHandle)
	if !ok {
		return nil, fmt.Errorf("ledger read lease returned unexpected reader")
	}
	return handle, nil
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

// GetClosedLedgerViewContext acquires a root lease for the closed ledger.
func (a *LedgerServiceAdapter) GetClosedLedgerViewContext(ctx context.Context) (types.LedgerStateView, error) {
	return a.acquireLedgerView(ctx, "closed")
}

// GetOpenLedgerView returns an immutable snapshot of the current open ledger.
func (a *LedgerServiceAdapter) GetOpenLedgerView() (types.LedgerStateView, error) {
	l := a.svc.GetOpenLedger()
	if l == nil {
		return nil, fmt.Errorf("no open ledger available")
	}
	snapshot, err := l.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// GetOpenLedgerViewContext acquires a root lease for the current ledger.
func (a *LedgerServiceAdapter) GetOpenLedgerViewContext(ctx context.Context) (types.LedgerStateView, error) {
	return a.acquireLedgerView(ctx, "current")
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

// GetLedgerViewBySeqContext returns one closeable handle through both view and
// metadata interfaces so callers can release the exact root once.
func (a *LedgerServiceAdapter) GetLedgerViewBySeqContext(ctx context.Context, seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	handle, err := a.acquireLedgerView(ctx, strconv.FormatUint(uint64(seq), 10))
	if err != nil {
		return nil, nil, err
	}
	return handle, handle, nil
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

// GetLedgerViewByHashContext is the context-aware, leased counterpart to
// GetLedgerViewByHash.
func (a *LedgerServiceAdapter) GetLedgerViewByHashContext(ctx context.Context, hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	handle, err := a.acquireLedgerView(ctx, hex.EncodeToString(hash[:]))
	if err != nil {
		return nil, nil, err
	}
	return handle, handle, nil
}
