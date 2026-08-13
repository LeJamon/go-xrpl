package rpcenv

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var errNotImplemented = errors.New("rpcenv: LedgerService method not implemented — extend the adapter when adding a consumer test")

// ledgerAdapter mirrors rippled's jtx::Env: tests reach the same handlers
// that production hits, against a real ledger that just had transactions
// applied. Methods not yet exercised by a consumer test return
// errNotImplemented so the gap is obvious.
type ledgerAdapter struct {
	env           *jtx.TestEnv
	closedLedgers map[uint32]*ledger.Ledger
}

var _ types.LedgerService = (*ledgerAdapter)(nil)
var _ types.AccountTxDelegateQuerier = (*ledgerAdapter)(nil)
var _ types.LedgerReadService = (*ledgerAdapter)(nil)
var _ types.LedgerMutationService = (*ledgerAdapter)(nil)

func newLedgerAdapter(env *jtx.TestEnv) *ledgerAdapter {
	a := &ledgerAdapter{env: env, closedLedgers: make(map[uint32]*ledger.Ledger)}
	a.recordClosedLedger()
	return a
}

func (a *ledgerAdapter) recordClosedLedger() {
	if closed := a.env.LastClosedLedger(); closed != nil {
		a.closedLedgers[closed.Sequence()] = closed
	}
}

func (a *ledgerAdapter) closedLedger(seq uint32) *ledger.Ledger {
	return a.closedLedgers[seq]
}

func (a *ledgerAdapter) completeLedgerRange(max uint32) string {
	min := max
	for min > 0 && a.closedLedger(min-1) != nil {
		min--
	}
	if min == max {
		return strconv.FormatUint(uint64(max), 10)
	}
	return fmt.Sprintf("%d-%d", min, max)
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
	if historical := a.closedLedger(want); historical != nil {
		return historical, validated, nil
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
	a.recordClosedLedger()
	return a.GetClosedLedgerIndex(), nil
}

func (a *ledgerAdapter) IsStandalone() bool { return true }

func (a *ledgerAdapter) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if closed := a.closedLedger(seq); closed != nil {
		return &ledgerReaderAdapter{l: closed}, nil
	}
	if open := a.env.Ledger(); open != nil && open.Sequence() == seq {
		return &ledgerReaderAdapter{l: open}, nil
	}
	return nil, fmt.Errorf("rpcenv: ledger %d not available", seq)
}

