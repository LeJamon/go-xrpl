package rpcenv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/keylet"
)

var errNotImplemented = errors.New("rpcenv: LedgerService method not implemented — extend the adapter when adding a consumer test")

// ledgerAdapter mirrors rippled's jtx::Env: tests reach the same handlers
// that production hits, against a real ledger that just had transactions
// applied. Methods not yet exercised by a consumer test return
// errNotImplemented so the gap is obvious.
type ledgerAdapter struct {
	env *testing.TestEnv
}

var _ types.LedgerService = (*ledgerAdapter)(nil)

func newLedgerAdapter(env *testing.TestEnv) *ledgerAdapter {
	return &ledgerAdapter{env: env}
}

// resolveLedger maps a ledgerIndex specifier to a ledger. In standalone
// test mode the most recent closed ledger plays the role of the validated
// one.
func (a *ledgerAdapter) resolveLedger(ledgerIndex string) (*ledger.Ledger, bool, error) {
	open := a.env.Ledger()
	closed := a.env.LastClosedLedger()

	switch ledgerIndex {
	case "", "validated", "closed":
		if closed == nil {
			return nil, false, fmt.Errorf("rpcenv: no closed ledger available — call env.Close() before querying %q", ledgerIndex)
		}
		return closed, true, nil
	case "current":
		return open, false, nil
	}

	seq, err := strconv.ParseUint(ledgerIndex, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("rpcenv: unsupported ledger_index %q", ledgerIndex)
	}
	want := uint32(seq)
	if closed != nil && closed.Sequence() == want {
		return closed, true, nil
	}
	if open != nil && open.Sequence() == want {
		return open, false, nil
	}
	return nil, false, fmt.Errorf("rpcenv: ledger %d not available (open=%d closed=%d)", want,
		ledgerSeq(open), ledgerSeq(closed))
}

func ledgerSeq(l *ledger.Ledger) uint32 {
	if l == nil {
		return 0
	}
	return l.Sequence()
}

func (a *ledgerAdapter) GetCurrentLedgerIndex() uint32 {
	return a.env.LedgerSeq()
}

func (a *ledgerAdapter) GetClosedLedgerIndex() uint32 {
	return ledgerSeq(a.env.LastClosedLedger())
}

func (a *ledgerAdapter) GetValidatedLedgerIndex() uint32 {
	return ledgerSeq(a.env.LastClosedLedger())
}

func (a *ledgerAdapter) AcceptLedger(ctx context.Context) (uint32, error) {
	a.env.Close()
	return a.GetClosedLedgerIndex(), nil
}

func (a *ledgerAdapter) AcceptLedgerAt(ctx context.Context, _ time.Time) (uint32, error) {
	return a.AcceptLedger(ctx)
}

func (a *ledgerAdapter) IsStandalone() bool { return true }

func (a *ledgerAdapter) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	closed := a.env.LastClosedLedger()
	if closed != nil && closed.Sequence() == seq {
		return &ledgerReaderAdapter{l: closed}, nil
	}
	if open := a.env.Ledger(); open != nil && open.Sequence() == seq {
		return &ledgerReaderAdapter{l: open}, nil
	}
	return nil, fmt.Errorf("rpcenv: ledger %d not available", seq)
}

func (a *ledgerAdapter) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if closed := a.env.LastClosedLedger(); closed != nil && closed.Hash() == hash {
		return &ledgerReaderAdapter{l: closed}, nil
	}
	if open := a.env.Ledger(); open != nil && open.Hash() == hash {
		return &ledgerReaderAdapter{l: open}, nil
	}
	return nil, fmt.Errorf("rpcenv: ledger %x not available", hash)
}

func (a *ledgerAdapter) GetServerInfo() types.LedgerServerInfo {
	closed := a.env.LastClosedLedger()
	info := types.LedgerServerInfo{
		Standalone:    true,
		ServerState:   "full",
		OpenLedgerSeq: a.env.LedgerSeq(),
	}
	if closed != nil {
		info.ClosedLedgerSeq = closed.Sequence()
		info.ClosedLedgerHash = closed.Hash()
		info.HaveValidated = true
		info.ValidatedLedgerSeq = closed.Sequence()
		info.ValidatedLedgerHash = closed.Hash()
	}
	return info
}

func (a *ledgerAdapter) GetGenesisAccount() (string, error) {
	return a.env.MasterAccount().Address, nil
}

func (a *ledgerAdapter) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return a.env.BaseFee(), a.env.ReserveBase(), a.env.ReserveIncrement()
}

