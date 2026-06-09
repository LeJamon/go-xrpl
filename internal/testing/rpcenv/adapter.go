package rpcenv

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
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

	validated := a.haveValidated()
	switch ledgerIndex {
	case "", "validated", "closed":
		if closed == nil {
			return nil, false, fmt.Errorf("rpcenv: no closed ledger available — call env.Close() before querying %q", ledgerIndex)
		}
		return closed, validated, nil
	case "current":
		return open, false, nil
	}

	seq, err := strconv.ParseUint(ledgerIndex, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("rpcenv: unsupported ledger_index %q", ledgerIndex)
	}
	want := uint32(seq)
	if closed != nil && closed.Sequence() == want {
		return closed, validated, nil
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

// haveValidated mirrors rippled's LedgerMaster::haveValidated() in
// standalone mode: the genesis ledger does not count — a real close must
// have advanced past it.
func (a *ledgerAdapter) haveValidated() bool {
	closed := a.env.LastClosedLedger()
	return closed != nil && closed.Sequence() > genesis.GenesisLedgerSequence
}

func (a *ledgerAdapter) GetCurrentLedgerIndex() uint32 {
	return a.env.LedgerSeq()
}

func (a *ledgerAdapter) GetClosedLedgerIndex() uint32 {
	return ledgerSeq(a.env.LastClosedLedger())
}

func (a *ledgerAdapter) GetValidatedLedgerIndex() uint32 {
	if !a.haveValidated() {
		return 0
	}
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
	}
	if a.haveValidated() {
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
		return nil, svcerr.ErrLedgerEntryNotFound
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

func (a *ledgerAdapter) GetAutofillFee(_ []byte, _ bool, _, _ int) (uint64, error) {
	return 0, errNotImplemented
}

func (a *ledgerAdapter) GetAutofillSequence(_ string, _ bool) (uint32, error) {
	return 0, errNotImplemented
}

// GetAccountInfo serves as the worked example for extending this adapter:
// decode address → keylet.Account → Exists/Read → parse SLE → fill
// types.AccountInfo with hex-formatted hashes and decimal-formatted balance.
// Matches the conversion done by internal/rpc/ledger_adapter.go so
// handlers see identical shapes whether they run against production or the
// harness.
func (a *ledgerAdapter) GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*types.AccountInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, validated, err := a.resolveLedger(ledgerIndex)
	if err != nil {
		return nil, err
	}

	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	accountKey := keylet.Account(accountID)
	exists, err := target.Exists(accountKey)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, svcerr.ErrAccountNotFound
	}
	data, err := target.Read(accountKey)
	if err != nil {
		return nil, err
	}
	root, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("rpcenv: parse AccountRoot: %w", err)
	}

	var prevTxnID string
	if root.PreviousTxnID != ([32]byte{}) {
		prevTxnID = fmt.Sprintf("%X", root.PreviousTxnID)
	}
	return &types.AccountInfo{
		Account:           account,
		Balance:           strconv.FormatUint(root.Balance, 10),
		Flags:             root.Flags,
		OwnerCount:        root.OwnerCount,
		Sequence:          root.Sequence,
		RegularKey:        root.RegularKey,
		Domain:            root.Domain,
		EmailHash:         root.EmailHash,
		TransferRate:      root.TransferRate,
		TickSize:          root.TickSize,
		PreviousTxnID:     prevTxnID,
		PreviousTxnLgrSeq: root.PreviousTxnLgrSeq,
		LedgerIndex:       target.Sequence(),
		LedgerHash:        fmt.Sprintf("%X", target.Hash()),
		Validated:         validated,
		RawData:           data,
		Index:             hex.EncodeToString(accountKey.Key[:]),
	}, nil
}

// Methods below return errNotImplemented. To wire one up, follow the
// GetAccountInfo pattern above: derive the keylet, read the SLE, parse
// it, and convert to the result type. internal/rpc/ledger_adapter.go has
// the canonical service→types conversions for reference.

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

func (a *ledgerAdapter) GetBookOffers(_ context.Context, _, _ types.Amount, _ string, _ string, _ string, _ uint32, _ string, _ bool) (*types.BookOffersResult, error) {
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

// IsValidated treats any post-genesis closed ledger as validated. In
// standalone there is no separate validation step; the genesis ledger
// itself is excluded so callers can still distinguish "just spun up" from
// "advanced at least one ledger".
func (r *ledgerReaderAdapter) IsValidated() bool {
	return r.l.IsClosed() && r.l.Sequence() > genesis.GenesisLedgerSequence
}
func (r *ledgerReaderAdapter) TotalDrops() uint64 { return r.l.TotalDrops() }

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