func (a *ledgerAdapter) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	for _, closed := range a.closedLedgers {
		if closed.Hash() == hash {
			return &ledgerReaderAdapter{l: closed}, nil
		}
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
		info.CompleteLedgers = a.completeLedgerRange(closed.Sequence())
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

func (a *ledgerAdapter) GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, validated, err := a.resolveLedger(ledgerIndex)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 200
	}

	var startKey [32]byte
	if marker != "" {
		decoded, decodeErr := hex.DecodeString(marker)
		if len(marker) != 64 || decodeErr != nil {
			return nil, svcerr.ErrInvalidMarker
		}
		copy(startKey[:], decoded)
	}

	result := &types.LedgerDataResult{
		LedgerIndex: target.Sequence(),
		LedgerHash:  target.Hash(),
		State:       make([]types.LedgerDataItem, 0, limit),
		Validated:   validated,
	}
	if marker == "" {
		header := target.Header()
		result.LedgerHeader = &types.LedgerHeaderInfo{
			AccountHash:         header.AccountHash,
			CloseFlags:          header.CloseFlags,
			CloseTime:           protocol.RippleSeconds(header.CloseTime),
			CloseTimeHuman:      header.CloseTime.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC"),
			CloseTimeISO:        protocol.FormatCloseTimeISO(header.CloseTime),
			CloseTimeResolution: uint32(header.CloseTimeResolution),
			Closed:              target.IsClosed(),
			LedgerHash:          header.Hash,
			LedgerIndex:         header.LedgerIndex,
			ParentCloseTime:     protocol.RippleSeconds(header.ParentCloseTime),
			ParentHash:          header.ParentHash,
			TotalCoins:          header.Drops,
			TransactionHash:     header.TxHash,
		}
	}

	count := uint32(0)
	err = target.IterateStateFrom(ctx, startKey, func(key [32]byte, data []byte) bool {
		if count >= limit {
			result.Marker = protocol.Hash256Hex(ledger.DecrementKey(key))
			return false
		}
		result.State = append(result.State, types.LedgerDataItem{
			Index: protocol.Hash256Hex(key),
			Data:  data,
		})
		count++
		return true
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *ledgerAdapter) GetClosedLedgerView() (types.LedgerStateView, error) {
	closed := a.env.LastClosedLedger()
	if closed == nil {
		return nil, fmt.Errorf("rpcenv: no closed ledger — call env.Close() first")
	}
	return closed, nil
}

func (a *ledgerAdapter) GetLedgerViewBySeq(seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	if closed := a.closedLedger(seq); closed != nil {
		return closed, &ledgerReaderAdapter{l: closed}, nil
	}
	if open := a.env.Ledger(); open != nil && open.Sequence() == seq {
		return open, &ledgerReaderAdapter{l: open}, nil
	}
	return nil, nil, fmt.Errorf("rpcenv: ledger %d not available", seq)
}

func (a *ledgerAdapter) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	for _, closed := range a.closedLedgers {
		if closed.Hash() == hash {
			return closed, &ledgerReaderAdapter{l: closed}, nil
		}
	}
	if open := a.env.Ledger(); open != nil && open.Hash() == hash {
		return open, &ledgerReaderAdapter{l: open}, nil
	}
	return nil, nil, fmt.Errorf("rpcenv: ledger %x not available", hash)
}

func (a *ledgerAdapter) IsAmendmentBlocked() bool { return false }