func (a *ledgerAdapter) GetLedgerRange(_ context.Context, _, _ uint32) (*types.LedgerRangeResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, validated, err := a.resolveLedger(ledgerIndex)
	if err != nil {
		return nil, err
	}
	k := keylet.Keylet{Key: entryKey}
	exists, err := target.Exists(k)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("rpcenv: ledger entry not found")
	}
	data, err := target.Read(k)
	if err != nil {
		return nil, err
	}
	return &types.LedgerEntryResult{
		Index:       fmt.Sprintf("%X", entryKey),
		LedgerIndex: target.Sequence(),
		LedgerHash:  target.Hash(),
		Node:        data,
		Validated:   validated,
	}, nil
}

func (a *ledgerAdapter) GetLedgerData(_ context.Context, _ string, _ uint32, _ string) (*types.LedgerDataResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetClosedLedgerView() (types.LedgerStateView, error) {
	closed := a.env.LastClosedLedger()
	if closed == nil {
		return nil, fmt.Errorf("rpcenv: no closed ledger — call env.Close() first")
	}
	return closed, nil
}

func (a *ledgerAdapter) IsAmendmentBlocked() bool { return false }

func (a *ledgerAdapter) SubmitTransaction(_ []byte, _ ...string) (*types.SubmitResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) SimulateTransaction(_ []byte) (*types.SubmitResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetTransaction(_ [32]byte) (*types.TransactionInfo, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) StoreTransaction(_ [32]byte, _ []byte) error {
	return errNotImplemented
}

func (a *ledgerAdapter) GetTransactionHistory(_ context.Context, _ uint32) (*types.TxHistoryResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAutofillFee(_ []byte) (uint64, error) {
	return 0, errNotImplemented
}

func (a *ledgerAdapter) GetAutofillSequence(_ string, _ bool) (uint32, error) {
	return 0, errNotImplemented
}

func (a *ledgerAdapter) GetAccountInfo(_ context.Context, _ string, _ string) (*types.AccountInfo, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountLines(_ context.Context, _ string, _ string, _ string, _ uint32) (*types.AccountLinesResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountOffers(_ context.Context, _ string, _ string, _ uint32) (*types.AccountOffersResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountTransactions(_ context.Context, _ string, _, _ int64, _ uint32, _ *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountChannels(_ context.Context, _ string, _ string, _ string, _ uint32) (*types.AccountChannelsResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountCurrencies(_ context.Context, _ string, _ string) (*types.AccountCurrenciesResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountObjects(_ context.Context, _ string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountNFTs(_ context.Context, _ string, _ string, _ uint32) (*types.AccountNFTsResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetBookOffers(_ context.Context, _, _ types.Amount, _ string, _ string, _ string, _ uint32) (*types.BookOffersResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetGatewayBalances(_ context.Context, _ string, _ []string, _ string) (*types.GatewayBalancesResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetNoRippleCheck(_ context.Context, _ string, _ string, _ string, _ uint32, _ bool) (*types.NoRippleCheckResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetDepositAuthorized(_ context.Context, _ string, _ string, _ string, _ []string) (*types.DepositAuthorizedResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetNFTBuyOffers(_ context.Context, _ [32]byte, _ string, _ uint32, _ string) (*types.NFTOffersResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetNFTSellOffers(_ context.Context, _ [32]byte, _ string, _ uint32, _ string) (*types.NFTOffersResult, error) {
	return nil, errNotImplemented
}

type ledgerReaderAdapter struct {
	l *ledger.Ledger
}

func (r *ledgerReaderAdapter) Sequence() uint32     { return r.l.Sequence() }
func (r *ledgerReaderAdapter) Hash() [32]byte       { return r.l.Hash() }
func (r *ledgerReaderAdapter) ParentHash() [32]byte { return r.l.ParentHash() }
func (r *ledgerReaderAdapter) IsClosed() bool       { return r.l.IsClosed() }
func (r *ledgerReaderAdapter) IsValidated() bool    { return r.l.IsClosed() }
func (r *ledgerReaderAdapter) TotalDrops() uint64   { return r.l.TotalDrops() }

func (r *ledgerReaderAdapter) CloseTime() int64 {
	t := r.l.CloseTime()
	if t.IsZero() {
		return 0
	}
	return rippleEpochSeconds(t)
}

func (r *ledgerReaderAdapter) CloseTimeResolution() uint32 { return r.l.Header().CloseTimeResolution }
func (r *ledgerReaderAdapter) CloseFlags() uint8           { return r.l.Header().CloseFlags }

func (r *ledgerReaderAdapter) ParentCloseTime() int64 {
	t := r.l.ParentCloseTime()
	if t.IsZero() {
		return 0
	}
	return rippleEpochSeconds(t)
}

func (r *ledgerReaderAdapter) TxMapHash() [32]byte {
	h, _ := r.l.TxMapHash()
	return h
}

func (r *ledgerReaderAdapter) StateMapHash() [32]byte {
	h, _ := r.l.StateMapHash()
	return h
}

func (r *ledgerReaderAdapter) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	return r.l.ForEachTransaction(fn)
}
