package adapter

import (
	"context"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerServiceAdapter adapts the ledger service to the RPC LedgerService interface
type LedgerServiceAdapter struct {
	svc           *service.Service
	txBroadcaster func(txBlob []byte) // called after successful submit to relay tx to peers
}

var _ types.LedgerService = (*LedgerServiceAdapter)(nil)
var _ types.LedgerNavigator = (*LedgerServiceAdapter)(nil)
var _ types.LedgerAccessor = (*LedgerServiceAdapter)(nil)
var _ types.TransactionSubmitter = (*LedgerServiceAdapter)(nil)
var _ types.AccountQuerier = (*LedgerServiceAdapter)(nil)
var _ types.OwnerDirectoryReader = (*LedgerServiceAdapter)(nil)
var _ types.TxTablesProvider = (*LedgerServiceAdapter)(nil)
var _ types.RangedTransactionLookup = (*LedgerServiceAdapter)(nil)
var _ types.TransactionSearcher = (*LedgerServiceAdapter)(nil)
var _ types.LedgerContextReader = (*LedgerServiceAdapter)(nil)
var _ types.LedgerViewSource = (*LedgerServiceAdapter)(nil)
var _ types.OpenLedgerViewSource = (*LedgerServiceAdapter)(nil)
var _ types.TransactionRulesSource = (*LedgerServiceAdapter)(nil)

// NewLedgerServiceAdapter creates a new adapter
func NewLedgerServiceAdapter(svc *service.Service) *LedgerServiceAdapter {
	return &LedgerServiceAdapter{svc: svc}
}

// SetTxBroadcaster sets the callback for relaying submitted transactions to P2P peers.
// Called during server startup once the overlay is available.
func (a *LedgerServiceAdapter) SetTxBroadcaster(fn func(txBlob []byte)) {
	a.txBroadcaster = fn
}

// GetCurrentLedgerIndex returns the current open ledger index
func (a *LedgerServiceAdapter) GetCurrentLedgerIndex() uint32 {
	return a.svc.GetCurrentLedgerIndex()
}

// GetClosedLedgerIndex returns the last closed ledger index
func (a *LedgerServiceAdapter) GetClosedLedgerIndex() uint32 {
	return a.svc.GetClosedLedgerIndex()
}

// GetValidatedLedgerIndex returns the highest validated ledger index
func (a *LedgerServiceAdapter) GetValidatedLedgerIndex() uint32 {
	return a.svc.GetValidatedLedgerIndex()
}

// AcceptLedger closes the current open ledger (standalone mode only)
func (a *LedgerServiceAdapter) AcceptLedger(ctx context.Context) (uint32, error) {
	return a.svc.AcceptLedger(ctx)
}

// AcceptLedgerAt is AcceptLedger with an explicit close_time.
func (a *LedgerServiceAdapter) AcceptLedgerAt(ctx context.Context, closeTime time.Time) (uint32, error) {
	return a.svc.AcceptLedgerAt(ctx, closeTime)
}

// IsStandalone returns true if running in standalone mode
func (a *LedgerServiceAdapter) IsStandalone() bool {
	return a.svc.IsStandalone()
}

// GetServerInfo returns server status information
func (a *LedgerServiceAdapter) GetServerInfo() types.LedgerServerInfo {
	info := a.svc.GetServerInfo()
	return types.LedgerServerInfo{
		Standalone:               info.Standalone,
		ServerState:              info.ServerState,
		NeedsNetworkLedger:       info.NeedsNetworkLedger,
		OpenLedgerSeq:            info.OpenLedgerSeq,
		ClosedLedgerSeq:          info.ClosedLedgerSeq,
		ClosedLedgerHash:         info.ClosedLedgerHash,
		ClosedLedgerCloseTime:    info.ClosedLedgerCloseTime,
		HaveValidated:            info.HaveValidated,
		ValidatedLedgerSeq:       info.ValidatedLedgerSeq,
		ValidatedLedgerHash:      info.ValidatedLedgerHash,
		ValidatedLedgerCloseTime: info.ValidatedLedgerCloseTime,
		CompleteLedgers:          info.CompleteLedgers,
		HavePublished:            info.HavePublished,
		PublishedLedgerSeq:       info.PublishedLedgerSeq,
		NetworkID:                info.NetworkID,
	}
}

// GetCurrentFees returns the current fee settings.
func (a *LedgerServiceAdapter) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return a.svc.GetCurrentFees()
}