func (a *ledgerAdapter) SubmitTransaction(_ []byte, _ string) (*types.SubmitResult, error) {
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
// Matches the conversion done by internal/rpc/adapter so
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
	root, err := state.ParseAccountRoot(data)
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
// it, and convert to the result type. The production adapter package has the
// canonical service-to-types conversions for reference.

func (a *ledgerAdapter) GetAccountLines(_ context.Context, _ string, _ string, _ string, _ uint32, _ string) (*types.AccountLinesResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountOffers(_ context.Context, _ string, _ string, _ uint32, _ string) (*types.AccountOffersResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return a.getAccountTransactions(ctx, account, ledgerMin, ledgerMax, limit, marker, forward, nil)
}

func (a *ledgerAdapter) GetAccountTransactionsWithDelegate(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool, delegate *types.AccountTxDelegateFilter) (*types.AccountTxResult, error) {
	return a.getAccountTransactions(ctx, account, ledgerMin, ledgerMax, limit, marker, forward, delegate)
}

func (a *ledgerAdapter) getAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool, delegate *types.AccountTxDelegateFilter) (*types.AccountTxResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	closed := a.env.LastClosedLedger()
	if closed == nil {
		return nil, errors.New("rpcenv: no closed ledger available")
	}
	_, rawAccount, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(rawAccount) != len(relationaldb.AccountID{}) {
		return nil, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
	}
	var accountID relationaldb.AccountID
	copy(accountID[:], rawAccount)
	if limit == 0 {
		limit = 200
	}

	minSequence := uint32(1)
	if ledgerMin > 0 {
		minSequence = uint32(ledgerMin)
	}
	maxSequence := closed.Sequence()
	if ledgerMax > 0 && uint32(ledgerMax) < maxSequence {
		maxSequence = uint32(ledgerMax)
	}
	result := &types.AccountTxResult{
		Account:   account,
		LedgerMin: minSequence,
		LedgerMax: maxSequence,
		Limit:     limit,
		Validated: true,
	}
	transactions := make([]types.AccountTransaction, 0)
	for sequence := minSequence; sequence <= maxSequence; sequence++ {
		closed := a.closedLedger(sequence)
		if closed == nil {
			continue
		}
		var iterationErr error
		err := closed.ForEachTransactionContext(ctx, func(hash [32]byte, data []byte) bool {
			accepted := ledgerservice.ParseAcceptedTransaction(data)
			if err := accepted.ParseError(); err != nil {
				iterationErr = fmt.Errorf("rpcenv: parse accepted transaction %x: %w", hash, err)
				return false
			}
			for _, affected := range accepted.AffectedAccounts() {
				if affected != account {
					continue
				}
				transactionIndex, ok := accepted.TransactionIndex()
				if !ok {
					iterationErr = fmt.Errorf("rpcenv: transaction %x has no transaction index", hash)
					return false
				}
				transactions = append(transactions, types.AccountTransaction{
					Hash:        hash,
					LedgerIndex: closed.Sequence(),
					TxnSeq:      transactionIndex,
					TxBlob:      accepted.TransactionBlob(),
					Meta:        accepted.MetadataBlob(),
				})
				break
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if iterationErr != nil {
			return nil, iterationErr
		}
		if sequence == maxSequence {
			break
		}
	}

	sort.Slice(transactions, func(i, j int) bool {
		if transactions[i].LedgerIndex != transactions[j].LedgerIndex {
			if forward {
				return transactions[i].LedgerIndex < transactions[j].LedgerIndex
			}
			return transactions[i].LedgerIndex > transactions[j].LedgerIndex
		}
		if forward {
			return transactions[i].TxnSeq < transactions[j].TxnSeq
		}
		return transactions[i].TxnSeq > transactions[j].TxnSeq
	})
	start := 0
	if marker != nil {
		found := false
		for i := range transactions {
			if transactions[i].LedgerIndex == marker.LedgerSeq && transactions[i].TxnSeq == marker.TxnSeq {
				start = i
				if delegate == nil {
					start++
				}
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("rpcenv: account_tx marker not found")
		}
	}
	if delegate != nil {
		return filterRPCEnvAccountTransactions(account, accountID, result, transactions[start:], limit, marker, delegate)
	}

	end := min(start+int(limit), len(transactions))
	result.Transactions = transactions[start:end]
	if end < len(transactions) && end > start {
		last := transactions[end-1]
		result.Marker = &types.AccountTxMarker{LedgerSeq: last.LedgerIndex, TxnSeq: last.TxnSeq}
	}
	return result, nil
}

func filterRPCEnvAccountTransactions(account string, accountID relationaldb.AccountID, result *types.AccountTxResult, transactions []types.AccountTransaction, limit uint32, marker *types.AccountTxMarker, delegate *types.AccountTxDelegateFilter) (*types.AccountTxResult, error) {
	filter := &relationaldb.AccountTxDelegateFilter{Role: relationaldb.AccountTxDelegateActor}
	if delegate.Role == types.AccountTxDelegateAuthorizer {
		filter.Role = relationaldb.AccountTxDelegateAuthorizer
	}
	if delegate.Counterparty != "" {
		_, rawCounterparty, err := addresscodec.DecodeClassicAddressToAccountID(delegate.Counterparty)
		if err != nil || len(rawCounterparty) != len(relationaldb.AccountID{}) {
			return nil, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
		}
		var counterparty relationaldb.AccountID
		copy(counterparty[:], rawCounterparty)
		filter.Counterparty = &counterparty
	}

	scanEnd := min(int(limit)+1, len(transactions))
	scanned := make([]relationaldb.TransactionInfo, scanEnd)
	for index, transaction := range transactions[:scanEnd] {
		scanned[index] = relationaldb.TransactionInfo{
			Hash:      relationaldb.Hash(transaction.Hash),
			LedgerSeq: relationaldb.LedgerIndex(transaction.LedgerIndex),
			TxnSeq:    transaction.TxnSeq,
			RawTxn:    transaction.TxBlob,
			TxnMeta:   transaction.Meta,
		}
	}
	var relationalMarker *relationaldb.AccountTxMarker
	if marker != nil {
		relationalMarker = &relationaldb.AccountTxMarker{
			LedgerSeq: relationaldb.LedgerIndex(marker.LedgerSeq),
			TxnSeq:    marker.TxnSeq,
		}
	}
	page, err := relationaldb.BuildAccountTxPage("rpcenv_account_tx", relationaldb.AccountTxPageOptions{
		Account:  accountID,
		Marker:   relationalMarker,
		Limit:    limit,
		Delegate: filter,
	}, scanned)
	if err != nil {
		return nil, err
	}
	result.Transactions = make([]types.AccountTransaction, len(page.Transactions))
	for index, transaction := range page.Transactions {
		result.Transactions[index] = types.AccountTransaction{
			Hash:        [32]byte(transaction.Hash),
			LedgerIndex: uint32(transaction.LedgerSeq),
			TxnSeq:      transaction.TxnSeq,
			TxBlob:      transaction.RawTxn,
			Meta:        transaction.TxnMeta,
		}
	}
	if page.Marker != nil {
		result.Marker = &types.AccountTxMarker{
			LedgerSeq: uint32(page.Marker.LedgerSeq),
			TxnSeq:    page.Marker.TxnSeq,
		}
	}
	result.Account = account
	return result, nil
}

func (a *ledgerAdapter) GetAccountChannels(_ context.Context, _ string, _ string, _ string, _ uint32, _ string) (*types.AccountChannelsResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountCurrencies(_ context.Context, _ string, _ string) (*types.AccountCurrenciesResult, error) {
	return nil, errNotImplemented
}

func (a *ledgerAdapter) GetAccountObjects(ctx context.Context, account, ledgerIndex, objectType string, limit uint32, marker string) (*types.AccountObjectsResult, error) {
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
	result, err := ledgerservice.QueryAccountObjects(ctx, target, account, accountID, validated, objectType, limit, marker)
	if err != nil {
		return nil, err
	}
	objects := make([]types.AccountObjectItem, len(result.AccountObjects))
	for index, object := range result.AccountObjects {
		objects[index] = types.AccountObjectItem{
			Index:           object.Index,
			LedgerEntryType: object.LedgerEntryType,
			Data:            object.Data,
		}
	}
	return &types.AccountObjectsResult{
		Account:        result.Account,
		AccountObjects: objects,
		LedgerIndex:    result.LedgerIndex,
		LedgerHash:     result.LedgerHash,
		Validated:      result.Validated,
		Marker:         result.Marker,
	}, nil
}

func (a *ledgerAdapter) GetAccountNFTs(_ context.Context, _ string, _ string, _ uint32, _ string) (*types.AccountNFTsResult, error) {
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
	return protocol.RippleSeconds(r.l.CloseTime())
}

func (r *ledgerReaderAdapter) CloseTimeResolution() uint32 {
	return uint32(r.l.Header().CloseTimeResolution)
}
func (r *ledgerReaderAdapter) CloseFlags() uint8 { return r.l.Header().CloseFlags }

func (r *ledgerReaderAdapter) ParentCloseTime() int64 {
	return protocol.RippleSeconds(r.l.ParentCloseTime())
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

func (r *ledgerReaderAdapter) GetLedgerTransaction(txHash [32]byte) ([]byte, bool, error) {
	return r.l.GetTransaction(txHash)
}

func (r *ledgerReaderAdapter) LedgerAmendmentRules() *amendment.Rules {
	rules, err := ledger.LoadAmendmentsFromLedger(r.l)
	if err != nil || rules == nil {
		return amendment.EmptyRules()
	}
	return rules
}
