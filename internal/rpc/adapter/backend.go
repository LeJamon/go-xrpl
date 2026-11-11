package adapter

import (
	"context"
	"reflect"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Backend is the ledger-layer surface the RPC adapter translates.
type Backend interface {
	ledgerControl
	ledgerReader
	accountQuerier
	marketQuerier
	transactionHistory
	transactionSubmitter
	amendmentControl
	replayFaultControl
}

type ledgerControl interface {
	GetCurrentLedgerIndex() uint32
	GetClosedLedgerIndex() uint32
	GetValidatedLedgerIndex() uint32
	IsStandalone() bool
	GetServerInfo() service.ServerInfo
	GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64)
	AcceptLedger(ctx context.Context) (uint32, error)
}

type ledgerReader interface {
	GetClosedLedger() *ledger.Ledger
	GetOpenLedger() *ledger.Ledger
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)
	GetLedgerByHashContext(ctx context.Context, hash [32]byte) (*ledger.Ledger, error)
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)
	GetGenesisAccount() (string, error)
	GetLedgerContext(ctx context.Context, sequence uint32) (*service.LedgerContext, error)
	GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*service.LedgerDataResult, error)
	GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*service.LedgerEntryResult, error)
	GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*service.LedgerRangeResult, error)
}

type accountQuerier interface {
	GetAccountInfo(ctx context.Context, account, ledgerIndex string) (*service.AccountInfoResult, error)
	GetAccountChannels(ctx context.Context, account, destinationAccount, ledgerIndex string, limit uint32, marker string) (*service.AccountChannelsResult, error)
	GetAccountCurrencies(ctx context.Context, account, ledgerIndex string) (*service.AccountCurrenciesResult, error)
	GetAccountLines(ctx context.Context, account, ledgerIndex, peer string, limit uint32, marker string) (*service.AccountLinesResult, error)
	GetAccountNFTs(ctx context.Context, account, ledgerIndex string, limit uint32, marker string) (*service.AccountNFTsResult, error)
	GetAccountObjects(ctx context.Context, account, ledgerIndex, objType string, limit uint32, marker string) (*service.AccountObjectsResult, error)
	GetAccountOffers(ctx context.Context, account, ledgerIndex string, limit uint32, marker string) (*service.AccountOffersResult, error)
	GetAccountTransactionsWithDelegate(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *relationaldb.AccountTxMarker, forward bool, delegate *relationaldb.AccountTxDelegateFilter) (*service.AccountTxResult, error)
	GetOwnerInfo(ctx context.Context, account, ledgerIndex string) (*service.OwnerInfoResult, error)
}

type marketQuerier interface {
	GetBookOffers(ctx context.Context, takerGets, takerPays tx.Amount, taker, domainHex, ledgerIndex string, limit uint32, marker string, withProofs bool) (*service.BookOffersResult, error)
	GetDepositAuthorized(ctx context.Context, sourceAccount, destinationAccount, ledgerIndex string, credentials []string) (*service.DepositAuthorizedResult, error)
	GetGatewayBalances(ctx context.Context, account string, hotWallets []string, ledgerIndex string) (*service.GatewayBalancesResult, error)
	GetNFTBuyOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*service.NFTOffersResult, error)
	GetNFTSellOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*service.NFTOffersResult, error)
	GetNoRippleCheck(ctx context.Context, account, role, ledgerIndex string, limit uint32, transactions bool) (*service.NoRippleCheckResult, error)
}

type transactionHistory interface {
	GetTransaction(txHash [32]byte) (*service.TransactionResult, error)
	GetTransactionHistory(ctx context.Context, startIndex uint32) (*service.TxHistoryResult, error)
	GetTransactionWithRange(ctx context.Context, txHash [32]byte, minLedger, maxLedger uint32) (*service.TransactionResult, relationaldb.TxSearchResult, error)
	UseTxTables() bool
}

type transactionSubmitter interface {
	SubmitTransaction(transaction tx.Transaction, rawBlob []byte, failHard bool) (*service.SubmitResult, error)
	SimulateTransaction(transaction tx.Transaction) (*service.SubmitResult, error)
	GetAutofillFee(parsedTx tx.Transaction, unlimited bool, mult, div int) (uint64, error)
	GetAutofillSequence(account string, hasTicketSequence bool) (uint32, error)
	TransactionRules() *amendment.Rules
}

type amendmentControl interface {
	Table() *amendment.Table
	IsAmendmentBlocked() bool
	SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error
}

type replayFaultControl interface {
	ReplayFaultStatus() replayfault.Status
	ReplayBlocked() bool
	StartReplayFaultRecovery(id string) error
	RevalidateReplayFault(ctx context.Context, id string) error
}

// isNilBackend also reports an interface holding a typed nil pointer, so the
// adapter's nil guards keep working when a nil service pointer is injected.
func isNilBackend(b Backend) bool {
	if b == nil {
		return true
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
